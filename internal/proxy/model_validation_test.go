package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnthropicHandlerRejectsUnknownModelBeforeRouting(t *testing.T) {
	previousConfig := AppConfig
	originalMap := SnapshotModelMap()
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{
		"opus-4.6": "notion-opus-46",
	})
	t.Cleanup(func() {
		AppConfig = previousConfig
		ReplaceModelMap(originalMap)
	})

	body := []byte(`{
		"model": "claude-opus-4.9",
		"max_tokens": 32,
		"messages": [{"role": "user", "content": "hello"}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleAnthropicMessages(NewAccountPool()).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var payload struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Type != "not_found_error" {
		t.Fatalf("error type = %q, want not_found_error", payload.Error.Type)
	}
	if payload.Error.Message == "" {
		t.Fatal("expected a useful model validation message")
	}
}

func TestOpenAIHandlerRejectsUnknownModelBeforeRouting(t *testing.T) {
	previousConfig := AppConfig
	originalMap := SnapshotModelMap()
	AppConfig = DefaultConfig()
	ReplaceModelMap(map[string]string{
		"gpt-5.4": "notion-gpt-54",
	})
	t.Cleanup(func() {
		AppConfig = previousConfig
		ReplaceModelMap(originalMap)
	})

	body := []byte(`{
		"model": "gpt-999",
		"messages": [{"role": "user", "content": "hello"}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleOpenAIChatCompletions(NewAccountPool()).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}

	var payload OpenAIErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Error.Type != "not_found_error" {
		t.Fatalf("error type = %q, want not_found_error", payload.Error.Type)
	}
}
