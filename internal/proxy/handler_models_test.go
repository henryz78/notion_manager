package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHandlePublicModels_UsesPoolModelsAndNormalizesIDs(t *testing.T) {
	original := SnapshotModelMap()
	ReplaceModelMap(map[string]string{
		"opus-4.6": "avocado-froyo-medium",
		"gpt-5.4":  "oval-kumquat-medium",
	})
	t.Cleanup(func() {
		ReplaceModelMap(original)
	})

	pool := NewAccountPool()
	pool.accounts = []*Account{
		{
			Models: []ModelEntry{
				{Name: "GPT 5.4", ID: "oval-kumquat-medium"},
				{Name: "Opus 4.6", ID: "avocado-froyo-medium"},
				{Name: "GPT 5.4", ID: "oval-kumquat-medium"},
			},
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	HandlePublicModels(pool).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp publicModelResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if resp.Object != "list" {
		t.Fatalf("expected object=list, got %q", resp.Object)
	}

	gotIDs := make([]string, 0, len(resp.Data))
	for _, item := range resp.Data {
		if item.Object != "model" {
			t.Fatalf("expected object=model, got %q", item.Object)
		}
		if item.Created != publicModelCreatedAt {
			t.Fatalf("expected created=%d, got %d", publicModelCreatedAt, item.Created)
		}
		if item.OwnedBy != "notion-manager" {
			t.Fatalf("expected owned_by notion-manager, got %q", item.OwnedBy)
		}
		gotIDs = append(gotIDs, item.ID)
	}

	wantIDs := []string{"claude-opus-4.6", "gpt-5.4"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("unexpected model ids: got %v want %v", gotIDs, wantIDs)
	}
}

func TestHandlePublicModels_FallsBackToDefaultModelMap(t *testing.T) {
	original := SnapshotModelMap()
	ReplaceModelMap(map[string]string{
		"gemini-2.5-flash": "vertex-gemini-2.5-flash",
		"sonnet-4.6":       "almond-croissant-low",
	})
	t.Cleanup(func() {
		ReplaceModelMap(original)
	})

	pool := NewAccountPool()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/models", nil)
	HandlePublicModels(pool).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp publicModelResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	gotIDs := make([]string, 0, len(resp.Data))
	for _, item := range resp.Data {
		gotIDs = append(gotIDs, item.ID)
	}

	wantIDs := []string{"claude-sonnet-4.6", "gemini-2.5-flash"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("unexpected fallback models: got %v want %v", gotIDs, wantIDs)
	}
}

func TestResolveModelAcceptsClaudeDotAliases(t *testing.T) {
	original := SnapshotModelMap()
	ReplaceModelMap(map[string]string{
		"sonnet-4.6": "almond-croissant-low",
		"opus-4.6":   "avocado-froyo-medium",
		"haiku-4.5":  "anthropic-haiku-4.5",
	})
	t.Cleanup(func() {
		ReplaceModelMap(original)
	})

	tests := map[string]string{
		"claude-sonnet-4.6": "almond-croissant-low",
		"claude-opus-4.6":   "avocado-froyo-medium",
		"claude-haiku-4.5":  "anthropic-haiku-4.5",
		"sonnet-4.6":        "almond-croissant-low",
	}

	for input, want := range tests {
		got, ok := ResolveModel(input)
		if !ok {
			t.Fatalf("ResolveModel(%q) unexpectedly failed", input)
		}
		if got != want {
			t.Fatalf("ResolveModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveModelRejectsUnknownClaudeVersions(t *testing.T) {
	original := SnapshotModelMap()
	ReplaceModelMap(map[string]string{
		"opus-4.6":        "notion-opus-46",
		"claude-opus-4.8": "notion-opus-48",
	})
	t.Cleanup(func() {
		ReplaceModelMap(original)
	})

	valid := map[string]string{
		"claude-opus-4.8":          "notion-opus-48",
		"claude-opus-4-8":          "notion-opus-48",
		"claude-opus-4-8-20260713": "notion-opus-48",
		"claude-opus-4.6":          "notion-opus-46",
	}
	for input, want := range valid {
		got, ok := ResolveModel(input)
		if !ok || got != want {
			t.Fatalf("ResolveModel(%q) = (%q, %v), want (%q, true)", input, got, ok, want)
		}
	}

	invalid := []string{
		"claude-opus-4.9",
		"claude-opus-4-9",
		"claude-opus-4-5",
		"opus-4.8",
		"totally-not-opus",
	}
	for _, input := range invalid {
		if got, ok := ResolveModel(input); ok {
			t.Fatalf("ResolveModel(%q) = (%q, true), want rejection", input, got)
		}
	}
}

func TestDisplayModelNameAddsClaudePrefixForAnthropicModels(t *testing.T) {
	tests := []struct {
		name       string
		internalID string
		family     string
		want       string
	}{
		{name: "Sonnet 4.6", internalID: "almond-croissant-low", family: "anthropic", want: "Claude Sonnet 4.6"},
		{name: "", internalID: "almond-croissant-low", family: "anthropic", want: "Claude Sonnet 4.6"},
		{name: "Claude Opus 4.6", internalID: "avocado-froyo-medium", family: "anthropic", want: "Claude Opus 4.6"},
		{name: "GPT 5.4", internalID: "oval-kumquat-medium", family: "openai", want: "GPT 5.4"},
	}

	for _, tc := range tests {
		if got := displayModelName(tc.name, tc.internalID, tc.family); got != tc.want {
			t.Fatalf("displayModelName(%q, %q, %q) = %q, want %q", tc.name, tc.internalID, tc.family, got, tc.want)
		}
	}
}

func TestHandlePublicModels_MethodNotAllowed(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/models", nil)
	HandlePublicModels(NewAccountPool()).ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", rec.Code, rec.Body.String())
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("expected Allow=GET, got %q", allow)
	}
}
