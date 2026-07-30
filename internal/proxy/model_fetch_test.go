package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchModelsUsesWorkflowFinalModelName(t *testing.T) {
	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	chromeHTTPClientForTest = func(timeout time.Duration) *http.Client {
		return &http.Client{Timeout: timeout}
	}
	t.Cleanup(func() {
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		chromeHTTPClientForTest = previousClientOverride
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getAvailableModels" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"models": [
				{
					"clientModel": "client-display-model",
					"modelMessage": "GPT-5.6 Sol",
					"modelFamily": "openai",
					"isDisabled": false,
					"workflow": {"finalModelName": "workflow-execution-model"}
				},
				{
					"model": "legacy-model",
					"modelMessage": "Legacy",
					"modelFamily": "openai",
					"isDisabled": false
				},
				{
					"model": "disabled-client-model",
					"modelMessage": "Disabled",
					"modelFamily": "openai",
					"isDisabled": true,
					"workflow": {"finalModelName": "disabled-workflow-model"}
				}
			]
		}`)
	}))
	defer server.Close()

	NotionAPIBase = server.URL
	models, err := FetchModels(&Account{
		SpaceID:       "space-id",
		UserID:        "user-id",
		ClientVersion: DefaultClientVersion,
	})
	if err != nil {
		t.Fatalf("FetchModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2: %#v", len(models), models)
	}
	if models[0].ID != "workflow-execution-model" {
		t.Fatalf("workflow model ID = %q, want workflow-execution-model", models[0].ID)
	}
	if models[1].ID != "legacy-model" {
		t.Fatalf("legacy fallback ID = %q, want legacy-model", models[1].ID)
	}
}
