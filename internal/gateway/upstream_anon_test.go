package gateway

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

func fakeResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(data)
}

// Deterministic 400 (including stale-reasoning 400) short-circuits the
// anonymous scan: the outer doUpstream triggers its strip-and-retry off
// the final response body's marker, so scanning more proxies only burns
// round trips for the same rejection.
func TestDeterministic400ShortCircuits(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"unknown field foo"}}`,
		`{"error":{"message":"Referenced reasoning item 'rs_abc' was not found or has expired"}}`,
	} {
		resp := fakeResp(400, body)
		if !isNonRetryableClientResponse(resp, nil) {
			t.Fatalf("expected non-retryable for %q", body)
		}
		if got := readAll(t, resp); !strings.Contains(got, body[len(body)-20:]) {
			t.Fatalf("body must stay intact for outer retry: %q", got)
		}
	}
}

// Retryable statuses must keep scanning.
func TestRetryableNotShortCircuited(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 502} {
		resp := fakeResp(status, "x")
		if isNonRetryableClientResponse(resp, nil) {
			t.Fatalf("status %d must remain retryable", status)
		}
	}
	if !bytes.Equal([]byte("x"), []byte(readAll(t, fakeResp(200, "x")))) {
		t.Fatal("sanity")
	}
}

// A provider that rejects an effort level it does not implement answers 400
// with a generic parameter-validation error that never names the field, so the
// signature cannot mention "reasoning". Stale-reference text must still be
// handled by its own branch, never by the effort fallback.
func TestUnsupportedReasoningEffortSignature(t *testing.T) {
	generic := [][]byte{
		[]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"The request contains invalid parameters."}}`),
		[]byte(`{"detail":[{"type":"missing","msg":"unrecognized parameter supplied"}]}`),
		[]byte(`unsupported value: 'max'`),
	}
	for _, body := range generic {
		if !isUnsupportedReasoningEffort(body) {
			t.Fatalf("must match generic parameter rejection: %s", body)
		}
	}
	for _, body := range [][]byte{
		[]byte(`{"error":{"message":"Referenced reasoning item 'rs_abc' was not found or has expired"}}`),
		[]byte(`{"error":{"message":"unknown field foo"}}`),
	} {
		if isUnsupportedReasoningEffort(body) {
			t.Fatalf("must not match unrelated or stale-reference 400: %s", body)
		}
	}
}

// The fallback only fires when a Responses body actually carries a reasoning
// control, so an unrelated 400 on a plain body is never replayed.
func TestDropReasoningControlsOnlyWhenPresent(t *testing.T) {
	route := models.Route{
		ID: "m", Tier: config.TierZen, Protocol: wire.Responses,
		Protocols: map[config.Tier]wire.Protocol{config.TierZen: wire.Responses},
	}
	withReasoning := map[config.Tier][]byte{
		config.TierZen: []byte(`{"model":"m","reasoning":{"effort":"max"},"input":[]}`),
	}
	out, changed := dropReasoningControls(route, withReasoning)
	if !changed || !strings.Contains(string(out[config.TierZen]), `"input"`) {
		t.Fatalf("expected reasoning to be dropped, changed=%v out=%s", changed, out[config.TierZen])
	}
	if strings.Contains(string(out[config.TierZen]), "reasoning") {
		t.Fatalf("reasoning must be gone: %s", out[config.TierZen])
	}
	plain := map[config.Tier][]byte{config.TierZen: []byte(`{"model":"m","input":[]}`)}
	if _, changed := dropReasoningControls(route, plain); changed {
		t.Fatal("plain body must not report a change")
	}
	chatRoute := models.Route{
		ID: "m", Tier: config.TierZen, Protocol: wire.Chat,
		Protocols: map[config.Tier]wire.Protocol{config.TierZen: wire.Chat},
	}
	chatBody := map[config.Tier][]byte{config.TierZen: []byte(`{"model":"m","reasoning_effort":"max"}`)}
	if _, changed := dropReasoningControls(chatRoute, chatBody); changed {
		t.Fatal("non-Responses tiers must pass through untouched")
	}
}
