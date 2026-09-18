package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestShapeAnonymousChatBody(t *testing.T) {
	// Minimal client body gains stream:true, usage options and the quartet tools.
	out := ShapeAnonymousBody([]byte(`{"model":"m","messages":[],"stream":false}`), Chat)
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["stream"] != true {
		t.Fatalf("stream not forced: %v", payload["stream"])
	}
	options, _ := payload["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Fatalf("stream_options missing include_usage: %v", payload["stream_options"])
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 5 {
		t.Fatalf("expected 5 quartet tools injected, got %d", len(tools))
	}
	names := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		fn, _ := tool["function"].(map[string]any)
		names[fn["name"].(string)] = true
	}
	for _, req := range FingerprintTools {
		if !names[req] {
			t.Fatalf("missing required tool %q", req)
		}
	}
	if !IsStreamBody(out) {
		t.Fatal("IsStreamBody must be true after shaping")
	}

	// Client tools are preserved, only missing quartet tools are appended.
	withTools := `{"model":"m","messages":[],"stream":true,"tools":[{"type":"function","function":{"name":"custom_tool","description":"c","parameters":{"type":"object"}}},{"type":"function","function":{"name":"bash","description":"b","parameters":{"type":"object"}}}]}`
	kept := ShapeAnonymousBody([]byte(withTools), Chat)
	var keptPayload map[string]any
	if err := json.Unmarshal(kept, &keptPayload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	keptTools, _ := keptPayload["tools"].([]any)
	// 1 custom + 1 existing bash + 4 appended (edit, glob, grep, read) = 6
	if len(keptTools) != 6 {
		t.Fatalf("expected 6 tools after merge, got %d", len(keptTools))
	}
	firstTool, _ := keptTools[0].(map[string]any)
	fn, _ := firstTool["function"].(map[string]any)
	if fn["name"] != "custom_tool" {
		t.Fatalf("client tool must remain first, got %v", fn["name"])
	}

	// HasClientTools checks
	if HasClientTools([]byte(withTools)) != true {
		t.Fatal("expected HasClientTools=true for custom_tool")
	}

	// Non-Chat bodies and malformed input pass through untouched.
	anthropic := `{"model":"m","max_tokens":8,"messages":[]}`
	if got := ShapeAnonymousBody([]byte(anthropic), Anthropic); string(got) != anthropic {
		t.Fatalf("anthropic body must pass through: %q", got)
	}
	if got := ShapeAnonymousBody([]byte("{bad"), Chat); string(got) != "{bad" {
		t.Fatalf("malformed body must pass through: %q", got)
	}
}

func TestShapeAnonymousResponsesBody(t *testing.T) {
	body := `{
		"model": "muse-spark-1.3-contributor-free",
		"stream": false,
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "function_call", "call_id": "c1", "name": "read", "arguments": "{}"},
			{"type": "function_call_output", "call_id": "c1", "output": "ok"}
		]
	}`
	out := ShapeAnonymousBody([]byte(body), Responses)
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload["stream"] != true {
		t.Fatal("expected stream: true")
	}
	// Verify quartet flat tools were injected
	tools, _ := payload["tools"].([]any)
	if len(tools) != 5 {
		t.Fatalf("expected 5 flat tools, got %d", len(tools))
	}
	first, _ := tools[0].(map[string]any)
	if first["type"] != "function" || first["name"] != "bash" {
		t.Fatalf("expected flat Responses tool shape, got %+v", first)
	}
}

func TestCollectStreamResponseChat(t *testing.T) {
	sse := "data: {\"id\":\"gen-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"gen-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n" +
		"data: [DONE]\n\n"
	body, usage, reported, err := CollectStreamResponse(strings.NewReader(sse), Chat, Chat, "m")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choices, _ := doc["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if message["content"] != "hi" {
		t.Fatalf("content = %v", message["content"])
	}
	if choice["finish_reason"] != "stop" {
		t.Fatalf("finish = %v", choice["finish_reason"])
	}
	if !reported || usage.Input != 5 || usage.Output != 2 {
		t.Fatalf("usage = %+v reported=%v", usage, reported)
	}
}

func TestCollectStreamResponseError(t *testing.T) {
	sse := "data: {\"error\":{\"message\":\"boom\",\"type\":\"upstream_error\"}}\n\n"
	if _, _, _, err := CollectStreamResponse(strings.NewReader(sse), Chat, Chat, "m"); err == nil {
		t.Fatal("expected stream error")
	}
}
