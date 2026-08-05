package proxy

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestConvertOpenAIChatCompletionRequest_WithFilesToolsAndJSONSchema(t *testing.T) {
	pdfData := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 mock"))
	imageData := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	req := &OpenAIChatCompletionRequest{
		Model:          "gpt-5.4",
		PromptCacheKey: "task-chat-123",
		Metadata:       map[string]interface{}{"source": "codex"},
		Messages: []OpenAIChatMessage{
			{Role: "developer", Content: "Always answer in Chinese."},
			{Role: "user", Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "分析这个文件"},
				map[string]interface{}{"type": "image_url", "image_url": map[string]interface{}{"url": "data:image/png;base64," + imageData}},
				map[string]interface{}{"type": "file", "file": map[string]interface{}{"filename": "spec.pdf", "file_data": pdfData}},
			}},
		},
		Tools: []OpenAITool{{
			Type: "function",
			Function: &OpenAIFunctionDefinition{
				Name:        "Read",
				Description: "Read a file",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{"type": "string"},
					},
				},
			},
		}},
		ToolChoice: map[string]interface{}{
			"type":     "function",
			"function": map[string]interface{}{"name": "Read"},
		},
		ResponseFormat: &OpenAIChatResponseFormat{
			Type:       "json_schema",
			JSONSchema: &OpenAIJSONSchemaConfig{Schema: map[string]interface{}{"type": "object"}},
		},
	}

	anthReq, err := convertOpenAIChatCompletionRequest(req)
	if err != nil {
		t.Fatalf("convertOpenAIChatCompletionRequest() error = %v", err)
	}
	if anthReq.Model != "gpt-5.4" {
		t.Fatalf("model = %q, want gpt-5.4", anthReq.Model)
	}
	if anthReq.Metadata["prompt_cache_key"] != "task-chat-123" || anthReq.Metadata["source"] != "codex" {
		t.Fatalf("metadata = %#v", anthReq.Metadata)
	}
	if anthReq.System != "Always answer in Chinese." {
		t.Fatalf("system = %#v", anthReq.System)
	}
	if len(anthReq.Tools) != 1 || anthReq.Tools[0].Name != "Read" {
		t.Fatalf("tools = %#v", anthReq.Tools)
	}
	if anthReq.OutputConfig == nil || anthReq.OutputConfig.Format == nil || anthReq.OutputConfig.Format.Type != "json_schema" {
		t.Fatalf("output_config = %#v", anthReq.OutputConfig)
	}
	if anthReq.Thinking == nil {
		t.Fatal("thinking bridge is not enabled")
	}
	if len(anthReq.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(anthReq.Messages))
	}
	blocks, ok := anthReq.Messages[0].Content.([]interface{})
	if !ok || len(blocks) != 3 {
		t.Fatalf("content blocks = %#v", anthReq.Messages[0].Content)
	}
	first := blocks[0].(map[string]interface{})
	if first["type"] != "text" {
		t.Fatalf("first block = %#v", first)
	}
	second := blocks[1].(map[string]interface{})
	if second["type"] != "image" {
		t.Fatalf("second block = %#v", second)
	}
	third := blocks[2].(map[string]interface{})
	if third["type"] != "document" {
		t.Fatalf("third block = %#v", third)
	}

	_, attachments, err := convertAnthropicMessages(anthReq.System, anthReq.Messages)
	if err != nil {
		t.Fatalf("convertAnthropicMessages() error = %v", err)
	}
	if len(attachments) != 2 {
		t.Fatalf("attachments len = %d, want 2", len(attachments))
	}
	if string(attachments[0].Data) != "png-bytes" || attachments[0].ContentType != "image/png" {
		t.Fatalf("image attachment = %#v", attachments[0])
	}
	if string(attachments[1].Data) != "%PDF-1.4 mock" || attachments[1].ContentType != "application/pdf" {
		t.Fatalf("document attachment = %#v", attachments[1])
	}
}

func TestConvertOpenAIChatCompletionRequestPreservesReasoningEffortAndResponseFormat(t *testing.T) {
	var req OpenAIChatCompletionRequest
	err := json.Unmarshal([]byte(`{
		"model":"gpt-5.4",
		"messages":[{"role":"user","content":"hello"}],
		"reasoning_effort":"high",
		"response_format":{"type":"json_object"}
	}`), &req)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	anthReq, err := convertOpenAIChatCompletionRequest(&req)
	if err != nil {
		t.Fatalf("convertOpenAIChatCompletionRequest() error = %v", err)
	}
	if anthReq.OutputConfig == nil || anthReq.OutputConfig.Effort != "high" {
		t.Fatalf("output_config effort = %#v", anthReq.OutputConfig)
	}
	if anthReq.OutputConfig.Format == nil || anthReq.OutputConfig.Format.Type != "json_schema" {
		t.Fatalf("output_config format = %#v", anthReq.OutputConfig)
	}

	req.ResponseFormat = nil
	anthReq, err = convertOpenAIChatCompletionRequest(&req)
	if err != nil {
		t.Fatalf("convertOpenAIChatCompletionRequest() without response_format error = %v", err)
	}
	if anthReq.OutputConfig == nil || anthReq.OutputConfig.Effort != "high" || anthReq.OutputConfig.Format != nil {
		t.Fatalf("output_config without response_format = %#v", anthReq.OutputConfig)
	}
}

