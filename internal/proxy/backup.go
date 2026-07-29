package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"notion-manager/internal/netutil"

	"gopkg.in/yaml.v3"
)

const (
	dashboardBackupFormat      = "notion-manager-backup"
	dashboardBackupVersion     = 1
	dashboardBackupMaxBytes    = 64 << 20
	dashboardBackupMaxAccounts = 20000
)

type dashboardBackupSettings struct {
	EnableWebSearch               bool   `json:"enable_web_search"`
	EnableWorkspaceSearch         bool   `json:"enable_workspace_search"`
	AskModeDefault                bool   `json:"ask_mode_default"`
	UseClientSystemPrompt         bool   `json:"use_client_system_prompt"`
	UseNotionPersonalInstructions bool   `json:"use_notion_personal_instructions"`
	EnableToolBridge              bool   `json:"enable_tool_bridge"`
	ToolChoicePolicy              string `json:"tool_choice_policy"`
	DebugLogging                  bool   `json:"debug_logging"`
	NotionProxy                   string `json:"notion_proxy"`
}

type dashboardBackupFile struct {
	Format     string                   `json:"format"`
	Version    int                      `json:"version"`
	CreatedAt  time.Time                `json:"created_at"`
	AppVersion string                   `json:"app_version"`
	Settings   *dashboardBackupSettings `json:"settings"`
	Accounts   []json.RawMessage        `json:"accounts"`
}

type restoreFileSnapshot struct {
	Path string
	Data []byte
	Mode os.FileMode
}

