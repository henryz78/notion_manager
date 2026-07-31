package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAnthropicStreamsExposeUpstreamErrorAfterPartialOutput(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runInferenceTranscript" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"partial answer"}]}`)
		fmt.Fprintln(w, `{"type":"error","message":"upstream exploded"}`)
	}))
	defer server.Close()

	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }
	t.Cleanup(func() {
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	account := &Account{
		UserID:        "user-id",
		UserEmail:     "user@example.com",
		SpaceID:       "space-id",
		ClientVersion: DefaultClientVersion,
		TokenV2:       "test-token",
	}
	messages := []ChatMessage{{Role: "user", Content: "hello"}}

	t.Run("plain text stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		err := streamAnthropicTextResponse(
			recorder,
			account,
			messages,
			"gpt-test",
			"req-text",
			false,
			false,
			nil,
			CallOptions{},
		)
		assertInterruptedAnthropicStream(t, recorder, err)
	})

	t.Run("tool-capable stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		err := handleAnthropicStream(
			context.Background(),
			recorder,
			account,
			messages,
			"gpt-test",
			"req-tools",
			true,
			true,
			map[string]struct{}{"lookup": {}},
			nil,
			"auto",
			false,
			false,
			nil,
			false,
			nil,
			nil,
			nil,
			nil,
		)
		assertInterruptedAnthropicStream(t, recorder, err)
	})
}

func TestRequiredToolStreamDoesNotExposePlainTextBeforeValidation(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runInferenceTranscript" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"plain answer instead of a tool call"}],"finishedAt":1,"inputTokens":10,"outputTokens":4}`)
	}))
	defer server.Close()

	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }
	t.Cleanup(func() {
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	recorder := httptest.NewRecorder()
	err := handleAnthropicStream(
		context.Background(),
		recorder,
		&Account{
			UserID:        "user-id",
			UserEmail:     "user@example.com",
			SpaceID:       "space-id",
			ClientVersion: DefaultClientVersion,
			TokenV2:       "test-token",
		},
		[]ChatMessage{{Role: "user", Content: "use the lookup tool"}},
		"gpt-test",
		"req-required",
		true,
		true,
		map[string]struct{}{"lookup": {}},
		nil,
		"required",
		true,
		false,
		nil,
		false,
		nil,
		nil,
		nil,
		nil,
	)
	if !errors.Is(err, ErrToolBridgeNoTool) {
		t.Fatalf("error=%v, want ErrToolBridgeNoTool", err)
	}
	if body := recorder.Body.String(); body != "" {
		t.Fatalf("required tool response leaked before validation: %s", body)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "" {
		t.Fatalf("SSE headers were committed before required tool validation: %q", contentType)
	}
}

func TestForcedToolChoiceRejectsDifferentDeclaredTool(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"{\"name\":\"B\",\"arguments\":{}}"}],"finishedAt":1,"inputTokens":10,"outputTokens":4}`)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }
	t.Cleanup(func() {
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	allowed, err := allowedToolNamesForChoice([]AnthropicTool{{Name: "A"}, {Name: "B"}}, "force:A")
	if err != nil {
		t.Fatal(err)
	}
	account := &Account{
		UserID: "user-id", UserEmail: "user@example.com", SpaceID: "space-id",
		ClientVersion: DefaultClientVersion, TokenV2: "test-token",
	}
	messages := []ChatMessage{{Role: "user", Content: "call A"}}

	t.Run("stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		err := handleAnthropicStream(
			context.Background(), recorder, account, messages, "gpt-test", "req-force-stream",
			true, true, allowed, nil, "force:A", false, false, nil, false, nil, nil, nil, nil,
		)
		if !errors.Is(err, ErrToolBridgeNoTool) {
			t.Fatalf("error=%v, want ErrToolBridgeNoTool", err)
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("wrong forced tool leaked into stream: %s", recorder.Body.String())
		}
	})

	t.Run("non-stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		err := handleAnthropicNonStream(
			context.Background(), recorder, account, messages, "gpt-test", "req-force-nonstream",
			true, true, allowed, nil, "force:A", false, false, nil, false, nil, nil, nil, nil,
		)
		if !errors.Is(err, ErrToolBridgeNoTool) {
			t.Fatalf("error=%v, want ErrToolBridgeNoTool", err)
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("wrong forced tool leaked into response: %s", recorder.Body.String())
		}
	})
}

func TestNativeToolStreamPublishesClientVisibleTextAndToolAnchor(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/runInferenceTranscript" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"I will check. "},{"type":"tool_use","id":"call-native","name":"lookup","input":{"key":"alpha"}}],"finishedAt":1,"inputTokens":10,"outputTokens":4}`)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }
	t.Cleanup(func() {
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		ReplaceModelMap(previousModels)
		chromeHTTPClientForTest = previousClientOverride
	})

	var published ChatMessage
	session := newConversationSession("stream-tools@example.com")
	session.publishAssistant = func(message ChatMessage) {
		published = message
	}
	recorder := httptest.NewRecorder()
	err := handleAnthropicStream(
		context.Background(),
		recorder,
		&Account{
			UserID: "user-id", UserEmail: "stream-tools@example.com", SpaceID: "space-id",
			ClientVersion: DefaultClientVersion, TokenV2: "test-token",
		},
		[]ChatMessage{{Role: "user", Content: "look it up"}},
		"gpt-test",
		"req-native-tool-anchor",
		true,
		true,
		map[string]struct{}{"lookup": {}},
		nil,
		"auto",
		false,
		false,
		nil,
		false,
		nil,
		nil,
		session,
		nil,
	)
	if err != nil {
		t.Fatalf("handleAnthropicStream() error=%v body=%s", err, recorder.Body.String())
	}
	if published.Content != "I will check. " || len(published.ToolCalls) != 1 ||
		published.ToolCalls[0].ID != "call-native" ||
		published.ToolCalls[0].Function.Name != "lookup" ||
		published.ToolCalls[0].Function.Arguments != `{"key":"alpha"}` {
		t.Fatalf("published assistant anchor does not match client-visible stream: %#v", published)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "I will check. ") ||
		!strings.Contains(body, `"name":"lookup"`) ||
		!strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Fatalf("stream omitted text or native tool call: %s", body)
	}

	clientMessages, _, _ := convertAnthropicMessages(nil, []AnthropicMessage{
		{Role: "user", Content: "look it up"},
		{Role: "assistant", Content: []interface{}{
			map[string]interface{}{"type": "text", "text": "I will check. "},
			map[string]interface{}{
				"type": "tool_use",
				"id":   "call-native",
				"name": "lookup",
				"input": map[string]interface{}{
					"key": "alpha",
				},
			},
		}},
	})
	serverKey := computeConversationContinuationKey([]ChatMessage{
		{Role: "user", Content: "look it up"},
		published,
	})
	if clientKey := extractAssistantContinuationKey(clientMessages); clientKey == "" || clientKey != serverKey {
		t.Fatalf("stream anchor did not round-trip through client history: client=%q server=%q", clientKey, serverKey)
	}
}

