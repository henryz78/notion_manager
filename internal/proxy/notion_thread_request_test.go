package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type contextObservingRoundTripper struct {
	started  chan struct{}
	canceled chan struct{}
}

func (rt *contextObservingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	close(rt.started)
	<-req.Context().Done()
	close(rt.canceled)
	return nil, req.Context().Err()
}

func TestCallInferenceCancelsUpstreamWithRequestContext(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})

	transport := &contextObservingRoundTripper{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	NotionAPIBase = "https://notion.invalid"
	chromeHTTPClientForTest = func(time.Duration) *http.Client {
		return &http.Client{Transport: transport}
	}
	t.Cleanup(func() {
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- CallInference(
			&Account{UserID: "user", UserEmail: "user@example.com", SpaceID: "space", TokenV2: "token"},
			[]ChatMessage{{Role: "user", Content: "wait"}},
			"gpt-test",
			false,
			func(string, bool, *UsageInfo) {},
			CallOptions{Context: ctx},
		)
	}()
	select {
	case <-transport.started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CallInference error=%v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CallInference did not stop after request cancellation")
	}
	select {
	case <-transport.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream HTTP request context was not canceled")
	}
}

func TestCallInferenceCreatesThenContinuesRealNotionThread(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})
	chromeHTTPClientForTest = func(timeout time.Duration) *http.Client {
		return &http.Client{Timeout: timeout}
	}
	t.Cleanup(func() {
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	var mu sync.Mutex
	var requests []NotionInferenceRequest
	storedAgentReplies := make(map[string]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runInferenceTranscript" {
			http.NotFound(w, r)
			return
		}
		var body NotionInferenceRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		reply := "My marker is cobalt."
		if body.IsPartialTranscript {
			if storedAgentReplies[body.ThreadID] == reply {
				reply = "I remember my previous answer: cobalt."
			} else {
				reply = "I cannot see my previous answer."
			}
		} else {
			storedAgentReplies[body.ThreadID] = reply
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":%q}],"finishedAt":1,"inputTokens":10,"outputTokens":1,"model":"workflow-model-id"}`+"\n", reply)
	}))
	defer server.Close()
	NotionAPIBase = server.URL

	account := &Account{
		UserID:        "user-id",
		UserEmail:     "user@example.com",
		SpaceID:       "space-id",
		SpaceName:     "space",
		SpaceViewID:   "space-view",
		Timezone:      "UTC",
		ClientVersion: DefaultClientVersion,
		TokenV2:       "test-token",
	}
	session := newConversationSession(account.UserEmail)
	firstMessages := []ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "first question"},
	}
	var firstOutput strings.Builder
	if err := CallInference(account, firstMessages, "gpt-test", false, func(delta string, _ bool, _ *UsageInfo) {
		firstOutput.WriteString(delta)
	}, CallOptions{Session: session}); err != nil {
		t.Fatalf("first CallInference() error = %v", err)
	}
	if got := firstOutput.String(); !strings.Contains(got, "cobalt") {
		t.Fatalf("first answer = %q, want stored marker", got)
	}
	completeConversationSession(session, 1, "gpt-test")

	secondMessages := []ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
	}
	secondAttachments := []UploadedAttachment{{
		AttachmentURL: "attachment:attachment-id:notes.txt",
		FileName:      "notes.txt",
		ContentType:   "text/plain",
		FileSizeBytes: 42,
	}}
	var secondOutput strings.Builder
	if err := CallInference(account, secondMessages, "gpt-test", false, func(delta string, _ bool, _ *UsageInfo) {
		secondOutput.WriteString(delta)
	}, CallOptions{
		Session:     session,
		Attachments: secondAttachments,
	}); err != nil {
		t.Fatalf("second CallInference() error = %v", err)
	}
	if got := secondOutput.String(); !strings.Contains(got, "remember my previous answer: cobalt") {
		t.Fatalf("continued model did not see its stored Agent reply: %q", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	first, second := requests[0], requests[1]
	if !first.CreateThread || first.IsPartialTranscript {
		t.Fatalf("first request must create a complete thread: create=%v partial=%v", first.CreateThread, first.IsPartialTranscript)
	}
	if second.CreateThread || !second.IsPartialTranscript {
		t.Fatalf("second request must continue the thread: create=%v partial=%v", second.CreateThread, second.IsPartialTranscript)
	}
	if first.ThreadID != session.ThreadID || second.ThreadID != session.ThreadID {
		t.Fatalf("thread changed: first=%q second=%q session=%q", first.ThreadID, second.ThreadID, session.ThreadID)
	}
	if first.DebugOverrides.Model != "" || second.DebugOverrides.Model != "" {
		t.Fatalf("outdated debugOverrides.model was sent: first=%q second=%q", first.DebugOverrides.Model, second.DebugOverrides.Model)
	}
	assertWorkflowModelConfig(t, first.Transcript, "workflow-model-id")
	assertWorkflowModelConfig(t, second.Transcript, "workflow-model-id")

	secondJSON, err := json.Marshal(second.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secondJSON), `"type":"updated-config"`) {
		t.Fatalf("continuation transcript is missing the completed-turn placeholder: %s", secondJSON)
	}
	if !strings.Contains(string(secondJSON), "second question") {
		t.Fatalf("continuation transcript is missing the new user message: %s", secondJSON)
	}
	if !strings.Contains(string(secondJSON), `"type":"attachment"`) ||
		!strings.Contains(string(secondJSON), "attachment:attachment-id:notes.txt") ||
		!strings.Contains(string(secondJSON), "notes.txt") {
		t.Fatalf("continuation transcript is missing its attachment: %s", secondJSON)
	}
	if strings.Contains(string(secondJSON), "first answer") {
		t.Fatalf("continuation resent synthetic assistant history instead of using the stored Agent reply: %s", secondJSON)
	}
}

