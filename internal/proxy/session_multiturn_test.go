package proxy

import (
	"strings"
	"testing"
	"time"
)

// TestBuildSessionChainFollowUp verifies that the session-based chain follow-up
// builds a concise message with only the latest tool results.
func TestBuildSessionChainFollowUp(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "list files in the current directory"},
		{Role: "assistant", Content: "I'll help with that.", ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "Bash", Arguments: `{"command":"ls"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Name: "Bash", Content: "file1.txt\nfile2.txt\nREADME.md"},
	}

	compactList := "- Bash(command: str) — Execute shell command\n- Read(file_path: str) — Read a file\n"
	result := buildSessionChainFollowUp(messages, compactList, "/home/user/project")

	if len(result) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result))
	}
	if result[0].Role != "user" {
		t.Fatalf("expected user role, got %s", result[0].Role)
	}

	content := result[0].Content
	// Should contain tool results
	if !strings.Contains(content, "[Bash]: file1.txt") {
		t.Errorf("expected tool results in follow-up, got: %s", content)
	}
	// Should contain CWD
	if !strings.Contains(content, "Working directory: /home/user/project") {
		t.Errorf("expected CWD in follow-up, got: %s", content)
	}
	// Should contain available action labels
	if !strings.Contains(content, "Labels for a new action:") {
		t.Errorf("expected action label list in follow-up, got: %s", content)
	}
	// Should contain __done__
	if !strings.Contains(content, "__done__") {
		t.Errorf("expected __done__ in follow-up, got: %s", content)
	}
	if !strings.Contains(content, "Do not repeat a label whose successful result is already shown above") {
		t.Errorf("expected repeat-action guard in follow-up, got: %s", content)
	}
	// Keep the original task explicit because some model/thread combinations do
	// not reliably retain it when consuming client action results.
	if !strings.Contains(content, "list files in the current directory") {
		t.Errorf("follow-up should preserve the original task")
	}
}

// TestBuildSessionChainFollowUp_MultipleToolResults verifies handling of parallel tool calls.
func TestBuildSessionChainFollowUp_MultipleToolResults(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "check both files"},
		{Role: "assistant", Content: "I'll read both.", ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "Read", Arguments: `{"file_path":"a.txt"}`}},
			{ID: "call_2", Type: "function", Function: ToolCallFunction{Name: "Read", Arguments: `{"file_path":"b.txt"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Name: "Read", Content: "content of a"},
		{Role: "tool", ToolCallID: "call_2", Name: "Read", Content: "content of b"},
	}

	result := buildSessionChainFollowUp(messages, "- Read(file_path: str)\n", "")

	content := result[0].Content
	if !strings.Contains(content, "[Read]: content of a") {
		t.Errorf("expected first tool result, got: %s", content)
	}
	if !strings.Contains(content, "[Read]: content of b") {
		t.Errorf("expected second tool result, got: %s", content)
	}
}

func TestBuildSessionChainFollowUpUsesLatestUserTask(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "old unrelated task"},
		{Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "find needle-731 in project files"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "Grep", Arguments: `{"pattern":"needle-731"}`}}}},
		{Role: "tool", ToolCallID: "call_1", Name: "Grep", Content: "src/example.txt: needle-731"},
	}

	content := buildSessionChainFollowUp(messages, "- Grep(pattern: str)\n", "")[0].Content
	if !strings.Contains(content, `Original task: "find needle-731 in project files"`) {
		t.Fatalf("latest task missing from follow-up: %s", content)
	}
	if strings.Contains(content, "old unrelated task") {
		t.Fatalf("stale task leaked into follow-up: %s", content)
	}
}

