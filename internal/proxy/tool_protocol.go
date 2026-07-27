package proxy

import (
	"fmt"
	"strings"
)

// validateToolProtocol rejects malformed client history instead of silently
// dropping tool messages that cannot be associated with an earlier call.
func validateToolProtocol(messages []ChatMessage) error {
	type callInfo struct {
		name     string
		position int
		resolved bool
	}
	calls := make(map[string]*callInfo)

	for messageIndex, message := range messages {
		if message.Role == "assistant" {
			for callIndex, call := range message.ToolCalls {
				id := strings.TrimSpace(call.ID)
				if id == "" {
					return fmt.Errorf("assistant tool call at messages[%d].tool_calls[%d] is missing an id", messageIndex, callIndex)
				}
				if previous, exists := calls[id]; exists {
					return fmt.Errorf("duplicate tool call id %q at messages[%d]; first declared at messages[%d]", id, messageIndex, previous.position)
				}
				calls[id] = &callInfo{name: call.Function.Name, position: messageIndex}
			}
		}

		if message.Role != "tool" {
			continue
		}

		id := strings.TrimSpace(message.ToolCallID)
		if id == "" {
			return fmt.Errorf("tool result at messages[%d] is missing tool_call_id/tool_use_id", messageIndex)
		}
		call, exists := calls[id]
		if !exists {
			return fmt.Errorf("tool result at messages[%d] references unknown tool call id %q", messageIndex, id)
		}
		if call.resolved {
			return fmt.Errorf("tool call id %q has more than one result (messages[%d])", id, messageIndex)
		}
		if message.Name != "" && call.name != "" && message.Name != call.name {
			return fmt.Errorf("tool result at messages[%d] names %q but tool call id %q was declared as %q", messageIndex, message.Name, id, call.name)
		}
		call.resolved = true
	}

	return nil
}
