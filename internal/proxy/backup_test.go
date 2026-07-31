package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandleAdminBackupDownloadIncludesAccountsAndDashboardSettings(t *testing.T) {
	accountsDir, configPath, pool := setupBackupTest(t)
	writeBackupTestAccount(t, accountsDir, "first.json", "first@example.com", "user-1", "space-1", "token-1", "cookie-1")
	if err := os.WriteFile(filepath.Join(accountsDir, ".request_history.json"), []byte(`{"private_state":"keep-out"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pool.LoadFromDir(accountsDir); err != nil {
		t.Fatal(err)
	}

	handler := HandleAdminBackup(pool, accountsDir, configPath, NewDashboardAuth("", ""))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/backup", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if !strings.Contains(recorder.Header().Get("Content-Disposition"), "notion-manager-backup-") {
		t.Fatalf("Content-Disposition=%q", recorder.Header().Get("Content-Disposition"))
	}

	var backup dashboardBackupFile
	if err := json.Unmarshal(recorder.Body.Bytes(), &backup); err != nil {
		t.Fatalf("decode backup: %v", err)
	}
	if backup.Format != dashboardBackupFormat || backup.Version != dashboardBackupVersion {
		t.Fatalf("unexpected manifest: %+v", backup)
	}
	if backup.Settings == nil || !backup.Settings.EnableWebSearch || backup.Settings.EnableWorkspaceSearch {
		t.Fatalf("settings=%+v", backup.Settings)
	}
	if backup.Settings.ToolChoicePolicy != ToolChoicePolicyClient {
		t.Fatalf("tool choice policy=%q", backup.Settings.ToolChoicePolicy)
	}
	if len(backup.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(backup.Accounts))
	}
	body := recorder.Body.String()
	for _, want := range []string{"token-1", "cookie-1"} {
		if !strings.Contains(body, want) {
			t.Fatalf("backup missing %q", want)
		}
	}
	for _, forbidden := range []string{"keep-api-secret", "keep-admin-secret", "keep-out"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("backup leaked excluded value %q", forbidden)
		}
	}
}

func TestHandleAdminBackupRestoreReplacesAccountsAndPreservesRuntimeFiles(t *testing.T) {
	accountsDir, configPath, pool := setupBackupTest(t)
	globalSessionManager.Clear()
	t.Cleanup(globalSessionManager.Clear)
	sessionKey := "backup-restore-invalidation"
	globalSessionManager.Set(sessionKey, newConversationSession("old@example.com"))
	writeBackupTestAccount(t, accountsDir, "old.json", "old@example.com", "old-user", "old-space", "old-token", "old-cookie")
	statePath := filepath.Join(accountsDir, ".token_stats.json")
	stateData := []byte(`{"requests":42}`)
	if err := os.WriteFile(statePath, stateData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pool.LoadFromDir(accountsDir); err != nil {
		t.Fatal(err)
	}

	newAccount := json.RawMessage(`{
  "token_v2": "new-token",
  "user_id": "new-user",
  "user_name": "New User",
  "user_email": "new@example.com",
  "space_id": "new-space",
  "space_name": "New Space",
  "full_cookie": "new-cookie",
  "disabled": true,
  "available_models": [{"name":"Claude Opus 5","id":"agave-flan"}]
}`)
	backup := dashboardBackupFile{
		Format:     dashboardBackupFormat,
		Version:    dashboardBackupVersion,
		CreatedAt:  time.Now().UTC(),
		AppVersion: "test",
		Settings: &dashboardBackupSettings{
			EnableWebSearch:               false,
			EnableWorkspaceSearch:         true,
			AskModeDefault:                true,
			UseClientSystemPrompt:         false,
			UseNotionPersonalInstructions: true,
			EnableToolBridge:              false,
			ToolChoicePolicy:              ToolChoicePolicyAuto,
			DebugLogging:                  false,
			NotionProxy:                   "",
		},
		Accounts: []json.RawMessage{newAccount},
	}
	payload, _ := json.Marshal(backup)
	handler := HandleAdminBackup(pool, accountsDir, configPath, NewDashboardAuth("", ""))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/backup", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if pool.Count() != 1 || pool.GetAccountByEmail("new@example.com") == nil || pool.GetAccountByEmail("old@example.com") != nil {
		t.Fatalf("pool was not replaced: %+v", pool.GetAccountDetails())
	}
	if globalSessionManager.Get(sessionKey) != nil {
		t.Fatal("successful backup restore did not clear existing sessions")
	}
	if !pool.GetAccountByEmail("new@example.com").ManuallyDisabled {
		t.Fatal("restored disabled state was lost")
	}
	if _, err := os.Stat(filepath.Join(accountsDir, "old.json")); !os.IsNotExist(err) {
		t.Fatalf("old account file still exists: %v", err)
	}
	restoredPath := filepath.Join(accountsDir, "new@example.com.json")
	restoredData, err := os.ReadFile(restoredPath)
	if err != nil {
		t.Fatalf("read restored account: %v", err)
	}
	if !strings.Contains(string(restoredData), "new-cookie") {
		t.Fatal("full_cookie was not preserved")
	}
	keptState, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(keptState, stateData) {
		t.Fatalf("runtime state changed: data=%s err=%v", keptState, err)
	}

	if AppConfig.WebSearchEnabled() || !AppConfig.WorkspaceSearchEnabled() || !AppConfig.AskModeDefault() {
		t.Fatalf("live settings not restored: %+v", currentDashboardBackupSettings())
	}
	if AppConfig.ClientSystemPromptEnabled() || !AppConfig.NotionPersonalInstructionsEnabled() || AppConfig.ToolBridgeEnabled() {
		t.Fatalf("prompt settings not restored: %+v", currentDashboardBackupSettings())
	}
	if AppConfig.ToolChoicePolicy() != ToolChoicePolicyAuto {
		t.Fatalf("tool choice policy not restored: %q", AppConfig.ToolChoicePolicy())
	}
	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"keep-api-secret", "keep-admin-secret", "enable_workspace_search: true", "ask_mode_default: true", "tool_choice_policy: auto"} {
		if !strings.Contains(string(persisted), want) {
			t.Fatalf("persisted config missing %q:\n%s", want, persisted)
		}
	}
}

func TestHandleAdminBackupRestoreRejectsInvalidAccountWithoutChangingState(t *testing.T) {
	accountsDir, configPath, pool := setupBackupTest(t)
	writeBackupTestAccount(t, accountsDir, "old.json", "old@example.com", "old-user", "old-space", "old-token", "old-cookie")
	if err := pool.LoadFromDir(accountsDir); err != nil {
		t.Fatal(err)
	}
	configBefore, _ := os.ReadFile(configPath)
	accountBefore, _ := os.ReadFile(filepath.Join(accountsDir, "old.json"))

	backup := dashboardBackupFile{
		Format:     dashboardBackupFormat,
		Version:    dashboardBackupVersion,
		CreatedAt:  time.Now().UTC(),
		AppVersion: "test",
		Settings:   backupSettingsPtr(currentDashboardBackupSettings()),
		Accounts: []json.RawMessage{
			json.RawMessage(`{"token_v2":"bad","user_id":"missing-space"}`),
		},
	}
	payload, _ := json.Marshal(backup)
	handler := HandleAdminBackup(pool, accountsDir, configPath, NewDashboardAuth("", ""))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/admin/backup", bytes.NewReader(payload)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if pool.Count() != 1 || pool.GetAccountByEmail("old@example.com") == nil {
		t.Fatal("pool changed after rejected restore")
	}
	configAfter, _ := os.ReadFile(configPath)
	accountAfter, _ := os.ReadFile(filepath.Join(accountsDir, "old.json"))
	if !bytes.Equal(configBefore, configAfter) || !bytes.Equal(accountBefore, accountAfter) {
		t.Fatal("disk state changed after rejected restore")
	}
}

func TestRestoredAccountFilenameDoesNotCollideWithRuntimeState(t *testing.T) {
	account := &Account{UserEmail: ".token_stats"}
	if got := restoredAccountFilename(account, 0); got != "account-token_stats.json" {
		t.Fatalf("restoredAccountFilename() = %q", got)
	}
}

func TestHandleAdminBackupRestoreConflictsWithAccountMutation(t *testing.T) {
	accountsDir, configPath, pool := setupBackupTest(t)
	writeBackupTestAccount(t, accountsDir, "old.json", "old@example.com", "old-user", "old-space", "old-token", "old-cookie")
	if err := pool.LoadFromDir(accountsDir); err != nil {
		t.Fatal(err)
	}

	releaseMutation, ok := pool.beginAccountMutation()
	if !ok {
		t.Fatal("failed to begin test account mutation")
	}
	defer releaseMutation()

	backup := dashboardBackupFile{
		Format:     dashboardBackupFormat,
		Version:    dashboardBackupVersion,
		CreatedAt:  time.Now().UTC(),
		AppVersion: "test",
		Settings:   backupSettingsPtr(currentDashboardBackupSettings()),
		Accounts: []json.RawMessage{
			json.RawMessage(`{"token_v2":"new-token","user_id":"new-user","user_email":"new@example.com","space_id":"new-space"}`),
		},
	}
	payload, _ := json.Marshal(backup)
	handler := HandleAdminBackup(pool, accountsDir, configPath, NewDashboardAuth("", ""))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/admin/backup", bytes.NewReader(payload)))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if pool.GetAccountByEmail("old@example.com") == nil || pool.GetAccountByEmail("new@example.com") != nil {
		t.Fatal("pool changed after conflicted restore")
	}
}

func TestAccountRestoreBlocksNewMutationEntryPoints(t *testing.T) {
	pool := NewAccountPool()
	releaseRestore, ok := pool.beginAccountRestore()
	if !ok {
		t.Fatal("failed to begin test restore")
	}
	defer releaseRestore()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/accounts/add", strings.NewReader(`{"token_v2":"unused"}`))
	HandleAddAccount(pool, t.TempDir(), NewDashboardAuth("", "")).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("add status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	manager, err := NewAccountBatchManager(pool, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Start(AccountBatchDisable, []string{"account@example.com"}, 1); err == nil || !strings.Contains(err.Error(), "restore") {
		t.Fatalf("batch start error=%v", err)
	}
	if pool.TriggerRefresh(t.TempDir()) {
		t.Fatal("refresh reported started while restore was active")
	}
	if refreshing, _ := pool.GetRefreshStatus()["refreshing"].(bool); refreshing {
		t.Fatal("refresh status changed while restore was active")
	}
}

func setupBackupTest(t *testing.T) (string, string, *AccountPool) {
	t.Helper()
	originalConfig := AppConfig
	originalModels := SnapshotModelMap()
	cfg := DefaultConfig()
	cfg.Proxy.EnableWebSearch = boolPtr(true)
	cfg.Proxy.EnableWorkspaceSearch = boolPtr(false)
	cfg.Proxy.AskModeDefault = boolPtr(false)
	cfg.Proxy.UseClientSystemPrompt = true
	cfg.Proxy.UseNotionPersonalInstructions = false
	cfg.Proxy.EnableToolBridge = true
	cfg.Proxy.ToolChoicePolicy = ToolChoicePolicyClient
	cfg.Server.DebugLogging = true
	AppConfig = cfg
	ApplyConfig(cfg)
	t.Cleanup(func() {
		AppConfig = originalConfig
		ReplaceModelMap(originalModels)
		if originalConfig != nil {
			ApplyConfig(originalConfig)
		}
	})

	root := t.TempDir()
	accountsDir := filepath.Join(root, "accounts")
	if err := os.MkdirAll(accountsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	config := `server:
  api_key: keep-api-secret
  admin_password: keep-admin-secret
  debug_logging: true
proxy:
  enable_web_search: true
  enable_workspace_search: false
  ask_mode_default: false
  use_client_system_prompt: true
  use_notion_personal_instructions: false
  enable_tool_bridge: true
  tool_choice_policy: client
  notion_proxy: ""
`
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return accountsDir, configPath, NewAccountPool()
}

func writeBackupTestAccount(t *testing.T, dir, filename, email, userID, spaceID, token, cookie string) {
	t.Helper()
	data := map[string]interface{}{
		"token_v2":       token,
		"user_id":        userID,
		"user_name":      strings.Split(email, "@")[0],
		"user_email":     email,
		"space_id":       spaceID,
		"space_name":     "Test Space",
		"full_cookie":    cookie,
		"browser_id":     "browser-" + userID,
		"plan_type":      "personal",
		"disabled":       false,
		"registered_via": "test",
	}
	raw, _ := json.MarshalIndent(data, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, filename), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