func TestCallInferencePartialTranscriptCarriesToolResultWithoutTools(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})
	chromeHTTPClientForTest = func(timeout time.Duration) *http.Client {
		return &http.Client{Timeout: timeout}
	}
	t.Cleanup(func() {
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	var captured NotionInferenceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintln(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"used tool result"}],"finishedAt":1,"inputTokens":10,"outputTokens":1,"model":"workflow-model-id"}`)
	}))
	defer server.Close()
	NotionAPIBase = server.URL

	account := &Account{
		UserID: "user-id", UserEmail: "user@example.com", SpaceID: "space-id",
		SpaceName: "space", Timezone: "UTC", TokenV2: "test-token",
	}
	session := newConversationSession(account.UserEmail)
	session.TurnCount = 1
	session.UpdatedConfigIDs = []string{generateUUIDv4()}
	messages := []ChatMessage{
		{Role: "user", Content: "look it up"},
		{Role: "assistant", ToolCalls: []ToolCall{{
			ID: "call-1", Type: "function",
			Function: ToolCallFunction{Name: "lookup", Arguments: `{"id":42}`},
		}}},
		{Role: "tool", ToolCallID: "call-1", Name: "lookup", Content: `{"value":"cobalt"}`},
		{Role: "user", Content: "answer using that result"},
	}

	if err := CallInference(account, messages, "gpt-test", false, func(string, bool, *UsageInfo) {}, CallOptions{
		Session:            session,
		ToolBridgeContract: toolBridgeContractMarker + " action_1",
	}); err != nil {
		t.Fatalf("CallInference() error = %v", err)
	}
	if captured.CreateThread || !captured.IsPartialTranscript || captured.ThreadID != session.ThreadID {
		t.Fatalf("request did not continue the real thread: create=%v partial=%v thread=%q", captured.CreateThread, captured.IsPartialTranscript, captured.ThreadID)
	}
	encoded, err := json.Marshal(captured.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	transcript := string(encoded)
	for _, expected := range []string{"Completed action result", "lookup", "cobalt", "answer using that result"} {
		if !strings.Contains(transcript, expected) {
			t.Fatalf("partial transcript is missing %q: %s", expected, transcript)
		}
	}
	if strings.Count(transcript, toolBridgeContractMarker) != 1 {
		t.Fatalf("tool bridge contract was not carried exactly once with the result: %s", transcript)
	}
	for _, protocolMarker := range []string{"TOOL_RESULT", "tool_call_id", "call-1"} {
		if strings.Contains(transcript, protocolMarker) {
			t.Fatalf("partial transcript leaked protocol marker %q: %s", protocolMarker, transcript)
		}
	}
	if strings.Contains(transcript, "look it up") {
		t.Fatalf("partial transcript replayed history before the latest assistant turn: %s", transcript)
	}
}

func assertWorkflowModelConfig(t *testing.T, transcript []interface{}, expected string) {
	t.Helper()
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) == 0 {
		t.Fatal("empty transcript")
	}
	value, ok := decoded[0]["value"].(map[string]interface{})
	if !ok {
		t.Fatalf("first transcript entry is not a config: %#v", decoded[0])
	}
	if value["model"] != expected || value["modelFromUser"] != true {
		t.Fatalf("workflow config model = %#v modelFromUser = %#v, want %q and true", value["model"], value["modelFromUser"], expected)
	}
}

func TestConversationMessageCountIgnoresSystem(t *testing.T) {
	second := []ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "remember marker"},
		ChatMessage{Role: "assistant", Content: "stored reply"},
		ChatMessage{Role: "user", Content: "what was it?"},
	}
	if got := countConversationMessages(second); got != 3 {
		t.Fatalf("message count = %d, want 3", got)
	}
}

func TestStableMetadataContinuesThreadWhenModelAndToolChoiceChange(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{
		"gpt-first":  "workflow-model-first",
		"gpt-second": "workflow-model-second",
	})
	globalSessionManager.Clear()
	t.Cleanup(func() {
		globalSessionManager.Clear()
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	var mu sync.Mutex
	var requests []NotionInferenceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body NotionInferenceRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		callNumber := len(requests)
		mu.Unlock()
		reply := "first stable anchor"
		if callNumber == 2 {
			reply = "continued after model and tool-choice change"
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"type":"agent-inference","id":"step%d","value":[{"type":"text","content":%q}],"finishedAt":1,"inputTokens":10,"outputTokens":2}`+"\n", callNumber, reply)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	handler := HandleAnthropicMessages(newPool(&Account{
		UserID: "user-stable", UserEmail: "stable@example.com", SpaceID: "space-stable",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-stable",
	}))
	metadata := map[string]interface{}{"session_id": "stable-model-switch"}
	tools := []AnthropicTool{{
		Name:        "lookup",
		Description: "Look up a value",
		InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
	}}
	first := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-first", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{{Role: "user", Content: "remember this turn"}},
		Tools:    tools,
	})
	second := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-second", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "remember this turn"},
			{Role: "assistant", Content: first},
			{Role: "user", Content: "continue with different request settings"},
		},
		Tools:      tools,
		ToolChoice: "none",
	})
	if first != "first stable anchor" || second != "continued after model and tool-choice change" {
		t.Fatalf("unexpected replies first=%q second=%q", first, second)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("request count=%d, want 2", len(requests))
	}
	if requests[0].ThreadID != requests[1].ThreadID ||
		!requests[0].CreateThread || requests[0].IsPartialTranscript ||
		requests[1].CreateThread || !requests[1].IsPartialTranscript {
		t.Fatalf("stable request settings change lost the real thread: %#v", requests)
	}
	assertWorkflowModelConfig(t, requests[0].Transcript, "workflow-model-first")
	assertWorkflowModelConfig(t, requests[1].Transcript, "workflow-model-second")
}

