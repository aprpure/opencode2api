package protocol

import (
	"testing"
)

func TestCacheAffinityKnobsRoundTrip(t *testing.T) {
	chat := map[string]any{
		"model":             "m",
		"messages":          []any{map[string]any{"role": "user", "content": "hi"}},
		"prompt_cache_key":  "ses_abc",
		"safety_identifier": "sid",
		"service_tier":      "flex",
		"store":             true,
	}
	// Same-protocol passes through untouched.
	same, err := ConvertRequest(Chat, Chat, chat)
	if err != nil {
		t.Fatalf("chat->chat: %v", err)
	}
	for _, key := range []string{"prompt_cache_key", "safety_identifier", "service_tier", "store"} {
		if same[key] != chat[key] {
			t.Fatalf("chat->chat %s = %v, want %v", key, same[key], chat[key])
		}
	}
	// Cross-protocol carries the knobs across the bridge.
	bridged, err := ConvertRequest(Chat, Responses, chat)
	if err != nil {
		t.Fatalf("chat->responses: %v", err)
	}
	for _, key := range []string{"prompt_cache_key", "safety_identifier", "service_tier", "store"} {
		if bridged[key] != chat[key] {
			t.Fatalf("chat->responses %s = %v, want %v", key, bridged[key], chat[key])
		}
	}

	// Absent knobs stay absent: no nil/false injection.
	bare := map[string]any{
		"model":    "m",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	}
	out, err := ConvertRequest(Chat, Responses, bare)
	if err != nil {
		t.Fatalf("bare chat->responses: %v", err)
	}
	for _, key := range []string{"prompt_cache_key", "safety_identifier", "service_tier", "store"} {
		if _, ok := out[key]; ok {
			t.Fatalf("bare request must omit %s, got %v", key, out[key])
		}
	}
}
