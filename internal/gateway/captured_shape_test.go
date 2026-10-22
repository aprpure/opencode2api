package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"opencode2api/internal/config"
	"opencode2api/internal/identity"
	"opencode2api/internal/models"
	wire "opencode2api/internal/protocol"
)

// capturedAnonymousIDs mirrors the identity headers of the OpenCode 1.19.3
// request the 2026-10-22 HAR captured against /inference.
func capturedAnonymousIDs() identity.RequestIDs {
	return identity.RequestIDs{
		Session:      "ses_e9dce96e9ffe1RLmIebQCK01k3",
		ChildSession: "ses_e9dce96e9ffe1RLmIebQCK01k3",
		Message:      "msg_162316945001VkpUsAHjRi6kAH",
		Project:      "global",
	}
}

// The keyless lane presents the OpenCode 1.19.3 client the capture shows being
// served on /inference: a bare 1.19.3 User-Agent, both session headers, the
// user message id as x-opencode-request, "global" as the project, and none of
// the 2.x affinity headers. A future identity change has to be deliberate.
func TestAnonymousRequestMatchesCapture(t *testing.T) {
	ids := capturedAnonymousIDs()
	req, err := upstreamRequest(context.Background(), "https://opencode.ai/inference", wire.Chat,
		[]byte(`{"model":"deepseek-v4.1-flash-free"}`), ids, anonymousZenKey, true)
	if err != nil {
		t.Fatalf("upstreamRequest: %v", err)
	}
	if got, want := req.URL.String(), "https://opencode.ai/inference/openai/v1/chat/completions"; got != want {
		t.Fatalf("endpoint = %q, want %q", got, want)
	}
	want := map[string]string{
		"Authorization":         "Bearer public",
		"Content-Type":          "application/json",
		"User-Agent":            "opencode/1.19.3 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14",
		"Accept":                "*/*",
		"x-opencode-client":     "cli",
		"x-opencode-project":    "global",
		"x-opencode-request":    ids.Message,
		"x-opencode-session":    ids.Session,
		"x-opencode-session-id": ids.Session,
	}
	for name, value := range want {
		if got := req.Header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
	for _, absent := range []string{
		"x-session-affinity", "X-Session-Id", "x-opencode-parent-session-id",
		"x-parent-session-id", "anthropic-beta",
	} {
		if got := req.Header.Get(absent); got != "" {
			t.Errorf("%s must not be sent on the keyless lane, got %q", absent, got)
		}
	}
}

// The gate requires one tool name and does not compare definitions, so the
// gateway injects a single read tool. An auxiliary request (no tools at all)
// keeps a bare fallback: the gate rejects it once a tool appears.
func TestPrepareAnonymousBodyTools(t *testing.T) {
	shaped, alias, bare := prepareAnonymousBody([]byte(`{"model":"m","stream":true}`), wire.Chat)
	if alias != "" {
		t.Fatalf("alias = %q, want empty", alias)
	}
	var payload map[string]any
	if err := json.Unmarshal(shaped, &payload); err != nil {
		t.Fatalf("unmarshal shaped: %v", err)
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("shaped body carries %d tools, want the single read tool", len(tools))
	}
	if bare == nil {
		t.Fatal("a request without tools must keep a bare fallback")
	}
	var barePayload map[string]any
	if err := json.Unmarshal(bare, &barePayload); err != nil {
		t.Fatalf("unmarshal bare: %v", err)
	}
	if _, exists := barePayload["tools"]; exists {
		t.Fatalf("bare fallback must not carry tools: %v", barePayload["tools"])
	}

	// A request that already declares tools has no bare fallback: dropping them
	// would change what the client asked for.
	if _, _, bare = prepareAnonymousBody([]byte(`{"model":"m","tools":[{"type":"function","function":{"name":"Read"}}]}`), wire.Chat); bare != nil {
		t.Fatalf("a tool-bearing request must not keep a bare fallback: %q", bare)
	}
}

// A 403 FreeTierError is the shape gate, not an IP abuse block.
func TestFreeTierErrorClassification(t *testing.T) {
	shape := []byte(`{"type":"error","error":{"type":"FreeTierError","message":"Free tier can only be used from within OpenCode"}}`)
	if !isFreeTierShapeRejection(403, shape) {
		t.Error("FreeTierError must classify as a shape rejection")
	}
	abuse := []byte("Forbidden, OpenCode Free Model Gateway Detect Abuse Traffic")
	if isFreeTierShapeRejection(403, abuse) {
		t.Error("an IP abuse block is not a shape rejection")
	}
}

// The catalog's reasoning_options is the only authority on which effort levels
// a model implements: the same "max" is valid for deepseek-v4.1-flash-free and
// rejected by muse-spark, and "xhigh" is the other way round.
func TestCoerceEffortFollowsCatalogLadder(t *testing.T) {
	cases := []struct {
		name    string
		current string
		allowed []string
		want    string
		moved   bool
	}{
		{"deepseek free keeps max", "max", []string{"low", "high", "max"}, "max", false},
		{"deepseek free lifts xhigh", "xhigh", []string{"low", "high", "max"}, "max", true},
		{"deepseek free lifts medium", "medium", []string{"low", "high", "max"}, "high", true},
		{"muse-spark lowers max", "max", []string{"minimal", "low", "medium", "high", "xhigh"}, "xhigh", true},
		{"unknown level untouched", "turbo", []string{"low", "high"}, "turbo", false},
		{"no ladder untouched", "max", nil, "max", false},
	}
	for _, tc := range cases {
		got, moved := coerceEffort(tc.current, tc.allowed)
		if got != tc.want || moved != tc.moved {
			t.Errorf("%s: coerceEffort(%q, %v) = (%q, %v), want (%q, %v)",
				tc.name, tc.current, tc.allowed, got, moved, tc.want, tc.moved)
		}
	}
}

// alignEffort rewrites the prepared Chat body using the model's published
// ladder, so the upstream never sees a level the model does not implement.
func TestAlignEffortRewritesChatBody(t *testing.T) {
	catalog := models.Catalog(config.TierZen, nil)
	catalog.ReplaceWithCapabilities(
		[]string{"deepseek-v4.1-flash-free"}, nil, nil, nil,
		map[config.Tier]map[string]models.Metadata{
			config.TierZen: {"deepseek-v4.1-flash-free": {ReasoningEfforts: []string{"low", "high", "max"}}},
		},
	)
	g := &Gateway{catalog: catalog}
	body := []byte(`{"model":"deepseek-v4.1-flash-free","reasoning_effort":"xhigh","stream":true}`)
	out := g.alignEffort("deepseek-v4.1-flash-free", config.TierZen, wire.Chat, body)
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %v, want max", payload["reasoning_effort"])
	}
	if payload["stream"] != true {
		t.Fatalf("unrelated fields must survive: %v", payload)
	}
}
