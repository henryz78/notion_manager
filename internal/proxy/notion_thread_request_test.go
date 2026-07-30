package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

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
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"agent-inference","id":"step1","value":[{"type":"text","content":"ok"}],"finishedAt":1,"inputTokens":10,"outputTokens":1,"model":"workflow-model-id"}`)
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
	if err := CallInference(account, firstMessages, "gpt-test", false, func(string, bool, *UsageInfo) {}, CallOptions{Session: session}); err != nil {
		t.Fatalf("first CallInference() error = %v", err)
	}
	completeConversationSession(session, 1, "gpt-test")

	secondMessages := []ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
		{Role: "user", Content: "second question"},
	}
	if err := CallInference(account, secondMessages, "gpt-test", false, func(string, bool, *UsageInfo) {}, CallOptions{Session: session}); err != nil {
		t.Fatalf("second CallInference() error = %v", err)
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
	if strings.Contains(string(secondJSON), "first answer") {
		t.Fatalf("continuation resent synthetic assistant history instead of using the stored Agent reply: %s", secondJSON)
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

func TestSessionFingerprintSurvivesGrowingClientHistory(t *testing.T) {
	first := []ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "remember marker"},
	}
	second := append(cloneChatMessages(first),
		ChatMessage{Role: "assistant", Content: "stored reply"},
		ChatMessage{Role: "user", Content: "what was it?"},
	)
	if got, want := computeSessionFingerprintWithSalt(second, ""), computeSessionFingerprintWithSalt(first, ""); got != want {
		t.Fatalf("growing history changed the conversation fingerprint: got=%q want=%q", got, want)
	}
	if got := countConversationMessages(second); got != 3 {
		t.Fatalf("message count = %d, want 3", got)
	}
}
