package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestConversationSessionsStayBoundToExactWorkspace(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	const (
		email      = "shared@example.com"
		businessID = "business-account-id"
		freeID     = "free-account-id"
	)

	businessSession := newConversationSessionForAccount(email, businessID)
	freeSession := newConversationSessionForAccount(email, freeID)
	manager.Set("business-chat", businessSession)
	manager.Set("free-chat", freeSession)

	manager.DeleteByAccountID(businessID, email)
	if got := manager.Get("business-chat"); got != nil {
		t.Fatalf("deleted workspace session survived: %+v", got)
	}
	if got := manager.Get("free-chat"); got != freeSession {
		t.Fatalf("other workspace session was removed: %+v", got)
	}

	current, reused, cacheable := lockConversationSessionForRequest(
		manager,
		"new-business-chat",
		nil,
		1,
		email,
		"",
		businessID,
	)
	defer current.unlockForRequest()
	if reused || !cacheable || current.AccountID != businessID {
		t.Fatalf("new session binding = %+v reused=%v cacheable=%v", current, reused, cacheable)
	}
}

func TestDashboardUsesOneLoginIDForSeveralWorkspaces(t *testing.T) {
	pool := NewAccountPool()
	pool.AddAccount(&Account{TokenV2: "token", UserID: "user", UserEmail: "shared@example.com", SpaceID: "space-a"})
	pool.AddAccount(&Account{TokenV2: "token", UserID: "user", UserEmail: "shared@example.com", SpaceID: "space-b"})
	details := pool.GetAccountDetails()
	if len(details) != 2 {
		t.Fatalf("details=%d want=2", len(details))
	}
	firstLoginID, _ := details[0]["login_id"].(string)
	secondLoginID, _ := details[1]["login_id"].(string)
	if firstLoginID == "" || firstLoginID != secondLoginID {
		t.Fatalf("login IDs differ: %q %q", firstLoginID, secondLoginID)
	}
	if details[0]["account_id"] == details[1]["account_id"] {
		t.Fatal("workspace account IDs were collapsed")
	}
}

func TestAccountBatchTargetsOneWorkspaceWhenEmailIsShared(t *testing.T) {
	dir := t.TempDir()
	business := &Account{
		TokenV2:   "shared-token",
		UserID:    "shared-user",
		UserEmail: "shared@example.com",
		SpaceID:   "business-space",
		SpaceName: "Business",
		PlanType:  "business",
		QuotaInfo: &QuotaInfo{IsEligible: true},
	}
	free := &Account{
		TokenV2:   "shared-token",
		UserID:    "shared-user",
		UserEmail: "shared@example.com",
		SpaceID:   "free-space",
		SpaceName: "Free",
		PlanType:  "free",
		QuotaInfo: &QuotaInfo{IsEligible: true},
	}
	for _, account := range []*Account{business, free} {
		if _, err := SaveAccountToFile(account, dir); err != nil {
			t.Fatalf("save %s: %v", account.SpaceName, err)
		}
	}
	pool := NewAccountPool()
	pool.AddAccount(business)
	pool.AddAccount(free)
	manager, err := NewAccountBatchManager(pool, dir, "")
	if err != nil {
		t.Fatalf("new batch manager: %v", err)
	}

	job, err := manager.Start(AccountBatchDisable, []string{business.AccountID}, 1)
	if err != nil {
		t.Fatalf("start batch: %v", err)
	}
	finished := waitForAccountBatchJob(t, manager, job.ID)
	if finished.Succeeded != 1 || finished.Failed != 0 {
		t.Fatalf("unexpected batch result: %+v", finished)
	}
	if !business.healthSnapshot().ManuallyDisabled {
		t.Fatal("target business workspace was not disabled")
	}
	if free.healthSnapshot().ManuallyDisabled {
		t.Fatal("same-email free workspace was disabled by mistake")
	}
}

