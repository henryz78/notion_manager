package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestHistoryStoreRetentionFilteringAndNewestFirst(t *testing.T) {
	store, err := NewRequestHistoryStore("", 3)
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 5; i++ {
		status := "success"
		if i == 4 {
			status = "error"
		}
		store.Record(RequestHistoryEntry{
			ID:             string(rune('0' + i)),
			CreatedAt:      time.Unix(int64(i), 0).UTC(),
			API:            "anthropic",
			RequestedModel: "claude-opus-4.8",
			NotionModel:    "internal-opus",
			AccountEmail:   "account@example.com",
			PromptMode:     RequestPromptModeExisting,
			Status:         status,
		})
	}

	page := store.Snapshot(RequestHistoryQuery{PageSize: 10})
	if page.Total != 3 || page.FilteredTotal != 3 {
		t.Fatalf("expected retained total 3, got %+v", page)
	}
	if got := []string{page.Entries[0].ID, page.Entries[1].ID, page.Entries[2].ID}; strings.Join(got, ",") != "5,4,3" {
		t.Fatalf("expected newest-first 5,4,3, got %v", got)
	}

	errorsOnly := store.Snapshot(RequestHistoryQuery{Status: "error", Query: "ACCOUNT@", PageSize: 10})
	if errorsOnly.FilteredTotal != 1 || len(errorsOnly.Entries) != 1 || errorsOnly.Entries[0].ID != "4" {
		t.Fatalf("unexpected filtered page: %+v", errorsOnly)
	}
}

func TestRequestHistoryStoreDefaultLimitIsOneHundred(t *testing.T) {
	store, err := NewRequestHistoryStore("", 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		store.Record(RequestHistoryEntry{ID: generateUUIDv4(), Status: "success"})
	}
	if got := store.Snapshot(RequestHistoryQuery{PageSize: 200}).Total; got != 100 {
		t.Fatalf("expected default retention limit 100, got %d", got)
	}
}

func TestRequestHistoryStoreConcurrentRecord(t *testing.T) {
	store, err := NewRequestHistoryStore("", 1000)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Record(RequestHistoryEntry{ID: generateUUIDv4(), Status: "success"})
		}()
	}
	wg.Wait()

	if got := store.Snapshot(RequestHistoryQuery{PageSize: 200}).Total; got != 100 {
		t.Fatalf("expected 100 entries, got %d", got)
	}
}

func TestRequestHistoryStoreSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".request_history.json")
	store, err := NewRequestHistoryStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	store.Record(RequestHistoryEntry{
		ID:             "req-1",
		CreatedAt:      time.Now().UTC(),
		API:            "openai_chat",
		RequestedModel: "claude-opus-4.8",
		NotionModel:    "internal-opus",
		AccountEmail:   "user@example.com",
		PromptMode:     RequestPromptModePersonalInstructions,
		ToolCount:      3,
		InputTokens:    120,
		OutputTokens:   40,
		DurationMs:     250,
		Status:         "success",
		HTTPStatus:     http.StatusOK,
		Attempts:       1,
	})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	loaded, err := NewRequestHistoryStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	page := loaded.Snapshot(RequestHistoryQuery{PageSize: 10})
	if len(page.Entries) != 1 {
		t.Fatalf("expected one entry, got %+v", page)
	}
	got := page.Entries[0]
	if got.NotionModel != "internal-opus" || got.AccountEmail != "user@example.com" || got.ToolCount != 3 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"messages", "system_prompt", "response_text", "tool_arguments", "personal_instruction_content"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("history file unexpectedly contains content field %q: %s", forbidden, raw)
		}
	}
}

