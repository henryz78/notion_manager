package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	AccountBatchCheckPersonal   = "check_personal_instructions"
	AccountBatchDisable         = "disable"
	AccountBatchEnable          = "enable"
	AccountBatchDelete          = "delete"
	AccountBatchDeleteMissing   = "delete_missing_personal_instructions"
	AccountBatchDeleteExhausted = "delete_exhausted"

	accountBatchHistoryLimit = 20
	accountBatchMaxAccounts  = 20000
)

var ErrAccountBatchJobRunning = errors.New("account batch job already running")

type AccountBatchStep struct {
	AccountID  string `json:"account_id,omitempty"`
	Email      string `json:"email"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	Configured *bool  `json:"configured,omitempty"`
	selector   string
}

type AccountBatchJob struct {
	ID          string             `json:"id"`
	Action      string             `json:"action"`
	State       string             `json:"state"`
	CreatedAt   time.Time          `json:"created_at"`
	EndedAt     *time.Time         `json:"ended_at,omitempty"`
	Concurrency int                `json:"concurrency"`
	Total       int                `json:"total"`
	Done        int                `json:"done"`
	Succeeded   int                `json:"succeeded"`
	Failed      int                `json:"failed"`
	Skipped     int                `json:"skipped"`
	Configured  int                `json:"configured"`
	Missing     int                `json:"missing"`
	Message     string             `json:"message,omitempty"`
	Steps       []AccountBatchStep `json:"steps"`
}

type accountBatchHistory struct {
	Jobs []*AccountBatchJob `json:"jobs"`
}

type AccountBatchManager struct {
	mu          sync.RWMutex
	pool        *AccountPool
	accountsDir string
	path        string
	jobs        map[string]*AccountBatchJob
	order       []string
}

func NewAccountBatchManager(pool *AccountPool, accountsDir, path string) (*AccountBatchManager, error) {
	manager := &AccountBatchManager{
		pool:        pool,
		accountsDir: accountsDir,
		path:        path,
		jobs:        make(map[string]*AccountBatchJob),
	}
	if path == "" {
		return manager, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return manager, nil
		}
		return nil, err
	}
	var history accountBatchHistory
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, job := range history.Jobs {
		if job == nil || strings.TrimSpace(job.ID) == "" {
			continue
		}
		if job.State == "running" {
			job.State = "interrupted"
			job.Message = "服务重启，任务已中断，可重试失败项"
			job.EndedAt = &now
			for i := range job.Steps {
				if job.Steps[i].Status == "pending" || job.Steps[i].Status == "running" {
					job.Steps[i].Status = "failed"
					job.Steps[i].Message = "服务重启，任务中断"
					job.Done++
					job.Failed++
				}
			}
		}
		manager.jobs[job.ID] = job
		manager.order = append(manager.order, job.ID)
	}
	manager.trimLocked()
	return manager, nil
}

func cloneAccountBatchJob(job *AccountBatchJob) *AccountBatchJob {
	if job == nil {
		return nil
	}
	clone := *job
	clone.Steps = append([]AccountBatchStep(nil), job.Steps...)
	if job.EndedAt != nil {
		ended := *job.EndedAt
		clone.EndedAt = &ended
	}
	for i := range clone.Steps {
		clone.Steps[i].Configured = cloneBoolPtr(job.Steps[i].Configured)
	}
	return &clone
}

func (m *AccountBatchManager) trimLocked() {
	if len(m.order) <= accountBatchHistoryLimit {
		return
	}
	overflow := len(m.order) - accountBatchHistoryLimit
	for _, id := range m.order[:overflow] {
		delete(m.jobs, id)
	}
	m.order = append([]string(nil), m.order[overflow:]...)
}

func (m *AccountBatchManager) persistLocked() {
	if m == nil || m.path == "" {
		return
	}
	jobs := make([]*AccountBatchJob, 0, len(m.order))
	for _, id := range m.order {
		if job := m.jobs[id]; job != nil {
			jobs = append(jobs, job)
		}
	}
	data, err := json.MarshalIndent(accountBatchHistory{Jobs: jobs}, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(m.path, append(data, '\n'), 0o600)
}

func normalizeAccountBatchAction(action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case AccountBatchCheckPersonal, AccountBatchDisable, AccountBatchEnable,
		AccountBatchDelete, AccountBatchDeleteMissing, AccountBatchDeleteExhausted:
		return action
	default:
		return ""
	}
}

func clampAccountBatchConcurrency(value int) int {
	if value <= 0 {
		return 10
	}
	if value > 20 {
		return 20
	}
	return value
}

func (m *AccountBatchManager) activeLocked() *AccountBatchJob {
	for index := len(m.order) - 1; index >= 0; index-- {
		if job := m.jobs[m.order[index]]; job != nil && job.State == "running" {
			return job
		}
	}
	return nil
}

// Active returns the currently running account batch job. The manager permits
// only one live job so two dashboard tabs cannot mutate the same account pool
// at the same time.
func (m *AccountBatchManager) Active() (*AccountBatchJob, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	job := cloneAccountBatchJob(m.activeLocked())
	m.mu.RUnlock()
	return job, job != nil
}

func (m *AccountBatchManager) Start(action string, selectors []string, concurrency int) (*AccountBatchJob, error) {
	if m == nil || m.pool == nil {
		return nil, fmt.Errorf("batch manager is not initialized")
	}
	action = normalizeAccountBatchAction(action)
	if action == "" {
		return nil, fmt.Errorf("unsupported batch action")
	}
	selectors = normalizeBulkEmails(selectors)
	if len(selectors) == 0 {
		return nil, fmt.Errorf("at least one account is required")
	}
	if len(selectors) > accountBatchMaxAccounts {
		return nil, fmt.Errorf("too many accounts selected")
	}
	releaseMutation, ok := m.pool.beginAccountMutation()
	if !ok {
		return nil, errors.New(accountRestoreConflictMessage)
	}
	concurrency = clampAccountBatchConcurrency(concurrency)
	targets := make(map[string]*Account, len(selectors))
	steps := make([]AccountBatchStep, 0, len(selectors))
	failed := 0
	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		var account *Account
		var resolveErr error
		if isAccountID(strings.ToLower(selector)) {
			account = m.pool.FindByAccountID(strings.ToLower(selector))
			if account == nil {
				resolveErr = os.ErrNotExist
			}
		} else {
			account, resolveErr = m.pool.FindByEmail(selector)
		}
		step := AccountBatchStep{Email: selector, Status: "pending"}
		if resolveErr != nil || account == nil {
			step.Status = "failed"
			var ambiguousEmail *AmbiguousEmailError
			if errors.As(resolveErr, &ambiguousEmail) {
				step.Message = "同一邮箱属于多个工作区，请按账号 ID 重试"
			} else {
				step.Message = "账号不存在"
			}
			failed++
		} else {
			account.EnsureAccountID()
			step.AccountID = account.AccountID
			step.Email = account.UserEmail
			step.selector = strings.ToLower(selector)
			targets[step.selector] = account
		}
		steps = append(steps, step)
	}
	job := &AccountBatchJob{
		ID:          generateUUIDv4(),
		Action:      action,
		State:       "running",
		CreatedAt:   time.Now().UTC(),
		Concurrency: concurrency,
		Total:       len(steps),
		Done:        failed,
		Failed:      failed,
		Steps:       steps,
	}
	m.mu.Lock()
	if active := m.activeLocked(); active != nil {
		snapshot := cloneAccountBatchJob(active)
		m.mu.Unlock()
		releaseMutation()
		return snapshot, ErrAccountBatchJobRunning
	}
	m.jobs[job.ID] = job
	m.order = append(m.order, job.ID)
	m.trimLocked()
	m.persistLocked()
	snapshot := cloneAccountBatchJob(job)
	m.mu.Unlock()

	go func() {
		defer releaseMutation()
		m.run(job.ID, targets)
	}()
	return snapshot, nil
}

type accountBatchExecution struct {
	status     string
	message    string
	configured *bool
}

func (m *AccountBatchManager) execute(action string, account *Account) accountBatchExecution {
	if account == nil {
		return accountBatchExecution{status: "failed", message: "账号不存在"}
	}
	switch action {
	case AccountBatchCheckPersonal:
		checkedAt := time.Now().UTC()
		pageID, err := fetchNotionPersonalInstructionsPageID(account)
		if err != nil {
			message := truncateForLog(err.Error(), 300)
			account.setPersonalInstructionsCheck(nil, checkedAt, message)
			_ = saveAccountFile(m.accountsDir, account)
			return accountBatchExecution{status: "failed", message: message}
		}
		configured := strings.TrimSpace(pageID) != ""
		account.setPersonalInstructionsCheck(&configured, checkedAt, "")
		if err := saveAccountFile(m.accountsDir, account); err != nil {
			return accountBatchExecution{status: "failed", message: err.Error(), configured: &configured}
		}
		if configured {
			return accountBatchExecution{status: "success", message: "已设置官网个人指令", configured: &configured}
		}
		return accountBatchExecution{status: "success", message: "未设置官网个人指令", configured: &configured}
	case AccountBatchDisable, AccountBatchEnable:
		disabled := action == AccountBatchDisable
		previous := account.setManuallyDisabled(disabled)
		if err := saveAccountFile(m.accountsDir, account); err != nil {
			account.setManuallyDisabled(previous)
			return accountBatchExecution{status: "failed", message: err.Error()}
		}
		if disabled {
			return accountBatchExecution{status: "success", message: "已禁用"}
		}
		return accountBatchExecution{status: "success", message: "已启用"}
	case AccountBatchDelete:
		account.EnsureAccountID()
		var err error
		if account.AccountID == "" {
			err = deleteAccountByIdentity(m.pool, m.accountsDir, account.UserEmail, account.TokenV2)
		} else {
			err = deleteAccountByAccountIdentity(m.pool, m.accountsDir, account.AccountID, account.UserEmail, account.TokenV2)
		}
		if err != nil {
			return accountBatchExecution{status: "failed", message: err.Error()}
		}
		return accountBatchExecution{status: "success", message: "已删除"}
	case AccountBatchDeleteMissing:
		checkedAt := time.Now().UTC()
		pageID, err := fetchNotionPersonalInstructionsPageID(account)
		if err != nil {
			message := truncateForLog(err.Error(), 300)
			account.setPersonalInstructionsCheck(nil, checkedAt, message)
			_ = saveAccountFile(m.accountsDir, account)
			return accountBatchExecution{status: "failed", message: message}
		}
		configured := strings.TrimSpace(pageID) != ""
		account.setPersonalInstructionsCheck(&configured, checkedAt, "")
		if configured {
			if err := saveAccountFile(m.accountsDir, account); err != nil {
				return accountBatchExecution{status: "failed", message: err.Error(), configured: &configured}
			}
			return accountBatchExecution{status: "skipped", message: "已设置，保留账号", configured: &configured}
		}
		account.EnsureAccountID()
		var deleteErr error
		if account.AccountID == "" {
			deleteErr = deleteAccountByIdentity(m.pool, m.accountsDir, account.UserEmail, account.TokenV2)
		} else {
			deleteErr = deleteAccountByAccountIdentity(m.pool, m.accountsDir, account.AccountID, account.UserEmail, account.TokenV2)
		}
		if deleteErr != nil {
			return accountBatchExecution{status: "failed", message: deleteErr.Error(), configured: &configured}
		}
		return accountBatchExecution{status: "success", message: "未设置，已删除", configured: &configured}
	case AccountBatchDeleteExhausted:
		if !isExhaustedComplimentaryAccount(account) {
			return accountBatchExecution{status: "skipped", message: "不符合清理条件，已保留"}
		}
		confirmed, err := confirmExhaustedComplimentaryAccount(m.pool, account)
		if err != nil {
			return accountBatchExecution{status: "failed", message: err.Error()}
		}
		if !confirmed {
			return accountBatchExecution{status: "skipped", message: "实时复核后仍可用，已保留"}
		}
		account.EnsureAccountID()
		var deleteErr error
		if account.AccountID == "" {
			deleteErr = deleteAccountByIdentity(m.pool, m.accountsDir, account.UserEmail, account.TokenV2)
		} else {
			deleteErr = deleteAccountByAccountIdentity(m.pool, m.accountsDir, account.AccountID, account.UserEmail, account.TokenV2)
		}
		if deleteErr != nil {
			return accountBatchExecution{status: "failed", message: deleteErr.Error()}
		}
		return accountBatchExecution{status: "success", message: "已删除用完试用额度的账号"}
	default:
		return accountBatchExecution{status: "failed", message: "未知操作"}
	}
}

func (m *AccountBatchManager) run(id string, targets map[string]*Account) {
	m.mu.RLock()
	job := m.jobs[id]
	if job == nil {
		m.mu.RUnlock()
		return
	}
	action := job.Action
	concurrency := job.Concurrency
	pending := make([]int, 0, job.Total-job.Done)
	for index, step := range job.Steps {
		if step.Status == "pending" {
			pending = append(pending, index)
		}
	}
	m.mu.RUnlock()

	work := make(chan int)
	var workers sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range work {
				m.mu.Lock()
				job := m.jobs[id]
				if job == nil || index >= len(job.Steps) {
					m.mu.Unlock()
					continue
				}
				job.Steps[index].Status = "running"
				selector := job.Steps[index].selector
				m.mu.Unlock()

				result := m.execute(action, targets[selector])

				m.mu.Lock()
				job = m.jobs[id]
				if job != nil && index < len(job.Steps) {
					step := &job.Steps[index]
					step.Status = result.status
					step.Message = result.message
					step.Configured = cloneBoolPtr(result.configured)
					job.Done++
					switch result.status {
					case "success":
						job.Succeeded++
					case "skipped":
						job.Skipped++
					default:
						job.Failed++
					}
					if result.configured != nil {
						if *result.configured {
							job.Configured++
						} else {
							job.Missing++
						}
					}
				}
				m.mu.Unlock()
			}
		}()
	}
	for _, index := range pending {
		work <- index
	}
	close(work)
	workers.Wait()

	m.mu.Lock()
	if job := m.jobs[id]; job != nil {
		now := time.Now().UTC()
		job.EndedAt = &now
		job.State = "done"
		job.Message = "批量任务已完成"
	}
	m.persistLocked()
	m.mu.Unlock()
}

func (m *AccountBatchManager) Get(id string) (*AccountBatchJob, bool) {
	if m == nil {
		return nil, false
	}
	m.mu.RLock()
	job := cloneAccountBatchJob(m.jobs[id])
	m.mu.RUnlock()
	return job, job != nil
}

func (m *AccountBatchManager) List(limit int) []*AccountBatchJob {
	if m == nil {
		return []*AccountBatchJob{}
	}
	if limit <= 0 || limit > accountBatchHistoryLimit {
		limit = accountBatchHistoryLimit
	}
	m.mu.RLock()
	result := make([]*AccountBatchJob, 0, limit)
	for index := len(m.order) - 1; index >= 0 && len(result) < limit; index-- {
		if job := m.jobs[m.order[index]]; job != nil {
			result = append(result, cloneAccountBatchJob(job))
		}
	}
	m.mu.RUnlock()
	return result
}

func (m *AccountBatchManager) Retry(id string) (*AccountBatchJob, error) {
	job, ok := m.Get(id)
	if !ok {
		return nil, os.ErrNotExist
	}
	selectors := make([]string, 0, job.Failed)
	for _, step := range job.Steps {
		if step.Status == "failed" {
			if step.AccountID != "" {
				selectors = append(selectors, step.AccountID)
			} else {
				selectors = append(selectors, step.Email)
			}
		}
	}
	if len(selectors) == 0 {
		return nil, fmt.Errorf("job has no failed accounts")
	}
	return m.Start(job.Action, selectors, job.Concurrency)
}

type StartAccountBatchJobRequest struct {
	Action      string   `json:"action"`
	AccountIDs  []string `json:"account_ids,omitempty"`
	Emails      []string `json:"emails,omitempty"`
	Concurrency int      `json:"concurrency"`
}

func authorizeAccountBatch(auth *DashboardAuth, w http.ResponseWriter, r *http.Request) bool {
	if auth.HasAdminPassword() && !auth.ValidateSession(r) {
		http.Error(w, `{"error":"unauthorized, dashboard login required"}`, http.StatusUnauthorized)
		return false
	}
	return true
}

// HandleAccountBatchJobs handles POST start and GET recent jobs.
func HandleAccountBatchJobs(manager *AccountBatchManager, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !authorizeAccountBatch(auth, w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"jobs": manager.List(20)})
		case http.MethodPost:
			r.Body = http.MaxBytesReader(w, r.Body, 4*1024*1024)
			var body StartAccountBatchJobRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
				return
			}
			selectors := append(append(make([]string, 0, len(body.AccountIDs)+len(body.Emails)), body.AccountIDs...), body.Emails...)
			job, err := manager.Start(body.Action, selectors, body.Concurrency)
			if err != nil {
				if errors.Is(err, ErrAccountBatchJobRunning) {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"error":      "已有批量任务正在运行",
						"active_job": job,
					})
					return
				}
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(job)
		default:
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		}
	}
}

// HandleAccountBatchJobRouter handles GET snapshot and POST retry.
func HandleAccountBatchJobRouter(manager *AccountBatchManager, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !authorizeAccountBatch(auth, w, r) {
			return
		}
		path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin/account-batch-jobs/"), "/")
		parts := strings.Split(path, "/")
		if len(parts) == 0 || parts[0] == "" {
			http.Error(w, `{"error":"job id is required"}`, http.StatusBadRequest)
			return
		}
		id := parts[0]
		if len(parts) == 2 && parts[1] == "retry" {
			if r.Method != http.MethodPost {
				http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
				return
			}
			job, err := manager.Retry(id)
			if err != nil {
				if errors.Is(err, ErrAccountBatchJobRunning) {
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"error":      "已有批量任务正在运行",
						"active_job": job,
					})
					return
				}
				status := http.StatusBadRequest
				if os.IsNotExist(err) {
					status = http.StatusNotFound
				}
				http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), status)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(job)
			return
		}
		if len(parts) != 1 || r.Method != http.MethodGet {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		job, ok := manager.Get(id)
		if !ok {
			http.Error(w, `{"error":"job not found"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(job)
	}
}
