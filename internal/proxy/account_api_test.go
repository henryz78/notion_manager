package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestHandleAddAccountSkipsExistingToken(t *testing.T) {
	pool := NewAccountPool()
	existing := &Account{
		TokenV2:   "existing-token",
		UserName:  "Existing User",
		UserEmail: "existing@example.com",
		SpaceName: "Existing Workspace",
		PlanType:  "free",
	}
	pool.AddAccount(existing)
	accountsDir := t.TempDir()

	req := httptest.NewRequest(
		http.MethodPost,
		"/admin/accounts/add",
		strings.NewReader(`{"token_v2":"existing-token","personal_instructions_policy":"all"}`),
	)
	rec := httptest.NewRecorder()
	HandleAddAccount(pool, accountsDir, NewDashboardAuth("", "")).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var response struct {
		Status  string            `json:"status"`
		Reason  string            `json:"reason"`
		Account map[string]string `json:"account"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != "skipped" {
		t.Fatalf("status = %q, want skipped", response.Status)
	}
	if response.Reason != "duplicate_account" {
		t.Fatalf("reason = %q, want duplicate_account", response.Reason)
	}
	if response.Account["email"] != existing.UserEmail {
		t.Fatalf("account email = %q, want %q", response.Account["email"], existing.UserEmail)
	}
	if got := pool.Count(); got != 1 {
		t.Fatalf("pool account count = %d, want 1", got)
	}
	entries, err := readDirectoryNames(accountsDir)
	if err != nil {
		t.Fatalf("read accounts directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("saved files = %v, want none", entries)
	}
}

func readDirectoryNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}