func TestConvertOpenAIResponsesRequest_WithFunctionCallOutput(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model:          "gpt-5.4",
		PromptCacheKey: "task-responses-123",
		Instructions:   "Return JSON only.",
		Input: []interface{}{
			map[string]interface{}{"type": "input_text", "text": "hello"},
			map[string]interface{}{"type": "function_call_output", "call_id": "call_123", "output": "done"},
		},
		Text: &OpenAIResponsesTextConfig{Format: &OpenAIChatResponseFormat{Type: "json_object"}},
	}

	anthReq, err := convertOpenAIResponsesRequest(req)
	if err != nil {
		t.Fatalf("convertOpenAIResponsesRequest() error = %v", err)
	}
	if anthReq.System != "Return JSON only." {
		t.Fatalf("system = %#v", anthReq.System)
	}
	if anthReq.Metadata["prompt_cache_key"] != "task-responses-123" {
		t.Fatalf("metadata = %#v", anthReq.Metadata)
	}
	if anthReq.OutputConfig == nil || anthReq.OutputConfig.Format == nil || anthReq.OutputConfig.Format.Type != "json_schema" {
		t.Fatalf("output_config = %#v", anthReq.OutputConfig)
	}
	if len(anthReq.Messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(anthReq.Messages))
	}
	firstBlocks := anthReq.Messages[0].Content.([]interface{})
	if firstBlocks[0].(map[string]interface{})["type"] != "text" {
		t.Fatalf("first message blocks = %#v", firstBlocks)
	}
	secondBlocks := anthReq.Messages[1].Content.([]interface{})
	toolResult := secondBlocks[0].(map[string]interface{})
	if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call_123" {
		t.Fatalf("tool result = %#v", toolResult)
	}
}

