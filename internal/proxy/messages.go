package proxy

// cloneChatMessages returns a deep copy so per-attempt aliases or directives
// cannot mutate the pristine client history reused by account failover.
func cloneChatMessages(src []ChatMessage) []ChatMessage {
	if src == nil {
		return nil
	}
	out := make([]ChatMessage, len(src))
	for i, message := range src {
		out[i] = message
		if len(message.ToolCalls) > 0 {
			out[i].ToolCalls = append([]ToolCall(nil), message.ToolCalls...)
		}
	}
	return out
}