// TestBuildSessionChainFollowUp_PreservesLargeOutput verifies that the bridge
// never clips client tool results.
func TestBuildSessionChainFollowUp_PreservesLargeOutput(t *testing.T) {
	marker := "TAIL_MUST_SURVIVE"
	largeOutput := strings.Repeat("x", 5000) + marker
	messages := []ChatMessage{
		{Role: "user", Content: "read large file"},
		{Role: "assistant", Content: "Reading.", ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "Read", Arguments: `{"file_path":"big.txt"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Name: "Read", Content: largeOutput},
	}

	result := buildSessionChainFollowUp(messages, "- Read(file_path: str)\n", "")

	content := result[0].Content
	if !strings.Contains(content, largeOutput) || !strings.Contains(content, marker) {
		t.Error("large tool output was truncated")
	}
}

func TestBuildSessionChainFollowUp_ReadOversizeGuard(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "检查为什么 copy 按钮不显示"},
		{Role: "assistant", Content: "I'll inspect the file.", ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "Read", Arguments: `{"file_path":"src/content.js"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Name: "Read", Content: "File content (31582 tokens) exceeds maximum allowed tokens (10000). Use offset and limit parameters to read specific portions of the file."},
	}

	result := buildSessionChainFollowUp(messages, "- Read(file_path: str, offset?: num, limit?: num)\n- Grep(pattern: str)\n", "")
	content := result[0].Content
	if !strings.Contains(content, "Do NOT repeat the same full-file Read") {
		t.Fatalf("expected oversize read guard in follow-up, got: %s", content)
	}
}

// TestCountNonSystemMessages verifies the new helper function.
func TestCountNonSystemMessages(t *testing.T) {
	tests := []struct {
		name     string
		messages []ChatMessage
		want     int
	}{
		{
			name:     "empty",
			messages: nil,
			want:     0,
		},
		{
			name: "system only",
			messages: []ChatMessage{
				{Role: "system", Content: "you are helpful"},
			},
			want: 0,
		},
		{
			name: "first turn",
			messages: []ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "hello"},
			},
			want: 1,
		},
		{
			name: "chain continuation",
			messages: []ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "tool call"},
				{Role: "tool", Content: "result"},
			},
			want: 3,
		},
		{
			name: "multi-round chain",
			messages: []ChatMessage{
				{Role: "system", Content: "system prompt"},
				{Role: "user", Content: "hello"},
				{Role: "assistant", Content: "tool call 1"},
				{Role: "tool", Content: "result 1"},
				{Role: "assistant", Content: "tool call 2"},
				{Role: "tool", Content: "result 2"},
			},
			want: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countNonSystemMessages(tt.messages)
			if got != tt.want {
				t.Errorf("countNonSystemMessages() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestSessionFingerprintStability verifies that the session fingerprint is stable
// across turns when computed on raw (pre-injection) messages.
func TestSessionFingerprintStability(t *testing.T) {
	systemPrompt := "You are Claude Code, a CLI assistant..."

	// Turn 1: just system + user
	turn1 := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "list files here"},
	}

	// Turn 2: system + user + assistant + tool (chain continuation)
	turn2 := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "list files here"},
		{Role: "assistant", Content: `{"name":"Bash","arguments":{"command":"ls"}}`},
		{Role: "tool", Content: "file1.txt\nfile2.txt"},
	}

	// Turn 3: system + user + assistant + tool + assistant + tool
	turn3 := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "list files here"},
		{Role: "assistant", Content: `{"name":"Bash","arguments":{"command":"ls"}}`},
		{Role: "tool", Content: "file1.txt\nfile2.txt"},
		{Role: "assistant", Content: `{"name":"Read","arguments":{"file_path":"file1.txt"}}`},
		{Role: "tool", Content: "content of file1"},
	}

	fp1 := computeSessionFingerprint(turn1)
	fp2 := computeSessionFingerprint(turn2)
	fp3 := computeSessionFingerprint(turn3)

	if fp1 != fp2 {
		t.Errorf("fingerprint changed between turn 1 and 2: %s vs %s", fp1, fp2)
	}
	if fp2 != fp3 {
		t.Errorf("fingerprint changed between turn 2 and 3: %s vs %s", fp2, fp3)
	}
}

