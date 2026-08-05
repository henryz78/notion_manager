package proxy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestApplyStructuredOutputBridge_JSONSchema(t *testing.T) {
	messages := []ChatMessage{
		{Role: "system", Content: "x-anthropic-billing-header: cc_version=2.1.81; cch=aaaa;"},
		{Role: "system", Content: "You are Claude Code, Anthropic's official CLI for Claude."},
		{Role: "system", Content: "Generate a concise title.\nReturn JSON with a single \"title\" field."},
		{Role: "user", Content: "检查为什么右侧预览栏的md copy按钮出不来"},
	}
	cfg := &AnthropicOutputConfig{
		Format: &AnthropicOutputFormat{
			Type: "json_schema",
			Schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title": map[string]interface{}{"type": "string"},
				},
				"required":             []string{"title"},
				"additionalProperties": false,
			},
		},
	}

	bridged := applyStructuredOutputBridge(messages, cfg)
	if len(bridged) != len(messages) {
		t.Fatalf("expected %d preserved messages, got %d", len(messages), len(bridged))
	}
	for i := 0; i < len(messages)-1; i++ {
		if bridged[i].Role != messages[i].Role || bridged[i].Content != messages[i].Content {
			t.Fatalf("structured output bridge altered message %d", i)
		}
	}

	content := bridged[len(bridged)-1].Content
	if !strings.Contains(content, "检查为什么右侧预览栏的md copy按钮出不来") {
		t.Fatalf("structured output bridge dropped user content: %s", content)
	}
	if !strings.Contains(content, `"title": {`) || !strings.Contains(content, `"required": [`) {
		t.Fatalf("structured output bridge did not embed schema JSON: %s", content)
	}
}

func TestToolBridgeContractPreservesClientToolSchemas(t *testing.T) {
	tools := []Tool{
		{Type: "function", Function: ToolFunction{Name: "Bash", Description: "Execute shell command", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Read", Description: "Read a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Write", Description: "Write a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Edit", Description: "Edit a file", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Glob", Description: "Find files", Parameters: map[string]interface{}{"type": "object"}}},
		{Type: "function", Function: ToolFunction{Name: "Grep", Description: "Search files", Parameters: map[string]interface{}{"type": "object"}}},
	}
	contract := buildToolBridgeContract(func() []Tool {
		aliased, _, _ := aliasClientTools(tools)
		return aliased
	}(), false)
	if !strings.Contains(contract, toolBridgeContractMarker) || !strings.Contains(contract, "action_6") {
		t.Fatalf("contract did not preserve the complete tool set: %q", contract)
	}
	if strings.Contains(contract, "ROUTES:") || strings.Contains(contract, "REQUEST:") {
		t.Fatalf("contract contains per-request framing: %q", contract)
	}
}

func TestToolBridgeContractWorksAcrossModelFamilies(t *testing.T) {
	tools := []Tool{{
		Type: "function",
		Function: ToolFunction{
			Name:        "get_test_value",
			Description: "Return a test value",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"key": map[string]interface{}{"type": "string"},
				},
				"required": []interface{}{"key"},
			},
		},
	}}

	for _, model := range []string{"claude-opus-5", "gpt-5.6-sol", "gemini-3.1-pro", "grok-4.5", "deepseek-v4-pro"} {
		t.Run(model, func(t *testing.T) {
			got := buildToolBridgeContract(tools, false)
			if !strings.Contains(got, "get_test_value") || !strings.Contains(got, `"required":["key"]`) {
				t.Fatalf("model %s did not receive the complete tool schema: %q", model, got)
			}
		})
	}
}

func TestToolBridgeForcedModeUsesCompactDirective(t *testing.T) {
	got := toolBridgeDirective("force:get_test_value", map[string]string{"get_test_value": "action_1"})
	if !strings.Contains(got, `"action_1"`) || strings.Contains(got, "get_test_value") {
		t.Fatalf("forced tool directive was not aliased: %q", got)
	}
}

func TestPrepareToolBridgeResponse_FiltersUndeclaredNotionTools(t *testing.T) {
	native := []AgentValueEntry{
		{Type: "tool_use", ID: "internal-1", Name: "callFunction", Input: json.RawMessage(`{"function":"connections.search.search"}`)},
		{Type: "tool_use", ID: "client-1", Name: "get_test_value", Input: json.RawMessage(`{"key":"alpha"}`)},
	}
	prepared := prepareToolBridgeResponse("", native, map[string]struct{}{"get_test_value": {}}, nil)

	if prepared.DroppedCalls != 1 || !prepared.HasCalls || len(prepared.ToolCalls) != 1 {
		t.Fatalf("unexpected filtered response: %+v", prepared)
	}
	if prepared.ToolCalls[0].Function.Name != "get_test_value" || prepared.ToolCalls[0].Function.Arguments != `{"key":"alpha"}` {
		t.Fatalf("declared tool call changed: %+v", prepared.ToolCalls[0])
	}
}