func TestLegacyBulkEmailAmbiguityFailsClosed(t *testing.T) {
	dir := t.TempDir()
	pool := NewAccountPool()
	accounts := []*Account{
		{TokenV2: "token", UserID: "user", UserEmail: "shared@example.com", SpaceID: "space-a"},
		{TokenV2: "token", UserID: "user", UserEmail: "shared@example.com", SpaceID: "space-b"},
	}
	for _, account := range accounts {
		if _, err := SaveAccountToFile(account, dir); err != nil {
			t.Fatal(err)
		}
		pool.AddAccount(account)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/accounts/bulk",
		strings.NewReader(`{"action":"disable","emails":["shared@example.com"]}`))
	rec := httptest.NewRecorder()
	HandleBulkAccountAction(pool, dir, NewDashboardAuth("", "")).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result BulkAccountActionResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Succeeded != 0 || result.Matched != 0 {
		t.Fatalf("ambiguous legacy request mutated an account: %+v", result)
	}
	if !strings.Contains(result.Failed["shared@example.com"], "multiple workspaces") {
		t.Fatalf("failure=%q", result.Failed["shared@example.com"])
	}
	for _, account := range accounts {
		if account.healthSnapshot().ManuallyDisabled {
			t.Fatalf("workspace %s was disabled", account.SpaceID)
		}
	}
}

