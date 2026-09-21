package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func chatFinishOf(t *testing.T, doc []byte) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(doc, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choices, _ := payload["choices"].([]any)
	choice, _ := choices[0].(map[string]any)
	return choice["finish_reason"].(string)
}

func TestEncodeBridgeResponseDemotesPhantomToolStop(t *testing.T) {
	// Empty tool list with a tool stop must demote to stop.
	out := encodeBridgeResponse(Chat, bridgeResponse{ID: "r1", Model: "m", Text: "hi", Stop: "tool_calls"})
	encoded, _ := json.Marshal(out)
	if got := chatFinishOf(t, encoded); got != "stop" {
		t.Fatalf("empty tools: finish = %q, want stop", got)
	}

	// Nameless tool blocks are not executable and must be filtered + demoted.
	out = encodeBridgeResponse(Chat, bridgeResponse{
		ID: "r2", Model: "m", Text: "hi", Stop: "tool_calls",
		Tools: []bridgeBlock{{Kind: "tool_call", ID: "c1", Name: "  ", ArgumentsJSON: "{}"}},
	})
	encoded, _ = json.Marshal(out)
	if got := chatFinishOf(t, encoded); got != "stop" {
		t.Fatalf("nameless tool: finish = %q, want stop", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	choices, _ := payload["choices"].([]any)
	message, _ := choices[0].(map[string]any)["message"].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("nameless tool_calls must be dropped: %v", message)
	}

	// A real tool call keeps the tool stop.
	out = encodeBridgeResponse(Chat, bridgeResponse{
		ID: "r3", Model: "m", Text: "", Stop: "tool_calls",
		Tools: []bridgeBlock{{Kind: "tool_call", ID: "c1", Name: "bash", ArgumentsJSON: "{}"}},
	})
	encoded, _ = json.Marshal(out)
	if got := chatFinishOf(t, encoded); got != "tool_calls" {
		t.Fatalf("real tool: finish = %q, want tool_calls", got)
	}
}

func TestCollectStreamResponseDemotesPhantomToolStop(t *testing.T) {
	// Upstream ends with finish_reason tool_calls but never sent a tool delta.
	sse := "data: {\"id\":\"gen-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"gen-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n\n"
	body, _, _, err := CollectStreamResponse(strings.NewReader(sse), Chat, Chat, "m")
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got := chatFinishOf(t, body); got != "stop" {
		t.Fatalf("phantom stream: finish = %q, want stop", got)
	}
}

// The streaming Responses completed document must not carry an unexecutable
// function_call for a tool delta that never had a name: such deltas never
// start downstream, so emitting them only confuses strict clients.
func TestResponsesFinishDropsNamelessTool(t *testing.T) {
	var buf bytes.Buffer
	emitter := newBridgeStreamEmitter(&buf, discardFlusher{}, Responses, "m")
	if err := emitter.Emit(bridgeStreamEvent{Kind: "text", Text: "hi"}); err != nil {
		t.Fatalf("emit text: %v", err)
	}
	// Nameless tool delta: registered in order but never started.
	if err := emitter.Emit(bridgeStreamEvent{Kind: "tool_delta", ToolKey: "k1", Text: "{}"}); err != nil {
		t.Fatalf("emit tool delta: %v", err)
	}
	emitter.stop = "tool_calls"
	if err := emitter.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if line == "" || line == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event["type"] != "response.completed" {
			continue
		}
		response, _ := event["response"].(map[string]any)
		output, _ := response["output"].([]any)
		for _, item := range output {
			entry, _ := item.(map[string]any)
			if entry["type"] == "function_call" {
				t.Fatalf("completed must not contain nameless function_call: %v", entry)
			}
		}
		return
	}
	t.Fatal("response.completed event not found")
}