// TestSessionContinuationDetection verifies that rawMsgCount correctly
// distinguishes first turn, continuation, and repeat.
func TestSessionContinuationDetection(t *testing.T) {
	sm := NewSessionManager(5 * time.Minute)

	systemPrompt := "You are Claude Code..."
	fingerprint := "test-fingerprint-123456789012"

	// Turn 1: first turn
	turn1Msgs := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "hello"},
	}
	rawMsgCount1 := countNonSystemMessages(turn1Msgs)
	if rawMsgCount1 != 1 {
		t.Fatalf("expected 1, got %d", rawMsgCount1)
	}

	session := sm.Get(fingerprint)
	if session != nil {
		t.Fatal("expected nil session for first turn")
	}

	// Save session after turn 1
	sm.Set(fingerprint, &Session{
		ThreadID:        "thread-1",
		TurnCount:       1,
		RawMessageCount: rawMsgCount1,
		AccountEmail:    "test@example.com",
		CreatedAt:       time.Now(),
		LastUsedAt:      time.Now(),
	})

	// Turn 2: chain continuation (rawMsgCount increases)
	turn2Msgs := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "tool call"},
		{Role: "tool", Content: "result"},
	}
	rawMsgCount2 := countNonSystemMessages(turn2Msgs)
	if rawMsgCount2 != 3 {
		t.Fatalf("expected 3, got %d", rawMsgCount2)
	}

	session = sm.Get(fingerprint)
	if session == nil {
		t.Fatal("expected existing session")
	}
	if rawMsgCount2 <= session.RawMessageCount {
		t.Error("expected continuation detection (rawMsgCount > session.RawMessageCount)")
	}

	// Simulate saving after turn 2
	session.TurnCount++
	session.RawMessageCount = rawMsgCount2

	// Retry of turn 2 (same messages): repeat detection
	rawMsgCountRetry := countNonSystemMessages(turn2Msgs)
	if rawMsgCountRetry != session.RawMessageCount {
		t.Errorf("expected repeat detection: rawMsgCount=%d, session.RawMessageCount=%d",
			rawMsgCountRetry, session.RawMessageCount)
	}
}

// TestInjectToolsSessionVsFreshThread verifies that an existing Notion thread
// receives only the follow-up, while a fresh thread receives the full protocol
// history plus that follow-up.
func TestInjectToolsSessionVsFreshThread(t *testing.T) {
	// Build a chain continuation scenario with >5 tools (triggers useLargeToolSet)
	tools := []Tool{
		{Type: "function", Function: ToolFunction{Name: "Bash", Description: "Execute shell command", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Read", Description: "Read a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Write", Description: "Write a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Edit", Description: "Edit a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Glob", Description: "Find files", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Grep", Description: "Search files", Parameters: map[string]interface{}{"type": "object"}}},
	}

	messages := []ChatMessage{
		{Role: "user", Content: "list all go files"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "Bash", Arguments: `{"command":"find . -name '*.go'"}`}},
		}},
		{Role: "tool", ToolCallID: "call_1", Name: "Bash", Content: "main.go\ntools.go\nserver.go"},
	}

	// With session: use a concise follow-up that still repeats the original task.
	session := &Session{TurnCount: 1, RawMessageCount: 1}
	resultWithSession := injectToolsIntoMessages(messages, tools, "claude-sonnet-4-20250514", session)

	if len(resultWithSession) != 1 {
		t.Fatalf("session path: expected 1 message, got %d", len(resultWithSession))
	}
	if !strings.Contains(resultWithSession[0].Content, "Completed action results:") {
		t.Error("session path: expected completed-action results prefix")
	}
	if !strings.Contains(resultWithSession[0].Content, "list all go files") {
		t.Error("session path: should preserve the original query for reliable completion")
	}

	// Without a session, preserve all protocol messages for cross-account replay.
	resultNoSession := injectToolsIntoMessages(messages, tools, "claude-sonnet-4-20250514", nil)

	if len(resultNoSession) != len(messages)+1 {
		t.Fatalf("fresh-thread path: expected %d messages, got %d", len(messages)+1, len(resultNoSession))
	}
	for i := range messages {
		if resultNoSession[i].Role != messages[i].Role || resultNoSession[i].Content != messages[i].Content || resultNoSession[i].ToolCallID != messages[i].ToolCallID {
			t.Fatalf("fresh-thread path altered message %d: got %#v want %#v", i, resultNoSession[i], messages[i])
		}
	}
	if !strings.Contains(resultNoSession[len(messages)].Content, "Completed action results:") ||
		!strings.Contains(resultNoSession[len(messages)].Content, "list all go files") {
		t.Error("fresh-thread path should append the complete continuation instruction")
	}
}