func TestToolBridgeContractIsSentOnceAndUpdatedForNewTools(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-tools": "workflow-model-tools"})
	globalSessionManager.Clear()
	t.Cleanup(func() {
		globalSessionManager.Clear()
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	var mu sync.Mutex
	var requests []NotionInferenceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body NotionInferenceRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		callNumber := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"type":"agent-inference","id":"step%d","value":[{"type":"text","content":"reply %d"}],"finishedAt":1,"inputTokens":10,"outputTokens":2}`+"\n", callNumber, callNumber)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	tools := make([]AnthropicTool, 6)
	for i := range tools {
		tools[i] = AnthropicTool{
			Name:        fmt.Sprintf("tool_%d", i+1),
			Description: "tool description",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		}
	}
	handler := HandleAnthropicMessages(newPool(&Account{
		UserID: "user-tools", UserEmail: "tools@example.com", SpaceID: "space-tools",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-tools",
	}))
	metadata := map[string]interface{}{"session_id": "tool-contract-session"}
	first := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-tools", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{{Role: "user", Content: "first request"}},
		Tools:    tools,
	})
	second := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-tools", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "first request"},
			{Role: "assistant", Content: first},
			{Role: "user", Content: "second request"},
		},
		Tools: tools,
	})
	updatedTools := append(append([]AnthropicTool(nil), tools...), AnthropicTool{
		Name:        "tool_7",
		Description: "new tool",
		InputSchema: map[string]interface{}{"type": "object"},
	})
	third := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-tools", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "first request"},
			{Role: "assistant", Content: first},
			{Role: "user", Content: "second request"},
			{Role: "assistant", Content: second},
			{Role: "user", Content: "third request"},
		},
		Tools: updatedTools,
	})
	if first != "reply 1" || second != "reply 2" || third != "reply 3" {
		t.Fatalf("unexpected replies: %q, %q, %q", first, second, third)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("request count=%d, want 3", len(requests))
	}
	serialized := make([]string, len(requests))
	for i, request := range requests {
		raw, err := json.Marshal(request.Transcript)
		if err != nil {
			t.Fatal(err)
		}
		serialized[i] = string(raw)
	}
	if strings.Count(serialized[0], toolBridgeContractMarker) != 1 || strings.Contains(serialized[0], "ROUTES:") {
		t.Fatalf("first request did not carry exactly one clean contract: %s", serialized[0])
	}
	if strings.Contains(serialized[1], toolBridgeContractMarker) || strings.Contains(serialized[1], "ROUTES:") || !strings.Contains(serialized[1], "second request") {
		t.Fatalf("second request repeated or lost the contract: %s", serialized[1])
	}
	if got := strings.Count(serialized[2], toolBridgeContractMarker); got != 1 {
		t.Fatalf("new tool contract marker count=%d: %s", got, serialized[2])
	}
	if !strings.Contains(serialized[2], "replaces the previous tool contract") {
		t.Fatalf("new tool contract did not identify replacement: %s", serialized[2])
	}
	if !strings.Contains(serialized[2], "action_7") {
		t.Fatalf("new tool did not produce one replacement contract: %s", serialized[2])
	}
}

func TestToolBridgeContractIsRecreatedOnceWhenConversationSwitchesAccount(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	previousRoutingQuotaFetcher := routingQuotaFetcher
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-switch": "workflow-model-switch"})
	globalSessionManager.Clear()
	routingQuotaFetcher = func(*Account) (*QuotaInfo, error) {
		return &QuotaInfo{IsEligible: true}, nil
	}
	t.Cleanup(func() {
		globalSessionManager.Clear()
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
		routingQuotaFetcher = previousRoutingQuotaFetcher
	})

	var mu sync.Mutex
	var requests []struct {
		userID string
		body   NotionInferenceRequest
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body NotionInferenceRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, struct {
			userID string
			body   NotionInferenceRequest
		}{userID: r.Header.Get("x-notion-active-user-header"), body: body})
		callNumber := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"type":"agent-inference","id":"switch-step%d","value":[{"type":"text","content":"switch reply %d"}],"finishedAt":1,"inputTokens":10,"outputTokens":2}`+"\n", callNumber, callNumber)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	accountA := &Account{UserID: "switch-a", UserEmail: "switch-a@example.com", SpaceID: "space-switch-a", PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "switch-a-token"}
	accountB := &Account{UserID: "switch-b", UserEmail: "switch-b@example.com", SpaceID: "space-switch-b", PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "switch-b-token"}
	handler := HandleAnthropicMessages(newPool(accountA, accountB))
	tools := make([]AnthropicTool, 6)
	for i := range tools {
		tools[i] = AnthropicTool{Name: fmt.Sprintf("tool_%d", i+1), InputSchema: map[string]interface{}{"type": "object"}}
	}
	metadata := map[string]interface{}{"session_id": "tool-account-switch"}
	first := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-switch", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{{Role: "user", Content: "first tool-aware turn"}}, Tools: tools,
	})
	accountA.mu.Lock()
	accountA.ManuallyDisabled = true
	accountA.mu.Unlock()
	second := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-switch", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "first tool-aware turn"},
			{Role: "assistant", Content: first},
			{Role: "user", Content: "continue after account switch"},
		}, Tools: tools,
	})
	if first != "switch reply 1" || second != "switch reply 2" {
		t.Fatalf("unexpected replies: %q, %q", first, second)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0].userID != "switch-a" || requests[1].userID != "switch-b" {
		t.Fatalf("conversation did not switch accounts as expected: %#v", requests)
	}
	if !requests[1].body.CreateThread || requests[1].body.IsPartialTranscript {
		t.Fatalf("account switch did not use one fresh replay: %#v", requests[1].body)
	}
	raw, err := json.Marshal(requests[1].body.Transcript)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	if strings.Count(serialized, toolBridgeContractMarker) != 1 || strings.Contains(serialized, "ROUTES:") || !strings.Contains(serialized, "continue after account switch") {
		t.Fatalf("switched account did not receive one clean current contract and request: %s", serialized)
	}
}

