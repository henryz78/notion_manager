package proxy

import (
	"strings"
	"testing"
)

func TestNeedsFreshThreadRecoveryDetectsPriorTurns(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "What is Opus 4.6?"},
		{Role: "assistant", Content: "It is Anthropic's flagship model."},
		{Role: "user", Content: "What about Sonnet?"},
	}
	if !needsFreshThreadRecovery(messages) {
		t.Fatal("expected prior-turn history to require a fresh Notion thread")
	}
}

func TestNeedsFreshThreadRecoverySkipsSingleTurn(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "Be concise."},
		{Role: "user", Content: "What is Opus 4.6?"},
	}
	if needsFreshThreadRecovery(messages) {
		t.Fatal("expected single-turn request to avoid fresh-thread replay")
	}
}

func TestCountNonSystemMessagesIgnoresWrapperOnlyUserMessage(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "You are Claude Code."},
		{Role: "user", Content: "<available-deferred-tools>\nRead\nEdit\n</available-deferred-tools>"},
		{Role: "user", Content: "fix login validation"},
	}
	if got := countNonSystemMessages(messages); got != 1 {
		t.Fatalf("expected wrapper-only user message to be excluded from raw count, got %d", got)
	}
}

func TestBuildToolBridgeRecoveryMessagesPreservesEveryMessage(t *testing.T) {
	marker := "TOOL_RESULT_TAIL_MUST_SURVIVE"
	messages := []ChatMessage{
		{Role: "system", Content: "Answer in Chinese."},
		{Role: "user", Content: "modify the file"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Type: "function", Function: ToolCallFunction{Name: "Read", Arguments: `{"path":"large.txt"}`}}}},
		{Role: "tool", ToolCallID: "call-1", Name: "Read", Content: strings.Repeat("x", 32*1024) + marker},
	}

	got := buildToolBridgeRecoveryMessages(messages)
	if len(got) != len(messages)+1 {
		t.Fatalf("recovery message count = %d, want %d", len(got), len(messages)+1)
	}
	for i := range messages {
		if got[i].Role != messages[i].Role || got[i].Content != messages[i].Content || got[i].ToolCallID != messages[i].ToolCallID {
			t.Fatalf("recovery altered message %d: got %#v want %#v", i, got[i], messages[i])
		}
	}
	if !strings.HasSuffix(got[3].Content, marker) {
		t.Fatal("recovery truncated the tool result")
	}
	if !strings.Contains(got[len(got)-1].Content, "client-provided action descriptors") {
		t.Fatal("recovery retry instruction was not appended")
	}
}

func TestBuildToolBridgeRecoveryMessagesPreservesInjectedContract(t *testing.T) {
	messages := []ChatMessage{{Role: "user", Content: "Labels:\n- action_1 schema-tail\nText: find needle"}}
	got := buildToolBridgeRecoveryMessages(messages)
	if len(got) != 2 || got[0].Content != messages[0].Content {
		t.Fatalf("tool recovery altered the injected contract: %#v", got)
	}
}
