package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func writeBulkActionAccountFile(t *testing.T, dir string, acc *Account) {
	t.Helper()
	data, err := json.Marshal(map[string]interface{}{
		"token_v2":   "token-" + acc.UserEmail,
		"user_id":    "user-" + acc.UserEmail,
		"user_email": acc.UserEmail,
		"space_id":   "space-" + acc.UserEmail,
	})
	if err != nil {
		t.Fatalf("marshal account file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, acc.UserEmail+".json"), data, 0o600); err != nil {
		t.Fatalf("write account file: %v", err)
	}
}

func callBulkAccountAction(t *testing.T, handler http.Handler, action string, emails []string) BulkAccountActionResult {
	t.Helper()
	body, err := json.Marshal(BulkAccountActionRequest{Action: action, Emails: emails})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/bulk", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result BulkAccountActionResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

func TestBulkDisableAndEnablePersistAndChangeRouting(t *testing.T) {
	dir := t.TempDir()
	first := &Account{
		UserEmail: "first@example.com",
		PlanType:  "business",
		QuotaInfo: &QuotaInfo{IsEligible: true},
	}
	second := &Account{
		UserEmail: "second@example.com",
		PlanType:  "business",
		QuotaInfo: &QuotaInfo{IsEligible: true},
	}
	for _, acc := range []*Account{first, second} {
		writeBulkActionAccountFile(t, dir, acc)
	}
	pool := NewAccountPool()
	pool.accounts = []*Account{first, second}
	handler := HandleBulkAccountAction(pool, dir, NewDashboardAuth("", ""))

	result := callBulkAccountAction(t, handler, "disable", []string{first.UserEmail})
	if result.Succeeded != 1 || len(result.Failed) != 0 {
		t.Fatalf("disable result: %+v", result)
	}
	if !first.healthSnapshot().ManuallyDisabled {
		t.Fatal("account was not disabled in memory")
	}
	if got := pool.AvailableCount(); got != 1 {
		t.Fatalf("available=%d, want 1", got)
	}
	if got := pool.NextBest(); got != second {
		t.Fatalf("disabled account was selected: %#v", got)
	}
	if got := pool.Next(); got != second {
		t.Fatalf("round-robin selected disabled account: %#v", got)
	}
	if got := pool.NextForResearch(); got != second {
		t.Fatalf("research picker selected disabled account: %#v", got)
	}
	if got := pool.GetByEmail(first.UserEmail); got != nil {
		t.Fatalf("GetByEmail returned disabled account: %#v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, first.UserEmail+".json"))
	if err != nil {
		t.Fatalf("read disabled account: %v", err)
	}
	var saved map[string]interface{}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode disabled account: %v", err)
	}
	if saved["disabled"] != true {
		t.Fatalf("disabled flag not persisted: %#v", saved)
	}
	reloaded := NewAccountPool()
	if err := reloaded.LoadFromDir(dir); err != nil {
		t.Fatalf("reload disabled accounts: %v", err)
	}
	if got := reloaded.AvailableCount(); got != 1 {
		t.Fatalf("reloaded available=%d, want 1", got)
	}
	var reloadedFirst *Account
	for _, acc := range reloaded.accounts {
		if acc.UserEmail == first.UserEmail {
			reloadedFirst = acc
			break
		}
	}
	if reloadedFirst == nil || !reloadedFirst.healthSnapshot().ManuallyDisabled {
		t.Fatal("disabled state was not restored after restart")
	}

	result = callBulkAccountAction(t, handler, "enable", []string{first.UserEmail})
	if result.Succeeded != 1 || len(result.Failed) != 0 {
		t.Fatalf("enable result: %+v", result)
	}
	if first.healthSnapshot().ManuallyDisabled {
		t.Fatal("account remained disabled in memory")
	}
	if got := pool.AvailableCount(); got != 2 {
		t.Fatalf("available=%d, want 2", got)
	}
	data, err = os.ReadFile(filepath.Join(dir, first.UserEmail+".json"))
	if err != nil {
		t.Fatalf("read enabled account: %v", err)
	}
	saved = make(map[string]interface{})
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("decode enabled account: %v", err)
	}
	if _, exists := saved["disabled"]; exists {
		t.Fatalf("false disabled flag should be removed: %#v", saved)
	}
}

func TestBulkDeleteOnlyRemovesSelectedAccounts(t *testing.T) {
	dir := t.TempDir()
	first := &Account{UserEmail: "first@example.com"}
	second := &Account{UserEmail: "second@example.com"}
	for _, acc := range []*Account{first, second} {
		writeBulkActionAccountFile(t, dir, acc)
	}
	pool := NewAccountPool()
	pool.accounts = []*Account{first, second}

	result := callBulkAccountAction(t,
		HandleBulkAccountAction(pool, dir, NewDashboardAuth("", "")),
		"delete",
		[]string{first.UserEmail},
	)
	if result.Succeeded != 1 || pool.Count() != 1 {
		t.Fatalf("delete result=%+v pool=%d", result, pool.Count())
	}
	if _, err := os.Stat(filepath.Join(dir, first.UserEmail+".json")); !os.IsNotExist(err) {
		t.Fatalf("selected account file still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, second.UserEmail+".json")); err != nil {
		t.Fatalf("unselected account file was removed: %v", err)
	}
}

func TestDeleteMissingPersonalInstructionsRechecksAndKeepsFailures(t *testing.T) {
	dir := t.TempDir()
	configured := &Account{UserEmail: "configured@example.com"}
	missing := &Account{UserEmail: "missing@example.com"}
	failed := &Account{UserEmail: "failed@example.com"}
	for _, acc := range []*Account{configured, missing, failed} {
		writeBulkActionAccountFile(t, dir, acc)
	}
	pool := NewAccountPool()
	pool.accounts = []*Account{configured, missing, failed}

	result := deleteMissingPersonalInstructions(pool, dir, func(acc *Account) (string, error) {
		switch acc.UserEmail {
		case configured.UserEmail:
			return "private-page-id", nil
		case missing.UserEmail:
			return "", nil
		default:
			return "", errors.New("probe failed")
		}
	})
	if result.Checked != 3 || result.Matched != 1 || result.Deleted != 1 || len(result.Failed) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if pool.Count() != 2 {
		t.Fatalf("pool count=%d, want 2", pool.Count())
	}
	if _, err := os.Stat(filepath.Join(dir, missing.UserEmail+".json")); !os.IsNotExist(err) {
		t.Fatalf("missing-instructions account was not deleted: %v", err)
	}
	for _, email := range []string{configured.UserEmail, failed.UserEmail} {
		if _, err := os.Stat(filepath.Join(dir, email+".json")); err != nil {
			t.Fatalf("account %s should be retained: %v", email, err)
		}
	}
	if failed.personalInstructionsSnapshot().Configured != nil {
		t.Fatal("probe failure must not be treated as missing instructions")
	}
}
