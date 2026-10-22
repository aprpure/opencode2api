package gateway

import (
	"context"

	"errors"

	"time"

	"opencode2api/internal/models"
)

// rateLimitedProbeDelay postpones the next probe after the free tier's usage
// cap answered. The cap is short-lived, so a quarter-hour retry finds the lane
// again without re-probing on every scheduler tick.
const rateLimitedProbeDelay = 15 * time.Minute

// AvailabilitySnapshot returns the availability state of every free model in
// the current runtime's catalog.
func (m *RuntimeController) AvailabilitySnapshot() []models.Availability {
	items := []models.Availability{}
	r := m.current.Load()
	if r == nil {
		return items
	}
	for _, model := range r.gateway.catalog.List() {
		if r.gateway.catalog.IsFreeModel(model) {
			items = append(items, r.availability.Get(model))
		}
	}
	return items
}

// RestoreModel re-enables a free model by putting it under manual control
// (see AvailabilityRegistry.Restore), which also discards the results of
// probes already in flight. It fails for a model that is not a free model of
// the current catalog.
func (m *RuntimeController) RestoreModel(model string) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	r := m.current.Load()
	if r == nil {
		return errors.New("gateway runtime is unavailable")
	}
	for _, item := range m.AvailabilitySnapshot() {
		if item.Model == model {
			return r.availability.Restore(model, time.Now().UTC())
		}
	}
	return errors.New("unknown free model")
}

// SetManualModel switches a free model between manual and automatic control.
// Manual models stay enabled and are excluded from automatic probing.
func (m *RuntimeController) SetManualModel(model string, manual bool) error {
	m.updateMu.Lock()
	defer m.updateMu.Unlock()
	r := m.current.Load()
	if r == nil {
		return errors.New("gateway runtime is unavailable")
	}
	for _, item := range m.AvailabilitySnapshot() {
		if item.Model == model {
			return r.availability.SetManual(model, manual, time.Now().UTC())
		}
	}
	return errors.New("unknown free model")
}

// startAvailabilityChecks probes due free models at once and then every
// minute until Shutdown or the root context stops it. It runs on the
// controller's root context, so it survives runtime swaps.
func (m *RuntimeController) startAvailabilityChecks() {
	ctx, cancel := context.WithCancel(m.root)
	m.availabilityCancel = cancel
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			m.checkFreeModels(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// checkFreeModels probes, one at a time, every free model under automatic
// control whose next check is due. A result is recorded only while the
// runtime that ran the probe is still current.
func (m *RuntimeController) checkFreeModels(ctx context.Context) {
	for _, item := range m.AvailabilitySnapshot() {
		if ctx.Err() != nil {
			return
		}
		r := m.current.Load()
		if r == nil {
			return
		}
		item = r.availability.Get(item.Model)
		if item.Manual {
			continue
		}
		if time.Now().Before(item.NextCheck) {
			continue
		}
		result := r.gateway.probeFreeModel(ctx, item.Model)
		if !result.attempted || ctx.Err() != nil {
			continue
		}
		if result.rateLimited {
			// The free tier's account-wide cap answered: it says nothing about
			// the model, so keep the previous record and try again later instead
			// of showing the model as unavailable.
			m.updateMu.Lock()
			if m.current.Load() == r {
				now := time.Now().UTC()
				if err := r.availability.Defer(item.Model, item.Generation, now.Add(rateLimitedProbeDelay), now); err != nil {
					m.logger.Warn("model availability deferral save failed", "error", err)
				}
				m.logger.Info("free model availability probe deferred; free tier usage cap reached", "component", "models", "event", "availability_rate_limited", "model", item.Model, "retry_in", rateLimitedProbeDelay.String())
			}
			m.updateMu.Unlock()
			continue
		}
		// Config changes may replace credentials or endpoints during a probe.
		m.updateMu.Lock()
		if m.current.Load() == r {
			if err := r.availability.Record(item.Model, item.Generation, result.success, result.reason, result.channel, time.Now().UTC(), result.effort); err != nil {
				m.logger.Warn("model availability state save failed", "error", err)
			}
			m.logger.Info("free model availability checked", "component", "models", "event", "availability_checked", "model", item.Model, "available", result.success, "reason", result.reason, "channel", result.channel)
		}
		m.updateMu.Unlock()
	}
}
