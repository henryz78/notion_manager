package proxy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckAllPersonalInstructionsReportsEachAccountWithoutPageIDs(t *testing.T) {
	pool := NewAccountPool()
	pool.accounts = []*Account{
		{UserEmail: "configured@example.com"},
		{UserEmail: "missing@example.com"},
		{UserEmail: "failed@example.com"},
	}

	summary := checkAllPersonalInstructions(pool, 3, func(acc *Account) (string, error) {
		switch acc.UserEmail {
		case "configured@example.com":
			return "private-page-id", nil
		case "missing@example.com":
			return "", nil
		default:
			return "", errors.New("loadUserContent returned 401")
		}
	})

	if summary.Total != 3 || summary.Configured != 1 || summary.Missing != 1 || summary.Failed != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if len(summary.Results) != 3 {
		t.Fatalf("results=%d, want 3", len(summary.Results))
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	if strings.Contains(string(encoded), "private-page-id") {
		t.Fatalf("response leaked personal-instructions page ID: %s", encoded)
	}

	configured := pool.accounts[0].personalInstructionsSnapshot()
	if configured.Configured == nil || !*configured.Configured || configured.Error != "" {
		t.Fatalf("configured state not stored correctly: %+v", configured)
	}
	missing := pool.accounts[1].personalInstructionsSnapshot()
	if missing.Configured == nil || *missing.Configured || missing.Error != "" {
		t.Fatalf("missing state not stored correctly: %+v", missing)
	}
	failed := pool.accounts[2].personalInstructionsSnapshot()
	if failed.Configured != nil || failed.Error == "" {
		t.Fatalf("failed state not stored correctly: %+v", failed)
	}
}

func TestPersonalInstructionsCheckStatePersistsWithoutPageContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "account.json")
	if err := os.WriteFile(path, []byte(`{
  "token_v2": "token",
  "user_id": "user",
  "user_email": "person@example.com",
  "space_id": "space"
}`), 0o644); err != nil {
		t.Fatalf("write account: %v", err)
	}

	pool := NewAccountPool()
	acc := &Account{UserEmail: "person@example.com"}
	configured := true
	checkedAt, err := time.Parse(time.RFC3339, "2026-07-18T12:00:00Z")
	if err != nil {
		t.Fatalf("parse checked time: %v", err)
	}
	acc.setPersonalInstructionsCheck(&configured, checkedAt, "")
	pool.accounts = []*Account{acc}
	pool.SaveAccounts(dir)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read account: %v", err)
	}
	var saved map[string]interface{}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	if saved["personal_instructions_configured"] != true {
		t.Fatalf("configured flag not persisted: %#v", saved)
	}
	if saved["personal_instructions_checked_at"] != "2026-07-18T12:00:00Z" {
		t.Fatalf("checked_at not persisted: %#v", saved)
	}
	for key, value := range saved {
		if strings.Contains(strings.ToLower(key), "page_id") ||
			strings.Contains(strings.ToLower(key), "instructions_content") ||
			strings.Contains(strings.ToLower(valueString(value)), "private-page") {
			t.Fatalf("unexpected personal-instructions content persisted: %s=%v", key, value)
		}
	}
}

func TestImportedAccountPersonalInstructionsCheckPersistsOnlyResult(t *testing.T) {
	acc := &Account{
		TokenV2:   "token",
		UserID:    "user",
		UserEmail: "import@example.com",
		SpaceID:   "space",
	}
	configured, checkError := checkPersonalInstructionsForImport(acc, func(*Account) (string, error) {
		return "private-import-page-id", nil
	})
	if checkError != "" || configured == nil || !*configured {
		t.Fatalf("unexpected import check result: configured=%v error=%q", configured, checkError)
	}

	dir := t.TempDir()
	filename, err := SaveAccountToFile(acc, dir)
	if err != nil {
		t.Fatalf("SaveAccountToFile: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, filename))
	if err != nil {
		t.Fatalf("read imported account: %v", err)
	}
	var saved map[string]interface{}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode imported account: %v", err)
	}
	if saved["personal_instructions_configured"] != true {
		t.Fatalf("configured flag not persisted: %#v", saved)
	}
	if _, ok := saved["personal_instructions_checked_at"]; !ok {
		t.Fatalf("checked_at not persisted: %#v", saved)
	}
	if strings.Contains(string(data), "private-import-page-id") {
		t.Fatalf("personal-instructions page ID leaked into account file: %s", data)
	}
}

func TestImportedAccountPersonalInstructionsCheckRecordsFailure(t *testing.T) {
	acc := &Account{UserEmail: "failed-import@example.com"}
	configured, checkError := checkPersonalInstructionsForImport(acc, func(*Account) (string, error) {
		return "", errors.New("probe failed")
	})
	if configured != nil || checkError == "" {
		t.Fatalf("unexpected failed import check result: configured=%v error=%q", configured, checkError)
	}
	state := acc.personalInstructionsSnapshot()
	if state.Configured != nil || state.CheckedAt == nil || state.Error == "" {
		t.Fatalf("failed import state not recorded: %+v", state)
	}
}

func valueString(value interface{}) string {
	if value == nil {
		return ""
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
