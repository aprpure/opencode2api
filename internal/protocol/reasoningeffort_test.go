package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"opencode2api/internal/jsonutil"
)

func TestEffortForThinkingBudgetRungs(t *testing.T) {
	cases := map[int]string{
		0:     "high",
		-5:    "high",
		1024:  "low",
		2048:  "low",
		2049:  "medium",
		4096:  "medium",
		8191:  "medium",
		8192:  "high",
		8193:  "high",
		16383: "high",
		16384: "xhigh",
		32000: "xhigh",
		32767: "xhigh",
		32768: "max",
		64000: "max",
	}
	for budget, want := range cases {
		if got := effortForThinkingBudget(budget); got != want {
			t.Fatalf("budget %d: got %q, want %q", budget, got, want)
		}
	}
}

func TestBudgetForEffortRoundTrip(t *testing.T) {
	for _, effort := range []string{"minimal", "low", "medium", "high", "xhigh", "max"} {
		budget := budgetForEffort(effort)
		if back := effortForThinkingBudget(budget); back != map[string]string{
			"minimal": "low", "low": "low", "medium": "medium",
			"high": "high", "xhigh": "xhigh", "max": "max",
		}[effort] {
			t.Fatalf("effort %q: budget %d maps back to %q", effort, budget, back)
		}
	}
}

