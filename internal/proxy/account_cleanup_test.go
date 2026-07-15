package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCleanupTestAccount(t *testing.T, dir string, acc *Account) {
	t.Helper()
	data, err := json.Marshal(acc)
	if err != nil {
		t.Fatalf("marshal account %s: %v", acc.UserEmail, err)
	}
	path := filepath.Join(dir, acc.UserEmail+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write account %s: %v", acc.UserEmail, err)
	}
}

func TestDeleteExhaustedComplimentaryAccountsOnlyRemovesEligibleCandidates(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	accounts := []*Account{
		{
			UserEmail:            "free-exhausted@example.com",
			PlanType:             "free",
			QuotaInfo:            &QuotaInfo{IsEligible: false},
			QuotaExhaustedAt:     &now,
			PermanentlyExhausted: true,
		},
		{
			UserEmail:        "plus-exhausted@example.com",
			PlanType:         "plus",
			QuotaInfo:        &QuotaInfo{IsEligible: false},
			QuotaExhaustedAt: &now,
		},
		{
			UserEmail:        "business-exhausted@example.com",
			PlanType:         "business",
			QuotaInfo:        &QuotaInfo{IsEligible: false},
			QuotaExhaustedAt: &now,
		},
		{
			UserEmail:        "plus-premium-signal@example.com",
			PlanType:         "plus",
			QuotaInfo:        &QuotaInfo{IsEligible: false, HasPremium: true},
			QuotaExhaustedAt: &now,
		},
		{
			UserEmail:        "free-cookie-invalid@example.com",
			PlanType:         "free",
			AuthInvalid:      true,
			QuotaInfo:        &QuotaInfo{IsEligible: false},
			QuotaExhaustedAt: &now,
		},
		{
			UserEmail:          "free-no-workspace@example.com",
			PlanType:           "free",
			QuotaInfo:          &QuotaInfo{IsEligible: false},
			QuotaExhaustedAt:   &now,
			WorkspaceCheckedAt: &now,
			SpaceCount:         0,
		},
		{
			UserEmail: "free-healthy@example.com",
			PlanType:  "free",
			QuotaInfo: &QuotaInfo{IsEligible: true},
		},
	}

	pool := NewAccountPool()
	pool.accounts = accounts
	for _, acc := range accounts {
		writeCleanupTestAccount(t, dir, acc)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/delete-exhausted-complimentary", nil)
	rec := httptest.NewRecorder()
	HandleDeleteExhaustedComplimentaryAccounts(pool, dir, NewDashboardAuth("", "")).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var result struct {
		Matched int               `json:"matched"`
		Deleted int               `json:"deleted"`
		Emails  []string          `json:"emails"`
		Failed  map[string]string `json:"failed"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.Matched != 2 || result.Deleted != 2 || len(result.Failed) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if pool.Count() != 5 {
		t.Fatalf("pool count=%d, want 5", pool.Count())
	}
	for _, email := range []string{"free-exhausted@example.com", "plus-exhausted@example.com"} {
		if _, err := os.Stat(filepath.Join(dir, email+".json")); !os.IsNotExist(err) {
			t.Errorf("expected %s file removed, stat err=%v", email, err)
		}
	}
	for _, email := range []string{
		"business-exhausted@example.com",
		"plus-premium-signal@example.com",
		"free-cookie-invalid@example.com",
		"free-no-workspace@example.com",
		"free-healthy@example.com",
	} {
		if _, err := os.Stat(filepath.Join(dir, email+".json")); err != nil {
			t.Errorf("expected %s file retained: %v", email, err)
		}
	}
}

func TestDeleteExhaustedComplimentaryAccountsRejectsWrongMethod(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/accounts/delete-exhausted-complimentary", nil)
	rec := httptest.NewRecorder()
	HandleDeleteExhaustedComplimentaryAccounts(NewAccountPool(), t.TempDir(), NewDashboardAuth("", "")).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
