package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"opencode2api/internal/jsonutil"
)

// FingerprintTools is the core agent toolset required by OpenCode free tier:
// upstream rejects requests on every lane that lack any of these function
// names with 403 FreeTierError.
var FingerprintTools = [...]string{"bash", "edit", "glob", "grep", "read"}

func isFingerprintTool(name string) bool {
	for _, tool := range FingerprintTools {
		if name == tool {
			return true
		}
	}
	return false
}

// HasClientTools reports whether the incoming prepared body carries any
// user-defined tools beyond the 5 fingerprint tools.
func HasClientTools(body []byte) bool {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	for _, raw := range jsonutil.SliceAt(payload, "tools") {
		if t, ok := raw.(map[string]any); ok {
			name := jsonutil.FirstString(jsonutil.StringAt(t, "function", "name"), jsonutil.StringAt(t, "name"))
			if name != "" && !isFingerprintTool(name) {
				return true
			}
		}
	}
	return false
}

// injectMissingFingerprintTools ensures all 5 fingerprint tools are present
// without altering any client-provided tools. It reports whether anything was
// appended. Anthropic tools use the native {name, description, input_schema}
// shape; Responses uses the flat function shape; Chat nests under "function".
func injectMissingFingerprintTools(payload map[string]any, protocol Protocol) bool {
	toolsRaw := jsonutil.SliceAt(payload, "tools")
	present := make(map[string]bool, len(toolsRaw)+len(FingerprintTools))
	for _, raw := range toolsRaw {
		if t, ok := raw.(map[string]any); ok {
			name := jsonutil.FirstString(jsonutil.StringAt(t, "function", "name"), jsonutil.StringAt(t, "name"))
			if name != "" {
				present[name] = true
			}
		}
	}
	tools := make([]any, len(toolsRaw), len(toolsRaw)+len(FingerprintTools))
	copy(tools, toolsRaw)
	schema := map[string]any{"type": "object", "properties": map[string]any{}}
	appended := false
	for _, name := range FingerprintTools {
		if present[name] {
			continue
		}
		desc := "Agent tool " + name
		if protocol == Responses {
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        name,
				"description": desc,
				"parameters":  schema,
			})
		} else if protocol == Anthropic {
			tools = append(tools, map[string]any{
				"name":         name,
				"description":  desc,
				"input_schema": schema,
			})
		} else {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        name,
					"description": desc,
					"parameters":  schema,
				},
			})
		}
		present[name] = true
		appended = true
	}
	if !appended {
		return false
	}
	payload["tools"] = tools
	return true
}

// ShapeAnonymousBody rewrites an upstream request body for the anonymous free
// tier: forcing stream mode and appending missing fingerprint tools. Bodies
// that already satisfy both are returned unchanged. Anthropic uses its native
// tool shape and stream flag, mirroring upstream.
func ShapeAnonymousBody(body []byte, protocol Protocol) []byte {
	if protocol != Chat && protocol != Responses && protocol != Anthropic {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	changed := false
	if streaming, ok := payload["stream"].(bool); !ok || !streaming {
		payload["stream"] = true
		changed = true
	}
	if protocol == Chat {
		options, _ := payload["stream_options"].(map[string]any)
		if include, ok := options["include_usage"].(bool); !ok || !include {
			if options == nil {
				options = map[string]any{}
			}
			options["include_usage"] = true
			payload["stream_options"] = options
			changed = true
		}
	}
	if injectMissingFingerprintTools(payload, protocol) {
		changed = true
	}
	if !changed {
		return body
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return encoded
}

// IsStreamBody reports whether a prepared JSON body requests streaming.
func IsStreamBody(body []byte) bool {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	stream, _ := payload["stream"].(bool)
	return stream
}

type discardFlusher struct{}

func (discardFlusher) Flush() {}

// CollectStreamResponse consumes one upstream SSE stream and aggregates it
// into a single non-streaming JSON document in the target protocol. It is
// the counterpart of forcing stream:true on the anonymous lane: when the client
// asked for a plain JSON reply while the upstream only serves SSE, the
// gateway joins the stream here instead of leaking raw SSE chunks downstream.
func CollectStreamResponse(reader io.Reader, from, to Protocol, model string) ([]byte, Usage, bool, error) {
	parser := &bridgeStreamParser{
		protocol:          from,
		tools:             map[string]bool{},
		toolIDs:           map[string]string{},
		toolNames:         map[string]string{},
		responseArgs:      map[string]bool{},
		responseReasoning: map[string]bool{},
	}
	emitter := newBridgeStreamEmitter(io.Discard, discardFlusher{}, from, model)
	termination := streamOpen
	readErr := readSSE(reader, func(eventName, data string) error {
		events, err := parser.Parse(eventName, data)
		if err != nil {
			termination = streamErrorTermination
			return err
		}
		for _, event := range events {
			switch event.Kind {
			case "done":
				termination = streamNormalTermination
			case "error":
				termination = streamErrorTermination
				return fmt.Errorf("upstream %s stream error: %s", from, jsonutil.FirstString(event.Error, "upstream stream failed"))
			}
			if err := emitter.Emit(event); err != nil {
				return err
			}
			if event.Kind == "done" {
				return errStreamNormalTermination
			}
		}
		return nil
	})
	if readErr != nil {
		if errors.Is(readErr, errStreamNormalTermination) {
			return finishCollection(emitter, to)
		}
		return nil, Usage{}, false, readErr
	}
	switch termination {
	case streamNormalTermination:
		return finishCollection(emitter, to)
	case streamErrorTermination:
		return nil, Usage{}, false, errStreamUpstreamFailure
	default:
		return nil, Usage{}, false, errSSEUnexpectedEOF
	}
}

func finishCollection(emitter *bridgeStreamEmitter, target Protocol) ([]byte, Usage, bool, error) {
	tools := make([]bridgeBlock, 0, len(emitter.order))
	for _, key := range emitter.order {
		tool := emitter.tools[key]
		tools = append(tools, bridgeBlock{
			Kind:          "tool_call",
			ID:            tool.ID,
			Name:          tool.Name,
			ArgumentsJSON: tool.Arguments.String(),
		})
	}
	// Same phantom-tool-call guard as the streaming Finish path: nameless
	// deltas never start a tool, so a tool stop with no usable block must
	// not leak to the non-streaming client.
	tools = usableToolBlocks(tools)
	response := bridgeResponse{
		ID:      emitter.id,
		Model:   emitter.model,
		Text:    emitter.text.String(),
		Tools:   tools,
		Stop:    emitter.stop,
		Usage:   emitter.usage,
		Created: emitter.created,
	}
	if response.Stop == "" {
		if len(tools) > 0 {
			response.Stop = "tool_calls"
		} else {
			response.Stop = "stop"
		}
	} else if isToolStop(response.Stop) && len(tools) == 0 {
		response.Stop = "stop"
	}
	if emitter.reasoning.Len() > 0 || emitter.reasoningSignature.Len() > 0 || emitter.reasoningEncrypted != "" {
		response.Reasoning = []bridgeBlock{{
			Kind:      "reasoning",
			Text:      emitter.reasoning.String(),
			Signature: emitter.reasoningSignature.String(),
			Encrypted: emitter.reasoningEncrypted,
		}}
	}
	encoded, err := json.Marshal(encodeBridgeResponse(target, response))
	if err != nil {
		return nil, Usage{}, false, err
	}
	return encoded, emitter.usage, emitter.usageReported, nil
}