func TestExactWorkspaceLookupHonorsDisabledState(t *testing.T) {
	pool := NewAccountPool()
	account := &Account{
		TokenV2:   "token",
		UserID:    "user",
		UserEmail: "disabled@example.com",
		SpaceID:   "space",
		QuotaInfo: &QuotaInfo{IsEligible: true},
	}
	account.setManuallyDisabled(true)
	pool.AddAccount(account)
	if got := pool.FindUsableByAccountID(account.AccountID); got != nil {
		t.Fatalf("disabled account returned as usable: %+v", got)
	}

	req := httptest.NewRequest(http.MethodGet, "/proxy/start?account_id="+account.AccountID, nil)
	rec := httptest.NewRecorder()
	HandleProxyStart(pool, nil, NewDashboardAuth("", "")).ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTokenRotationReplacesLiveWorkspace(t *testing.T) {
	dir := t.TempDir()
	old := &Account{
		TokenV2:   "old-token",
		UserID:    "user",
		UserEmail: "rotate@example.com",
		SpaceID:   "space",
	}
	if _, err := SaveAccountToFile(old, dir); err != nil {
		t.Fatal(err)
	}
	pool := NewAccountPool()
	pool.AddAccount(old)

	replacement := &Account{
		TokenV2:   "new-token",
		UserID:    old.UserID,
		UserEmail: old.UserEmail,
		SpaceID:   old.SpaceID,
	}
	if _, err := SaveAccountToFile(replacement, dir); err != nil {
		t.Fatal(err)
	}
	previousHook := postRegisterRefreshHook
	postRegisterRefreshHook = func(*RegisterJobsDeps, string) {}
	t.Cleanup(func() { postRegisterRefreshHook = previousHook })
	activateRegisteredAccount(&RegisterJobsDeps{Pool: pool, AccountsDir: dir}, replacement.UserEmail, replacement.AccountID)

	current := pool.FindByAccountID(replacement.AccountID)
	if current == nil || current.TokenV2 != "new-token" {
		t.Fatalf("live token=%v want new-token", current)
	}
}

func TestActivationUsesLatestPersistedImport(t *testing.T) {
	dir := t.TempDir()
	first := &Account{
		TokenV2:   "first-token",
		UserID:    "user",
		UserName:  "First",
		UserEmail: "winner@example.com",
		SpaceID:   "space",
	}
	second := &Account{
		TokenV2:   "second-token",
		UserID:    first.UserID,
		UserName:  "Second",
		UserEmail: first.UserEmail,
		SpaceID:   first.SpaceID,
	}
	if _, err := SaveAccountToFile(first, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveAccountToFile(second, dir); err != nil {
		t.Fatal(err)
	}
	pool := NewAccountPool()
	// This simulates the first request reaching its activation step only after
	// the second request has become the persisted winner.
	if err := pool.ActivateAccountByIDFromDir(dir, first.AccountID); err != nil {
		t.Fatal(err)
	}
	current := pool.FindByAccountID(first.AccountID)
	if current == nil || current.TokenV2 != "second-token" || current.UserName != "Second" {
		t.Fatalf("activated stale import: %+v", current)
	}
}

func TestConditionalPoolRemovalRetainsConcurrentReplacement(t *testing.T) {
	dir := t.TempDir()
	old := &Account{
		TokenV2:   "old-token",
		UserID:    "user",
		UserEmail: "replace@example.com",
		SpaceID:   "space",
	}
	if _, err := SaveAccountToFile(old, dir); err != nil {
		t.Fatal(err)
	}
	pool := NewAccountPool()
	pool.AddAccount(old)
	if err := deleteAccountFileByIdentity(old.AccountID, old.TokenV2, dir); err != nil {
		t.Fatal(err)
	}

	replacement := &Account{
		TokenV2:   "new-token",
		UserID:    old.UserID,
		UserEmail: old.UserEmail,
		SpaceID:   old.SpaceID,
	}
	if _, err := SaveAccountToFile(replacement, dir); err != nil {
		t.Fatal(err)
	}
	pool.AddAccount(replacement)
	if pool.RemoveAccountByAccountIDIfToken(old.AccountID, old.TokenV2) {
		t.Fatal("old deletion removed the replacement")
	}
	if current := pool.FindByAccountID(old.AccountID); current == nil || current.TokenV2 != "new-token" {
		t.Fatalf("replacement missing: %+v", current)
	}
}

func TestDeleteAndSameTokenReimportStayConsistent(t *testing.T) {
	dir := t.TempDir()
	old := &Account{
		TokenV2:   "same-token",
		UserID:    "user",
		UserName:  "Old",
		UserEmail: "same-token@example.com",
		SpaceID:   "space",
	}
	if _, err := SaveAccountToFile(old, dir); err != nil {
		t.Fatal(err)
	}
	pool := NewAccountPool()
	pool.AddAccount(old)

	diskDeleted := make(chan struct{})
	releaseDelete := make(chan struct{})
	previousHook := accountDeletionAfterDiskHook
	accountDeletionAfterDiskHook = func() {
		close(diskDeleted)
		<-releaseDelete
	}
	t.Cleanup(func() { accountDeletionAfterDiskHook = previousHook })

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- deleteAccountByAccountIdentity(pool, dir, old.AccountID, old.UserEmail, old.TokenV2)
	}()
	select {
	case <-diskDeleted:
	case <-time.After(2 * time.Second):
		t.Fatal("delete did not reach disk barrier")
	}

	replacement := &Account{
		TokenV2:   old.TokenV2,
		UserID:    old.UserID,
		UserName:  "Replacement",
		UserEmail: old.UserEmail,
		SpaceID:   old.SpaceID,
	}
	importStarted := make(chan struct{})
	importDone := make(chan error, 1)
	go func() {
		close(importStarted)
		if _, err := SaveAccountToFile(replacement, dir); err != nil {
			importDone <- err
			return
		}
		importDone <- pool.ActivateAccountByIDFromDir(dir, replacement.AccountID)
	}()
	<-importStarted
	select {
	case err := <-importDone:
		t.Fatalf("reimport bypassed directory critical section: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseDelete)
	if err := <-deleteDone; err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := <-importDone; err != nil {
		t.Fatalf("reimport: %v", err)
	}
	current := pool.FindByAccountID(old.AccountID)
	if current == nil || current.UserName != "Replacement" {
		t.Fatalf("pool winner=%+v", current)
	}
	if _, err := LoadAccountByIDFromDir(dir, old.AccountID); err != nil {
		t.Fatalf("disk winner missing: %v", err)
	}
}