func TestConvertOpenAIResponsesRequestPreservesReasoningEffortAndTextFormat(t *testing.T) {
	var req OpenAIResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"gpt-5.4",
		"input":"hello",
		"reasoning":{"effort":"high"},
		"text":{"format":{"type":"json_object"}}
	}`), &req)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	anthReq, err := convertOpenAIResponsesRequest(&req)
	if err != nil {
		t.Fatalf("convertOpenAIResponsesRequest() error = %v", err)
	}
	if anthReq.OutputConfig == nil || anthReq.OutputConfig.Effort != "high" {
		t.Fatalf("output_config effort = %#v", anthReq.OutputConfig)
	}
	if anthReq.OutputConfig.Format == nil || anthReq.OutputConfig.Format.Type != "json_schema" {
		t.Fatalf("output_config format = %#v", anthReq.OutputConfig)
	}

	req.Text = nil
	anthReq, err = convertOpenAIResponsesRequest(&req)
	if err != nil {
		t.Fatalf("convertOpenAIResponsesRequest() without text format error = %v", err)
	}
	if anthReq.OutputConfig == nil || anthReq.OutputConfig.Effort != "high" || anthReq.OutputConfig.Format != nil {
		t.Fatalf("output_config without text format = %#v", anthReq.OutputConfig)
	}
}

func TestConvertOpenAIResponsesRequestPreservesDeveloperInstructionsAndTaskIdentity(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model:          "kimi-k3",
		PromptCacheKey: "codex-task-123",
		Input: []interface{}{
			map[string]interface{}{
				"type": "message",
				"role": "developer",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "完整保留这段开发者指令。"},
				},
			},
			map[string]interface{}{
				"type": "message",
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": "回答当前问题。"},
				},
			},
		},
	}

	anthReq, err := convertOpenAIResponsesRequest(req)
	if err != nil {
		t.Fatalf("convertOpenAIResponsesRequest() error = %v", err)
	}
	if anthReq.System != "完整保留这段开发者指令。" {
		t.Fatalf("system = %#v", anthReq.System)
	}
	if anthReq.Metadata["prompt_cache_key"] != "codex-task-123" {
		t.Fatalf("metadata = %#v", anthReq.Metadata)
	}
	if len(anthReq.Messages) != 1 || anthReq.Messages[0].Role != "user" {
		t.Fatalf("messages = %#v", anthReq.Messages)
	}
}

func TestConvertOpenAIResponsesRequestExpandsNamespacesAndWebSearch(t *testing.T) {
	var req OpenAIResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"kimi-k3",
		"input":"hello",
		"tools":[
			{"type":"function","name":"same_name","description":"direct","parameters":{"type":"object"}},
			{"type":"namespace","name":"alpha","description":"Alpha namespace.","tools":[
				{"type":"function","name":"same_name","description":"Alpha tool.","parameters":{"type":"object","properties":{"alpha":{"type":"string"}}},"defer_loading":true}
			]},
			{"type":"namespace","name":"beta","tools":[
				{"type":"function","name":"same_name","description":"beta tool","parameters":{"type":"object","properties":{"beta":{"type":"boolean"}}}}
			]},
			{"type":"web_search"}
		],
		"tool_choice":{"type":"function","namespace":"alpha","name":"same_name"}
	}`), &req)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !req.Tools[1].Tools[0].DeferLoading {
		t.Fatal("namespace child defer_loading was not parsed")
	}

	anthReq, aliases, err := convertOpenAIResponsesRequestWithToolAliases(&req)
	if err != nil {
		t.Fatalf("convertOpenAIResponsesRequestWithToolAliases() error = %v", err)
	}
	if len(anthReq.Tools) != 4 {
		t.Fatalf("tools = %#v, want direct + two namespaced + WebSearch", anthReq.Tools)
	}
	if anthReq.Tools[0].Name != "same_name" {
		t.Fatalf("direct tool name = %q, want same_name", anthReq.Tools[0].Name)
	}

	aliasByNamespace := make(map[string]string)
	validAlias := regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	for alias, identity := range aliases {
		if identity.Name != "same_name" {
			t.Fatalf("identity for %q = %#v", alias, identity)
		}
		if !validAlias.MatchString(alias) {
			t.Fatalf("alias %q is not a legal tool name", alias)
		}
		aliasByNamespace[identity.Namespace] = alias
	}
	if aliasByNamespace["alpha"] == "" || aliasByNamespace["beta"] == "" {
		t.Fatalf("aliases = %#v, want alpha and beta identities", aliases)
	}
	if aliasByNamespace["alpha"] == aliasByNamespace["beta"] || aliasByNamespace["alpha"] == "same_name" {
		t.Fatalf("aliases collide: %#v", aliasByNamespace)
	}
	alphaSchema, ok := anthReq.Tools[1].InputSchema.(map[string]interface{})
	if !ok || nestedMapValue(alphaSchema, "properties", "alpha") == nil {
		t.Fatalf("alpha inputSchema was not preserved: %#v", anthReq.Tools[1].InputSchema)
	}
	if anthReq.Tools[1].Description != "Alpha namespace.\n\nAlpha tool." {
		t.Fatalf("alpha description = %q", anthReq.Tools[1].Description)
	}
	if anthReq.Tools[3].Name != "WebSearch" {
		t.Fatalf("web_search mapped to %q, want WebSearch", anthReq.Tools[3].Name)
	}
	_, hasWebSearch := filterNativeSearchTools([]Tool{{
		Type: "function",
		Function: ToolFunction{
			Name:       anthReq.Tools[3].Name,
			Parameters: anthReq.Tools[3].InputSchema,
		},
	}})
	if !hasWebSearch {
		t.Fatal("mapped web_search did not enter the existing native WebSearch path")
	}

	toolChoice, ok := anthReq.ToolChoice.(map[string]interface{})
	if !ok || toolChoice["type"] != "tool" || toolChoice["name"] != aliasByNamespace["alpha"] {
		t.Fatalf("tool_choice = %#v, want forced alpha alias", anthReq.ToolChoice)
	}
}

func TestConvertOpenAIResponsesRequestAcceptsNamespacedFunctionCallHistory(t *testing.T) {
	var req OpenAIResponsesRequest
	err := json.Unmarshal([]byte(`{
		"model":"kimi-k3",
		"tools":[{"type":"namespace","name":"collaboration","description":"Coordinate agents.","tools":[
			{"type":"function","name":"send_message","description":"Send a message.","parameters":{"type":"object","properties":{"target":{"type":"string"}},"required":["target"]},"defer_loading":true}
		]}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"notify the agent"}]},
			{"type":"reasoning","id":"rs_123","summary":[{"type":"summary_text","text":"Need to notify the agent."}]},
			{"type":"function_call","call_id":"call_123","name":"send_message","namespace":"collaboration","arguments":"{\"target\":\"/root\"}"},
			{"type":"function_call_output","call_id":"call_123","output":"delivered"}
		]
	}`), &req)
	if err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	anthReq, aliases, err := convertOpenAIResponsesRequestWithToolAliases(&req)
	if err != nil {
		t.Fatalf("convertOpenAIResponsesRequestWithToolAliases() error = %v", err)
	}
	if len(anthReq.Messages) != 3 {
		t.Fatalf("messages = %#v, want user -> assistant tool_use -> user tool_result", anthReq.Messages)
	}
	if anthReq.Messages[0].Role != "user" || anthReq.Messages[1].Role != "assistant" || anthReq.Messages[2].Role != "user" {
		t.Fatalf("message roles = %#v", anthReq.Messages)
	}
	toolUseBlocks, ok := anthReq.Messages[1].Content.([]interface{})
	if !ok || len(toolUseBlocks) != 1 {
		t.Fatalf("tool use content = %#v", anthReq.Messages[1].Content)
	}
	toolUse := toolUseBlocks[0].(map[string]interface{})
	alias := stringValue(toolUse["name"])
	if identity := aliases[alias]; identity != (openAIToolIdentity{Namespace: "collaboration", Name: "send_message"}) {
		t.Fatalf("tool use alias %q identity = %#v", alias, identity)
	}
	if toolUse["id"] != "call_123" || string(toolUse["input"].(json.RawMessage)) != `{"target":"/root"}` {
		t.Fatalf("tool use = %#v", toolUse)
	}
	toolResultBlocks := anthReq.Messages[2].Content.([]interface{})
	toolResult := toolResultBlocks[0].(map[string]interface{})
	if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call_123" || toolResult["content"] != "delivered" {
		t.Fatalf("tool result = %#v", toolResult)
	}
}