// HandleAdminBackup downloads and restores the account pool plus the settings
// controlled by the Dashboard. Deployment identity (API key, admin password,
// paths) and diagnostic/history files intentionally stay outside this format.
func HandleAdminBackup(pool *AccountPool, accountsDir, configPath string, auth *DashboardAuth) http.HandlerFunc {
	var restoreMu sync.Mutex
	return func(w http.ResponseWriter, r *http.Request) {
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
			return
		}

		switch r.Method {
		case http.MethodGet:
			restoreMu.Lock()
			defer restoreMu.Unlock()
			dashboardSettingsMu.Lock()
			defer dashboardSettingsMu.Unlock()
			handleBackupDownload(w, accountsDir)
		case http.MethodPost:
			restoreMu.Lock()
			defer restoreMu.Unlock()
			dashboardSettingsMu.Lock()
			defer dashboardSettingsMu.Unlock()
			handleBackupRestore(w, r, pool, accountsDir, configPath)
		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

func handleBackupDownload(w http.ResponseWriter, accountsDir string) {
	accounts, err := readBackupAccounts(accountsDir)
	if err != nil {
		writeBackupError(w, http.StatusInternalServerError, err.Error())
		return
	}

	backup := dashboardBackupFile{
		Format:     dashboardBackupFormat,
		Version:    dashboardBackupVersion,
		CreatedAt:  time.Now().UTC(),
		AppVersion: CurrentBuildVersion(),
		Settings:   backupSettingsPtr(currentDashboardBackupSettings()),
		Accounts:   accounts,
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="notion-manager-backup-%s.json"`, backup.CreatedAt.Format("20060102-150405")))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(backup); err != nil {
		log.Printf("[backup] encode download: %v", err)
	}
}

func handleBackupRestore(w http.ResponseWriter, r *http.Request, pool *AccountPool, accountsDir, configPath string) {
	r.Body = http.MaxBytesReader(w, r.Body, dashboardBackupMaxBytes)
	decoder := json.NewDecoder(r.Body)
	var backup dashboardBackupFile
	if err := decoder.Decode(&backup); err != nil {
		writeBackupError(w, http.StatusBadRequest, "invalid backup file: "+err.Error())
		return
	}
	if err := ensureJSONEOF(decoder); err != nil {
		writeBackupError(w, http.StatusBadRequest, "invalid backup file: "+err.Error())
		return
	}
	if backup.Format != dashboardBackupFormat || backup.Version != dashboardBackupVersion {
		writeBackupError(w, http.StatusBadRequest, "unsupported backup format or version")
		return
	}
	if backup.Settings == nil {
		writeBackupError(w, http.StatusBadRequest, "backup settings are missing")
		return
	}
	if len(backup.Accounts) > dashboardBackupMaxAccounts {
		writeBackupError(w, http.StatusBadRequest, fmt.Sprintf("backup contains too many accounts (max %d)", dashboardBackupMaxAccounts))
		return
	}
	if err := validateDashboardBackupSettings(*backup.Settings); err != nil {
		writeBackupError(w, http.StatusBadRequest, err.Error())
		return
	}

	restoredAccounts, restoredFiles, err := prepareRestoredAccounts(backup.Accounts)
	if err != nil {
		writeBackupError(w, http.StatusBadRequest, err.Error())
		return
	}
	releaseRestore, ok := pool.beginAccountRestore()
	if !ok {
		writeBackupError(w, http.StatusConflict, "an account update or restore is running; retry after it finishes")
		return
	}
	defer releaseRestore()

	previousSettings := currentDashboardBackupSettings()
	if err := persistDashboardBackupSettings(configPath, *backup.Settings); err != nil {
		writeBackupError(w, http.StatusInternalServerError, "save restored settings: "+err.Error())
		return
	}
	if err := replaceAccountFilesAndPool(pool, accountsDir, restoredAccounts, restoredFiles); err != nil {
		if rollbackErr := persistDashboardBackupSettings(configPath, previousSettings); rollbackErr != nil {
			log.Printf("[backup] settings rollback failed: %v", rollbackErr)
			writeBackupError(w, http.StatusInternalServerError, fmt.Sprintf("restore accounts: %v; settings rollback also failed: %v", err, rollbackErr))
			return
		}
		writeBackupError(w, http.StatusInternalServerError, "restore accounts: "+err.Error())
		return
	}

	applyDashboardBackupSettings(*backup.Settings)
	log.Printf("[backup] restored %d account(s) and dashboard settings", len(restoredAccounts))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "ok",
		"accounts": len(restoredAccounts),
		"settings": currentDashboardSettingsResponse(),
	})
}

func readBackupAccounts(accountsDir string) ([]json.RawMessage, error) {
	entries, err := os.ReadDir(accountsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []json.RawMessage{}, nil
		}
		return nil, fmt.Errorf("read accounts directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	accounts := make([]json.RawMessage, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(accountsDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if _, _, err := decodeBackupAccount(data); err != nil {
			// Hidden state files in accountsDir are JSON too; only valid account
			// records belong in this backup.
			continue
		}
		accounts = append(accounts, json.RawMessage(append([]byte(nil), data...)))
	}
	return accounts, nil
}

func prepareRestoredAccounts(records []json.RawMessage) ([]*Account, map[string][]byte, error) {
	accounts := make([]*Account, 0, len(records))
	files := make(map[string][]byte, len(records))
	seenUsers := make(map[string]bool, len(records))
	seenEmails := make(map[string]bool, len(records))
	seenTokens := make(map[string]bool, len(records))

	for index, raw := range records {
		acc, normalized, err := decodeBackupAccount(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("account %d: %w", index+1, err)
		}
		userKey := strings.ToLower(strings.TrimSpace(acc.UserID))
		emailKey := strings.ToLower(strings.TrimSpace(acc.UserEmail))
		tokenKey := strings.TrimSpace(acc.TokenV2)
		if seenUsers[userKey] {
			return nil, nil, fmt.Errorf("account %d duplicates user_id %s", index+1, acc.UserID)
		}
		if emailKey != "" && seenEmails[emailKey] {
			return nil, nil, fmt.Errorf("account %d duplicates email %s", index+1, acc.UserEmail)
		}
		if seenTokens[tokenKey] {
			return nil, nil, fmt.Errorf("account %d duplicates token_v2", index+1)
		}
		seenUsers[userKey] = true
		seenEmails[emailKey] = emailKey != ""
		seenTokens[tokenKey] = true

		filename := restoredAccountFilename(acc, index)
		if _, exists := files[filename]; exists {
			return nil, nil, fmt.Errorf("account %d produces a duplicate filename", index+1)
		}
		files[filename] = normalized
		accounts = append(accounts, acc)
	}
	return accounts, files, nil
}

func decodeBackupAccount(raw []byte) (*Account, []byte, error) {
	if len(raw) == 0 {
		return nil, nil, errors.New("empty account record")
	}
	var object map[string]interface{}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if object == nil {
		return nil, nil, errors.New("account record must be a JSON object")
	}
	var acc Account
	if err := json.Unmarshal(raw, &acc); err != nil {
		return nil, nil, fmt.Errorf("decode account: %w", err)
	}
	if strings.TrimSpace(acc.TokenV2) == "" || strings.TrimSpace(acc.UserID) == "" || strings.TrimSpace(acc.SpaceID) == "" {
		return nil, nil, errors.New("token_v2, user_id, and space_id are required")
	}
	if acc.TokenV2 == "YOUR_TOKEN_V2_HERE" || strings.HasPrefix(acc.UserID, "xxxxxxxx") {
		return nil, nil, errors.New("placeholder account is not allowed")
	}
	if acc.BrowserID == "" {
		acc.BrowserID = generateUUIDv4()
		object["browser_id"] = acc.BrowserID
	}
	if acc.ClientVersion == "" || acc.ClientVersion == "unknown" {
		acc.ClientVersion = DefaultClientVersion
		object["client_version"] = acc.ClientVersion
	}
	acc.QuotaInfo = loadPersistedQuotaInfo(raw)
	loadPersistedWorkspace(raw, &acc)

	normalized, err := json.MarshalIndent(object, "", "  ")
	if err != nil {
		return nil, nil, fmt.Errorf("encode account: %w", err)
	}
	return &acc, append(normalized, '\n'), nil
}

func restoredAccountFilename(acc *Account, index int) string {
	name := strings.TrimSpace(acc.UserEmail)
	if name == "" {
		name = strings.TrimSpace(acc.UserID)
	}
	name = strings.Map(func(r rune) rune {
		if r == '@' || r == '.' || r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, name)
	// Dashboard runtime files also use hidden .json names in accountsDir.
	// Never allow an imported account name to collide with those files.
	if strings.HasPrefix(name, ".") {
		name = "account-" + strings.TrimLeft(name, ".")
	}
	if name == "" {
		name = fmt.Sprintf("account-%d", index+1)
	}
	return name + ".json"
}

func replaceAccountFilesAndPool(pool *AccountPool, accountsDir string, accounts []*Account, files map[string][]byte) error {
	if err := os.MkdirAll(accountsDir, 0o700); err != nil {
		return fmt.Errorf("create accounts directory: %w", err)
	}
	stageDir, err := os.MkdirTemp(accountsDir, ".restore-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(stageDir)

	filenames := make([]string, 0, len(files))
	for name := range files {
		filenames = append(filenames, name)
	}
	sort.Strings(filenames)
	for _, name := range filenames {
		if err := os.WriteFile(filepath.Join(stageDir, name), files[name], 0o600); err != nil {
			return fmt.Errorf("stage %s: %w", name, err)
		}
	}

	existing, err := snapshotRestoredAccountTargets(accountsDir, filenames)
	if err != nil {
		return err
	}
	for _, snapshot := range existing {
		if err := os.Remove(snapshot.Path); err != nil && !os.IsNotExist(err) {
			rollbackRestoreFiles(existing, nil)
			return fmt.Errorf("remove existing account %s: %w", filepath.Base(snapshot.Path), err)
		}
	}

	movedPaths := make([]string, 0, len(filenames))
	for _, name := range filenames {
		target := filepath.Join(accountsDir, name)
		if err := os.Rename(filepath.Join(stageDir, name), target); err != nil {
			rollbackRestoreFiles(existing, movedPaths)
			return fmt.Errorf("activate %s: %w", name, err)
		}
		movedPaths = append(movedPaths, target)
	}

	pool.mu.Lock()
	pool.accounts = accounts
	pool.index.Store(0)
	pool.mu.Unlock()
	pool.liveQuotaMu.Lock()
	pool.liveQuotaInflight = make(map[*Account]bool)
	pool.liveQuotaMu.Unlock()
	for _, acc := range accounts {
		registerModelEntries(acc.Models)
	}
	return nil
}

func snapshotRestoredAccountTargets(accountsDir string, restoredFilenames []string) ([]restoreFileSnapshot, error) {
	targets := make(map[string]bool)
	entries, err := os.ReadDir(accountsDir)
	if err != nil {
		return nil, fmt.Errorf("read accounts directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(accountsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		if _, _, err := decodeBackupAccount(data); err == nil {
			targets[path] = true
		}
	}
	for _, name := range restoredFilenames {
		path := filepath.Join(accountsDir, name)
		if _, err := os.Stat(path); err == nil {
			targets[path] = true
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect %s: %w", name, err)
		}
	}

	paths := make([]string, 0, len(targets))
	for path := range targets {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	snapshots := make([]restoreFileSnapshot, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", filepath.Base(path), err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", filepath.Base(path), err)
		}
		snapshots = append(snapshots, restoreFileSnapshot{Path: path, Data: data, Mode: info.Mode().Perm()})
	}
	return snapshots, nil
}

func rollbackRestoreFiles(existing []restoreFileSnapshot, movedPaths []string) {
	for _, path := range movedPaths {
		_ = os.Remove(path)
	}
	for _, snapshot := range existing {
		mode := snapshot.Mode
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(snapshot.Path, snapshot.Data, mode); err != nil {
			log.Printf("[backup] rollback %s failed: %v", filepath.Base(snapshot.Path), err)
		}
	}
}

func currentDashboardBackupSettings() dashboardBackupSettings {
	return dashboardBackupSettings{
		EnableWebSearch:               AppConfig.WebSearchEnabled(),
		EnableWorkspaceSearch:         AppConfig.WorkspaceSearchEnabled(),
		AskModeDefault:                AppConfig.AskModeDefault(),
		UseClientSystemPrompt:         AppConfig.ClientSystemPromptEnabled(),
		UseNotionPersonalInstructions: AppConfig.NotionPersonalInstructionsEnabled(),
		EnableToolBridge:              AppConfig.ToolBridgeEnabled(),
		ToolChoicePolicy:              AppConfig.ToolChoicePolicy(),
		DebugLogging:                  AppConfig.Server.DebugLogging,
		NotionProxy:                   AppConfig.NotionProxyURL(),
	}
}

func currentDashboardSettingsResponse() map[string]interface{} {
	settings := currentDashboardBackupSettings()
	return map[string]interface{}{
		"enable_web_search":                settings.EnableWebSearch,
		"enable_workspace_search":          settings.EnableWorkspaceSearch,
		"ask_mode_default":                 settings.AskModeDefault,
		"disable_notion_prompt":            AppConfig.Proxy.DisableNotionPrompt,
		"use_client_system_prompt":         settings.UseClientSystemPrompt,
		"use_notion_personal_instructions": settings.UseNotionPersonalInstructions,
		"enable_tool_bridge":               settings.EnableToolBridge,
		"tool_choice_policy":               settings.ToolChoicePolicy,
		"debug_logging":                    settings.DebugLogging,
		"notion_proxy":                     settings.NotionProxy,
	}
}

func backupSettingsPtr(settings dashboardBackupSettings) *dashboardBackupSettings {
	return &settings
}

func validateDashboardBackupSettings(settings dashboardBackupSettings) error {
	if _, ok := normalizeToolChoicePolicy(settings.ToolChoicePolicy); !ok {
		return errors.New("backup contains an unsupported tool choice policy")
	}
	settings.NotionProxy = strings.TrimSpace(settings.NotionProxy)
	if settings.NotionProxy != "" {
		if err := netutil.ValidateProxyURL(settings.NotionProxy); err != nil {
			return errors.New("backup contains an unsupported Notion proxy URL")
		}
	}
	return nil
}

func applyDashboardBackupSettings(settings dashboardBackupSettings) {
	previousProxy := AppConfig.Proxy.NotionProxy
	AppConfig.Proxy.EnableWebSearch = boolPtr(settings.EnableWebSearch)
	AppConfig.Proxy.EnableWorkspaceSearch = boolPtr(settings.EnableWorkspaceSearch)
	AppConfig.Proxy.AskModeDefault = boolPtr(settings.AskModeDefault)
	AppConfig.Proxy.UseClientSystemPrompt = settings.UseClientSystemPrompt
	AppConfig.Proxy.UseNotionPersonalInstructions = settings.UseNotionPersonalInstructions
	AppConfig.Proxy.EnableToolBridge = settings.EnableToolBridge
	AppConfig.Proxy.ToolChoicePolicy, _ = normalizeToolChoicePolicy(settings.ToolChoicePolicy)
	AppConfig.Server.DebugLogging = settings.DebugLogging
	AppConfig.Proxy.NotionProxy = strings.TrimSpace(settings.NotionProxy)
	SetDebugLoggingEnabled(settings.DebugLogging)
	if AppConfig.Proxy.NotionProxy != previousProxy {
		RebuildChromeTransport()
	}
}

func persistDashboardBackupSettings(configPath string, settings dashboardBackupSettings) error {
	if configPath == "" {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil || root.Kind == 0 {
		return fmt.Errorf("parse config: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return errors.New("config root is invalid")
	}
	mapping := root.Content[0]
	proxyNode := getOrCreateYAMLMapping(mapping, "proxy")
	setYAMLBool(proxyNode, "enable_web_search", settings.EnableWebSearch)
	setYAMLBool(proxyNode, "enable_workspace_search", settings.EnableWorkspaceSearch)
	setYAMLBool(proxyNode, "ask_mode_default", settings.AskModeDefault)
	setYAMLBool(proxyNode, "use_client_system_prompt", settings.UseClientSystemPrompt)
	setYAMLBool(proxyNode, "use_notion_personal_instructions", settings.UseNotionPersonalInstructions)
	setYAMLBool(proxyNode, "enable_tool_bridge", settings.EnableToolBridge)
	toolChoicePolicy, _ := normalizeToolChoicePolicy(settings.ToolChoicePolicy)
	setYAMLString(proxyNode, "tool_choice_policy", toolChoicePolicy)
	setYAMLString(proxyNode, "notion_proxy", strings.TrimSpace(settings.NotionProxy))
	serverNode := getOrCreateYAMLMapping(mapping, "server")
	setYAMLBool(serverNode, "debug_logging", settings.DebugLogging)

	out, err := yaml.Marshal(&root)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, out, 0o600)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func writeBackupError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
