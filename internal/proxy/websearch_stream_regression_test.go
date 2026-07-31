package proxy

import (
	"bytes"
	"context"
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

func TestWebSearchBridgeContinuesSameThreadAndPublishesSearchAnswer(t *testing.T) {
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
	searchAnswerStored := make(map[string]bool)
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
		callNumber := len(requests)
		hasStoredSearchAnswer := searchAnswerStored[body.ThreadID]
		if callNumber == 2 {
			searchAnswerStored[body.ThreadID] = true
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "application/x-ndjson")
		switch callNumber {
		case 1:
			fmt.Fprintln(w, `{"type":"agent-inference","id":"bridge","value":[{"type":"text","content":"{\"name\":\"action_1\",\"arguments\":{\"query\":\"weather in Seattle\"}}"}],"finishedAt":1,"inputTokens":10,"outputTokens":2}`)
		case 2:
			fmt.Fprintln(w, `{"type":"agent-inference","id":"search","value":[{"type":"text","content":"Seattle is rainy today."}],"finishedAt":2,"inputTokens":100,"outputTokens":7}`)
		case 3:
			reply := "follow-up lost the search answer"
			if hasStoredSearchAnswer {
				reply = "follow-up can see the stored search answer"
			}
			fmt.Fprintf(w, `{"type":"agent-inference","id":"followup","value":[{"type":"text","content":%q}],"finishedAt":3,"inputTokens":80,"outputTokens":4}`+"\n", reply)
		default:
			http.Error(w, "unexpected inference call", http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	handler := HandleAnthropicMessages(newPool(&Account{
		UserID: "user-a", UserEmail: "search@example.com", SpaceID: "space-a",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-a",
	}))
	tool := AnthropicTool{
		Name:        "WebSearch",
		Description: "Search the web",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
			},
			"required": []string{"query"},
		},
	}
	metadata := map[string]interface{}{"session_id": "web-search-session"}

	first := callAnthropicHandlerForResponse(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100, Metadata: metadata,
		Tools: []AnthropicTool{tool},
		Messages: []AnthropicMessage{{
			Role: "user", Content: "Search the weather.",
		}},
	})
	firstText := anthropicResponseText(first)
	if firstText != "Seattle is rainy today." {
		t.Fatalf("search response=%q", firstText)
	}
	if first.Usage == nil || first.Usage.InputTokens != 100 || first.Usage.OutputTokens != 9 {
		t.Fatalf("search usage=%+v, want peak input 100 and summed output 9", first.Usage)
	}

	second := callAnthropicHandlerForResponse(t, handler, AnthropicRequest{
		Model: "gpt-test", MaxTokens: 100, Metadata: metadata,
		Tools: []AnthropicTool{tool},
		Messages: []AnthropicMessage{
			{Role: "user", Content: "Search the weather."},
			{Role: "assistant", Content: firstText},
			{Role: "user", Content: "Can you still see that answer?"},
		},
	})
	if text := anthropicResponseText(second); text != "follow-up can see the stored search answer" {
		t.Fatalf("follow-up response=%q", text)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("inference calls=%d, want bridge + search + follow-up", len(requests))
	}
	if requests[0].ThreadID == "" ||
		requests[1].ThreadID != requests[0].ThreadID ||
		requests[2].ThreadID != requests[0].ThreadID {
		t.Fatalf("WebSearch did not remain on one thread: %#v", requests)
	}
	if requests[0].IsPartialTranscript ||
		!requests[1].IsPartialTranscript ||
		!requests[2].IsPartialTranscript {
		t.Fatalf("unexpected full/partial sequence: %#v", requests)
	}
	if got := countTranscriptEntriesOfType(requests[1].Transcript, "updated-config"); got != 1 {
		t.Fatalf("search continuation updated-config count=%d, want 1", got)
	}
	if got := countTranscriptEntriesOfType(requests[2].Transcript, "updated-config"); got != 2 {
		t.Fatalf("next client turn updated-config count=%d, want 2", got)
	}
}