func TestConvertOpenAIResponsesRequestRejectsUnknownNamespacedFunctionCall(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "kimi-k3",
		Tools: []OpenAITool{{
			Type:  "namespace",
			Name:  "known",
			Tools: []OpenAITool{{Type: "function", Name: "run"}},
		}},
		Input: []interface{}{
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_123",
				"name":      "run",
				"namespace": "unknown",
				"arguments": `{}`,
			},
		},
	}

	_, err := convertOpenAIResponsesRequest(req)
	if err == nil || !strings.Contains(err.Error(), `unknown tool "run" in namespace "unknown"`) {
		t.Fatalf("error = %v, want unknown namespaced function_call rejection", err)
	}
}

func TestConvertOpenAIChatCompletionRequestRejectsNamespaceTools(t *testing.T) {
	req := &OpenAIChatCompletionRequest{
		Model:    "gpt-5.4",
		Messages: []OpenAIChatMessage{{Role: "user", Content: "hello"}},
		Tools: []OpenAITool{{
			Type:  "namespace",
			Name:  "collaboration",
			Tools: []OpenAITool{{Type: "function", Name: "send_message"}},
		}},
	}

	_, err := convertOpenAIChatCompletionRequest(req)
	if err == nil || !strings.Contains(err.Error(), `unsupported tool type "namespace" for Chat Completions`) {
		t.Fatalf("error = %v, want explicit Chat namespace rejection", err)
	}
}

func TestConvertOpenAIResponsesRequestRejectsNamespaceOnlyToolChoice(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "kimi-k3",
		Input: "hello",
		Tools: []OpenAITool{{
			Type:  "namespace",
			Name:  "alpha",
			Tools: []OpenAITool{{Type: "function", Name: "run"}},
		}},
		ToolChoice: map[string]interface{}{"type": "namespace", "name": "alpha"},
	}

	_, err := convertOpenAIResponsesRequest(req)
	if err == nil || !strings.Contains(err.Error(), "choose a function within the namespace") {
		t.Fatalf("error = %v, want explicit namespace tool_choice rejection", err)
	}
}

func TestBuildOpenAIResponsesResponseRestoresNamespace(t *testing.T) {
	aliases := map[string]openAIToolIdentity{
		"ns_alpha_same": {Namespace: "alpha", Name: "same_name"},
	}
	resp := buildOpenAIResponsesResponse("resp_test", 123, "kimi-k3", &AnthropicResponse{
		Content: []AnthropicContentBlock{
			{Type: "tool_use", ID: "call_namespaced", Name: "ns_alpha_same", Input: json.RawMessage(`{"value":1}`)},
			{Type: "tool_use", ID: "call_direct", Name: "direct_tool", Input: json.RawMessage(`{"value":2}`)},
		},
	}, aliases)

	output, ok := resp["output"].([]map[string]interface{})
	if !ok || len(output) != 2 {
		t.Fatalf("output = %#v", resp["output"])
	}
	if output[0]["name"] != "same_name" || output[0]["namespace"] != "alpha" {
		t.Fatalf("namespaced output = %#v", output[0])
	}
	if output[1]["name"] != "direct_tool" {
		t.Fatalf("direct output = %#v", output[1])
	}
	if _, exists := output[1]["namespace"]; exists {
		t.Fatalf("direct output unexpectedly has namespace: %#v", output[1])
	}
}

