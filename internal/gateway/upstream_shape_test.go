package gateway

import (
	"encoding/json"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

func shapeTestGateway() *Gateway {
	return &Gateway{catalog: models.NewCatalog(config.TierZen, nil)}
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

	shaped, changed := g.shapeKeyBody([]byte(plain), shapeTestRoute("x-free"), config.TierZen)
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
	if tools, _ := payload["tools"].([]any); len(tools) != 5 {
		t.Fatalf("expected 5 tools injected, got %d", len(tools))
	}

	// Paid model: byte-identical passthrough.
	if out, changed := g.shapeKeyBody([]byte(plain), shapeTestRoute("gpt-5"), config.TierZen); changed || string(out) != plain {
		t.Fatalf("paid body must pass through unchanged: changed=%v out=%q", changed, out)
	}

	// Already-shaped free body: no change reported, no Shaped marking downstream.
	already := `{"model":"m","messages":[],"stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"bash","description":"b","parameters":{"type":"object"}}},{"type":"function","function":{"name":"edit","description":"b","parameters":{"type":"object"}}},{"type":"function","function":{"name":"glob","description":"b","parameters":{"type":"object"}}},{"type":"function","function":{"name":"grep","description":"b","parameters":{"type":"object"}}},{"type":"function","function":{"name":"read","description":"b","parameters":{"type":"object"}}},{"type":"function","function":{"name":"custom","description":"b","parameters":{"type":"object"}}}]}`
	if out, changed := g.shapeKeyBody([]byte(already), shapeTestRoute("x-free"), config.TierZen); changed || string(out) != already {
		t.Fatalf("already-shaped body must pass through unchanged: changed=%v", changed)
	}
}