func TestWebSearchStreamFailureNeverEmitsNormalStop(t *testing.T) {
	tests := []struct {
		name       string
		secondBody string
	}{
		{
			name:       "upstream error",
			secondBody: `{"type":"error","message":"search failed"}`,
		},
		{
			name:       "empty search result",
			secondBody: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previousBase := NotionAPIBase
			previousConfig := AppConfig
			previousModels := SnapshotModelMap()
			previousClientOverride := chromeHTTPClientForTest
			AppConfig = DefaultConfig()
			ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})
			t.Cleanup(func() {
				NotionAPIBase = previousBase
				AppConfig = previousConfig
				ReplaceModelMap(previousModels)
				chromeHTTPClientForTest = previousClientOverride
			})

			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/x-ndjson")
				if calls == 1 {
					fmt.Fprintln(w, `{"type":"agent-inference","id":"bridge","value":[{"type":"text","content":"{\"name\":\"action_1\",\"arguments\":{\"query\":\"weather\"}}"}],"finishedAt":1,"inputTokens":10,"outputTokens":2}`)
					return
				}
				if test.secondBody != "" {
					fmt.Fprintln(w, test.secondBody)
				}
			}))
			defer server.Close()
			NotionAPIBase = server.URL
			chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

			recorder := httptest.NewRecorder()
			session := newConversationSession("search@example.com")
			err := handleAnthropicStream(
				context.Background(),
				recorder,
				&Account{
					UserID: "user-a", UserEmail: "search@example.com", SpaceID: "space-a",
					PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-a",
				},
				[]ChatMessage{{Role: "user", Content: "search"}},
				"gpt-test",
				"req-search-error",
				true,
				true,
				map[string]struct{}{"WebSearch": {}},
				map[string]string{"action_1": "WebSearch"},
				"auto",
				true,
				true,
				nil,
				false,
				nil,
				nil,
				session,
				nil,
			)
			if !errors.Is(err, ErrStreamResponseStarted) {
				t.Fatalf("error=%v, want ErrStreamResponseStarted", err)
			}
			body := recorder.Body.String()
			if !strings.Contains(body, "event: error") {
				t.Fatalf("stream is missing explicit error event: %s", body)
			}
			if strings.Contains(body, "message_stop") ||
				strings.Contains(body, `"stop_reason":"end_turn"`) {
				t.Fatalf("failed search stream falsely completed: %s", body)
			}
		})
	}
}

func TestThinkingOnlyStreamsEndWithExplicitError(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModels := SnapshotModelMap()
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"gpt-test": "workflow-model-id"})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"agent-inference","id":"thinking","value":[{"type":"thinking","content":"I am still reasoning."}],"finishedAt":1,"inputTokens":10,"outputTokens":3}`)
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
		UserID: "user-a", UserEmail: "thinking@example.com", SpaceID: "space-a",
		PlanType: "team", ClientVersion: DefaultClientVersion, TokenV2: "token-a",
	}
	messages := []ChatMessage{{Role: "user", Content: "answer completely"}}
	t.Run("plain stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		err := streamAnthropicTextResponse(
			recorder,
			account,
			messages,
			"gpt-test",
			"req-thinking-plain",
			true,
			false,
			nil,
			CallOptions{},
		)
		assertThinkingOnlyStreamError(t, recorder, err)
	})
	t.Run("tool-capable auto stream", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		err := handleAnthropicStream(
			context.Background(),
			recorder,
			account,
			messages,
			"gpt-test",
			"req-thinking-tools",
			true,
			true,
			map[string]struct{}{"lookup": {}},
			nil,
			"auto",
			true,
			false,
			nil,
			false,
			nil,
			nil,
			nil,
			nil,
		)
		assertThinkingOnlyStreamError(t, recorder, err)
	})
}

func callAnthropicHandlerForResponse(t *testing.T, handler http.HandlerFunc, request AnthropicRequest) *AnthropicResponse {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(raw))
	httpRequest.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, httpRequest)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response AnthropicResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	return &response
}

func anthropicResponseText(response *AnthropicResponse) string {
	if response == nil {
		return ""
	}
	var text strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String()
}

func countTranscriptEntriesOfType(transcript []interface{}, entryType string) int {
	count := 0
	for _, entry := range transcript {
		object, _ := entry.(map[string]interface{})
		if object["type"] == entryType {
			count++
		}
	}
	return count
}

func assertThinkingOnlyStreamError(t *testing.T, recorder *httptest.ResponseRecorder, err error) {
	t.Helper()
	if !errors.Is(err, ErrStreamResponseStarted) {
		t.Fatalf("error=%v, want ErrStreamResponseStarted", err)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "I am still reasoning.") ||
		!strings.Contains(body, "event: error") {
		t.Fatalf("thinking or explicit error missing: %s", body)
	}
	if strings.Contains(body, "message_stop") ||
		strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("thinking-only stream falsely completed: %s", body)
	}
}