func TestAnthropicNonStreamResponseDeclaresUTF8(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-utf8": "workflow-model-utf8"})
	globalSessionManager.Clear()
	t.Cleanup(func() {
		globalSessionManager.Clear()
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"agent-inference","id":"step-utf8","value":[{"type":"text","content":"中文回答"}],"finishedAt":1,"inputTokens":1,"outputTokens":1}`)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	handler := HandleAnthropicMessages(newPool(&Account{
		UserID: "user-utf8", UserEmail: "utf8@example.com", SpaceID: "space-utf8",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-utf8",
	}))
	recorder := httptest.NewRecorder()
	raw, err := json.Marshal(AnthropicRequest{
		Model: "gpt-utf8", MaxTokens: 100,
		Messages: []AnthropicMessage{{Role: "user", Content: "请回答"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(raw)))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("content type=%q, want application/json; charset=utf-8", got)
	}
}

func TestPartialContinuationDescribesToolResultWithoutProtocolMarkers(t *testing.T) {
	messages := []ChatMessage{
		{Role: "user", Content: "look up the value"},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-secret", Function: ToolCallFunction{Name: "lookup", Arguments: `{}`}}}},
		{Role: "tool", ToolCallID: "call-secret", Name: "lookup", Content: "VALUE_42"},
		{Role: "user", Content: "now explain it"},
	}

	content := buildPartialContinuationContent(messages)
	for _, marker := range []string{"VALUE_42", "lookup", "now explain it"} {
		if !strings.Contains(content, marker) {
			t.Fatalf("continuation dropped %q: %s", marker, content)
		}
	}
	for _, protocolMarker := range []string{"TOOL_RESULT", "tool_call_id", "call-secret"} {
		if strings.Contains(content, protocolMarker) {
			t.Fatalf("continuation leaked protocol marker %q: %s", protocolMarker, content)
		}
	}
}

func TestAnthropicHandlerPreservesAgentRepliesAcrossContinuationAndAccountSwitch(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	previousQuotaFetcher := quotaFetcher
	previousRoutingQuotaFetcher := routingQuotaFetcher
	previousAttachmentUploader := attachmentUploader
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})
	globalSessionManager.Clear()

	quotaRefreshes := make(chan struct{}, 4)
	routingQuotaFetcher = func(*Account) (*QuotaInfo, error) {
		quotaRefreshes <- struct{}{}
		return &QuotaInfo{IsEligible: true}, nil
	}
	t.Cleanup(func() {
		globalSessionManager.Clear()
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
		quotaFetcher = previousQuotaFetcher
		routingQuotaFetcher = previousRoutingQuotaFetcher
		attachmentUploader = previousAttachmentUploader
	})

	type capturedRequest struct {
		UserID string
		Body   NotionInferenceRequest
	}
	var mu sync.Mutex
	var captured []capturedRequest
	storedAgentReplies := make(map[string]string)
	type uploadCall struct {
		ThreadID     string
		CreateThread bool
		Data         string
	}
	var uploads []uploadCall
	attachmentUploader = func(_ *Account, file *FileAttachment, threadID string, createThread bool) (*UploadedAttachment, error) {
		mu.Lock()
		uploads = append(uploads, uploadCall{
			ThreadID: threadID, CreateThread: createThread, Data: string(file.Data),
		})
		mu.Unlock()
		return &UploadedAttachment{
			AttachmentURL: "attachment:" + threadID + ":" + file.FileName,
			FileName:      file.FileName,
			ContentType:   file.ContentType,
			FileSizeBytes: int64(len(file.Data)),
			SessionID:     threadID,
		}, nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runInferenceTranscript" {
			http.NotFound(w, r)
			return
		}
		var body NotionInferenceRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		userID := r.Header.Get("x-notion-active-user-header")
		rawTranscript, _ := json.Marshal(body.Transcript)

		mu.Lock()
		captured = append(captured, capturedRequest{UserID: userID, Body: body})
		reply := "My marker is cobalt."
		switch {
		case body.IsPartialTranscript && storedAgentReplies[body.ThreadID] == "I can see both previous assistant answers after the account switch.":
			reply = "I continued incrementally on account B."
			storedAgentReplies[body.ThreadID] = reply
		case body.IsPartialTranscript && storedAgentReplies[body.ThreadID] == "My marker is cobalt.":
			reply = "I remember my previous answer: cobalt."
			storedAgentReplies[body.ThreadID] = reply
		case userID == "user-b" &&
			strings.Contains(string(rawTranscript), "My marker is cobalt.") &&
			strings.Contains(string(rawTranscript), "I remember my previous answer: cobalt."):
			reply = "I can see both previous assistant answers after the account switch."
			storedAgentReplies[body.ThreadID] = reply
		case !body.IsPartialTranscript:
			storedAgentReplies[body.ThreadID] = reply
		default:
			reply = "I cannot see my previous answer."
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":%q}],"finishedAt":1,"inputTokens":10,"outputTokens":1,"model":"workflow-model-id"}`+"\n", reply)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	accountA := &Account{
		UserID: "user-a", UserEmail: "a@example.com", SpaceID: "space-a",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-a",
	}
	accountB := &Account{
		UserID: "user-b", UserEmail: "b@example.com", SpaceID: "space-b",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-b",
	}
	pool := newPool(accountA, accountB)
	handler := HandleAnthropicMessages(pool)
	metadata := map[string]interface{}{"session_id": "stable-conversation-id"}
	documentBlock := func(text string) map[string]interface{} {
		return map[string]interface{}{
			"type": "document",
			"source": map[string]interface{}{
				"type":       "base64",
				"media_type": "text/plain",
				"data":       base64.StdEncoding.EncodeToString([]byte(text)),
			},
		}
	}
	firstUserContent := []interface{}{
		map[string]interface{}{"type": "text", "text": "Choose and remember a marker."},
		documentBlock("old-attachment-one"),
		documentBlock("old-attachment-two"),
	}

	first := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{{Role: "user", Content: firstUserContent}},
	})
	if !strings.Contains(first, "cobalt") {
		t.Fatalf("first answer=%q", first)
	}

	second := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{
			{Role: "user", Content: firstUserContent},
			{Role: "assistant", Content: first},
			{Role: "user", Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "What marker did you choose?"},
				documentBlock("new-attachment"),
			}},
		},
	})
	if !strings.Contains(second, "remember my previous answer: cobalt") {
		t.Fatalf("continuation lost the stored Agent reply: %q", second)
	}

	accountA.mu.Lock()
	accountA.ManuallyDisabled = true
	accountA.mu.Unlock()

	third := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{
			{Role: "user", Content: firstUserContent},
			{Role: "assistant", Content: first},
			{Role: "user", Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "What marker did you choose?"},
				documentBlock("new-attachment"),
			}},
			{Role: "assistant", Content: second},
			{Role: "user", Content: "Summarize both of your previous answers."},
		},
	})
	if !strings.Contains(third, "both previous assistant answers") {
		t.Fatalf("account-switch replay lost assistant history: %q", third)
	}
	fourth := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{
			{Role: "user", Content: firstUserContent},
			{Role: "assistant", Content: first},
			{Role: "user", Content: []interface{}{
				map[string]interface{}{"type": "text", "text": "What marker did you choose?"},
				documentBlock("new-attachment"),
			}},
			{Role: "assistant", Content: second},
			{Role: "user", Content: "Summarize both of your previous answers."},
			{Role: "assistant", Content: third},
			{Role: "user", Content: "Continue once more."},
		},
	})
	if fourth != "I continued incrementally on account B." {
		t.Fatalf("post-migration turn did not continue incrementally: %q", fourth)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 4 {
		t.Fatalf("captured request count=%d, want 4", len(captured))
	}
	if captured[0].UserID != "user-a" || captured[0].Body.CreateThread ||
		captured[1].UserID != "user-a" || !captured[1].Body.IsPartialTranscript {
		t.Fatalf("attachment-created thread did not continue on account A: %#v", captured[:2])
	}
	if captured[2].UserID != "user-b" || captured[2].Body.CreateThread || captured[2].Body.IsPartialTranscript {
		t.Fatalf("account switch did not perform a complete fresh-thread replay: %#v", captured[2])
	}
	if captured[3].UserID != "user-b" || captured[3].Body.ThreadID != captured[2].Body.ThreadID || !captured[3].Body.IsPartialTranscript {
		t.Fatalf("post-migration request did not continue account B's thread: %#v", captured[3])
	}
	if len(uploads) != 6 {
		t.Fatalf("upload count=%d, want first-turn 2 + continuation 1 + account-switch replay 3: %#v", len(uploads), uploads)
	}
	firstThreadID := captured[0].Body.ThreadID
	for i, upload := range uploads[:3] {
		if upload.ThreadID != firstThreadID {
			t.Fatalf("upload %d thread=%q, want inference thread %q", i, upload.ThreadID, firstThreadID)
		}
	}
	switchedThreadID := captured[2].Body.ThreadID
	for i, upload := range uploads[3:] {
		if upload.ThreadID != switchedThreadID {
			t.Fatalf("account-switch upload %d thread=%q, want inference thread %q", i, upload.ThreadID, switchedThreadID)
		}
	}
	if !uploads[0].CreateThread || uploads[1].CreateThread || uploads[2].CreateThread ||
		!uploads[3].CreateThread || uploads[4].CreateThread || uploads[5].CreateThread {
		t.Fatalf("unexpected upload createThread sequence: %#v", uploads)
	}
	if uploads[0].Data != "old-attachment-one" ||
		uploads[1].Data != "old-attachment-two" ||
		uploads[2].Data != "new-attachment" ||
		uploads[3].Data != "old-attachment-one" ||
		uploads[4].Data != "old-attachment-two" ||
		uploads[5].Data != "new-attachment" {
		t.Fatalf("continuation re-uploaded history or lost new attachment: %#v", uploads)
	}
}