func TestTrackRequestHistoryRecordsMetadataAndSanitizedError(t *testing.T) {
	originalConfig := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = originalConfig })

	store, err := NewRequestHistoryStore("", 10)
	if err != nil {
		t.Fatal(err)
	}
	handler := TrackRequestHistory("openai_chat", store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		diagnostic := RequestDiagnosticFromContext(r.Context())
		if diagnostic == nil {
			t.Fatal("missing request diagnostic in context")
		}
		diagnostic.SetRequestedModel("claude-opus-4.8", false)
		diagnostic.SetNotionModel("notion-internal-opus")
		diagnostic.SetClientRequest(true, "required", nil, 2)
		diagnostic.SetToolBridge("sentinel_json")
		diagnostic.SetFinishReason("tool_calls")
		diagnostic.SetSession("0123456789abcdef", "replayed_account_switch", 3)
		diagnostic.BeginAttempt("selected@example.com")
		diagnostic.AddUsage(100, 25)
		diagnostic.FinishAttempt("upstream_error")
		writeOpenAIError(w, http.StatusBadGateway, "upstream failed\nwith details", "api_error", "")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"secret question":"do not save me"}`))
	handler.ServeHTTP(rec, req)

	page := store.Snapshot(RequestHistoryQuery{PageSize: 10})
	if len(page.Entries) != 1 {
		t.Fatalf("expected one entry, got %+v", page)
	}
	entry := page.Entries[0]
	if entry.API != "openai_chat" || entry.RequestedModel != "claude-opus-4.8" || entry.NotionModel != "notion-internal-opus" {
		t.Fatalf("model metadata mismatch: %+v", entry)
	}
	if entry.AccountEmail != "selected@example.com" || entry.Attempts != 1 || entry.ToolCount != 2 {
		t.Fatalf("routing metadata mismatch: %+v", entry)
	}
	if !entry.Stream || entry.ToolChoice != "required" || entry.ToolBridge != "sentinel_json" || entry.FinishReason != "tool_calls" {
		t.Fatalf("protocol metadata mismatch: %+v", entry)
	}
	if entry.SessionID != "0123456789ab" || entry.SessionState != "replayed_account_switch" || entry.SessionTurn != 3 {
		t.Fatalf("session metadata mismatch: %+v", entry)
	}
	if len(entry.AttemptDetails) != 1 || entry.AttemptDetails[0].Outcome != "upstream_error" || entry.AttemptDetails[0].AccountEmail != "selected@example.com" {
		t.Fatalf("attempt metadata mismatch: %+v", entry.AttemptDetails)
	}
	if entry.InputTokens != 100 || entry.OutputTokens != 25 || entry.Status != "error" || entry.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("result metadata mismatch: %+v", entry)
	}
	if entry.Error != "upstream failed with details" {
		t.Fatalf("expected sanitized error, got %q", entry.Error)
	}
	encoded, _ := json.Marshal(entry)
	if bytes.Contains(encoded, []byte("do not save me")) {
		t.Fatalf("request body leaked into history: %s", encoded)
	}
}

func TestTrackedAnthropicInvalidModelCapturesRequestedModelWithoutAttempt(t *testing.T) {
	originalConfig := AppConfig
	originalModels := SnapshotModelMap()
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{"claude-opus-4.8": "notion-opus"})
	t.Cleanup(func() {
		AppConfig = originalConfig
		ReplaceModelMap(originalModels)
	})

	store, err := NewRequestHistoryStore("", 10)
	if err != nil {
		t.Fatal(err)
	}
	handler := TrackRequestHistory("anthropic", store, HandleAnthropicMessages(NewAccountPool()))
	body := `{
		"model":"claude-opus-4.999",
		"max_tokens":32,
		"messages":[{"role":"user","content":"private question"}],
		"tools":[{"name":"lookup","description":"private tool details","input_schema":{"type":"object"}}]
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
	page := store.Snapshot(RequestHistoryQuery{PageSize: 10})
	if len(page.Entries) != 1 {
		t.Fatalf("expected one entry, got %+v", page)
	}
	entry := page.Entries[0]
	if entry.RequestedModel != "claude-opus-4.999" || entry.ToolCount != 1 {
		t.Fatalf("request metadata mismatch: %+v", entry)
	}
	if entry.NotionModel != "" || entry.AccountEmail != "" || entry.Attempts != 0 {
		t.Fatalf("invalid model must not look sent to Notion: %+v", entry)
	}
	encoded, _ := json.Marshal(entry)
	for _, secret := range []string{"private question", "private tool details"} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatalf("private content %q leaked into history: %s", secret, encoded)
		}
	}
}

func TestHandleAdminRequestHistoryGetAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".request_history.json")
	store, err := NewRequestHistoryStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	store.Record(RequestHistoryEntry{ID: "req-1", Status: "success"})
	handler := HandleAdminRequestHistory(store, NewDashboardAuth("", ""))

	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/admin/request-history?page_size=5", nil))
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), `"req-1"`) {
		t.Fatalf("unexpected GET response: %d %s", getRec.Code, getRec.Body.String())
	}

	deleteRec := httptest.NewRecorder()
	handler.ServeHTTP(deleteRec, httptest.NewRequest(http.MethodDelete, "/admin/request-history", nil))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("unexpected DELETE response: %d %s", deleteRec.Code, deleteRec.Body.String())
	}
	if got := store.Snapshot(RequestHistoryQuery{}).Total; got != 0 {
		t.Fatalf("expected cleared history, got %d", got)
	}

	reloaded, err := NewRequestHistoryStore(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Snapshot(RequestHistoryQuery{}).Total; got != 0 {
		t.Fatalf("expected persisted clear, got %d", got)
	}
}