func TestPrepareToolBridgeResponse_FiltersUndeclaredTextTool(t *testing.T) {
	prepared := prepareToolBridgeResponse(
		`{"name":"callFunction","arguments":{"function":"connections.fs.readFiles"}}`,
		nil,
		map[string]struct{}{"get_test_value": {}},
		nil,
	)

	if prepared.HasCalls || len(prepared.ToolCalls) != 0 || prepared.DroppedCalls != 1 {
		t.Fatalf("undeclared text tool leaked through: %+v", prepared)
	}
}

func TestPrepareToolBridgeResponse_UsesDeclaredTextToolAfterInternalNativeTool(t *testing.T) {
	prepared := prepareToolBridgeResponse(
		`{"name":"get_test_value","arguments":{"key":"alpha"}}`,
		[]AgentValueEntry{{Type: "tool_use", ID: "internal-1", Name: "callFunction"}},
		map[string]struct{}{"get_test_value": {}},
		nil,
	)

	if !prepared.HasCalls || len(prepared.ToolCalls) != 1 || prepared.DroppedCalls != 1 {
		t.Fatalf("declared text tool was not recovered: %+v", prepared)
	}
	if prepared.ToolCalls[0].Function.Name != "get_test_value" {
		t.Fatalf("wrong recovered tool: %+v", prepared.ToolCalls[0])
	}
}

func TestPrepareToolBridgeResponseRejectsEmptyDoneResult(t *testing.T) {
	prepared := prepareToolBridgeResponse(
		`{"name":"__done__","arguments":{}}`,
		nil,
		map[string]struct{}{"get_test_value": {}},
		nil,
	)
	if !prepared.InvalidDone || prepared.DoneText != "" || prepared.HasCalls {
		t.Fatalf("empty __done__ result should request recovery: %+v", prepared)
	}
}

func TestPrepareToolBridgeResponseAcceptsDoneAlias(t *testing.T) {
	prepared := prepareToolBridgeResponse(
		`{"name":"done","arguments":{"result":"Clipboard contents: https://example.com"}}`,
		nil,
		map[string]struct{}{"clipboard_tool": {}},
		nil,
	)
	if prepared.DoneText != "Clipboard contents: https://example.com" || prepared.HasCalls || prepared.DroppedCalls != 0 {
		t.Fatalf("done alias should be intercepted as __done__: %+v", prepared)
	}
}

func TestPrepareToolBridgeResponseAcceptsSentinelWrappedDoneAlias(t *testing.T) {
	for _, content := range []string{
		`<|{"name":"done","arguments":{"result":"I can see the earlier context."}}`,
		`<|{"name":"done","arguments":{"result":"I can see the earlier context."}}|>`,
	} {
		prepared := prepareToolBridgeResponse(content, nil, map[string]struct{}{"clipboard_tool": {}}, nil)
		if prepared.DoneText != "I can see the earlier context." || prepared.HasCalls || prepared.DroppedCalls != 0 || prepared.Remaining != "" {
			t.Fatalf("sentinel-wrapped done alias should be intercepted: %+v", prepared)
		}
	}
}

func TestPrepareToolBridgeResponseAcceptsSentinelActionWithTrailingProtocolMarker(t *testing.T) {
	prepared := prepareToolBridgeResponse(
		`<|{"name":"action_3","arguments":{"action":"read"}}<|eot_id|>`,
		nil,
		map[string]struct{}{"clipboard": {}},
		map[string]string{"action_3": "clipboard"},
	)

	if prepared.Protocol != "sentinel_json" || !prepared.HasCalls || len(prepared.ToolCalls) != 1 || prepared.DroppedCalls != 0 {
		t.Fatalf("sentinel action was not recovered: %+v", prepared)
	}
	call := prepared.ToolCalls[0]
	if call.Function.Name != "clipboard" || call.Function.Arguments != `{"action":"read"}` {
		t.Fatalf("sentinel action was not restored to the client tool: %+v", call)
	}
}

