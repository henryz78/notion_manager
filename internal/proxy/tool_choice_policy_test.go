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

func TestEffectiveToolChoicePolicy(t *testing.T) {
	clientChoice := map[string]interface{}{
		"type": "tool",
		"name": "get_current_time",
	}

	tests := []struct {
		policy   string
		wantMode string
	}{
		{policy: ToolChoicePolicyClient, wantMode: "force:get_current_time"},
		{policy: ToolChoicePolicyAuto, wantMode: "auto"},
		{policy: ToolChoicePolicyRequired, wantMode: "required"},
		{policy: ToolChoicePolicyNone, wantMode: "none"},
		{policy: "invalid", wantMode: "force:get_current_time"},
	}

	for _, test := range tests {
		t.Run(test.policy, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Proxy.ToolChoicePolicy = test.policy
			if got := resolveToolChoiceMode(cfg.EffectiveToolChoice(clientChoice)); got != test.wantMode {
				t.Fatalf("effective mode = %q, want %q", got, test.wantMode)
			}
		})
	}
}

func TestAdminSettingsUpdatesPersistsAndValidatesToolChoicePolicy(t *testing.T) {
	previous := AppConfig
	AppConfig = DefaultConfig()
	t.Cleanup(func() { AppConfig = previous })

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("proxy:\n  enable_tool_bridge: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := HandleAdminSettings(configPath, NewDashboardAuth("", ""))

	put := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"tool_choice_policy":"required"}`))
	put.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, put)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var updated map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated["tool_choice_policy"] != ToolChoicePolicyRequired || AppConfig.ToolChoicePolicy() != ToolChoicePolicyRequired {
		t.Fatalf("policy was not applied: response=%#v runtime=%q", updated, AppConfig.ToolChoicePolicy())
	}
	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(persisted), "tool_choice_policy: required") {
		t.Fatalf("policy was not persisted:\n%s", persisted)
	}

	invalid := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"tool_choice_policy":"always"}`))
	invalid.Header.Set("Content-Type", "application/json")
	invalidRecorder := httptest.NewRecorder()
	handler.ServeHTTP(invalidRecorder, invalid)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid PUT status=%d body=%s", invalidRecorder.Code, invalidRecorder.Body.String())
	}
	if AppConfig.ToolChoicePolicy() != ToolChoicePolicyRequired {
		t.Fatalf("invalid PUT changed policy to %q", AppConfig.ToolChoicePolicy())
	}
}

func TestLoadConfigNormalizesToolChoicePolicy(t *testing.T) {
	previous := AppConfig
	t.Cleanup(func() { AppConfig = previous })
	t.Setenv("TOOL_CHOICE_POLICY", "")

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("proxy:\n  tool_choice_policy: AUTO\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ToolChoicePolicy() != ToolChoicePolicyAuto || cfg.Proxy.ToolChoicePolicy != ToolChoicePolicyAuto {
		t.Fatalf("policy=%q", cfg.Proxy.ToolChoicePolicy)
	}
}
