package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestHandleAnthropicMessagesRejectsOrphanToolResultWith400(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"max_tokens":32,
		"messages":[{
			"role":"user",
			"content":[{"type":"tool_result","tool_use_id":"orphan-call","content":"result"}]
		}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleAnthropicMessages(NewAccountPool()).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `unknown tool call id \"orphan-call\"`) {
		t.Fatalf("response did not identify the orphan id: %s", rec.Body.String())
	}
}

func TestValidateToolProtocol(t *testing.T) {
	valid := []ChatMessage{
		{Role: "user", Content: "read it"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Function: ToolCallFunction{Name: "Read", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "call-1", Name: "Read", Content: "contents"},
	}
	if err := validateToolProtocol(valid); err != nil {
		t.Fatalf("valid tool history rejected: %v", err)
	}

	tests := []struct {
		name     string
		messages []ChatMessage
		want     string
	}{
		{
			name:     "missing result id",
			messages: []ChatMessage{{Role: "tool", Content: "contents"}},
			want:     "missing tool_call_id/tool_use_id",
		},
		{
			name:     "orphan result",
			messages: []ChatMessage{{Role: "tool", ToolCallID: "missing", Content: "contents"}},
			want:     `unknown tool call id "missing"`,
		},
		{
			name:     "missing call id",
			messages: []ChatMessage{{Role: "assistant", ToolCalls: []ToolCall{{Function: ToolCallFunction{Name: "Read"}}}}},
			want:     "missing an id",
		},
		{
			name: "duplicate result",
			messages: []ChatMessage{
				{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Function: ToolCallFunction{Name: "Read"}}}},
				{Role: "tool", ToolCallID: "call-1", Name: "Read"},
				{Role: "tool", ToolCallID: "call-1", Name: "Read"},
			},
			want: "more than one result",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateToolProtocol(test.messages)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateToolProtocol() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestFilterNativeSearchToolsPreservesEveryDefinition(t *testing.T) {
	tools := []Tool{
		{Type: "function", Function: ToolFunction{Name: "WebSearch"}},
		{Type: "function", Function: ToolFunction{Name: "WebFetch"}},
		{Type: "function", Function: ToolFunction{Name: "Read"}},
	}
	got, hasWebSearch := filterNativeSearchTools(tools)
	if !hasWebSearch {
		t.Fatal("WebSearch was not detected")
	}
	if len(got) != len(tools) {
		t.Fatalf("tool detection dropped definitions: got %d want %d", len(got), len(tools))
	}
	for i := range tools {
		if got[i].Function.Name != tools[i].Function.Name {
			t.Fatalf("tool %d changed from %q to %q", i, tools[i].Function.Name, got[i].Function.Name)
		}
	}
}

func TestBuildFullTranscriptPreservesCompleteToolHistory(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	callMarker := "CALL_ARGUMENT_TAIL_MUST_SURVIVE"
	resultMarker := "TOOL_RESULT_TAIL_MUST_SURVIVE"
	messages := []ChatMessage{
		{Role: "user", Content: "read the large file"},
		{
			Role:    "assistant",
			Content: "I will read it.",
			ToolCalls: []ToolCall{{
				ID:   "call-large",
				Type: "function",
				Function: ToolCallFunction{
					Name:      "Read",
					Arguments: `{"path":"` + strings.Repeat("p", 16*1024) + callMarker + `"}`,
				},
			}},
		},
		{Role: "tool", ToolCallID: "call-large", Name: "Read", Content: strings.Repeat("r", 64*1024) + resultMarker},
		{Role: "user", Content: "what was at the end?"},
	}

	transcript := buildFullTranscript(
		&Account{UserID: "user", UserEmail: "user@example.com"}, messages,
		"model", false, false, nil, false, nil, "config", "context",
		"2026-07-26T00:00:00Z", true, false, "",
	)
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	for _, want := range []string{
		"read the large file",
		"I will read it.",
		"call-large",
		callMarker,
		resultMarker,
		"what was at the end?",
	} {
		if !strings.Contains(serialized, want) {
			t.Fatalf("full transcript dropped %q", want)
		}
	}
}

func TestCrossAccountToolReplayKeepsHistoryOnFreshThread(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	marker := "ACCOUNT_A_TOOL_RESULT_VISIBLE_TO_ACCOUNT_B"
	history := []ChatMessage{
		{Role: "user", Content: "inspect the file"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-a", Type: "function", Function: ToolCallFunction{Name: "Read", Arguments: `{"path":"README.md"}`}}}},
		{Role: "tool", ToolCallID: "call-a", Name: "Read", Content: "contents\n" + marker},
	}
	tools := make([]Tool, 6)
	for i := range tools {
		tools[i] = Tool{Type: "function", Function: ToolFunction{
			Name:       "action_" + string(rune('1'+i)),
			Parameters: map[string]interface{}{"type": "object"},
		}}
	}
	tools[0].Function.Name = "Read"

	replayed := injectToolsIntoMessages(cloneChatMessages(history), tools, "claude-opus-4.6", nil)
	if len(replayed) != len(history)+1 {
		t.Fatalf("fresh account replay has %d messages, want %d", len(replayed), len(history)+1)
	}
	transcript := buildFullTranscript(
		&Account{UserID: "account-b-user", UserEmail: "b@example.com"}, replayed,
		"model", false, false, nil, false, nil, "config-b", "context-b",
		"2026-07-26T00:00:00Z", true, false, "",
	)
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	for _, want := range []string{"call-a", marker, "account-b-user", "Completed action results"} {
		if !strings.Contains(serialized, want) {
			t.Fatalf("fresh account transcript dropped %q", want)
		}
	}
}

func TestSessionContextInputTokens(t *testing.T) {
	session := &Session{}
	if got := session.ApplyContextInputTokens(98_200, true, false); got != 98_200 {
		t.Fatalf("initial baseline = %d, want 98200", got)
	}
	if got := session.ApplyContextInputTokens(6_600, false, false); got != 104_800 {
		t.Fatalf("continuation context = %d, want 104800", got)
	}
	if got := session.ApplyContextInputTokens(6_600, false, true); got != 104_800 {
		t.Fatalf("repeat grew context to %d", got)
	}

	// Account failover starts a fresh Notion thread whose full replay usage is
	// the new baseline, rather than adding the old account's counter again.
	failover := &Session{}
	if got := failover.ApplyContextInputTokens(109_591, true, false); got != 109_591 {
		t.Fatalf("failover baseline = %d, want 109591", got)
	}
}

func TestSessionContextInputTokensConcurrent(t *testing.T) {
	session := &Session{}
	session.ApplyContextInputTokens(100, true, false)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			session.ApplyContextInputTokens(1, false, false)
		}()
	}
	wg.Wait()
	if got := session.ApplyContextInputTokens(0, false, true); got != 200 {
		t.Fatalf("concurrent context total = %d, want 200", got)
	}
}
