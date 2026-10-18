package gateway

import (
	"encoding/json"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

func shapeTestGateway() *Gateway {
	return &Gateway{catalog: models.Catalog(config.TierZen, nil)}
}

func shapeTestRoute(model string) models.Route {
	return models.Route{
		ID: model, Tier: config.TierZen, Protocol: wire.Chat,
		Protocols: map[config.Tier]wire.Protocol{config.TierZen: wire.Chat},
	}
}

// Free models (by -free name) get agent-shaped bodies; paid bodies pass through.
func TestShapeKeyBodyFreeVsPaid(t *testing.T) {
	g := shapeTestGateway()
	plain := `{"model":"m","messages":[],"stream":false}`

	shaped, _, changed := g.shapeKeyBody([]byte(plain), shapeTestRoute("x-free"), config.TierZen)
	if !changed {
		t.Fatal("expected free model body to be shaped")
	}
	var payload map[string]any
	if err := json.Unmarshal(shaped, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["stream"] != true {
		t.Fatalf("stream not forced: %v", payload["stream"])
	}
	options, _ := payload["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Fatalf("include_usage not forced: %v", payload["stream_options"])
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != len(v2CoreTools) {
		t.Fatalf("expected %d tools injected, got %d", len(v2CoreTools), len(tools))
	}
	names := make(map[string]bool, len(tools))
	for _, raw := range tools {
		entry, _ := raw.(map[string]any)
		fn, _ := entry["function"].(map[string]any)
		if name, _ := fn["name"].(string); name != "" {
			names[name] = true
		}
	}
	for _, name := range v2CoreTools {
		if !names[name] {
			t.Fatalf("core tool %q missing from shaped body", name)
		}
	}

	// Paid model: byte-identical passthrough.
	if out, _, changed := g.shapeKeyBody([]byte(plain), shapeTestRoute("gpt-5"), config.TierZen); changed || string(out) != plain {
		t.Fatalf("paid body must pass through unchanged: changed=%v out=%q", changed, out)
	}

	// Shaping is idempotent: a second pass re-encodes the same bytes.
	if out, _, changed := g.shapeKeyBody(shaped, shapeTestRoute("x-free"), config.TierZen); changed || string(out) != string(shaped) {
		t.Fatalf("already-shaped body must pass through unchanged: changed=%v", changed)
	}
}