func TestBusyBoundLoginPreservesThreadForRetry(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	previousRoutingQuotaFetcher := routingQuotaFetcher
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})
	globalSessionManager.Clear()
	routingQuotaFetcher = func(*Account) (*QuotaInfo, error) {
		return &QuotaInfo{IsEligible: true}, nil
	}
	t.Cleanup(func() {
		globalSessionManager.Clear()
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
		routingQuotaFetcher = previousRoutingQuotaFetcher
	})

	var mu sync.Mutex
	var requests []NotionInferenceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body NotionInferenceRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		callNumber := len(requests)
		mu.Unlock()
		reply := "first answer"
		if callNumber == 2 {
			reply = "continued after retry"
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"type":"agent-inference","id":"step%d","value":[{"type":"text","content":%q}],"finishedAt":1,"inputTokens":10,"outputTokens":1}`+"\n", callNumber, reply)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	account := &Account{
		UserID: "busy-user", UserEmail: "busy@example.com", SpaceID: "busy-space",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "busy-token",
	}
	pool := newPool(account)
	handler := HandleAnthropicMessages(pool)
	metadata := map[string]interface{}{"session_id": "busy-thread-session"}
	first := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{{Role: "user", Content: "first"}},
	})

	firstLease, err := pool.LeaseAccount(account)
	if err != nil {
		t.Fatal(err)
	}
	secondLease, err := pool.LeaseAccount(account)
	if err != nil {
		firstLease.Release()
		t.Fatal(err)
	}
	defer firstLease.Release()
	defer secondLease.Release()

	continuation := AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: first},
			{Role: "user", Content: "second"},
		},
	}
	raw, err := json.Marshal(continuation)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw)))
	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), "busy") {
		t.Fatalf("busy continuation status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	mu.Lock()
	if len(requests) != 1 {
		mu.Unlock()
		t.Fatalf("busy continuation unexpectedly called upstream %d times", len(requests))
	}
	firstThreadID := requests[0].ThreadID
	mu.Unlock()

	firstLease.Release()
	secondLease.Release()
	if got := callAnthropicHandlerForText(t, handler, continuation); got != "continued after retry" {
		t.Fatalf("retry reply=%q", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[1].ThreadID != firstThreadID || !requests[1].IsPartialTranscript {
		t.Fatalf("busy retry did not preserve the real thread: %#v", requests)
	}
}

func TestAnthropicHandlerSafelyContinuesUnsaltedChatsWithSameShortReply(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	previousQuotaFetcher := quotaFetcher
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})
	globalSessionManager.Clear()

	quotaRefreshes := make(chan struct{}, 8)
	quotaFetcher = func(*Account) (*QuotaInfo, error) {
		quotaRefreshes <- struct{}{}
		return &QuotaInfo{IsEligible: true}, nil
	}
	t.Cleanup(func() {
		globalSessionManager.Clear()
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
		quotaFetcher = previousQuotaFetcher
	})

	var mu sync.Mutex
	threadChat := make(map[string]string)
	var requests []NotionInferenceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runInferenceTranscript" {
			http.NotFound(w, r)
			return
		}
		var body NotionInferenceRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rawTranscript, _ := json.Marshal(body.Transcript)
		mu.Lock()
		requests = append(requests, body)
		reply := "OK"
		if body.CreateThread {
			switch {
			case strings.Contains(string(rawTranscript), "chat A opening"):
				threadChat[body.ThreadID] = "A"
			case strings.Contains(string(rawTranscript), "chat B opening"):
				threadChat[body.ThreadID] = "B"
			}
		} else if body.IsPartialTranscript {
			reply = "remember " + threadChat[body.ThreadID]
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":%q}],"finishedAt":1,"inputTokens":10,"outputTokens":1}`+"\n", reply)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	account := &Account{
		UserID: "user-a", UserEmail: "a@example.com", SpaceID: "space-a",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-a",
	}
	handler := HandleAnthropicMessages(newPool(account))

	firstA := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100,
		Messages: []AnthropicMessage{{Role: "user", Content: "chat A opening"}},
	})
	firstB := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100,
		Messages: []AnthropicMessage{{Role: "user", Content: "chat B opening"}},
	})
	if firstA != "OK" || firstB != "OK" {
		t.Fatalf("first replies A=%q B=%q, want identical short replies", firstA, firstB)
	}

	secondA := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "chat A opening"},
			{Role: "assistant", Content: firstA},
			{Role: "user", Content: "which chat?"},
		},
	})
	secondB := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "chat B opening"},
			{Role: "assistant", Content: firstB},
			{Role: "user", Content: "which chat?"},
		},
	})
	if secondA != "remember A" || secondB != "remember B" {
		t.Fatalf("unsalted chats crossed or lost their real threads: A=%q B=%q", secondA, secondB)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 ||
		!requests[0].CreateThread ||
		!requests[1].CreateThread ||
		!requests[2].IsPartialTranscript ||
		!requests[3].IsPartialTranscript ||
		requests[2].ThreadID == requests[3].ThreadID {
		t.Fatalf("unexpected unsalted request routing: %#v", requests)
	}
}

