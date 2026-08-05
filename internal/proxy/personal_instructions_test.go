package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestIndependentPromptAndToolSettingsDefaultsAndEnv(t *testing.T) {
	t.Setenv("USE_CLIENT_SYSTEM_PROMPT", "")
	t.Setenv("ENABLE_TOOL_BRIDGE", "")

	defaults := DefaultConfig()
	if !defaults.ClientSystemPromptEnabled() {
		t.Fatal("client system prompt must default to true")
	}
	if !defaults.ToolBridgeEnabled() {
		t.Fatal("tool bridge must default to true")
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("proxy:\n  default_model: claude-opus-4.6\n"), 0o644); err != nil {
		t.Fatalf("write old config: %v", err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig old config: %v", err)
	}
	if !cfg.ClientSystemPromptEnabled() || !cfg.ToolBridgeEnabled() {
		t.Fatal("old configs must retain client prompts and tool compatibility")
	}

	t.Setenv("USE_CLIENT_SYSTEM_PROMPT", "false")
	t.Setenv("ENABLE_TOOL_BRIDGE", "false")
	cfg, err = LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig env overrides: %v", err)
	}
	if cfg.ClientSystemPromptEnabled() || cfg.ToolBridgeEnabled() {
		t.Fatal("environment variables should independently disable both settings")
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
	globalSessionManager.Clear()
	t.Cleanup(func() {
		globalSessionManager.Clear()
		AppConfig = previous
	})

	sessionKey := "settings-invalidation"
	globalSessionManager.Set(sessionKey, newConversationSession("settings@example.com"))

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("proxy:\n  enable_web_search: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	handler := HandleAdminSettings(configPath, NewDashboardAuth("", ""))
	put := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"use_client_system_prompt":false,"use_notion_personal_instructions":true,"enable_tool_bridge":false}`))
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
	if AppConfig.ClientSystemPromptEnabled() || AppConfig.ToolBridgeEnabled() {
		t.Fatal("independent prompt/tool settings were not updated")
	}
	if globalSessionManager.Get(sessionKey) != nil {
		t.Fatal("thread-affecting settings update did not clear existing sessions")
	}
	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if !strings.Contains(string(persisted), "use_notion_personal_instructions: true") {
		t.Fatalf("setting was not persisted:\n%s", persisted)
	}
	if !strings.Contains(string(persisted), "use_client_system_prompt: false") ||
		!strings.Contains(string(persisted), "enable_tool_bridge: false") {
		t.Fatalf("independent settings were not persisted:\n%s", persisted)
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
	if enabled, _ := current["use_client_system_prompt"].(bool); enabled {
		t.Fatalf("GET response did not expose disabled client prompt: %#v", current)
	}
	if enabled, _ := current["enable_tool_bridge"].(bool); enabled {
		t.Fatalf("GET response did not expose disabled tool bridge: %#v", current)
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

	pageID, err = extractNotionPersonalInstructionsPageID(nested, "", "")
	if err != nil {
		t.Fatalf("extract empty account context: %v", err)
	}
	if pageID != "" {
		t.Fatalf("must not select a page without account workspace context, got %q", pageID)
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
		false,
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

func TestPersonalInstructionsModePreservesOneToolBridgeContract(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	tools := []Tool{{
		Type: "function",
		Function: ToolFunction{
			Name:       "Read",
			Parameters: map[string]interface{}{"type": "object"},
		},
	}}

	transcript := buildFullTranscript(
		&Account{UserID: "user-a", SpaceID: "space-a"},
		[]ChatMessage{
			{Role: "system", Content: "client behavioral prompt"},
			{Role: "user", Content: "read the file"},
		},
		"model-a",
		false,
		true,
		nil,
		false,
		nil,
		"config-id",
		"context-id",
		"2026-07-13T00:00:00Z",
		false,
		true,
		"page-a",
		buildToolBridgeContract(tools, false),
	)
	contractText := transcript[2].(ResearcherTranscriptMsg).Value.([][]string)[0][0]
	userText := transcript[3].(ResearcherTranscriptMsg).Value.([][]string)[0][0]
	if strings.Contains(userText, "client behavioral prompt") {
		t.Fatalf("client system prompt leaked after tool contract: %q", userText)
	}
	if !strings.Contains(contractText, "Read") || !strings.Contains(contractText, toolBridgeContractMarker) {
		t.Fatalf("tool bridge contract was not preserved: %q", contractText)
	}
}

func TestPersonalInstructionsOverrideDisableNotionPrompt(t *testing.T) {
	if !effectiveDisableBuiltinTools(true, false, false) {
		t.Fatal("existing mode must preserve disable_notion_prompt=true")
	}
	if effectiveDisableBuiltinTools(true, true, false) {
		t.Fatal("personal instructions mode must keep the default Agent prompt chain enabled")
	}
	if !effectiveDisableBuiltinTools(false, true, true) {
		t.Fatal("client tools must disable Notion built-in tools without disabling personal-instructions context")
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
		true,
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

func TestBuildFullTranscriptSupportsBothPromptSourcesAndNeither(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	acc := &Account{UserID: "user-a", SpaceID: "space-a"}
	build := func(useClient, usePersonal bool, pageID string) []interface{} {
		return buildFullTranscript(
			acc,
			[]ChatMessage{
				{Role: "system", Content: "client system"},
				{Role: "user", Content: "hello"},
			},
			"model-a",
			false,
			true,
			nil,
			false,
			nil,
			"config-id",
			"context-id",
			"2026-07-13T00:00:00Z",
			useClient,
			usePersonal,
			pageID,
		)
	}

	both := build(true, true, "page-a")
	bothContext := both[1].(ResearcherTranscriptMsg).Value.(map[string]interface{})
	bothText := both[2].(ResearcherTranscriptMsg).Value.([][]string)[0][0]
	if bothContext["context_page_id"] != "page-a" || !strings.Contains(bothText, "client system") {
		t.Fatalf("both prompt sources were not included: context=%#v text=%q", bothContext, bothText)
	}

	neither := build(false, false, "")
	neitherContext := neither[1].(ResearcherTranscriptMsg).Value.(map[string]interface{})
	neitherText := neither[2].(ResearcherTranscriptMsg).Value.([][]string)[0][0]
	if _, ok := neitherContext["context_page_id"]; ok || strings.Contains(neitherText, "client system") {
		t.Fatalf("disabled prompt sources were included: context=%#v text=%q", neitherContext, neitherText)
	}
	if !strings.Contains(neitherText, "hello") {
		t.Fatalf("normal user message was lost: %q", neitherText)
	}
}

func TestCurrentRequestPromptModeSupportsAllCombinations(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	tests := []struct {
		client   bool
		personal bool
		want     string
	}{
		{true, false, RequestPromptModeExisting},
		{false, true, RequestPromptModePersonalInstructions},
		{true, true, RequestPromptModeClientAndPersonal},
		{false, false, RequestPromptModeNone},
	}
	for _, tt := range tests {
		AppConfig.Proxy.UseClientSystemPrompt = tt.client
		AppConfig.Proxy.UseNotionPersonalInstructions = tt.personal
		if got := currentRequestPromptMode(); got != tt.want {
			t.Fatalf("client=%v personal=%v: got %q, want %q", tt.client, tt.personal, got, tt.want)
		}
	}
}

func TestToolBridgeSettingControlsExternalToolHandling(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	if !toolBridgeActive(false, 1) {
		t.Fatal("default configuration should handle client tools")
	}
	AppConfig.Proxy.EnableToolBridge = false
	if toolBridgeActive(false, 1) {
		t.Fatal("disabled tool bridge should treat tool requests as normal chat")
	}
	AppConfig.Proxy.EnableToolBridge = true
	if toolBridgeActive(true, 1) || toolBridgeActive(false, 0) {
		t.Fatal("researcher requests and requests without tools should skip the bridge")
	}
}

func TestFullReplayOmitsClientSystemInPersonalInstructionsMode(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	AppConfig.Proxy.UseClientSystemPrompt = false
	AppConfig.Proxy.UseNotionPersonalInstructions = true
	t.Cleanup(func() { AppConfig = previous })

	messages := []ChatMessage{
		{Role: "system", Content: "CLIENT SYSTEM PROMPT MUST NOT BE SENT"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
		{Role: "user", Content: "second"},
	}
	transcript := buildFullTranscript(
		&Account{UserID: "user", UserEmail: "user@example.com"}, messages,
		"model", false, false, nil, false, nil, "config", "context",
		"2026-07-26T00:00:00Z", false, true, "page-id",
	)
	raw, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	if strings.Contains(serialized, "CLIENT SYSTEM PROMPT") {
		t.Fatalf("client system prompt leaked through full replay: %s", serialized)
	}
	for _, want := range []string{"first", "answer", "second"} {
		if !strings.Contains(serialized, want) {
			t.Fatalf("full replay dropped %q: %s", want, serialized)
		}
	}
}