func TestPrepareToolBridgeResponseClassifiesNativeTextAndDone(t *testing.T) {
	allowed := map[string]struct{}{"lookup": {}}
	native := prepareToolBridgeResponse("", []AgentValueEntry{{Type: "tool_use", Name: "lookup", Input: json.RawMessage(`{}`)}}, allowed, nil)
	if native.Protocol != "native" || !native.HasCalls {
		t.Fatalf("unexpected native bridge result: %+v", native)
	}

	text := prepareToolBridgeResponse(`{"name":"lookup","arguments":{}}`, nil, allowed, nil)
	if text.Protocol != "text_json" || !text.HasCalls {
		t.Fatalf("unexpected text bridge result: %+v", text)
	}

	done := prepareToolBridgeResponse(`{"name":"__done__","arguments":{"result":"complete"}}`, nil, map[string]struct{}{}, nil)
	if done.Protocol != "done" || done.DoneText != "complete" || done.HasCalls {
		t.Fatalf("unexpected done bridge result: %+v", done)
	}
}

func TestParseToolCallsDoesNotTreatMalformedSentinelAsAction(t *testing.T) {
	for _, content := range []string{
		`<|this is ordinary malformed output`,
		`<|{"arguments":{"action":"read"}}`,
	} {
		toolCalls, remaining, ok := parseToolCalls(content)
		if ok || len(toolCalls) != 0 || remaining != content {
			t.Fatalf("malformed sentinel should remain plain text: calls=%+v remaining=%q ok=%v", toolCalls, remaining, ok)
		}
	}
}

func TestParseToolCallsDoesNotTreatOrdinaryNamedJSONAsAction(t *testing.T) {
	content := `{"name":"Alice","answer":"ordinary JSON"}`
	toolCalls, remaining, ok := parseToolCalls(content)
	if ok || len(toolCalls) != 0 || remaining != content {
		t.Fatalf("ordinary named JSON became a tool call: calls=%+v remaining=%q ok=%v", toolCalls, remaining, ok)
	}
	prepared := prepareToolBridgeResponse(content, nil, map[string]struct{}{"lookup": {}}, nil)
	if prepared.HasCalls || prepared.Remaining != content {
		t.Fatalf("ordinary named JSON was removed by bridge preparation: %+v", prepared)
	}
}

func TestPrepareToolBridgeResponsePreservesDeclaredDoneTool(t *testing.T) {
	prepared := prepareToolBridgeResponse(
		`{"name":"done","arguments":{"value":"client action"}}`,
		nil,
		map[string]struct{}{"done": {}},
		nil,
	)
	if !prepared.HasCalls || len(prepared.ToolCalls) != 1 || prepared.ToolCalls[0].Function.Name != "done" || prepared.DoneText != "" {
		t.Fatalf("declared client tool named done should remain a real tool call: %+v", prepared)
	}
}

func TestClientToolAliasesRoundTripToOriginalName(t *testing.T) {
	tools := []Tool{{
		Type: "function",
		Function: ToolFunction{
			Name:        "get_test_value",
			Description: "Call get_test_value for a key",
			Parameters:  map[string]interface{}{"type": "object"},
		},
	}}
	aliased, originalToAlias, aliasToOriginal := aliasClientTools(tools)
	if aliased[0].Function.Name != "action_1" || strings.Contains(aliased[0].Function.Description, "get_test_value") {
		t.Fatalf("tool definition was not anonymized: %+v", aliased[0])
	}
	if originalToAlias["get_test_value"] != "action_1" || aliasToOriginal["action_1"] != "get_test_value" {
		t.Fatalf("alias maps are inconsistent: original=%v alias=%v", originalToAlias, aliasToOriginal)
	}
	choice := aliasToolChoice(map[string]interface{}{"type": "tool", "name": "get_test_value"}, originalToAlias)
	choiceMap := choice.(map[string]interface{})
	if choiceMap["name"] != "action_1" {
		t.Fatalf("Anthropic forced tool choice was not aliased: %v", choiceMap)
	}
	openAIChoice := aliasToolChoice(map[string]interface{}{
		"type":     "function",
		"function": map[string]interface{}{"name": "get_test_value"},
	}, originalToAlias).(map[string]interface{})
	if openAIChoice["function"].(map[string]interface{})["name"] != "action_1" {
		t.Fatalf("OpenAI forced tool choice was not aliased: %v", openAIChoice)
	}
	aliasedMessages := aliasToolNamesInMessages([]ChatMessage{{
		Role: "assistant",
		ToolCalls: []ToolCall{{
			ID: "call-1", Function: ToolCallFunction{Name: "get_test_value", Arguments: `{}`},
		}},
	}}, originalToAlias)
	if aliasedMessages[0].ToolCalls[0].Function.Name != "action_1" {
		t.Fatalf("assistant tool history was not aliased: %+v", aliasedMessages)
	}

	prepared := prepareToolBridgeResponse(
		`{"name":"action_1","arguments":{"key":"alpha"}}`,
		nil,
		map[string]struct{}{"get_test_value": {}},
		aliasToOriginal,
	)
	if !prepared.HasCalls || len(prepared.ToolCalls) != 1 || prepared.ToolCalls[0].Function.Name != "get_test_value" {
		t.Fatalf("alias was not restored before returning to the client: %+v", prepared)
	}
}

