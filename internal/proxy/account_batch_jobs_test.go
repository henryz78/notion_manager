package proxy

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitForAccountBatchJob(t *testing.T, manager *AccountBatchManager, id string) *AccountBatchJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := manager.Get(id)
		if !ok {
			t.Fatalf("job %s disappeared", id)
		}
		if job.State != "running" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish", id)
	return nil
}

func TestAccountBatchManagerRunsAndPersistsParallelDisable(t *testing.T) {
	dir := t.TempDir()
	accounts := []*Account{
		{UserEmail: "a@example.com", QuotaInfo: &QuotaInfo{IsEligible: true}},
		{UserEmail: "b@example.com", QuotaInfo: &QuotaInfo{IsEligible: true}},
		{UserEmail: "c@example.com", QuotaInfo: &QuotaInfo{IsEligible: true}},
		{UserEmail: "d@example.com", QuotaInfo: &QuotaInfo{IsEligible: true}},
	}
	for _, account := range accounts {
		writeBulkActionAccountFile(t, dir, account)
	}
	pool := NewAccountPool()
	pool.accounts = accounts
	path := filepath.Join(dir, ".account_batch_jobs.json")
	manager, err := NewAccountBatchManager(pool, dir, path)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	job, err := manager.Start(AccountBatchDisable, []string{
		accounts[0].UserEmail,
		accounts[1].UserEmail,
		accounts[2].UserEmail,
		accounts[3].UserEmail,
	}, 4)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	finished := waitForAccountBatchJob(t, manager, job.ID)
	if finished.State != "done" || finished.Done != 4 || finished.Succeeded != 4 || finished.Failed != 0 {
		t.Fatalf("unexpected finished job: %+v", finished)
	}
	if finished.Concurrency != 4 {
		t.Fatalf("concurrency=%d, want 4", finished.Concurrency)
	}
	if got := pool.AvailableCount(); got != 0 {
		t.Fatalf("available=%d, want 0", got)
	}

	reloaded, err := NewAccountBatchManager(pool, dir, path)
	if err != nil {
		t.Fatalf("reload manager: %v", err)
	}
	restored, ok := reloaded.Get(job.ID)
	if !ok || restored.State != "done" || restored.Succeeded != 4 {
		t.Fatalf("persisted job not restored: %+v", restored)
	}
}

