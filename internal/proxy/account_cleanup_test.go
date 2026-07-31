package proxy

import (
	"encoding/json"
	"errors"
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
	previousWorkspaceProbe := workspaceProbe
	previousRoutingQuotaFetcher := routingQuotaFetcher
	t.Cleanup(func() {
		workspaceProbe = previousWorkspaceProbe
		routingQuotaFetcher = previousRoutingQuotaFetcher
	})
	workspaceProbe = func(acc *Account) (WorkspaceProbeResult, error) {
		return WorkspaceProbeResult{Count: 1, PlanType: acc.planTypeSnapshot(), AIEnabled: true}, nil
	}
	routingQuotaFetcher = func(acc *Account) (*QuotaInfo, error) {
		return acc.quotaInfoSnapshot(), nil
	}

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
		acc.TokenV2 = "token-" + acc.UserEmail
		acc.UserID = "user-" + acc.UserEmail
		acc.SpaceID = "space-" + acc.UserEmail
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
	if result.Matched != 3 || result.Deleted != 3 || len(result.Failed) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if pool.Count() != 4 {
		t.Fatalf("pool count=%d, want 4", pool.Count())
	}
	for _, email := range []string{"free-exhausted@example.com", "plus-exhausted@example.com", "plus-premium-signal@example.com"} {
		if _, err := os.Stat(filepath.Join(dir, email+".json")); !os.IsNotExist(err) {
			t.Errorf("expected %s file removed, stat err=%v", email, err)
		}
	}
	for _, email := range []string{
		"business-exhausted@example.com",
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

func TestDeleteExhaustedComplimentaryAccountsKeepsAccountWhenLiveConfirmationFails(t *testing.T) {
	previousWorkspaceProbe := workspaceProbe
	previousRoutingQuotaFetcher := routingQuotaFetcher
	t.Cleanup(func() {
		workspaceProbe = previousWorkspaceProbe
		routingQuotaFetcher = previousRoutingQuotaFetcher
	})
	workspaceProbe = func(acc *Account) (WorkspaceProbeResult, error) {
		return WorkspaceProbeResult{Count: 1, PlanType: acc.planTypeSnapshot(), AIEnabled: true}, nil
	}
	routingQuotaFetcher = func(*Account) (*QuotaInfo, error) {
		return nil, errors.New("temporary V1 failure")
	}

	dir := t.TempDir()
	now := time.Now()
	acc := &Account{
		UserEmail:            "keep-on-error@example.com",
		PlanType:             "free",
		QuotaInfo:            &QuotaInfo{IsEligible: false},
		QuotaExhaustedAt:     &now,
		PermanentlyExhausted: true,
	}
	writeCleanupTestAccount(t, dir, acc)
	pool := newPool(acc)

	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/delete-exhausted-complimentary", nil)
	rec := httptest.NewRecorder()
	HandleDeleteExhaustedComplimentaryAccounts(pool, dir, NewDashboardAuth("", "")).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result struct {
		Deleted int               `json:"deleted"`
		Failed  map[string]string `json:"failed"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Deleted != 0 || result.Failed[acc.UserEmail] == "" {
		t.Fatalf("failed confirmation did not preserve account: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, acc.UserEmail+".json")); err != nil {
		t.Fatalf("account file was removed after failed confirmation: %v", err)
	}
}