func TestExplicitEffortBeatsBudget(t *testing.T) {
	// output_config.effort max + budget 2048 must stay max, not low.
	out, err := ConvertRequest(Anthropic, Chat, map[string]any{
		"model":         "m",
		"max_tokens":    8192,
		"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking":      map[string]any{"type": "adaptive", "budget_tokens": float64(2048)},
		"output_config": map[string]any{"effort": "max"},
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if out["reasoning_effort"] != "max" {
		t.Fatalf("reasoning_effort = %v, want max", out["reasoning_effort"])
	}

	// none clears instead of resurrecting a level from the budget.
	cleared, err := ConvertRequest(Anthropic, Chat, map[string]any{
		"model":         "m",
		"max_tokens":    8192,
		"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking":      map[string]any{"type": "enabled", "budget_tokens": float64(8192)},
		"output_config": map[string]any{"effort": "none"},
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if _, ok := cleared["reasoning_effort"]; ok {
		t.Fatalf("none must clear reasoning_effort, got %v", cleared["reasoning_effort"])
	}
}

func TestAnthropicChatBudgetRoundTrip(t *testing.T) {
	// 32000 maps to the xhigh rung on the way to Chat. The exact budget is an
	// in-memory value and is never serialized into the Chat body (upstream
	// vendors reject unknown fields with a 400), so the way back restores the
	// rung's lower bound (16384) rather than the original number.
	chat, err := ConvertRequest(Anthropic, Chat, map[string]any{
		"model":      "m",
		"max_tokens": 40000,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": float64(32000)},
	})
	if err != nil {
		t.Fatalf("a->c: %v", err)
	}
	if chat["reasoning_effort"] != "xhigh" {
		t.Fatalf("reasoning_effort = %v, want xhigh", chat["reasoning_effort"])
	}
	back, err := ConvertRequest(Chat, Anthropic, chat)
	if err != nil {
		t.Fatalf("c->a: %v", err)
	}
	thinking, _ := back["thinking"].(map[string]any)
	if thinking["budget_tokens"] != float64(16384) {
		encoded, _ := json.Marshal(back["thinking"])
		t.Fatalf("budget_tokens = %s, want 16384 (xhigh rung)", encoded)
	}
}

// TestResponsesEffortLadder pins the Anthropic -> Responses effort mapping.
// Anthropic's "max" has no Responses equivalent (the OpenAI ladder stops at
// "xhigh"), and forwarding it verbatim is rejected upstream with a generic
// invalid_request_error, so it must land as "xhigh" on every path that can
// produce a Responses body: the bridge, the same-protocol pass-through, and
// an operator-forced level.
func TestResponsesEffortLadder(t *testing.T) {
	// Bridge path: Anthropic output_config.effort -> Responses reasoning.effort.
	out, err := ConvertRequest(Anthropic, Responses, map[string]any{
		"model":         "m",
		"max_tokens":    2048,
		"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
		"output_config": map[string]any{"effort": "max"},
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	reasoning, _ := out["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("bridge effort = %v, want xhigh", reasoning["effort"])
	}

	// Pass-through path: a client posting reasoning.effort straight to
	// /v1/responses bypasses the bridge and still needs the translation.
	prepared, err := PrepareRequest(Responses, Responses, map[string]any{
		"model":     "m",
		"reasoning": map[string]any{"effort": "max"},
		"input":     "hi",
	}, "https://opencode.ai/zen")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	reasoning, _ = prepared["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("pass-through effort = %v, want xhigh", reasoning["effort"])
	}

	// Operator-forced path.
	forced := map[string]any{"model": "m", "input": "hi"}
	ForcedEffort(Responses, forced, "max")
	reasoning, _ = forced["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("forced effort = %v, want xhigh", reasoning["effort"])
	}

	// Every level the Responses ladder does define is forwarded unchanged.
	for _, effort := range []string{"minimal", "low", "medium", "high", "xhigh"} {
		body := map[string]any{"model": "m", "input": "hi"}
		ForcedEffort(Responses, body, effort)
		reasoning, _ = body["reasoning"].(map[string]any)
		if reasoning["effort"] != effort {
			t.Fatalf("effort %q was rewritten to %v", effort, reasoning["effort"])
		}
	}

	// An unknown level is left for the upstream to judge, not silently
	// rewritten by the gateway. ForcedEffort is not the right probe here: it
	// rejects a level outside its own vocabulary before it ever reaches the
	// ladder, so use the pass-through path.
	unknown, err := PrepareRequest(Responses, Responses, map[string]any{
		"model":     "m",
		"reasoning": map[string]any{"effort": "ultra"},
		"input":     "hi",
	}, "https://opencode.ai/zen")
	if err != nil {
		t.Fatalf("prepare unknown: %v", err)
	}
	reasoning, _ = unknown["reasoning"].(map[string]any)
	if reasoning["effort"] != "ultra" {
		t.Fatalf("unknown effort = %v, want it passed through", reasoning["effort"])
	}

	// Chat and Anthropic targets keep their own ladders untouched.
	chat, err := ConvertRequest(Anthropic, Chat, map[string]any{
		"model":         "m",
		"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
		"output_config": map[string]any{"effort": "max"},
	})
	if err != nil {
		t.Fatalf("convert to chat: %v", err)
	}
	if chat["reasoning_effort"] != "max" {
		t.Fatalf("chat effort = %v, want max untouched", chat["reasoning_effort"])
	}
	anthropic, err := ConvertRequest(Anthropic, Anthropic, map[string]any{
		"model":         "m",
		"max_tokens":    2048,
		"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
		"output_config": map[string]any{"effort": "max"},
	})
	if err != nil {
		t.Fatalf("convert to anthropic: %v", err)
	}
	outputConfig, _ := anthropic["output_config"].(map[string]any)
	if outputConfig["effort"] != "max" {
		t.Fatalf("anthropic effort = %v, want max untouched", outputConfig["effort"])
	}

	// A Chat pass-through that carries reasoning.effort is a client speaking
	// the object form on a native Chat endpoint. The ladder is scoped to the
	// Responses target, so a Chat-native model that implements "max" must
	// still receive it.
	chatPassthrough, err := PrepareRequest(Chat, Chat, map[string]any{
		"model":     "m",
		"messages":  []any{map[string]any{"role": "user", "content": "hi"}},
		"reasoning": map[string]any{"effort": "max"},
	}, "https://opencode.ai/zen")
	if err != nil {
		t.Fatalf("prepare chat pass-through: %v", err)
	}
	chatReasoning, _ := chatPassthrough["reasoning"].(map[string]any)
	if chatReasoning["effort"] != "max" {
		t.Fatalf("chat pass-through effort = %v, want max untouched", chatReasoning["effort"])
	}
}

// A budget the client names can derive the same top rung that an explicit
// "max" does, so the ladder has to cover the derived path too, not just the
// explicit one.
func TestResponsesEffortLadderBudgetDerived(t *testing.T) {
	out, err := ConvertRequest(Anthropic, Responses, map[string]any{
		"model":      "m",
		"max_tokens": 40000,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": float64(32768)},
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	reasoning, _ := out["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("budget-derived effort = %v, want xhigh", reasoning["effort"])
	}
}

// TestChatUpstreamCarriesNoInternalFields is the regression test for the
// upstream 400: a Claude-format request with thinking must encode to a Chat
// body containing only fields an upstream Chat endpoint accepts. Internal
// round-trip markers (reasoning_budget_tokens) and fabricated empty assistant
// turns (content nil) must never reach the wire.
func TestChatUpstreamCarriesNoInternalFields(t *testing.T) {
	for _, input := range []map[string]any{
		{
			"model":      "m",
			"max_tokens": 512,
			"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
			"thinking":   map[string]any{"type": "enabled", "budget_tokens": float64(1024)},
		},
		{
			"model":         "m",
			"max_tokens":    32768,
			"messages":      []any{map[string]any{"role": "user", "content": "hi"}},
			"thinking":      map[string]any{"type": "adaptive", "budget_tokens": float64(2048)},
			"output_config": map[string]any{"effort": "max"},
		},
	} {
		chat, err := ConvertRequest(Anthropic, Chat, input)
		if err != nil {
			t.Fatalf("convert: %v", err)
		}
		encoded, _ := json.Marshal(chat)
		if strings.Contains(string(encoded), "reasoning_budget_tokens") {
			t.Fatalf("internal marker leaked to Chat body: %s", encoded)
		}
		for i, raw := range jsonutil.SliceAt(chat, "messages") {
			message, _ := raw.(map[string]any)
			if message == nil {
				continue
			}
			if message["content"] == nil && len(jsonutil.SliceAt(message, "tool_calls")) == 0 && jsonutil.StringAt(message, "reasoning_content") == "" {
				t.Fatalf("messages[%d] is an empty assistant turn: %s", i, encoded)
			}
		}
	}
}

func TestForcedEffortThreeProtocols(t *testing.T) {
	// Chat: set and clear.
	chatBody := map[string]any{"model": "m", "messages": []any{}}
	ForcedEffort(Chat, chatBody, "high")
	if chatBody["reasoning_effort"] != "high" {
		t.Fatalf("chat forced = %v", chatBody["reasoning_effort"])
	}
	// Explicit client level wins.
	chatBody["reasoning_effort"] = "low"
	ForcedEffort(Chat, chatBody, "max")
	if chatBody["reasoning_effort"] != "low" {
		t.Fatalf("explicit must win, got %v", chatBody["reasoning_effort"])
	}
	// none on explicit low: clientEffortExplicit is true so nothing happens.
	keep := map[string]any{"model": "m", "reasoning_effort": "low"}
	ForcedEffort(Chat, keep, "none")
	if _, ok := keep["reasoning_effort"]; !ok {
		t.Fatal("explicit low must survive none-forced")
	}
	clearBody := map[string]any{"model": "m"}
	ForcedEffort(Chat, clearBody, "none")
	if _, ok := clearBody["reasoning_effort"]; ok {
		t.Fatalf("none must not invent reasoning_effort: %v", clearBody)
	}

	// Anthropic: effort + thinking + max_tokens move together.
	anthropicBody := map[string]any{"model": "m", "max_tokens": float64(1024), "messages": []any{}}
	ForcedEffort(Anthropic, anthropicBody, "max")
	outputConfig, _ := anthropicBody["output_config"].(map[string]any)
	if outputConfig["effort"] != "max" {
		t.Fatalf("output_config = %v", anthropicBody["output_config"])
	}
	thinking, _ := anthropicBody["thinking"].(map[string]any)
	if thinking["budget_tokens"] != float64(32768) {
		t.Fatalf("budget = %v", anthropicBody["thinking"])
	}
	if anthropicBody["max_tokens"] != 32768+4096 {
		t.Fatalf("max_tokens = %v, want %d", anthropicBody["max_tokens"], 32768+4096)
	}
	// none removes the trio (fresh body: the forced max above already counts
	// as an explicit client level, so none correctly leaves it alone).
	noneBody := map[string]any{
		"model":      "m",
		"max_tokens": float64(40000),
		"messages":   []any{},
		"thinking":   map[string]any{"type": "enabled", "budget_tokens": float64(4096)},
	}
	ForcedEffort(Anthropic, noneBody, "none")
	if _, ok := noneBody["thinking"]; ok {
		t.Fatalf("none must drop thinking: %v", noneBody["thinking"])
	}
	if _, ok := noneBody["output_config"]; ok {
		t.Fatalf("none must drop output_config: %v", noneBody["output_config"])
	}

	// Responses: set into reasoning.effort.
	responsesBody := map[string]any{"model": "m", "input": "hi"}
	ForcedEffort(Responses, responsesBody, "low")
	reasoning, _ := responsesBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" {
		t.Fatalf("reasoning = %v", responsesBody["reasoning"])
	}

	// Invalid level is ignored.
	untouched := map[string]any{"model": "m"}
	ForcedEffort(Chat, untouched, "ultra")
	if _, ok := untouched["reasoning_effort"]; ok {
		t.Fatalf("invalid level must be ignored: %v", untouched)
	}
	ForcedEffort(Chat, nil, "high")
}