func TestOpenAIResponsesStreamTranscoderRestoresNamespace(t *testing.T) {
	rr := httptest.NewRecorder()
	aliases := map[string]openAIToolIdentity{
		"ns_alpha_same": {Namespace: "alpha", Name: "same_name"},
	}
	transcoder := newOpenAIResponsesStreamTranscoder(rr, rr, "resp_test", "kimi-k3", 456, aliases)
	frames := []anthropicSSEFrame{
		{Event: "message_start", Data: json.RawMessage(`{"message":{"usage":{"input_tokens":9}}}`)},
		{Event: "content_block_start", Data: json.RawMessage(`{"index":0,"content_block":{"type":"tool_use","id":"call_1","name":"ns_alpha_same","input":{}}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":0,"delta":{"type":"input_json_delta","partial_json":"{\"value\":1}"}}`)},
		{Event: "content_block_stop", Data: json.RawMessage(`{"index":0}`)},
		{Event: "message_delta", Data: json.RawMessage(`{"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}`)},
	}
	for _, frame := range frames {
		if err := transcoder.HandleFrame(frame); err != nil {
			t.Fatalf("HandleFrame(%s) error = %v", frame.Event, err)
		}
	}

	body := rr.Body.String()
	if count := strings.Count(body, `"namespace":"alpha"`); count != 3 {
		t.Fatalf("namespace appears %d times, want added/done/completed events: %s", count, body)
	}
	if strings.Contains(body, `"name":"ns_alpha_same"`) || !strings.Contains(body, `"name":"same_name"`) {
		t.Fatalf("stream did not restore original tool identity: %s", body)
	}
}

func TestConvertOpenAIResponsesRequestWithFunctionCallHistory(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.4",
		Input: []interface{}{
			map[string]interface{}{"type": "input_text", "text": "look up both cities"},
			map[string]interface{}{"type": "function_call", "call_id": "call_beijing", "name": "weather", "arguments": `{"city":"Beijing"}`},
			map[string]interface{}{"type": "function_call", "call_id": "call_shanghai", "name": "weather", "arguments": map[string]interface{}{"city": "Shanghai"}},
			map[string]interface{}{"type": "function_call_output", "call_id": "call_beijing", "output": "sunny"},
			map[string]interface{}{"type": "function_call_output", "call_id": "call_shanghai", "output": "rainy"},
			map[string]interface{}{"type": "input_text", "text": "compare them"},
		},
	}

	anthReq, err := convertOpenAIResponsesRequest(req)
	if err != nil {
		t.Fatalf("convertOpenAIResponsesRequest() error = %v", err)
	}
	if len(anthReq.Messages) != 5 {
		t.Fatalf("messages len = %d, want 5: %#v", len(anthReq.Messages), anthReq.Messages)
	}
	callBlocks := anthReq.Messages[1].Content.([]interface{})
	if len(callBlocks) != 2 {
		t.Fatalf("function call blocks = %#v", callBlocks)
	}
	firstCall := callBlocks[0].(map[string]interface{})
	if firstCall["type"] != "tool_use" || firstCall["id"] != "call_beijing" || firstCall["name"] != "weather" {
		t.Fatalf("first function call = %#v", firstCall)
	}
	var arguments map[string]interface{}
	if err := json.Unmarshal(firstCall["input"].(json.RawMessage), &arguments); err != nil {
		t.Fatalf("decode first function arguments: %v", err)
	}
	if arguments["city"] != "Beijing" {
		t.Fatalf("first function arguments = %#v", arguments)
	}
	firstResult := anthReq.Messages[2].Content.([]interface{})[0].(map[string]interface{})
	if firstResult["type"] != "tool_result" || firstResult["tool_use_id"] != "call_beijing" {
		t.Fatalf("first function result = %#v", firstResult)
	}
	lastBlocks := anthReq.Messages[4].Content.([]interface{})
	if lastBlocks[0].(map[string]interface{})["text"] != "compare them" {
		t.Fatalf("last user message = %#v", lastBlocks)
	}
}

