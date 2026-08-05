package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildToolBridgeContractIsStableAndComplete(t *testing.T) {
	tools := []Tool{{
		Type: "function",
		Function: ToolFunction{
			Name:        "action_1",
			Description: "Read a file",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"path"},
			},
		},
	}}

	contract := buildToolBridgeContract(tools, false)
	if !strings.Contains(contract, toolBridgeContractMarker) {
		t.Fatalf("contract is missing its marker: %q", contract)
	}
	if !strings.Contains(contract, "action_1") || !strings.Contains(contract, `"required":["path"]`) {
		t.Fatalf("contract dropped the tool schema: %q", contract)
	}
	if strings.Contains(contract, "ROUTES:") || strings.Contains(contract, "REQUEST:") {
		t.Fatalf("contract still contains per-request routing framing: %q", contract)
	}
	if got := strings.Count(contract, toolBridgeContractMarker); got != 1 {
		t.Fatalf("contract marker count=%d, want 1", got)
	}
	if contract != buildToolBridgeContract(tools, false) {
		t.Fatal("same tool set produced a non-deterministic contract")
	}
	if !strings.Contains(buildToolBridgeContract(tools, true), "replaces the previous") {
		t.Fatal("contract update did not identify itself as a replacement")
	}
}

func TestToolBridgeFingerprintChangesOnlyWhenContractChanges(t *testing.T) {
	base := []Tool{{Type: "function", Function: ToolFunction{
		Name: "action_1", Parameters: map[string]interface{}{"type": "object"},
	}}}
	copyOfBase := []Tool{{Type: "function", Function: ToolFunction{
		Name: "action_1", Parameters: map[string]interface{}{"type": "object"},
	}}}
	changed := []Tool{{Type: "function", Function: ToolFunction{
		Name: "action_1", Parameters: map[string]interface{}{"type": "object", "additionalProperties": false},
	}}}
	if toolBridgeFingerprint(base) != toolBridgeFingerprint(copyOfBase) {
		t.Fatal("equivalent tool sets produced different fingerprints")
	}
	if toolBridgeFingerprint(base) == toolBridgeFingerprint(changed) {
		t.Fatal("changed tool schema kept the old fingerprint")
	}
}

func TestFullTranscriptCarriesBridgeContractOnceOutsideMigrationHistory(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	contract := "[contract-once] action_1"
	messages := []ChatMessage{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
	}
	transcript := buildFullTranscript(
		&Account{UserID: "user", UserEmail: "user@example.com"},
		messages,
		"model", false, false, nil, false, nil,
		"config", "context", "2026-08-04T00:00:00Z", true, false, "",
		contract,
	)
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	if got := strings.Count(serialized, contract); got != 1 {
		t.Fatalf("contract appears %d times, want 1: %s", got, serialized)
	}
	if !strings.Contains(serialized, "first question") || !strings.Contains(serialized, "second question") {
		t.Fatalf("transcript lost conversation messages: %s", serialized)
	}
	if !strings.Contains(serialized, `"type":"user"`) {
		t.Fatalf("contract was not represented as a transcript entry: %s", serialized)
	}
}

func TestOpenCodeStyleSystemPromptIsPreservedWithoutRouteWrapper(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	tools := []Tool{{
		Type: "function",
		Function: ToolFunction{
			Name:        "action_1",
			Description: "Run a command",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}}
	clientSystem := "You are OpenCode. You are powered by the model named claude-opus-5."
	transcript := buildFullTranscript(
		&Account{UserID: "user", UserEmail: "user@example.com"},
		[]ChatMessage{
			{Role: "system", Content: clientSystem},
			{Role: "user", Content: "你好"},
		},
		"model", false, false, nil, false, nil,
		"config", "context", "2026-08-04T00:00:00Z", true, false, "",
		buildToolBridgeContract(tools, false),
	)
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	if strings.Count(serialized, clientSystem) != 1 {
		t.Fatalf("client system prompt was duplicated or dropped: %s", serialized)
	}
	if !strings.Contains(serialized, "你好") || strings.Count(serialized, toolBridgeContractMarker) != 1 {
		t.Fatalf("current user or one-time contract was lost: %s", serialized)
	}
	if strings.Contains(serialized, "ROUTES:") || strings.Contains(serialized, "REQUEST:") {
		t.Fatalf("OpenCode-style transcript still contains per-turn route framing: %s", serialized)
	}
}

func TestPartialTranscriptDoesNotRepeatBridgeContractWhenAbsent(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	session := newConversationSessionForAccount("user@example.com", "account")
	session.TurnCount = 2
	session.UpdatedConfigIDs = []string{"updated-1", "updated-2"}
	transcript := buildPartialTranscript(
		&Account{UserID: "user", UserEmail: "user@example.com"},
		"next question", "model", false, false, nil, false, nil,
		session, "",
	)
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), toolBridgeContractMarker) {
		t.Fatalf("partial continuation unexpectedly repeated the bridge contract: %s", raw)
	}
}

func TestToolBridgeRequestDirectiveIsCompactAndModeSpecific(t *testing.T) {
	messages := []ChatMessage{{Role: "user", Content: "do the task"}}
	got := addToolBridgeDirective(messages, `Tool mode for this request: call exactly "action_2".`)
	if len(got) != 1 || !strings.HasPrefix(got[0].Content, "Tool mode for this request:") || !strings.Contains(got[0].Content, "do the task") {
		t.Fatalf("directive did not stay with the current request: %#v", got)
	}
	if messages[0].Content != "do the task" {
		t.Fatal("adding a directive mutated the caller's message slice")
	}
}

func TestToolBridgeDirectiveStaysVisibleAfterToolResult(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "call-1", Function: ToolCallFunction{Name: "action_1", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "call-1", Name: "action_1", Content: `{"ok":true}`},
	}
	withDirective := addToolBridgeDirective(messages, "Tool mode for this request: none. Do not call any tool; answer naturally.")
	if len(withDirective) != len(messages)+1 || withDirective[1].Content != "" {
		t.Fatalf("directive modified old history instead of current tail: %#v", withDirective)
	}
	continuation := buildPartialContinuationContent(withDirective)
	if !strings.Contains(continuation, "Completed action result") || !strings.Contains(continuation, "Tool mode for this request: none") {
		t.Fatalf("tool result continuation lost the mode directive: %s", continuation)
	}
}
