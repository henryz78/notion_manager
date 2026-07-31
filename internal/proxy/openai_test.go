package proxy

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConvertOpenAIChatCompletionRequest_WithFilesToolsAndJSONSchema(t *testing.T) {
	pdfData := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 mock"))
	imageData := base64.StdEncoding.EncodeToString([]byte("png-bytes"))
	req := &OpenAIChatCompletionRequest{
		Model: "gpt-5.4",
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
}

func TestConvertOpenAIResponsesRequest_WithFunctionCallOutput(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model:        "gpt-5.4",
		Instructions: "Return JSON only.",
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

func TestConvertOpenAIResponsesRequest_WithFunctionCallHistory(t *testing.T) {
	req := &OpenAIResponsesRequest{
		Model: "gpt-5.4",
		Input: []interface{}{
			map[string]interface{}{"type": "input_text", "text": "look up both cities"},
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_beijing",
				"name":      "weather",
				"arguments": `{"city":"Beijing"}`,
			},
			map[string]interface{}{
				"type":      "function_call",
				"call_id":   "call_shanghai",
				"name":      "weather",
				"arguments": map[string]interface{}{"city": "Shanghai"},
			},
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
	if anthReq.Messages[1].Role != "assistant" {
		t.Fatalf("function calls role = %q, want assistant", anthReq.Messages[1].Role)
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

func TestConvertOpenAIResponsesRequest_RejectsInvalidFunctionCallHistory(t *testing.T) {
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
	forced := map[string]interface{}{
		"type":     "function",
		"function": map[string]interface{}{"name": "Read"},
	}
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