func TestConvertOpenAIResponsesRequestRejectsInvalidFunctionCallHistory(t *testing.T) {
	tests := []struct {
		name string
		item map[string]interface{}
		want string
	}{
		{name: "missing call id", item: map[string]interface{}{"type": "function_call", "name": "lookup"}, want: "call_id is required"},
		{name: "missing name", item: map[string]interface{}{"type": "function_call", "call_id": "call_1"}, want: "name is required"},
		{name: "invalid arguments", item: map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": "{"}, want: "invalid JSON arguments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := convertOpenAIResponsesRequest(&OpenAIResponsesRequest{Model: "gpt-5.4", Input: []interface{}{test.item}})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestNormalizeOpenAIToolChoiceModes(t *testing.T) {
	forced := map[string]interface{}{"type": "function", "function": map[string]interface{}{"name": "Read"}}
	tests := []struct {
		name         string
		toolChoice   interface{}
		functionCall interface{}
		wantMode     string
	}{
		{name: "default auto", wantMode: "auto"},
		{name: "auto", toolChoice: "auto", wantMode: "auto"},
		{name: "none", toolChoice: "none", wantMode: "none"},
		{name: "required", toolChoice: "required", wantMode: "required"},
		{name: "named", toolChoice: forced, wantMode: "force:Read"},
		{name: "responses named", toolChoice: map[string]interface{}{"type": "function", "name": "Read"}, wantMode: "force:Read"},
		{name: "legacy none", functionCall: "none", wantMode: "none"},
		{name: "legacy named", functionCall: map[string]interface{}{"name": "Read"}, wantMode: "force:Read"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeOpenAIToolChoice(test.toolChoice, test.functionCall)
			if got := resolveToolChoiceMode(normalized); got != test.wantMode {
				t.Fatalf("normalized mode = %q, want %q (value %#v)", got, test.wantMode, normalized)
			}
		})
	}
}

func TestBuildOpenAIChatCompletionResponse_FromAnthropicBlocks(t *testing.T) {
	stopReason := "tool_use"
	resp := buildOpenAIChatCompletionResponse("chatcmpl_test", 123, "gpt-5.4", &AnthropicResponse{
		Content: []AnthropicContentBlock{
			{Type: "thinking", Thinking: "先判断需要读取哪个文件。"},
			{Type: "text", Text: "先读文件"},
			{Type: "tool_use", ID: "call_1", Name: "Read", Input: json.RawMessage(`{"path":"README.md"}`)},
		},
		StopReason: &stopReason,
		Usage:      &AnthropicUsage{InputTokens: 10, OutputTokens: 5},
	})

	if got := resp.Choices[0].Message["content"]; got != "先读文件" {
		t.Fatalf("content = %#v", got)
	}
	if got := resp.Choices[0].Message["reasoning_content"]; got != "先判断需要读取哪个文件。" {
		t.Fatalf("reasoning_content = %#v", got)
	}
	toolCalls, ok := resp.Choices[0].Message["tool_calls"].([]OpenAIChatToolCall)
	if !ok || len(toolCalls) != 1 {
		t.Fatalf("tool_calls = %#v", resp.Choices[0].Message["tool_calls"])
	}
	if resp.Choices[0].FinishReason == nil || *resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish_reason = %#v", resp.Choices[0].FinishReason)
	}
	if resp.Usage["total_tokens"] != 15 {
		t.Fatalf("usage = %#v", resp.Usage)
	}
}

func TestOpenAIChatStreamTranscoder_EmitsToolCallsAndDone(t *testing.T) {
	rr := httptest.NewRecorder()
	transcoder := newOpenAIChatStreamTranscoder(rr, rr, "chatcmpl_test", "gpt-5.4", 123, true)
	frames := []anthropicSSEFrame{
		{Event: "message_start", Data: json.RawMessage(`{"message":{"usage":{"input_tokens":11}}}`)},
		{Event: "content_block_start", Data: json.RawMessage(`{"index":0,"content_block":{"type":"thinking","thinking":""}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":0,"delta":{"type":"thinking_delta","thinking":"Need to inspect the file."}}`)},
		{Event: "content_block_start", Data: json.RawMessage(`{"index":1,"content_block":{"type":"tool_use","id":"call_1","name":"Read","input":{}}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"README.md\"}"}}`)},
		{Event: "message_delta", Data: json.RawMessage(`{"delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`)},
		{Event: "message_stop", Data: json.RawMessage(`{"type":"message_stop"}`)},
	}
	for _, frame := range frames {
		if err := transcoder.HandleFrame(frame); err != nil {
			t.Fatalf("HandleFrame(%s) error = %v", frame.Event, err)
		}
	}
	body := rr.Body.String()
	if !strings.Contains(body, "chat.completion.chunk") {
		t.Fatalf("body missing chat.completion.chunk: %s", body)
	}
	if !strings.Contains(body, `"tool_calls"`) || !strings.Contains(body, `README.md`) {
		t.Fatalf("body missing tool call data: %s", body)
	}
	if !strings.Contains(body, `"reasoning_content":"Need to inspect the file."`) {
		t.Fatalf("body missing reasoning content: %s", body)
	}
	if !strings.Contains(body, `"usage":{`) || !strings.Contains(body, `"prompt_tokens":11`) || !strings.Contains(body, `"completion_tokens":7`) || !strings.Contains(body, `"total_tokens":18`) {
		t.Fatalf("body missing usage chunk: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("body missing DONE: %s", body)
	}
	thinkingAt := strings.Index(body, `"reasoning_content":"Need to inspect the file."`)
	toolAt := strings.Index(body, `"tool_calls"`)
	argsAt := strings.Index(body, `README.md`)
	usageAt := strings.Index(body, `"prompt_tokens":11`)
	doneAt := strings.Index(body, "data: [DONE]")
	if thinkingAt < 0 || toolAt < thinkingAt || argsAt < toolAt || usageAt < argsAt || doneAt < usageAt {
		t.Fatalf("stream frames out of order: thinking=%d tool=%d args=%d usage=%d done=%d\n%s", thinkingAt, toolAt, argsAt, usageAt, doneAt, body)
	}
}

func TestOpenAIChatStreamTranscoder_StreamsTextDeltasInOrder(t *testing.T) {
	rr := httptest.NewRecorder()
	transcoder := newOpenAIChatStreamTranscoder(rr, rr, "chatcmpl_text", "gpt-5.6-sol", 123, true)
	frames := []anthropicSSEFrame{
		{Event: "message_start", Data: json.RawMessage(`{"message":{"usage":{"input_tokens":120}}}`)},
		{Event: "content_block_start", Data: json.RawMessage(`{"index":0,"content_block":{"type":"text","text":""}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":0,"delta":{"type":"text_delta","text":"first"}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":0,"delta":{"type":"text_delta","text":" second"}}`)},
		{Event: "message_delta", Data: json.RawMessage(`{"delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":120,"output_tokens":2}}`)},
		{Event: "message_stop", Data: json.RawMessage(`{"type":"message_stop"}`)},
	}
	for _, frame := range frames {
		if err := transcoder.HandleFrame(frame); err != nil {
			t.Fatalf("HandleFrame(%s) error = %v", frame.Event, err)
		}
	}
	body := rr.Body.String()
	firstAt := strings.Index(body, `"content":"first"`)
	secondAt := strings.Index(body, `"content":" second"`)
	usageAt := strings.Index(body, `"prompt_tokens":120`)
	doneAt := strings.Index(body, "data: [DONE]")
	if firstAt < 0 || secondAt < firstAt || usageAt < secondAt || doneAt < usageAt {
		t.Fatalf("text stream out of order: first=%d second=%d usage=%d done=%d\n%s", firstAt, secondAt, usageAt, doneAt, body)
	}
}

func TestOpenAIStreamTranscodersExposeAnthropicErrorWithoutSuccessMarker(t *testing.T) {
	errorFrame := anthropicSSEFrame{
		Event: "error",
		Data:  json.RawMessage(`{"type":"error","error":{"type":"api_error","message":"upstream stream interrupted"}}`),
	}

	chatRecorder := httptest.NewRecorder()
	chat := newOpenAIChatStreamTranscoder(chatRecorder, chatRecorder, "chatcmpl_error", "gpt-test", 123, false)
	if err := chat.HandleFrame(anthropicSSEFrame{
		Event: "content_block_delta",
		Data:  json.RawMessage(`{"index":0,"delta":{"type":"text_delta","text":"partial"}}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := chat.HandleFrame(errorFrame); err != nil {
		t.Fatal(err)
	}
	chatBody := chatRecorder.Body.String()
	if !strings.Contains(chatBody, `"error"`) || !strings.Contains(chatBody, "upstream stream interrupted") {
		t.Fatalf("chat stream missing error payload: %s", chatBody)
	}
	if strings.Contains(chatBody, "[DONE]") || strings.Contains(chatBody, `"finish_reason":"stop"`) {
		t.Fatalf("chat stream falsely completed after error: %s", chatBody)
	}

	responsesRecorder := httptest.NewRecorder()
	responses := newOpenAIResponsesStreamTranscoder(responsesRecorder, responsesRecorder, "resp_error", "gpt-test", 123)
	if err := responses.HandleFrame(anthropicSSEFrame{
		Event: "message_start",
		Data:  json.RawMessage(`{"message":{"usage":{"input_tokens":1}}}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := responses.HandleFrame(errorFrame); err != nil {
		t.Fatal(err)
	}
	responsesBody := responsesRecorder.Body.String()
	if !strings.Contains(responsesBody, "event: response.failed") ||
		!strings.Contains(responsesBody, `"status":"failed"`) ||
		!strings.Contains(responsesBody, "upstream stream interrupted") {
		t.Fatalf("responses stream missing failed event: %s", responsesBody)
	}
	if strings.Contains(responsesBody, "event: response.completed") {
		t.Fatalf("responses stream falsely completed after error: %s", responsesBody)
	}
}

func TestOpenAIChatOmitsReasoningContentWhenAnthropicHasNoThinking(t *testing.T) {
	stopReason := "end_turn"
	resp := buildOpenAIChatCompletionResponse("chatcmpl_plain", 123, "gpt-5.4", &AnthropicResponse{
		Content:    []AnthropicContentBlock{{Type: "text", Text: "plain answer"}},
		StopReason: &stopReason,
	})
	if _, exists := resp.Choices[0].Message["reasoning_content"]; exists {
		t.Fatalf("reasoning_content should be omitted: %#v", resp.Choices[0].Message)
	}
}

func TestOpenAIChatStreamTranscoder_UsesInputTokensFromFinalUsage(t *testing.T) {
	rr := httptest.NewRecorder()
	transcoder := newOpenAIChatStreamTranscoder(rr, rr, "chatcmpl_usage", "claude-opus-5", 123, true)
	frames := []anthropicSSEFrame{
		{Event: "message_start", Data: json.RawMessage(`{"message":{"usage":{"input_tokens":0}}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":0,"delta":{"type":"text_delta","text":"ok"}}`)},
		{Event: "message_delta", Data: json.RawMessage(`{"delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":30600,"output_tokens":127}}`)},
		{Event: "message_stop", Data: json.RawMessage(`{"type":"message_stop"}`)},
	}
	for _, frame := range frames {
		if err := transcoder.HandleFrame(frame); err != nil {
			t.Fatalf("HandleFrame(%s) error = %v", frame.Event, err)
		}
	}
	body := rr.Body.String()
	for _, want := range []string{`"prompt_tokens":30600`, `"completion_tokens":127`, `"total_tokens":30727`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing final usage %s:\n%s", want, body)
		}
	}
}

func TestOpenAIResponsesStreamTranscoder_EmitsCompletedResponse(t *testing.T) {
	rr := httptest.NewRecorder()
	transcoder := newOpenAIResponsesStreamTranscoder(rr, rr, "resp_test", "gpt-5.4", 456)
	frames := []anthropicSSEFrame{
		{Event: "message_start", Data: json.RawMessage(`{"message":{"usage":{"input_tokens":0}}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":0,"delta":{"type":"text_delta","text":"你好"}}`)},
		{Event: "content_block_start", Data: json.RawMessage(`{"index":1,"content_block":{"type":"tool_use","id":"call_2","name":"Read","input":{}}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.txt\"}"}}`)},
		{Event: "content_block_stop", Data: json.RawMessage(`{"index":1}`)},
		{Event: "message_delta", Data: json.RawMessage(`{"delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":9,"output_tokens":6}}`)},
	}
	for _, frame := range frames {
		if err := transcoder.HandleFrame(frame); err != nil {
			t.Fatalf("HandleFrame(%s) error = %v", frame.Event, err)
		}
	}
	body := rr.Body.String()
	for _, required := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.content_part.done",
		"event: response.output_item.done",
		"event: response.function_call_arguments.delta",
		"event: response.completed",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("missing %s in body:\n%s", required, body)
		}
	}
	if !strings.Contains(body, "你好") {
		t.Fatalf("missing text content: %s", body)
	}
	if !strings.Contains(body, `a.txt`) {
		t.Fatalf("missing function call arguments: %s", body)
	}
	for _, want := range []string{`"input_tokens":9`, `"output_tokens":6`, `"total_tokens":15`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing final usage %s:\n%s", want, body)
		}
	}
}

func TestOpenAIResponsesStreamTranscoder_ThinkingBlocks(t *testing.T) {
	rr := httptest.NewRecorder()
	transcoder := newOpenAIResponsesStreamTranscoder(rr, rr, "resp_think", "claude-opus-4.6", 789)
	frames := []anthropicSSEFrame{
		{Event: "message_start", Data: json.RawMessage(`{"message":{"usage":{"input_tokens":5}}}`)},
		{Event: "content_block_start", Data: json.RawMessage(`{"index":0,"content_block":{"type":"thinking","thinking":""}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":0,"delta":{"type":"signature_delta","signature":"sig123"}}`)},
		{Event: "content_block_stop", Data: json.RawMessage(`{"index":0}`)},
		{Event: "content_block_start", Data: json.RawMessage(`{"index":1,"content_block":{"type":"text","text":""}}`)},
		{Event: "content_block_delta", Data: json.RawMessage(`{"index":1,"delta":{"type":"text_delta","text":"Hello!"}}`)},
		{Event: "content_block_stop", Data: json.RawMessage(`{"index":1}`)},
		{Event: "message_delta", Data: json.RawMessage(`{"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`)},
	}
	for _, frame := range frames {
		if err := transcoder.HandleFrame(frame); err != nil {
			t.Fatalf("HandleFrame(%s) error = %v", frame.Event, err)
		}
	}
	body := rr.Body.String()
	for _, required := range []string{
		"event: response.created",
		"event: response.in_progress",
		"event: response.output_item.added",
		"event: response.reasoning_summary_part.added",
		"event: response.reasoning_summary_text.delta",
		"event: response.reasoning_summary_text.done",
		"event: response.reasoning_summary_part.done",
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.content_part.done",
		"event: response.output_item.done",
		"event: response.completed",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("missing %s in body:\n%s", required, body)
		}
	}
	if !strings.Contains(body, "Let me think...") {
		t.Fatalf("missing thinking text in body:\n%s", body)
	}
	if !strings.Contains(body, "Hello!") {
		t.Fatalf("missing text content in body:\n%s", body)
	}
}
