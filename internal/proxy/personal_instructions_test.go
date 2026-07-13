package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNotionPersonalInstructionsConfigDefaultsAndEnv(t *testing.T) {
	previous := AppConfig
	t.Cleanup(func() { AppConfig = previous })

	if DefaultConfig().NotionPersonalInstructionsEnabled() {
		t.Fatal("personal instructions mode must default to false")
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("proxy:\n  use_notion_personal_instructions: false\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("USE_NOTION_PERSONAL_INSTRUCTIONS", "true")

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.NotionPersonalInstructionsEnabled() {
		t.Fatal("environment variable should override config file")
	}

	t.Setenv("USE_NOTION_PERSONAL_INSTRUCTIONS", "false")
	cfg, err = LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig false override: %v", err)
	}
	if cfg.NotionPersonalInstructionsEnabled() {
		t.Fatal("false environment variable should disable the mode")
	}
}

func TestLoadOldConfigDefaultsPersonalInstructionsOff(t *testing.T) {
	previous := AppConfig
	t.Cleanup(func() { AppConfig = previous })
	t.Setenv("USE_NOTION_PERSONAL_INSTRUCTIONS", "")

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("proxy:\n  default_model: claude-opus-4.6\n"), 0o644); err != nil {
		t.Fatalf("write old config: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.NotionPersonalInstructionsEnabled() {
		t.Fatal("old configs without the new field must remain in existing prompt mode")
	}
}

func TestAdminSettingsReadsUpdatesAndPersistsPersonalInstructions(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	globalSessionManager.Clear()
	globalSessionManager.Set("existing", &Session{LastUsedAt: time.Now()})
	t.Cleanup(globalSessionManager.Clear)

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("proxy:\n  enable_web_search: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	handler := HandleAdminSettings(configPath, NewDashboardAuth("", ""))
	put := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"use_notion_personal_instructions":true}`))
	put.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, put)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var updated map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode PUT response: %v", err)
	}
	if enabled, _ := updated["use_notion_personal_instructions"].(bool); !enabled {
		t.Fatalf("PUT response did not enable setting: %#v", updated)
	}
	if !AppConfig.NotionPersonalInstructionsEnabled() {
		t.Fatal("runtime config was not updated")
	}
	if globalSessionManager.Count() != 0 {
		t.Fatal("prompt-mode change must clear existing Notion thread sessions")
	}

	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if !strings.Contains(string(persisted), "use_notion_personal_instructions: true") {
		t.Fatalf("setting was not persisted:\n%s", persisted)
	}

	get := httptest.NewRequest(http.MethodGet, "/admin/settings", nil)
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", getRec.Code, getRec.Body.String())
	}
	var current map[string]interface{}
	if err := json.Unmarshal(getRec.Body.Bytes(), &current); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if enabled, _ := current["use_notion_personal_instructions"].(bool); !enabled {
		t.Fatalf("GET response did not expose setting: %#v", current)
	}
}

func TestExtractNotionPersonalInstructionsPageID(t *testing.T) {
	nested := []byte(`{
		"recordMap": {
			"space_view": {
				"view-a": {
					"value": {
						"value": {
							"id": "view-a",
							"space_id": "space-a",
							"settings": {
								"agent_personalization_settings": {
									"context_page_id": "page-a"
								}
							}
						}
					}
				},
				"view-b": {
					"value": {
						"value": {
							"id": "view-b",
							"space_id": "space-b",
							"settings": {
								"agent_personalization_settings": {
									"context_page_id": "page-b"
								}
							}
						}
					}
				}
			}
		}
	}`)

	pageID, err := extractNotionPersonalInstructionsPageID(nested, "view-b", "space-b")
	if err != nil {
		t.Fatalf("extract nested: %v", err)
	}
	if pageID != "page-b" {
		t.Fatalf("got %q, want page-b", pageID)
	}

	flat := []byte(`{
		"recordMap": {
			"space_view": {
				"view-c": {
					"value": {
						"id": "view-c",
						"space_id": "space-c",
						"settings": {
							"agent_personalization_settings": {
								"context_page_id": "page-c"
							}
						}
					}
				}
			}
		}
	}`)
	pageID, err = extractNotionPersonalInstructionsPageID(flat, "", "space-c")
	if err != nil {
		t.Fatalf("extract flat: %v", err)
	}
	if pageID != "page-c" {
		t.Fatalf("got %q, want page-c", pageID)
	}

	pageID, err = extractNotionPersonalInstructionsPageID(nested, "missing", "space-missing")
	if err != nil {
		t.Fatalf("extract missing: %v", err)
	}
	if pageID != "" {
		t.Fatalf("must not reuse another account's page ID, got %q", pageID)
	}
}

func TestBuildFullTranscriptPersonalInstructionsMode(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	acc := &Account{
		UserID:      "user-a",
		UserName:    "User A",
		UserEmail:   "a@example.com",
		SpaceID:     "space-a",
		SpaceName:   "Space A",
		SpaceViewID: "view-a",
		Timezone:    "UTC",
	}
	messages := []ChatMessage{
		{Role: "system", Content: "CLIENT SYSTEM PROMPT MUST NOT BE SENT"},
		{Role: "user", Content: `Available functions: Read(path). Output JSON for the matching function.`},
	}

	transcript := buildFullTranscript(
		acc,
		messages,
		"model-a",
		false,
		true,
		nil,
		false,
		nil,
		"config-id",
		"context-id",
		"2026-07-13T00:00:00Z",
		true,
		"page-a",
	)

	config := transcript[0].(ResearcherTranscriptMsg).Value.(map[string]interface{})
	if custom, _ := config["isCustomAgent"].(bool); custom {
		t.Fatal("default Agent personal instructions must not set isCustomAgent=true")
	}

	context := transcript[1].(ResearcherTranscriptMsg).Value.(map[string]interface{})
	if got := context["context_page_id"]; got != "page-a" {
		t.Fatalf("context_page_id = %#v, want page-a", got)
	}

	user := transcript[2].(ResearcherTranscriptMsg)
	userText := user.Value.([][]string)[0][0]
	if strings.Contains(userText, "CLIENT SYSTEM PROMPT") {
		t.Fatalf("client system prompt leaked into personal-instructions mode: %q", userText)
	}
	if !strings.Contains(userText, "Available functions: Read") {
		t.Fatalf("tool protocol was not preserved: %q", userText)
	}
}

func TestPersonalInstructionsModePreservesInjectedToolBridge(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	bridged := injectToolsIntoMessages(
		[]ChatMessage{
			{Role: "system", Content: "client behavioral prompt"},
			{Role: "user", Content: "read the file"},
		},
		[]Tool{{
			Type: "function",
			Function: ToolFunction{
				Name:       "Read",
				Parameters: map[string]interface{}{"type": "object"},
			},
		}},
		"claude-opus-4-6",
		nil,
	)

	transcript := buildFullTranscript(
		&Account{UserID: "user-a", SpaceID: "space-a"},
		bridged,
		"model-a",
		false,
		true,
		nil,
		false,
		nil,
		"config-id",
		"context-id",
		"2026-07-13T00:00:00Z",
		true,
		"page-a",
	)
	userText := transcript[2].(ResearcherTranscriptMsg).Value.([][]string)[0][0]
	if strings.Contains(userText, "client behavioral prompt") {
		t.Fatalf("client system prompt leaked after tool injection: %q", userText)
	}
	if !strings.Contains(userText, "Read") || !strings.Contains(userText, `"name": "function_name"`) {
		t.Fatalf("tool bridge instructions were not preserved: %q", userText)
	}
}

func TestPersonalInstructionsOverrideDisableNotionPrompt(t *testing.T) {
	if !effectiveDisableBuiltinTools(true, false) {
		t.Fatal("existing mode must preserve disable_notion_prompt=true")
	}
	if effectiveDisableBuiltinTools(true, true) {
		t.Fatal("personal instructions mode must keep the default Agent prompt chain enabled")
	}
}

func TestBuildFullTranscriptExistingModeUnchanged(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	acc := &Account{UserID: "user-a", SpaceID: "space-a"}
	transcript := buildFullTranscript(
		acc,
		[]ChatMessage{
			{Role: "system", Content: "existing system"},
			{Role: "user", Content: "hello"},
		},
		"model-a",
		true,
		false,
		nil,
		false,
		nil,
		"config-id",
		"context-id",
		"2026-07-13T00:00:00Z",
		false,
		"",
	)

	context := transcript[1].(ResearcherTranscriptMsg).Value.(map[string]interface{})
	if _, ok := context["context_page_id"]; ok {
		t.Fatal("default mode must not add context_page_id")
	}
	userText := transcript[2].(ResearcherTranscriptMsg).Value.([][]string)[0][0]
	if !strings.Contains(userText, "existing system") || !strings.Contains(userText, "hello") {
		t.Fatalf("existing system-prompt behavior changed: %q", userText)
	}
}

func TestBuildPartialTranscriptUsesCurrentAccountsPageID(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	session := &Session{
		ConfigID:         "config-id",
		ContextID:        "context-id",
		OriginalDatetime: "2026-07-13T00:00:00Z",
	}
	accA := &Account{UserID: "user-a", SpaceID: "space-a"}
	accB := &Account{UserID: "user-b", SpaceID: "space-b"}

	getPageID := func(transcript []interface{}) interface{} {
		return transcript[1].(ResearcherTranscriptMsg).Value.(map[string]interface{})["context_page_id"]
	}
	a := buildPartialTranscript(accA, "hello", "model", false, true, nil, false, session, "page-a")
	b := buildPartialTranscript(accB, "hello", "model", false, true, nil, false, session, "page-b")
	if getPageID(a) != "page-a" || getPageID(b) != "page-b" {
		t.Fatalf("account-specific page IDs were mixed: A=%v B=%v", getPageID(a), getPageID(b))
	}
}

func TestRecoveryPromptOmitsClientSystemInPersonalInstructionsMode(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	AppConfig.Proxy.UseNotionPersonalInstructions = true
	t.Cleanup(func() { AppConfig = previous })

	recovered := buildFreshThreadRecoveryMessages([]ChatMessage{
		{Role: "system", Content: "CLIENT SYSTEM PROMPT MUST NOT BE SENT"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "second"},
	})
	if len(recovered) != 1 {
		t.Fatalf("recovered message count = %d, want 1", len(recovered))
	}
	if strings.Contains(recovered[0].Content, "CLIENT SYSTEM PROMPT") {
		t.Fatalf("client system prompt leaked through recovery: %q", recovered[0].Content)
	}
	if !strings.Contains(recovered[0].Content, "second") {
		t.Fatalf("latest user message missing from recovery: %q", recovered[0].Content)
	}
}