func TestOpenAIChatRoundTripContinuesRealAgentReplyWithoutMetadata(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})
	globalSessionManager.Clear()
	t.Cleanup(func() {
		globalSessionManager.Clear()
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	var mu sync.Mutex
	var requests []NotionInferenceRequest
	storedReply := make(map[string]bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body NotionInferenceRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		reply := "Agent chose violet."
		if body.IsPartialTranscript && storedReply[body.ThreadID] {
			reply = "Agent remembers violet."
		} else if !body.IsPartialTranscript {
			storedReply[body.ThreadID] = true
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":%q}],"finishedAt":1,"inputTokens":10,"outputTokens":1}`+"\n", reply)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	handler := HandleOpenAIChatCompletions(newPool(&Account{
		UserID: "user-a", UserEmail: "openai@example.com", SpaceID: "space-a",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-a",
	}))
	call := func(messages []OpenAIChatMessage) string {
		t.Helper()
		raw, err := json.Marshal(OpenAIChatCompletionRequest{
			Model: "gpt-test", Messages: messages, MaxTokens: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var response OpenAIChatCompletionResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode OpenAI response: %v body=%s", err, recorder.Body.String())
		}
		content, _ := response.Choices[0].Message["content"].(string)
		return content
	}

	first := call([]OpenAIChatMessage{{Role: "user", Content: "choose a color"}})
	second := call([]OpenAIChatMessage{
		{Role: "user", Content: "choose a color"},
		{Role: "assistant", Content: first},
		{Role: "user", Content: "what did you choose?"},
	})
	if first != "Agent chose violet." || second != "Agent remembers violet." {
		t.Fatalf("OpenAI round trip lost Agent reply: first=%q second=%q", first, second)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0].ThreadID != requests[1].ThreadID || !requests[1].IsPartialTranscript {
		t.Fatalf("OpenAI continuation did not reuse the real Notion thread: %#v", requests)
	}
}

func TestPartialStreamFailureInvalidatesThreadBeforeNextTurn(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})
	globalSessionManager.Clear()
	t.Cleanup(func() {
		globalSessionManager.Clear()
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	var mu sync.Mutex
	var requests []NotionInferenceRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body NotionInferenceRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		callNumber := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		switch callNumber {
		case 1:
			fmt.Fprintln(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"first complete answer"}],"finishedAt":1,"inputTokens":10,"outputTokens":2}`)
		case 2:
			fmt.Fprintln(w, `{"type":"agent-inference","id":"step2","value":[{"type":"text","content":"partial second answer"}]}`)
			fmt.Fprintln(w, `{"type":"error","message":"connection failed"}`)
		default:
			fmt.Fprintln(w, `{"type":"agent-inference","id":"step3","value":[{"type":"text","content":"safe full replay"}],"finishedAt":1,"inputTokens":20,"outputTokens":2}`)
		}
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	handler := HandleAnthropicMessages(newPool(&Account{
		UserID: "user-a", UserEmail: "stream@example.com", SpaceID: "space-a",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-a",
	}))
	metadata := map[string]interface{}{"session_id": "stream-failure-session"}
	first := callAnthropicHandlerForText(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100, Metadata: metadata,
		Messages: []AnthropicMessage{{Role: "user", Content: "first"}},
	})
	secondRequest := AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100, Metadata: metadata, Stream: true,
		Messages: []AnthropicMessage{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: first},
			{Role: "user", Content: "second"},
		},
	}
	raw, _ := json.Marshal(secondRequest)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw)))
	if recorder.Code != http.StatusOK ||
		!strings.Contains(recorder.Body.String(), "partial second answer") ||
		!strings.Contains(recorder.Body.String(), "event: error") ||
		strings.Contains(recorder.Body.String(), "message_stop") {
		t.Fatalf("unexpected interrupted stream status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	secondRequest.Stream = false
	third := callAnthropicHandlerForText(t, handler, secondRequest)
	if third != "safe full replay" {
		t.Fatalf("next request did not recover through full replay: %q", third)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 ||
		requests[0].ThreadID != requests[1].ThreadID ||
		!requests[1].IsPartialTranscript ||
		requests[2].ThreadID == requests[1].ThreadID ||
		requests[2].IsPartialTranscript ||
		!requests[2].CreateThread {
		t.Fatalf("partial stream failure retained the contaminated thread: %#v", requests)
	}
}

func callAnthropicHandlerForText(t *testing.T, handler http.HandlerFunc, request AnthropicRequest) string {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(raw)))
	httpRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response AnthropicResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	for _, block := range response.Content {
		if block.Type == "text" {
			return block.Text
		}
	}
	t.Fatalf("response has no text block: %#v", response)
	return ""
}

func waitForQuotaRefresh(t *testing.T, refreshes <-chan struct{}) {
	t.Helper()
	select {
	case <-refreshes:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for async quota refresh")
	}
}
