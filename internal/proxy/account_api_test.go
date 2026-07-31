package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiscoverAccountsFromTokenReturnsEveryWorkspacePaidFirst(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loadUserContent" {
			http.NotFound(w, r)
			return
		}
		if cookie, err := r.Cookie("token_v2"); err != nil || cookie.Value != "multi-token" {
			t.Fatalf("token cookie missing: cookie=%v err=%v", cookie, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"recordMap": {
				"notion_user": {
					"user-1": {"value":{"value":{"name":"Shared User","email":"shared@example.com"}}}
				},
				"user_root": {
					"user-1": {"value":{"value":{"space_view_pointers":[
						{"spaceId":"free-space","id":"free-view"},
						{"spaceId":"business-space","id":"business-view"}
					]}}}
				},
				"space": {
					"free-space": {"value":{"value":{
						"id":"free-space","name":"Free Workspace","plan_type":"free","settings":{}
					}}},
					"business-space": {"value":{"value":{
						"id":"business-space","name":"Business Workspace","plan_type":"business","settings":{}
					}}}
				},
				"user_settings": {
					"user-1": {"value":{"value":{"settings":{"time_zone":"Asia/Shanghai"}}}}
				}
			}
		}`))
	}))
	defer server.Close()

	previousBase := NotionAPIBase
	previousConfig := AppConfig
	previousModelsFetcher := modelsFetcher
	previousQuotaFetcher := quotaFetcher
	previousClientOverride := chromeHTTPClientForTest
	NotionAPIBase = server.URL
	AppConfig = DefaultConfig()
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }
	modelsFetcher = func(acc *Account) ([]ModelEntry, error) {
		return []ModelEntry{{ID: "model-" + acc.SpaceID, Name: "Model"}}, nil
	}
	quotaFetcher = func(*Account) (*QuotaInfo, error) {
		return &QuotaInfo{IsEligible: true}, nil
	}
	t.Cleanup(func() {
		NotionAPIBase = previousBase
		AppConfig = previousConfig
		modelsFetcher = previousModelsFetcher
		quotaFetcher = previousQuotaFetcher
		chromeHTTPClientForTest = previousClientOverride
	})

	accounts, err := DiscoverAccountsFromToken("multi-token")
	if err != nil {
		t.Fatalf("DiscoverAccountsFromToken: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts=%d, want 2", len(accounts))
	}
	if accounts[0].SpaceID != "business-space" || accounts[0].PlanType != "business" {
		t.Fatalf("first workspace=%+v, want business workspace", accounts[0])
	}
	if accounts[1].SpaceID != "free-space" || accounts[1].PlanType != "free" {
		t.Fatalf("second workspace=%+v, want free workspace", accounts[1])
	}
	if accounts[0].AccountID == accounts[1].AccountID || accounts[0].SpaceCount != 2 || accounts[1].SpaceCount != 2 {
		t.Fatalf("workspace identities/counts are not independent: %+v %+v", accounts[0], accounts[1])
	}
}

func TestHandleAddAccountSkipsExistingToken(t *testing.T) {
	pool := NewAccountPool()
	existing := &Account{
		TokenV2:   "existing-token",
		UserID:    "existing-user",
		SpaceID:   "existing-space",
		UserName:  "Existing User",
		UserEmail: "existing@example.com",
		SpaceName: "Existing Workspace",
		PlanType:  "free",
	}
	pool.AddAccount(existing)
	originalDiscover := discoverAccountsFromToken
	discoverAccountsFromToken = func(token string) ([]*Account, error) {
		if token != existing.TokenV2 {
			t.Fatalf("unexpected token: %q", token)
		}
		return []*Account{existing}, nil
	}
	t.Cleanup(func() { discoverAccountsFromToken = originalDiscover })
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

func TestHandleAddAccountImportsMissingWorkspaceForExistingToken(t *testing.T) {
	pool := NewAccountPool()
	existing := &Account{
		TokenV2:   "shared-token",
		UserID:    "same-user",
		SpaceID:   "free-space",
		UserName:  "Shared User",
		UserEmail: "shared@example.com",
		SpaceName: "Free Workspace",
		PlanType:  "free",
	}
	paid := &Account{
		TokenV2:   "shared-token",
		UserID:    "same-user",
		SpaceID:   "business-space",
		UserName:  "Shared User",
		UserEmail: "shared@example.com",
		SpaceName: "Business Workspace",
		PlanType:  "business",
	}
	pool.AddAccount(existing)
	originalDiscover := discoverAccountsFromToken
	discoverAccountsFromToken = func(string) ([]*Account, error) {
		return []*Account{paid, existing}, nil
	}
	t.Cleanup(func() { discoverAccountsFromToken = originalDiscover })

	req := httptest.NewRequest(
		http.MethodPost,
		"/admin/accounts/add",
		strings.NewReader(`{"token_v2":"shared-token"}`),
	)
	rec := httptest.NewRecorder()
	HandleAddAccount(pool, t.TempDir(), NewDashboardAuth("", "")).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Status   string              `json:"status"`
		Imported int                 `json:"imported"`
		Skipped  int                 `json:"skipped"`
		Accounts []map[string]string `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Status != "ok" || response.Imported != 1 || response.Skipped != 1 {
		t.Fatalf("unexpected response: %+v", response)
	}
	if pool.Count() != 2 {
		t.Fatalf("pool count = %d, want 2", pool.Count())
	}
	if got := pool.NextBest(); got == nil || got.SpaceID != paid.SpaceID {
		t.Fatalf("best workspace = %+v, want paid workspace %q", got, paid.SpaceID)
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
