package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolChainAutoAnswersNaturallyWhenResultsAreComplete(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "read the file"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Function: ToolCallFunction{Name: "Read", Arguments: `{"path":"a.txt"}`}}}},
		{Role: "tool", ToolCallID: "call-1", Name: "Read", Content: "the answer is 42"},
	}

	followUp := buildToolChainFollowUp(messages, "- action_1\n", "", "auto")
	if len(followUp) != 1 {
		t.Fatalf("follow-up messages = %d, want 1", len(followUp))
	}
	content := followUp[0].Content
	if !strings.Contains(content, "answer directly in natural language") {
		t.Fatalf("auto mode still forces a tool wrapper: %s", content)
	}
	if strings.Contains(content, "__done__") {
		t.Fatalf("auto mode still advertises the legacy done action: %s", content)
	}
}

func TestToolChainRequiredStillRequiresAClientAction(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "inspect the file"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Function: ToolCallFunction{Name: "Read", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "call-1", Name: "Read", Content: "result"},
	}
	content := buildToolChainFollowUp(messages, "- action_1\n", "", "required")[0].Content
	if !strings.Contains(content, "Select one action") || strings.Contains(content, "__done__") {
		t.Fatalf("required mode contract is wrong: %s", content)
	}
}

func TestResolveToolChoiceMode(t *testing.T) {
	tests := []struct {
		name   string
		choice interface{}
		want   string
	}{
		{name: "omitted", choice: nil, want: "auto"},
		{name: "auto", choice: "auto", want: "auto"},
		{name: "none", choice: "none", want: "none"},
		{name: "required", choice: "required", want: "required"},
		{name: "anthropic any", choice: map[string]interface{}{"type": "any"}, want: "required"},
		{name: "anthropic named", choice: map[string]interface{}{"type": "tool", "name": "Read"}, want: "force:Read"},
		{name: "openai named", choice: map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "Read"}}, want: "force:Read"},
		{name: "responses named", choice: map[string]interface{}{"type": "function", "name": "Read"}, want: "force:Read"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveToolChoiceMode(test.choice); got != test.want {
				t.Fatalf("resolveToolChoiceMode(%#v) = %q, want %q", test.choice, got, test.want)
			}
		})
	}
}

func TestToolChoiceNoneBypassesInjection(t *testing.T) {
	messages := []ChatMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "answer normally"}}
	tools := []Tool{{Type: "function", Function: ToolFunction{Name: "Read", Description: "read", Parameters: map[string]interface{}{"type": "object"}}}}
	got := injectToolsIntoMessages(cloneChatMessages(messages), tools, "none")
	if len(got) != len(messages) {
		t.Fatalf("messages len = %d, want %d", len(got), len(messages))
	}
	for i := range messages {
		if got[i].Role != messages[i].Role || got[i].Content != messages[i].Content {
			t.Fatalf("message %d changed in none mode: got=%#v want=%#v", i, got[i], messages[i])
		}
	}
}

func TestToolProtocolPrefixState(t *testing.T) {
	tests := []struct {
		input     string
		protocol  bool
		undecided bool
	}{
		{"", false, true},
		{"  <", false, true},
		{`{"name":"action_1"`, false, true},
		{`{"name":"action_1","arguments":`, true, false},
		{`{"answer":`, false, false},
		{`<|{"name":"action_1"`, true, false},
		{"```go\nfmt.Println", false, false},
		{"```json\n{\"answer\":", false, false},
		{"ordinary answer", false, false},
	}
	for _, test := range tests {
		protocol, undecided := toolProtocolPrefixState(test.input)
		if protocol != test.protocol || undecided != test.undecided {
			t.Fatalf("prefix %q = (%v,%v), want (%v,%v)", test.input, protocol, undecided, test.protocol, test.undecided)
		}
	}
}