func TestResearcherQuotaEventAfterPartialOutputDoesNotLookRetryable(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"researcher-report","id":"report","value":"partial report"}`)
		fmt.Fprintln(w, `{"type":"premium-feature-unavailable"}`)
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }
	t.Cleanup(func() {
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		chromeHTTPClientForTest = previousClientOverride
	})

	recorder := httptest.NewRecorder()
	err := handleResearcherStream(
		context.Background(),
		recorder,
		&Account{
			UserID: "user-id", UserEmail: "user@example.com", SpaceID: "space-id",
			ClientVersion: DefaultClientVersion, TokenV2: "test-token",
		},
		[]ChatMessage{{Role: "user", Content: "research"}},
		"researcher",
		"req-research",
		true,
		nil,
	)
	if !errors.Is(err, ErrStreamResponseStarted) {
		t.Fatalf("error=%v, want ErrStreamResponseStarted", err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "partial report") || !strings.Contains(body, "event: error") {
		t.Fatalf("partial researcher output or terminal error missing: %s", body)
	}
	if strings.Contains(body, "message_stop") {
		t.Fatalf("partial researcher response was falsely completed: %s", body)
	}
}

func assertInterruptedAnthropicStream(t *testing.T, recorder *httptest.ResponseRecorder, err error) {
	t.Helper()
	if !errors.Is(err, ErrStreamResponseStarted) {
		t.Fatalf("error=%v, want ErrStreamResponseStarted", err)
	}
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, body)
	}
	if !strings.Contains(body, "partial answer") || !strings.Contains(body, "event: error") {
		t.Fatalf("partial content or terminal error event missing: %s", body)
	}
	if strings.Contains(body, "message_stop") ||
		strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("interrupted stream was falsely completed: %s", body)
	}
}