func TestAccountBatchManagerRetryUsesFailedAccounts(t *testing.T) {
	dir := t.TempDir()
	account := &Account{UserEmail: "exists@example.com", QuotaInfo: &QuotaInfo{IsEligible: true}}
	writeBulkActionAccountFile(t, dir, account)
	pool := NewAccountPool()
	pool.accounts = []*Account{account}
	manager, err := NewAccountBatchManager(pool, dir, "")
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	job, err := manager.Start(AccountBatchDisable, []string{account.UserEmail, "missing@example.com"}, 2)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	finished := waitForAccountBatchJob(t, manager, job.ID)
	if finished.Succeeded != 1 || finished.Failed != 1 {
		t.Fatalf("unexpected first job: %+v", finished)
	}
	retry, err := manager.Retry(job.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	retried := waitForAccountBatchJob(t, manager, retry.ID)
	if retried.Total != 1 || retried.Failed != 1 || retried.Steps[0].Email != "missing@example.com" {
		t.Fatalf("retry did not isolate failed account: %+v", retried)
	}
}

func TestAccountBatchManagerReturnsExistingRunningJob(t *testing.T) {
	pool := NewAccountPool()
	pool.accounts = []*Account{{UserEmail: "selected@example.com"}}
	manager, err := NewAccountBatchManager(pool, t.TempDir(), "")
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	active := &AccountBatchJob{
		ID:        "already-running",
		Action:    AccountBatchCheckPersonal,
		State:     "running",
		CreatedAt: time.Now().UTC(),
		Total:     1,
		Steps:     []AccountBatchStep{{Email: "other@example.com", Status: "running"}},
	}
	manager.jobs[active.ID] = active
	manager.order = append(manager.order, active.ID)

	got, err := manager.Start(AccountBatchDisable, []string{"selected@example.com"}, 10)
	if !errors.Is(err, ErrAccountBatchJobRunning) {
		t.Fatalf("error=%v, want ErrAccountBatchJobRunning", err)
	}
	if got == nil || got.ID != active.ID {
		t.Fatalf("returned job=%+v, want active job %s", got, active.ID)
	}
	if len(manager.List(20)) != 1 {
		t.Fatalf("duplicate job was created")
	}
}

func TestHandleAccountBatchJobsConflictReturnsActiveJob(t *testing.T) {
	pool := NewAccountPool()
	pool.accounts = []*Account{{UserEmail: "selected@example.com"}}
	manager, err := NewAccountBatchManager(pool, t.TempDir(), "")
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	active := &AccountBatchJob{
		ID:        "active-from-other-tab",
		Action:    AccountBatchDisable,
		State:     "running",
		CreatedAt: time.Now().UTC(),
		Total:     1,
		Steps:     []AccountBatchStep{{Email: "other@example.com", Status: "running"}},
	}
	manager.jobs[active.ID] = active
	manager.order = append(manager.order, active.ID)

	req := httptest.NewRequest(http.MethodPost, "/admin/account-batch-jobs", strings.NewReader(
		`{"action":"enable","emails":["selected@example.com"],"concurrency":10}`,
	))
	rec := httptest.NewRecorder()
	HandleAccountBatchJobs(manager, NewDashboardAuth("", "")).ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Error     string           `json:"error"`
		ActiveJob *AccountBatchJob `json:"active_job"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Error == "" || response.ActiveJob == nil || response.ActiveJob.ID != active.ID {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestHandleAccountBatchJobsAcceptsMixedIDsAndLegacyEmails(t *testing.T) {
	dir := t.TempDir()
	exact := &Account{
		TokenV2:   "exact-token",
		UserID:    "exact-user",
		UserEmail: "exact@example.com",
		SpaceID:   "exact-space",
		QuotaInfo: &QuotaInfo{IsEligible: true},
	}
	legacy := &Account{
		TokenV2:   "legacy-token",
		UserEmail: "legacy@example.com",
		QuotaInfo: &QuotaInfo{IsEligible: true},
	}
	for _, account := range []*Account{exact, legacy} {
		if _, err := SaveAccountToFile(account, dir); err != nil {
			t.Fatal(err)
		}
	}
	pool := NewAccountPool()
	pool.accounts = []*Account{exact, legacy}
	manager, err := NewAccountBatchManager(pool, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	exact.EnsureAccountID()

	body := `{"action":"disable","account_ids":["` + exact.AccountID + `"],"emails":["legacy@example.com"],"concurrency":2}`
	req := httptest.NewRequest(http.MethodPost, "/admin/account-batch-jobs", strings.NewReader(body))
	rec := httptest.NewRecorder()
	HandleAccountBatchJobs(manager, NewDashboardAuth("", "")).ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var started AccountBatchJob
	if err := json.NewDecoder(rec.Body).Decode(&started); err != nil {
		t.Fatal(err)
	}
	finished := waitForAccountBatchJob(t, manager, started.ID)
	if finished.Total != 2 || finished.Succeeded != 2 || finished.Failed != 0 {
		t.Fatalf("job=%+v", finished)
	}
}

func TestDeleteNoWorkspaceBatchRechecksBeforeDeleting(t *testing.T) {
	originalWorkspaceProbe := workspaceProbe
	t.Cleanup(func() { workspaceProbe = originalWorkspaceProbe })

	dir := t.TempDir()
	recovered := &Account{
		TokenV2:   "recovered-token",
		UserID:    "recovered-user",
		UserEmail: "recovered@example.com",
		SpaceID:   "recovered-space",
	}
	stillMissing := &Account{
		TokenV2:   "missing-token",
		UserID:    "missing-user",
		UserEmail: "missing@example.com",
		SpaceID:   "missing-space",
	}
	for _, account := range []*Account{recovered, stillMissing} {
		if _, err := SaveAccountToFile(account, dir); err != nil {
			t.Fatalf("save %s: %v", account.UserEmail, err)
		}
	}
	workspaceProbe = func(account *Account) (WorkspaceProbeResult, error) {
		if account == recovered {
			return WorkspaceProbeResult{Count: 1, PlanType: "business", AIEnabled: true}, nil
		}
		return WorkspaceProbeResult{Count: 0}, nil
	}

	pool := NewAccountPool()
	pool.accounts = []*Account{recovered, stillMissing}
	manager, err := NewAccountBatchManager(pool, dir, "")
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	job, err := manager.Start(AccountBatchDeleteNoSpace, []string{recovered.AccountID, stillMissing.AccountID}, 2)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	finished := waitForAccountBatchJob(t, manager, job.ID)
	if finished.Succeeded != 1 || finished.Skipped != 1 || finished.Failed != 0 {
		t.Fatalf("unexpected cleanup result: %+v", finished)
	}
	if pool.FindByAccountID(recovered.AccountID) == nil {
		t.Fatal("recovered workspace was deleted")
	}
	if got := pool.FindByAccountID(stillMissing.AccountID); got != nil {
		t.Fatalf("confirmed no-workspace account was retained: %#v", got)
	}
}