func TestLargeClientToolSetAliasesEveryTool(t *testing.T) {
	tools := make([]Tool, 6)
	for i := range tools {
		tools[i] = Tool{Type: "function", Function: ToolFunction{Name: fmt.Sprintf("tool_%d", i)}}
	}
	aliased, originalToAlias, aliasToOriginal := aliasClientTools(tools)
	if len(aliased) != 6 || aliased[0].Function.Name != "action_1" || aliased[5].Function.Name != "action_6" {
		t.Fatalf("large tool set was not fully anonymized: %+v", aliased)
	}
	if originalToAlias["tool_5"] != "action_6" || aliasToOriginal["action_6"] != "tool_5" {
		t.Fatalf("large tool aliases are inconsistent: original=%v alias=%v", originalToAlias, aliasToOriginal)
	}
}

func TestLargeClientToolSetUsesAnonymousLabelsAndFullSchemasBelowLimit(t *testing.T) {
	params := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"value": map[string]interface{}{"type": "string"}},
	}
	tools := []Tool{
		{Type: "function", Function: ToolFunction{Name: "Bash", Description: "Run a shell command.", Parameters: params}},
		{Type: "function", Function: ToolFunction{Name: "Read", Description: "Read a file.", Parameters: params}},
		{Type: "function", Function: ToolFunction{Name: "Edit", Description: "Edit a file.", Parameters: params}},
		{Type: "function", Function: ToolFunction{Name: "Write", Description: "Write a file.", Parameters: params}},
		{Type: "function", Function: ToolFunction{Name: "Glob", Description: "Find files.", Parameters: params}},
		{Type: "function", Function: ToolFunction{Name: "Grep", Description: "Search file contents.", Parameters: params}},
	}
	aliased, _, _ := aliasClientTools(tools)
	got := buildToolBridgeContract(aliased, false)
	if !strings.Contains(got, "action_6") || strings.Contains(got, "Grep") || !strings.Contains(got, "Argument schema:") {
		t.Fatalf("tool contract did not preserve anonymous schemas: %q", got)
	}
}

func TestBuildSizedToolListNeverCompactsClientSchemas(t *testing.T) {
	smallTools := make([]Tool, 6)
	for i := range smallTools {
		smallTools[i] = Tool{Type: "function", Function: ToolFunction{
			Name:        fmt.Sprintf("action_%d", i+1),
			Description: "A small tool.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"mode": map[string]interface{}{"type": "string", "enum": []interface{}{"fast", "exact"}},
				},
			},
		}}
	}
	list, compacted, fullBytes := buildSizedToolList(smallTools)
	if compacted || fullBytes != len(list) || !strings.Contains(list, `"enum":["fast","exact"]`) {
		t.Fatalf("six small tools should retain full schemas: compacted=%v bytes=%d list=%q", compacted, fullBytes, list)
	}

	marker := "SCHEMA_TAIL_MUST_SURVIVE"
	hugeTool := Tool{Type: "function", Function: ToolFunction{
		Name:        "action_1",
		Description: strings.Repeat("x", 64*1024) + marker,
		Parameters:  map[string]interface{}{"type": "object", "description": marker},
	}}
	list, compacted, fullBytes = buildSizedToolList([]Tool{hugeTool})
	if compacted || fullBytes != len(list) || !strings.Contains(list, "Argument schema:") || strings.Count(list, marker) != 2 {
		t.Fatalf("oversized tool definition was altered: compacted=%v bytes=%d sent=%d", compacted, fullBytes, len(list))
	}
}

func TestNormalizeStructuredOutputText_StripsLangTagAndMarkdownFence(t *testing.T) {
	raw := "<lang primary=\"zh-CN\"/>\n\n```json\n{\"title\":\"Fix digest error\"}\n```"
	got := normalizeStructuredOutputText(raw)
	want := "{\"title\":\"Fix digest error\"}"
	if got != want {
		t.Fatalf("normalizeStructuredOutputText() = %q, want %q", got, want)
	}
}

func TestNormalizeStructuredOutputText_ExtractsJSONObjectFromPrefixedText(t *testing.T) {
	raw := "Here is the JSON output you requested:\n{\"title\":\"Fix invalid password\"}"
	got := normalizeStructuredOutputText(raw)
	want := "{\"title\":\"Fix invalid password\"}"
	if got != want {
		t.Fatalf("normalizeStructuredOutputText() = %q, want %q", got, want)
	}
}