func TestIncrementalToolStreamReleasesOrdinaryTextImmediately(t *testing.T) {
	stream := newIncrementalToolStream("auto")
	if got := stream.Push("H"); got != "H" {
		t.Fatalf("first ordinary delta = %q, want H", got)
	}
	if got := stream.Push("ello"); got != "ello" {
		t.Fatalf("second ordinary delta = %q, want ello", got)
	}
	if stream.mode != "text" {
		t.Fatalf("ordinary response mode = %q, want text", stream.mode)
	}
}

func TestIncrementalToolStreamBuffersSplitToolJSON(t *testing.T) {
	stream := newIncrementalToolStream("auto")
	for _, delta := range []string{" ", "{", `"name":"action_1",`, `"arguments":{"path":"a"}}`} {
		if got := stream.Push(delta); got != "" {
			t.Fatalf("tool protocol leaked as text: %q", got)
		}
	}
	if stream.mode != "protocol" {
		t.Fatalf("tool response mode = %q, want protocol", stream.mode)
	}
}

func TestIncrementalToolStreamReleasesOrdinaryJSONAndCodeFence(t *testing.T) {
	for _, chunks := range [][]string{
		{`{"answer":`, `"plain JSON"}`},
		{"```go\n", "fmt.Println(\"hello\")\n```"},
		{"```json\n", `{"answer":`, "42}\n```"},
	} {
		stream := newIncrementalToolStream("auto")
		var output strings.Builder
		for _, chunk := range chunks {
			output.WriteString(stream.Push(chunk))
		}
		if stream.mode != "text" || output.String() == "" {
			t.Fatalf("ordinary structured answer remained buffered: mode=%q output=%q chunks=%q", stream.mode, output.String(), chunks)
		}
	}
}

func TestAllowedToolNamesForChoiceRestrictsForcedTool(t *testing.T) {
	tools := []AnthropicTool{{Name: "A"}, {Name: "B"}}
	allowed, err := allowedToolNamesForChoice(tools, "force:A")
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 1 {
		t.Fatalf("allowed tools=%v, want only A", allowed)
	}
	if _, ok := allowed["A"]; !ok {
		t.Fatalf("forced tool A is not allowed: %v", allowed)
	}
	if _, err := allowedToolNamesForChoice(tools, "force:missing"); err == nil {
		t.Fatal("undeclared forced tool was accepted")
	}
}

func TestIncrementalToolStreamRequiredKeepsResponseRetryable(t *testing.T) {
	stream := newIncrementalToolStream("required")
	if got := stream.Push("plain text instead of a tool call"); got != "" {
		t.Fatalf("required response leaked before validation: %q", got)
	}
	if stream.mode != "protocol" {
		t.Fatalf("required response mode = %q, want protocol", stream.mode)
	}
}

func TestReportedInputTokensDoesNotAccumulate(t *testing.T) {
	diagnostic := newRequestDiagnostic("anthropic")
	if got := reportedInputTokens(98_200, diagnostic); got != 98_200 {
		t.Fatalf("first upstream input = %d", got)
	}
	if got := reportedInputTokens(6_600, diagnostic); got != 6_600 {
		t.Fatalf("second upstream input accumulated to %d", got)
	}
	entry := diagnostic.finish(200)
	if entry.ContextTokens != 6_600 {
		t.Fatalf("stored upstream input = %d, want 6600", entry.ContextTokens)
	}
}

func TestFullTranscriptIncludesClientSystemExactlyOnce(t *testing.T) {
	previousConfig := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previousConfig })

	const marker = "SYSTEM_MARKER_MUST_APPEAR_ONCE_731"
	messages := []ChatMessage{
		{Role: "system", Content: marker},
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
	}
	transcript := buildFullTranscript(
		&Account{UserID: "user", SpaceID: "space"}, messages, "model", false, false, nil, false, nil,
		"config", "context", "2026-07-27T00:00:00Z", true, false, "",
	)
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(raw), marker); count != 1 {
		t.Fatalf("client system marker appears %d times in one inference transcript", count)
	}
	for _, marker := range []string{"first question", "first answer", "second question"} {
		if !strings.Contains(string(raw), marker) {
			t.Fatalf("full transcript dropped %q", marker)
		}
	}
}
