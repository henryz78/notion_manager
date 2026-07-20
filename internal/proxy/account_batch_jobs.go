package proxy

import (
	"encoding/json"
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

type AccountBatchStep struct {
	Email      string `json:"email"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	Configured *bool  `json:"configured,omitempty"`
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

func (m *AccountBatchManager) Start(action string, emails []string, concurrency int) (*AccountBatchJob, error) {
	if m == nil || m.pool == nil {
		return nil, fmt.Errorf("batch manager is not initialized")
	}
	action = normalizeAccountBatchAction(action)
	if action == "" {
		return nil, fmt.Errorf("unsupported batch action")
	}
	emails = normalizeBulkEmails(emails)
	if len(emails) == 0 {
		return nil, fmt.Errorf("at least one email is required")
	}
	if len(emails) > accountBatchMaxAccounts {
		return nil, fmt.Errorf("too many accounts selected")
	}
	concurrency = clampAccountBatchConcurrency(concurrency)
	accounts, missing := accountsMatchingEmails(m.pool, emails)
	targets := make(map[string]*Account, len(accounts))
	for _, account := range accounts {
		targets[strings.ToLower(strings.TrimSpace(account.UserEmail))] = account
	}
	missingSet := make(map[string]struct{}, len(missing))
	for _, email := range missing {
		missingSet[strings.ToLower(strings.TrimSpace(email))] = struct{}{}
	}
	steps := make([]AccountBatchStep, 0, len(emails))
	failed := 0
	for _, email := range emails {
		step := AccountBatchStep{Email: email, Status: "pending"}
		if _, absent := missingSet[strings.ToLower(email)]; absent {
			step.Status = "failed"
			step.Message = "账号不存在"
			failed++
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
	m.jobs[job.ID] = job
	m.order = append(m.order, job.ID)
	m.trimLocked()
	m.persistLocked()
	snapshot := cloneAccountBatchJob(job)
	m.mu.Unlock()

	go m.run(job.ID, targets)
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
		if err := deleteAccountByEmail(m.pool, m.accountsDir, account.UserEmail); err != nil {
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
		if err := deleteAccountByEmail(m.pool, m.accountsDir, account.UserEmail); err != nil {
			return accountBatchExecution{status: "failed", message: err.Error(), configured: &configured}
		}
		return accountBatchExecution{status: "success", message: "未设置，已删除", configured: &configured}
	case AccountBatchDeleteExhausted:
		if !isExhaustedComplimentaryAccount(account) {
			return accountBatchExecution{status: "skipped", message: "不符合清理条件，已保留"}
		}
		if err := deleteAccountByEmail(m.pool, m.accountsDir, account.UserEmail); err != nil {
			return accountBatchExecution{status: "failed", message: err.Error()}
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
				email := job.Steps[index].Email
				m.mu.Unlock()

				result := m.execute(action, targets[strings.ToLower(strings.TrimSpace(email))])

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
	emails := make([]string, 0, job.Failed)
	for _, step := range job.Steps {
		if step.Status == "failed" {
			emails = append(emails, step.Email)
		}
	}
	if len(emails) == 0 {
		return nil, fmt.Errorf("job has no failed accounts")
	}
	return m.Start(job.Action, emails, job.Concurrency)
}

type StartAccountBatchJobRequest struct {
	Action      string   `json:"action"`
	Emails      []string `json:"emails"`
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
			job, err := manager.Start(body.Action, body.Emails, body.Concurrency)
			if err != nil {
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
