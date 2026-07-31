package proxy

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	mrand "math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"notion-manager/internal/accountstore"
)

// quotaFetcher / modelsFetcher / workspaceProbe are package-level seams
// so unit tests can substitute deterministic stubs without spinning up a
// fake Notion server (the real code path uses a TLS-fingerprinted
// transport pinned to www.notion.so, which can't be repointed at httptest
// URLs).
var (
	quotaFetcher        = CheckQuota
	routingQuotaFetcher = CheckQuotaV1
	modelsFetcher       = FetchModels
	workspaceProbe      = CheckUserWorkspaceProfile
)

func lockAccountFilePath(path string) (func(), error) {
	return accountstore.LockPath(path)
}

const (
	defaultAccountFailureCooldown = 2 * time.Minute
	accountRestoreConflictMessage = "account restore is in progress; retry after it finishes"
)

type AccountPool struct {
	mu       sync.RWMutex
	accounts []*Account
	index    atomic.Uint64
	// accountsDir is set when the pool loads its workspace files. Durable
	// health transitions (notably a revoked login cookie) use it to persist
	// only the affected account files immediately.
	accountsDir string

	// Account file mutations and backup restore share this state. A restore
	// starts only while no mutation is active; new mutations cannot start
	// until that restore finishes.
	maintenanceMu        sync.Mutex
	maintenanceMutations int
	maintenanceRestoring bool

	// Refresh state (protected by refreshMu)
	refreshMu     sync.RWMutex
	refreshing    bool
	refreshDone   int
	refreshTotal  int
	refreshFailed int
	lastRefreshAt *time.Time

	// Per-account live quota refresh state. A routing check is singleflight:
	// synchronous callers wait for the in-flight V1 result, while asynchronous
	// callers simply coalesce. Generations also prevent a slower, older
	// background/diagnostic response from overwriting a newer routing result.
	liveQuotaMu      sync.Mutex
	liveQuotaFlights map[*Account]*quotaRefreshFlight
	quotaGeneration  map[*Account]uint64
	quotaApplied     map[*Account]uint64
}

func NewAccountPool() *AccountPool {
	return &AccountPool{
		liveQuotaFlights: make(map[*Account]*quotaRefreshFlight),
		quotaGeneration:  make(map[*Account]uint64),
		quotaApplied:     make(map[*Account]uint64),
	}
}

type quotaRefreshFlight struct {
	done       chan struct{}
	generation uint64
	err        error
	eligible   bool
	hasResult  bool
}

func (p *AccountPool) beginAccountMutation() (func(), bool) {
	if p == nil {
		return func() {}, true
	}
	p.maintenanceMu.Lock()
	if p.maintenanceRestoring {
		p.maintenanceMu.Unlock()
		return nil, false
	}
	p.maintenanceMutations++
	p.maintenanceMu.Unlock()
	return p.endAccountMutation, true
}

func (p *AccountPool) endAccountMutation() {
	p.maintenanceMu.Lock()
	if p.maintenanceMutations > 0 {
		p.maintenanceMutations--
	}
	p.maintenanceMu.Unlock()
}

func (p *AccountPool) beginAccountRestore() (func(), bool) {
	if p == nil {
		return nil, false
	}
	p.maintenanceMu.Lock()
	if p.maintenanceRestoring || p.maintenanceMutations > 0 {
		p.maintenanceMu.Unlock()
		return nil, false
	}
	p.maintenanceRestoring = true
	p.maintenanceMu.Unlock()
	return p.endAccountRestore, true
}

func (p *AccountPool) endAccountRestore() {
	p.maintenanceMu.Lock()
	p.maintenanceRestoring = false
	p.maintenanceMu.Unlock()
}

type accountQuotaSnapshot struct {
	Info                 *QuotaInfo
	CheckedAt            *time.Time
	ExhaustedAt          *time.Time
	PermanentlyExhausted bool
}

func cloneModelEntries(src []ModelEntry) []ModelEntry {
	if len(src) == 0 {
		return nil
	}
	dst := make([]ModelEntry, len(src))
	copy(dst, src)
	return dst
}

func cloneQuotaInfo(src *QuotaInfo) *QuotaInfo {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

func cloneTimePtr(src *time.Time) *time.Time {
	if src == nil {
		return nil
	}
	dst := *src
	return &dst
}

func (acc *Account) modelsSnapshot() []ModelEntry {
	if acc == nil {
		return nil
	}
	acc.mu.RLock()
	defer acc.mu.RUnlock()
	return cloneModelEntries(acc.Models)
}

func (acc *Account) quotaSnapshot() accountQuotaSnapshot {
	if acc == nil {
		return accountQuotaSnapshot{}
	}
	acc.mu.RLock()
	defer acc.mu.RUnlock()
	return accountQuotaSnapshot{
		Info:                 cloneQuotaInfo(acc.QuotaInfo),
		CheckedAt:            cloneTimePtr(acc.QuotaCheckedAt),
		ExhaustedAt:          cloneTimePtr(acc.QuotaExhaustedAt),
		PermanentlyExhausted: acc.PermanentlyExhausted,
	}
}

func (acc *Account) quotaInfoSnapshot() *QuotaInfo {
	return acc.quotaSnapshot().Info
}

type accountHealthSnapshot struct {
	TemporaryUnavailableUntil *time.Time
	LastFailureReason         string
	LastFailureAt             *time.Time
	AuthFailureCount          int
	AuthInvalid               bool
	ManuallyDisabled          bool
}

type accountPersonalInstructionsSnapshot struct {
	Configured *bool
	CheckedAt  *time.Time
	Error      string
}

type accountProfileSnapshot struct {
	PlanType           string
	SpaceCount         int
	WorkspaceCheckedAt *time.Time
	AIEnabled          *bool
}

type accountPersistSnapshot struct {
	Models               []ModelEntry
	Quota                accountQuotaSnapshot
	PersonalInstructions accountPersonalInstructionsSnapshot
	Health               accountHealthSnapshot
	Profile              accountProfileSnapshot
}

func (acc *Account) profileSnapshot() accountProfileSnapshot {
	if acc == nil {
		return accountProfileSnapshot{}
	}
	acc.mu.RLock()
	defer acc.mu.RUnlock()
	return accountProfileSnapshot{
		PlanType:           acc.PlanType,
		SpaceCount:         acc.SpaceCount,
		WorkspaceCheckedAt: cloneTimePtr(acc.WorkspaceCheckedAt),
		AIEnabled:          cloneBoolPtr(acc.WorkspaceAIEnabled),
	}
}

func (acc *Account) planTypeSnapshot() string {
	return acc.profileSnapshot().PlanType
}

func (acc *Account) persistSnapshot() accountPersistSnapshot {
	if acc == nil {
		return accountPersistSnapshot{}
	}
	acc.mu.RLock()
	defer acc.mu.RUnlock()
	return accountPersistSnapshot{
		Models: cloneModelEntries(acc.Models),
		Quota: accountQuotaSnapshot{
			Info:                 cloneQuotaInfo(acc.QuotaInfo),
			CheckedAt:            cloneTimePtr(acc.QuotaCheckedAt),
			ExhaustedAt:          cloneTimePtr(acc.QuotaExhaustedAt),
			PermanentlyExhausted: acc.PermanentlyExhausted,
		},
		PersonalInstructions: accountPersonalInstructionsSnapshot{
			Configured: cloneBoolPtr(acc.PersonalInstructionsConfigured),
			CheckedAt:  cloneTimePtr(acc.PersonalInstructionsCheckedAt),
			Error:      acc.PersonalInstructionsCheckError,
		},
		Health: accountHealthSnapshot{
			TemporaryUnavailableUntil: cloneTimePtr(acc.TemporaryUnavailableUntil),
			LastFailureReason:         acc.LastFailureReason,
			LastFailureAt:             cloneTimePtr(acc.LastFailureAt),
			AuthFailureCount:          acc.AuthFailureCount,
			AuthInvalid:               acc.AuthInvalid,
			ManuallyDisabled:          acc.ManuallyDisabled,
		},
		Profile: accountProfileSnapshot{
			PlanType:           acc.PlanType,
			SpaceCount:         acc.SpaceCount,
			WorkspaceCheckedAt: cloneTimePtr(acc.WorkspaceCheckedAt),
			AIEnabled:          cloneBoolPtr(acc.WorkspaceAIEnabled),
		},
	}
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (acc *Account) personalInstructionsSnapshot() accountPersonalInstructionsSnapshot {
	if acc == nil {
		return accountPersonalInstructionsSnapshot{}
	}
	acc.mu.RLock()
	defer acc.mu.RUnlock()
	return accountPersonalInstructionsSnapshot{
		Configured: cloneBoolPtr(acc.PersonalInstructionsConfigured),
		CheckedAt:  cloneTimePtr(acc.PersonalInstructionsCheckedAt),
		Error:      acc.PersonalInstructionsCheckError,
	}
}

func (acc *Account) setPersonalInstructionsCheck(configured *bool, checkedAt time.Time, checkError string) {
	if acc == nil {
		return
	}
	acc.mu.Lock()
	acc.PersonalInstructionsConfigured = cloneBoolPtr(configured)
	acc.PersonalInstructionsCheckedAt = cloneTimePtr(&checkedAt)
	acc.PersonalInstructionsCheckError = strings.TrimSpace(checkError)
	acc.mu.Unlock()
}

func writePersonalInstructionsState(target map[string]interface{}, state accountPersonalInstructionsSnapshot) {
	if state.Configured != nil {
		target["personal_instructions_configured"] = *state.Configured
	} else {
		delete(target, "personal_instructions_configured")
	}
	if state.CheckedAt != nil {
		target["personal_instructions_checked_at"] = state.CheckedAt.Format(time.RFC3339)
	} else {
		delete(target, "personal_instructions_checked_at")
	}
	if state.Error != "" {
		target["personal_instructions_check_error"] = state.Error
	} else {
		delete(target, "personal_instructions_check_error")
	}
}

func (acc *Account) setManuallyDisabled(disabled bool) bool {
	if acc == nil {
		return false
	}
	acc.mu.Lock()
	previous := acc.ManuallyDisabled
	acc.ManuallyDisabled = disabled
	acc.mu.Unlock()
	return previous
}

func writeManualDisabledState(target map[string]interface{}, disabled bool) {
	if disabled {
		target["disabled"] = true
	} else {
		delete(target, "disabled")
	}
}

func writePersistedHealthState(target map[string]interface{}, state accountHealthSnapshot) {
	if state.AuthInvalid {
		target["auth_invalid"] = true
		target["last_failure_reason"] = "auth_invalid"
		if state.LastFailureAt != nil {
			target["last_failure_at"] = state.LastFailureAt.Format(time.RFC3339)
		}
		return
	}
	delete(target, "auth_invalid")
	delete(target, "last_failure_reason")
	delete(target, "last_failure_at")
}

func (acc *Account) healthSnapshot() accountHealthSnapshot {
	if acc == nil {
		return accountHealthSnapshot{}
	}
	acc.mu.RLock()
	defer acc.mu.RUnlock()
	return accountHealthSnapshot{
		TemporaryUnavailableUntil: cloneTimePtr(acc.TemporaryUnavailableUntil),
		LastFailureReason:         acc.LastFailureReason,
		LastFailureAt:             cloneTimePtr(acc.LastFailureAt),
		AuthFailureCount:          acc.AuthFailureCount,
		AuthInvalid:               acc.AuthInvalid,
		ManuallyDisabled:          acc.ManuallyDisabled,
	}
}

func (acc *Account) setModels(models []ModelEntry) {
	if acc == nil {
		return
	}
	cloned := cloneModelEntries(models)
	acc.mu.Lock()
	acc.Models = cloned
	acc.mu.Unlock()
	registerModelEntries(cloned)
}

func (acc *Account) setQuotaInfo(info *QuotaInfo, checkedAt *time.Time) {
	if acc == nil {
		return
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	acc.QuotaInfo = cloneQuotaInfo(info)
	acc.QuotaCheckedAt = cloneTimePtr(checkedAt)
}

func (acc *Account) markQuotaExhausted(now time.Time, permanent bool) bool {
	if acc == nil {
		return false
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	if acc.QuotaExhaustedAt != nil {
		if permanent {
			acc.PermanentlyExhausted = true
		}
		return false
	}
	ts := now
	acc.QuotaExhaustedAt = &ts
	acc.PermanentlyExhausted = permanent
	return true
}

func (acc *Account) clearQuotaExhausted() {
	if acc == nil {
		return
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	acc.QuotaExhaustedAt = nil
	acc.PermanentlyExhausted = false
}

func (p *AccountPool) LoadFromDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read accounts dir: %w", err)
	}
	p.accountsDir = dir

	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[account] skip %s: %v", entry.Name(), err)
			continue
		}
		var acc Account
		if err := json.Unmarshal(data, &acc); err != nil {
			log.Printf("[account] skip %s: %v", entry.Name(), err)
			continue
		}
		if acc.TokenV2 == "" || acc.UserID == "" || acc.SpaceID == "" {
			log.Printf("[account] skip %s: missing required fields", entry.Name())
			continue
		}
		if acc.TokenV2 == "YOUR_TOKEN_V2_HERE" || strings.HasPrefix(acc.UserID, "xxxxxxxx") {
			log.Printf("[account] skip %s: placeholder/example config", entry.Name())
			continue
		}
		acc.EnsureAccountID()
		if acc.AccountID != "" && seen[acc.AccountID] {
			log.Printf("[account] skip %s: duplicate account_id (same user_id+space_id)", entry.Name())
			continue
		}
		if acc.AccountID != "" {
			seen[acc.AccountID] = true
		}
		if acc.BrowserID == "" {
			acc.BrowserID = generateUUIDv4()
		}
		if acc.ClientVersion == "" || acc.ClientVersion == "unknown" {
			acc.ClientVersion = DefaultClientVersion
		}
		// Load persisted quota info (snake_case keys) into runtime QuotaInfo
		acc.QuotaInfo = loadPersistedQuotaInfo(data)
		// Load persisted workspace probe (`space_count` /
		// `workspace_checked_at`) so a server restart doesn't have to
		// re-probe every account before the pool can refuse known-bad
		// ones.
		loadPersistedWorkspace(data, &acc)
		loadPersistedHealth(data, &acc)
		registerModelEntries(acc.Models)
		p.accounts = append(p.accounts, &acc)
		log.Printf("[account] loaded: %s (%s) [%s] workspace=%s",
			acc.UserName, acc.UserEmail, acc.planTypeSnapshot(), acc.ShortSpaceID())
	}

	if len(p.accounts) == 0 {
		return fmt.Errorf("no valid accounts found in %s", dir)
	}
	log.Printf("[account] total: %d accounts loaded", len(p.accounts))
	return nil
}

// ReloadFromDir scans the accounts directory and adds any account whose
// user_id is not already present in the pool. Existing entries (and their
// runtime quota state) are preserved. This is invoked after bulk
// registration so newly-created files become live without a server
// restart.
func (p *AccountPool) ReloadFromDir(dir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.accountsDir = dir

	known := make(map[string]bool, len(p.accounts))
	for _, acc := range p.accounts {
		acc.EnsureAccountID()
		if acc.AccountID != "" {
			known[acc.AccountID] = true
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("[account] reload %s: %v", dir, err)
		return
	}
	added := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var acc Account
		if err := json.Unmarshal(data, &acc); err != nil {
			continue
		}
		if acc.TokenV2 == "" || acc.UserID == "" || acc.SpaceID == "" {
			continue
		}
		acc.EnsureAccountID()
		if acc.AccountID != "" && known[acc.AccountID] {
			continue
		}
		if acc.BrowserID == "" {
			acc.BrowserID = generateUUIDv4()
		}
		if acc.ClientVersion == "" || acc.ClientVersion == "unknown" {
			acc.ClientVersion = DefaultClientVersion
		}
		acc.QuotaInfo = loadPersistedQuotaInfo(data)
		loadPersistedWorkspace(data, &acc)
		loadPersistedHealth(data, &acc)
		registerModelEntries(acc.Models)
		p.accounts = append(p.accounts, &acc)
		if acc.AccountID != "" {
			known[acc.AccountID] = true
		}
		added++
		log.Printf("[account] reload added: %s (%s)", acc.UserName, acc.UserEmail)
	}
	if added > 0 {
		log.Printf("[account] reload: %d new account(s); pool now has %d", added, len(p.accounts))
	}
}

// LoadAccountByIDFromDir reads exactly one persisted workspace profile. It is
// used after registration so a refreshed token replaces the live pool entry
// immediately instead of waiting for a process restart.
func LoadAccountByIDFromDir(dir, accountID string) (*Account, error) {
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return nil, fmt.Errorf("account_id is required")
	}
	unlockDirectory, err := accountstore.LockDirectory(dir)
	if err != nil {
		return nil, err
	}
	defer unlockDirectory()
	return loadAccountByIDFromDirLocked(dir, accountID)
}

func loadAccountByIDFromDirLocked(dir, accountID string) (*Account, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read accounts dir: %w", err)
	}
	var match *Account
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var acc Account
		if err := json.Unmarshal(data, &acc); err != nil {
			continue
		}
		acc.EnsureAccountID()
		if acc.AccountID != accountID {
			continue
		}
		if match != nil {
			return nil, fmt.Errorf("multiple account files match account_id %s", accountID)
		}
		if acc.TokenV2 == "" || acc.UserID == "" || acc.SpaceID == "" {
			return nil, fmt.Errorf("account file %s is missing required fields", entry.Name())
		}
		if acc.BrowserID == "" {
			acc.BrowserID = generateUUIDv4()
		}
		if acc.ClientVersion == "" || acc.ClientVersion == "unknown" {
			acc.ClientVersion = DefaultClientVersion
		}
		acc.QuotaInfo = loadPersistedQuotaInfo(data)
		loadPersistedWorkspace(data, &acc)
		loadPersistedHealth(data, &acc)
		match = &acc
	}
	if match == nil {
		return nil, fmt.Errorf("account file not found for account_id %s: %w", accountID, os.ErrNotExist)
	}
	registerModelEntries(match.Models)
	return match, nil
}

// ActivateAccountByIDFromDir keeps the directory mutation lock until the
// freshly written profile is live in memory. A concurrent dashboard deletion
// therefore cannot remove the file and then be undone by a late registration
// callback.
func (p *AccountPool) ActivateAccountByIDFromDir(dir, accountID string) error {
	if p == nil {
		return fmt.Errorf("account pool is required")
	}
	accountID = strings.ToLower(strings.TrimSpace(accountID))
	if accountID == "" {
		return fmt.Errorf("account_id is required")
	}
	unlockDirectory, err := accountstore.LockDirectory(dir)
	if err != nil {
		return err
	}
	defer unlockDirectory()
	account, err := loadAccountByIDFromDirLocked(dir, accountID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.accountsDir = dir
	p.mu.Unlock()
	p.AddAccount(account)
	return nil
}

func (p *AccountPool) LoadSingle(tokenFile string) error {
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return err
	}

	var acc Account
	if err := json.Unmarshal(data, &acc); err != nil {
		// Treat as plain token file
		acc = Account{
			TokenV2:       string(data),
			UserID:        "322d872b-594c-816e-b8ce-00022c725bb3",
			SpaceID:       "176faced-55bd-8161-bbbf-000339934d27",
			UserName:      "default",
			SpaceName:     "default",
			Timezone:      "UTC",
			ClientVersion: DefaultClientVersion,
			BrowserID:     generateUUIDv4(),
		}
	}
	if acc.BrowserID == "" {
		acc.BrowserID = generateUUIDv4()
	}
	if acc.ClientVersion == "" || acc.ClientVersion == "unknown" {
		acc.ClientVersion = DefaultClientVersion
	}
	registerModelEntries(acc.Models)
	acc.EnsureAccountID()
	p.accounts = append(p.accounts, &acc)
	log.Printf("[account] loaded single account: %s (aid=%s)", acc.UserName, acc.ShortSpaceID())
	return nil
}

func (p *AccountPool) Next() *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pickNextRoundRobin(nil)
}

// NextForResearch returns the best usable account for research mode using the
// same deterministic product-tier preference as ordinary new conversations.
// The real request result still decides feature eligibility.
func (p *AccountPool) NextForResearch() *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pickBestAccountLocked(nil)
}

// NextExcluding returns the next available account excluding the given ones (for retry)
func (p *AccountPool) NextExcluding(exclude map[*Account]bool) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pickNextRoundRobin(exclude)
}

// pickNextRoundRobin returns the first available (non-exhausted, non-excluded)
// account starting from the rotating index. This gives true round-robin
// distribution across accounts.
func (p *AccountPool) pickNextRoundRobin(exclude map[*Account]bool) *Account {
	n := len(p.accounts)
	if n == 0 {
		return nil
	}
	start := p.index.Add(1) - 1
	for i := 0; i < n; i++ {
		acc := p.accounts[(start+uint64(i))%uint64(n)]
		if exclude != nil && exclude[acc] {
			continue
		}
		if p.isUnusable(acc) {
			continue
		}
		return acc
	}
	return nil
}

// pickBestAccountLocked returns the best available account by evidence-backed
// service tier. Used by GetBestAccount (dashboard) and request routing.
//
// Selection rules:
//  1. Skip exhausted/excluded accounts.
//  2. Prefer Enterprise > Business > Team > Plus/Personal/Free while keeping
//     included-AI paid plans above limited trials. Private Space/User/Premium
//     counters are not used as plan evidence.
//  3. If no scored account is available (e.g. all accounts are unrefreshed),
//     fall back to the first usable account in rotation order so freshly
//     loaded accounts can still serve traffic before the first refresh
//     completes.
func (p *AccountPool) pickBestAccountLocked(exclude map[*Account]bool) *Account {
	n := len(p.accounts)
	if n == 0 {
		return nil
	}
	start := p.index.Add(1) - 1
	var best *Account
	var fallback *Account
	bestScore := -1
	for i := 0; i < n; i++ {
		acc := p.accounts[(start+uint64(i))%uint64(n)]
		if exclude != nil && exclude[acc] {
			continue
		}
		if p.isUnusable(acc) {
			continue
		}
		score := accountQuotaPriority(acc)
		if score < 0 {
			// Unknown quota — keep as fallback if no scored account exists
			if fallback == nil {
				fallback = acc
			}
			continue
		}
		if best == nil || score > bestScore {
			best = acc
			bestScore = score
		}
	}
	if best != nil {
		return best
	}
	return fallback
}

// NextBest returns the next available account, preferring full Notion AI plans
// without inventing arithmetic across undocumented private counters.
func (p *AccountPool) NextBest() *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pickBestAccountLocked(nil)
}

// NextBestExcluding returns the next best available account, excluding the
// given accounts (used for retries / failover).
func (p *AccountPool) NextBestExcluding(exclude map[*Account]bool) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pickBestAccountLocked(exclude)
}

// MarkQuotaExhausted marks an account as quota-exhausted with a timestamp.
// Recovery only happens when RefreshAll confirms isEligible=true via API.
// Trial accounts are retained and rechecked by RefreshAll so a later plan
// upgrade can recover them. No account file is deleted solely for exhaustion.
func (p *AccountPool) MarkQuotaExhausted(acc *Account) {
	if acc == nil {
		return
	}
	if planIncludesFullNotionAI(acc.planTypeSnapshot()) {
		acc.clearQuotaExhausted()
		log.Printf("[quota] ignored legacy Basic exhaustion for included-AI plan %s (%s)", acc.UserName, acc.UserEmail)
		return
	}
	if !acc.markQuotaExhausted(time.Now(), false) {
		return // already marked
	}
	log.Printf("[quota] marked %s (%s) as exhausted (recovery via API re-check only)", acc.UserName, acc.UserEmail)
}

// ClearQuotaExhausted removes the exhausted mark (called when API confirms recovery)
func (p *AccountPool) ClearQuotaExhausted(acc *Account) {
	acc.clearQuotaExhausted()
}

// MarkTemporarilyUnavailable skips an account for a short runtime cooldown.
// This is deliberately not persisted; it protects active traffic from a bad
// account without turning transient Notion/network failures into durable state.
func (p *AccountPool) MarkTemporarilyUnavailable(acc *Account, reason string, cooldown time.Duration) {
	if acc == nil {
		return
	}
	if cooldown <= 0 {
		cooldown = defaultAccountFailureCooldown
	}
	now := time.Now()
	until := now.Add(cooldown)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "temporary_failure"
	}
	acc.mu.Lock()
	acc.TemporaryUnavailableUntil = &until
	acc.LastFailureReason = reason
	acc.LastFailureAt = &now
	acc.mu.Unlock()
	log.Printf("[health] temporarily disabled %s for %s (%s)", acc.UserEmail, cooldown, reason)
}

// RecordAuthFailure records a confirmed login authentication failure. A 401
// invalidates every workspace that shares the same Notion login immediately:
// retrying sibling workspaces only repeats the same revoked cookie. The state
// is persisted per workspace so a service restart cannot resurrect the login.
func (p *AccountPool) RecordAuthFailure(acc *Account, _ time.Duration) bool {
	if acc == nil {
		return false
	}
	now := time.Now()
	p.mu.RLock()
	accountsDir := p.accountsDir
	affected := make([]*Account, 0, 1)
	currentFailure := false
	for _, candidate := range p.accounts {
		if candidate == acc && candidate.TokenV2 == acc.TokenV2 {
			currentFailure = true
		}
		sameLogin := candidate == acc
		if !sameLogin {
			sameLogin = candidate.TokenV2 != "" && candidate.TokenV2 == acc.TokenV2
		}
		if sameLogin && candidate != acc && acc.UserID != "" {
			sameLogin = candidate.UserID == acc.UserID
		}
		if sameLogin {
			affected = append(affected, candidate)
		}
	}
	p.mu.RUnlock()
	if !currentFailure {
		log.Printf("[health] ignored stale auth failure for replaced login %s", acc.UserEmail)
		return false
	}

	for _, candidate := range affected {
		candidate.mu.Lock()
		candidate.AuthFailureCount = 1
		candidate.AuthInvalid = true
		candidate.TemporaryUnavailableUntil = nil
		candidate.LastFailureReason = "auth_invalid"
		candidate.LastFailureAt = cloneTimePtr(&now)
		candidate.mu.Unlock()
	}
	log.Printf("[health] marked login %s auth invalid across %d workspace(s)", acc.UserEmail, len(affected))

	if accountsDir != "" {
		for _, candidate := range affected {
			if err := saveAccountFile(accountsDir, candidate); err != nil {
				log.Printf("[health] persist auth-invalid %s workspace=%s failed: %v",
					candidate.UserEmail, candidate.ShortSpaceID(), err)
			}
		}
	}
	return true
}

func (p *AccountPool) ClearTemporaryUnavailable(acc *Account) {
	if acc == nil {
		return
	}
	acc.mu.Lock()
	acc.TemporaryUnavailableUntil = nil
	if !acc.AuthInvalid {
		acc.LastFailureReason = ""
		acc.LastFailureAt = nil
		acc.AuthFailureCount = 0
	}
	acc.mu.Unlock()
}

func (p *AccountPool) isTemporarilyUnavailable(acc *Account) bool {
	health := acc.healthSnapshot()
	return health.TemporaryUnavailableUntil != nil && health.TemporaryUnavailableUntil.After(time.Now())
}

func (p *AccountPool) isAuthInvalid(acc *Account) bool {
	return acc.healthSnapshot().AuthInvalid
}

func (p *AccountPool) isManuallyDisabled(acc *Account) bool {
	return acc.healthSnapshot().ManuallyDisabled
}

func (p *AccountPool) isQuotaExhausted(acc *Account) bool {
	if acc != nil && planIncludesFullNotionAI(acc.planTypeSnapshot()) {
		return false
	}
	quota := acc.quotaSnapshot()
	if quota.PermanentlyExhausted {
		return true
	}
	if quota.Info != nil {
		return !quota.Info.IsEligible
	}
	if quota.ExhaustedAt == nil {
		return false
	}
	return true
}

// hasNoWorkspace returns true only after a probe has confirmed the
// account has zero accessible workspaces. Unprobed accounts (fresh
// registrations, accounts loaded before the probe ever ran) are treated
// as unknown / usable so the pool can still serve traffic on the first
// boot and the next refresh tick promotes/demotes them.
func (p *AccountPool) hasNoWorkspace(acc *Account) bool {
	profile := acc.profileSnapshot()
	return acc != nil && profile.WorkspaceCheckedAt != nil && profile.SpaceCount == 0
}

func (p *AccountPool) hasAIDisabled(acc *Account) bool {
	profile := acc.profileSnapshot()
	return profile.WorkspaceCheckedAt != nil && profile.AIEnabled != nil && !*profile.AIEnabled
}

// isUnusable folds manual disable, quota, workspace, cooldown, and auth
// state into a single "do not select" predicate used by every picker.
func (p *AccountPool) isUnusable(acc *Account) bool {
	return p.isManuallyDisabled(acc) || p.isQuotaExhausted(acc) || p.hasNoWorkspace(acc) || p.hasAIDisabled(acc) || p.isTemporarilyUnavailable(acc) || p.isAuthInvalid(acc)
}

// applyWorkspaceProfile records the latest workspace probe and authoritative
// plan for the currently selected workspace. The selected SpaceID is not
// changed under active traffic; if it disappeared, Count is zero and the
// account is excluded until it is re-imported.
func (p *AccountPool) applyWorkspaceProfile(acc *Account, result WorkspaceProbeResult) (prev int, changed bool, planChanged bool) {
	if acc == nil {
		return 0, false, false
	}
	acc.mu.Lock()
	defer acc.mu.Unlock()
	prev = acc.SpaceCount
	hadCheck := acc.WorkspaceCheckedAt != nil
	oldPlan := acc.PlanType
	now := time.Now()
	acc.SpaceCount = result.Count
	acc.WorkspaceCheckedAt = &now
	if result.AIEnabledKnown {
		enabled := result.AIEnabled
		acc.WorkspaceAIEnabled = &enabled
	}
	if strings.TrimSpace(result.PlanType) != "" {
		acc.PlanType = result.PlanType
	}
	if planIncludesFullNotionAI(acc.PlanType) {
		acc.QuotaExhaustedAt = nil
		acc.PermanentlyExhausted = false
	}
	changed = !hadCheck || prev != result.Count
	planChanged = !strings.EqualFold(strings.TrimSpace(oldPlan), strings.TrimSpace(acc.PlanType))
	return prev, changed, planChanged
}

// quotaApplyResult describes how applyQuotaInfo changed the account state.
// Caller can use it to emit a human-friendly log line.
type quotaApplyResult struct {
	Recovered    bool // was previously exhausted, now eligible
	NowExhausted bool // is currently not eligible
	NowPermanent bool // free-plan account that is now permanently exhausted
	Unlimited    bool // included-AI paid plan; legacy Basic counters are advisory only
	BasicLeft    int  // basic remaining after the update (0 if info nil)
	HasPremium   bool
}

// applyQuotaInfo records the latest quota check result for the account and
// updates exhausted/permanent state accordingly. Caller must NOT hold acc.mu.
// The returned snapshot describes what changed so the caller can log the
// transition without re-locking.
func (p *AccountPool) applyQuotaInfo(acc *Account, info *QuotaInfo) quotaApplyResult {
	acc.mu.Lock()
	defer acc.mu.Unlock()
	res := quotaApplyResult{}
	now := time.Now()
	acc.QuotaInfo = cloneQuotaInfo(info)
	acc.QuotaCheckedAt = &now
	if info == nil {
		return res
	}
	res.BasicLeft = basicRemaining(info)
	res.HasPremium = info.HasPremium
	res.Unlimited = planIncludesFullNotionAI(acc.PlanType)
	if info.IsEligible || res.Unlimited {
		if acc.QuotaExhaustedAt != nil {
			res.Recovered = true
		}
		acc.QuotaExhaustedAt = nil
		acc.PermanentlyExhausted = false
		// Quota eligibility is not proof that inference authentication works:
		// Notion can keep this endpoint available while the AI endpoint returns
		// 401. Preserve a confirmed auth-invalid state until new credentials
		// are imported.
		if !acc.AuthInvalid {
			acc.TemporaryUnavailableUntil = nil
			acc.LastFailureReason = ""
			acc.LastFailureAt = nil
			acc.AuthFailureCount = 0
		}
		return res
	}
	res.NowExhausted = true
	if acc.QuotaExhaustedAt == nil {
		acc.QuotaExhaustedAt = &now
	}
	isTrial := isComplimentaryPlanType(acc.PlanType) || strings.TrimSpace(acc.PlanType) == ""
	if isTrial {
		acc.PermanentlyExhausted = true
		res.NowPermanent = true
	}
	return res
}

func (p *AccountPool) nextQuotaGeneration(acc *Account) uint64 {
	p.liveQuotaMu.Lock()
	defer p.liveQuotaMu.Unlock()
	if p.quotaGeneration == nil {
		p.quotaGeneration = make(map[*Account]uint64)
	}
	p.quotaGeneration[acc]++
	return p.quotaGeneration[acc]
}

func (p *AccountPool) applyQuotaInfoIfCurrent(acc *Account, info *QuotaInfo, generation uint64) (quotaApplyResult, bool) {
	p.liveQuotaMu.Lock()
	defer p.liveQuotaMu.Unlock()
	if p.quotaApplied == nil {
		p.quotaApplied = make(map[*Account]uint64)
	}
	if generation <= p.quotaApplied[acc] {
		return quotaApplyResult{}, false
	}
	p.quotaApplied[acc] = generation
	return p.applyQuotaInfo(acc, info), true
}

func (p *AccountPool) beginRoutingQuotaFlight(acc *Account) (*quotaRefreshFlight, bool) {
	p.liveQuotaMu.Lock()
	defer p.liveQuotaMu.Unlock()
	if flight := p.liveQuotaFlights[acc]; flight != nil {
		return flight, false
	}
	if p.liveQuotaFlights == nil {
		p.liveQuotaFlights = make(map[*Account]*quotaRefreshFlight)
	}
	if p.quotaGeneration == nil {
		p.quotaGeneration = make(map[*Account]uint64)
	}
	p.quotaGeneration[acc]++
	flight := &quotaRefreshFlight{
		done:       make(chan struct{}),
		generation: p.quotaGeneration[acc],
	}
	p.liveQuotaFlights[acc] = flight
	return flight, true
}

func (p *AccountPool) finishRoutingQuotaFlight(acc *Account, flight *quotaRefreshFlight, info *QuotaInfo, err error) {
	p.liveQuotaMu.Lock()
	flight.err = err
	if err == nil && info != nil {
		flight.eligible = info.IsEligible
		flight.hasResult = true
	}
	if p.liveQuotaFlights[acc] == flight {
		delete(p.liveQuotaFlights, acc)
	}
	close(flight.done)
	p.liveQuotaMu.Unlock()
}

// refreshAccountQuotaNow performs an authoritative V1 routing check. V2
// diagnostics are deliberately excluded: a private dashboard-only endpoint
// must never delay a user request. The returned error lets destructive callers
// distinguish a confirmed exhausted account from a failed re-check.
func (p *AccountPool) refreshAccountQuotaNow(acc *Account) (bool, error) {
	if acc == nil {
		return false, fmt.Errorf("nil account")
	}
	if planIncludesFullNotionAI(acc.planTypeSnapshot()) {
		return true, nil
	}

	flight, leader := p.beginRoutingQuotaFlight(acc)
	if !leader {
		<-flight.done
		if flight.err == nil && flight.hasResult {
			return flight.eligible, nil
		}
		quota := acc.quotaSnapshot()
		if flight.err != nil {
			if quota.Info != nil {
				return quota.Info.IsEligible, flight.err
			}
			return !p.isQuotaExhausted(acc), flight.err
		}
		if quota.Info != nil {
			return quota.Info.IsEligible, nil
		}
		return !p.isQuotaExhausted(acc), nil
	}

	info, err := routingQuotaFetcher(acc)
	if err == nil {
		_, _ = p.applyQuotaInfoIfCurrent(acc, info, flight.generation)
	}
	p.finishRoutingQuotaFlight(acc, flight, info, err)
	if err != nil {
		quota := acc.quotaSnapshot()
		if quota.Info != nil {
			return quota.Info.IsEligible, err
		}
		return !p.isQuotaExhausted(acc), err
	}
	return info != nil && info.IsEligible, nil
}

// RefreshAccountQuota performs a live quota check for a single account.
//
//   - When minInterval > 0 and the cached quota was checked recently, returns
//     the cached eligibility without making an HTTP call. This avoids hammering
//     the Notion quota API on retry loops.
//   - Updates the account's QuotaInfo / QuotaExhaustedAt / PermanentlyExhausted
//     fields atomically based on the live result.
//
// Returns true when the account is currently eligible to serve traffic.
func (p *AccountPool) RefreshAccountQuota(acc *Account, minInterval time.Duration) bool {
	if acc == nil {
		return false
	}
	// Included-AI workspace plans are not governed by the legacy Basic
	// eligibility counters. Do not make a user request wait for diagnostic
	// quota endpoints that cannot change the routing decision.
	if planIncludesFullNotionAI(acc.planTypeSnapshot()) {
		return true
	}
	// Cached fast path: avoid hammering Notion on tight retry loops.
	quota := acc.quotaSnapshot()
	if minInterval > 0 && quota.CheckedAt != nil && time.Since(*quota.CheckedAt) < minInterval {
		if quota.Info != nil {
			return quota.Info.IsEligible
		}
		return !p.isQuotaExhaustedRLock(acc)
	}
	eligible, err := p.refreshAccountQuotaNow(acc)
	if err != nil {
		log.Printf("[quota-live] %s check failed: %v (using cached state)", acc.UserEmail, err)
		return eligible
	}
	quota = acc.quotaSnapshot()
	if quota.Info != nil {
		if eligible {
			log.Printf("[quota-live] %s eligible (basic remaining ~%d)", acc.UserEmail, basicRemaining(quota.Info))
		} else {
			log.Printf("[quota-live] %s NOT eligible — disabled (space %d/%d, user %d/%d)",
				acc.UserEmail, quota.Info.SpaceUsage, quota.Info.SpaceLimit, quota.Info.UserUsage, quota.Info.UserLimit)
		}
	}
	return eligible
}

// RefreshAccountQuotaAsync triggers a live quota check in the background.
// Used to refresh the cached quota after a successful inference call so the
// next selection sees up-to-date numbers without blocking the user request.
// Concurrent calls for the same account are deduplicated.
func (p *AccountPool) RefreshAccountQuotaAsync(acc *Account) {
	if acc == nil {
		return
	}
	if planIncludesFullNotionAI(acc.planTypeSnapshot()) {
		return
	}

	go func() {
		eligible, err := p.refreshAccountQuotaNow(acc)
		if err != nil {
			log.Printf("[quota-live-async] %s check failed: %v", acc.UserEmail, err)
			return
		}
		if !eligible {
			log.Printf("[quota-live-async] %s NOT eligible", acc.UserEmail)
		}
	}()
}

// isQuotaExhaustedRLock is a read-locked variant of isQuotaExhausted for
// callers that hold no lock yet.
func (p *AccountPool) isQuotaExhaustedRLock(acc *Account) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.isQuotaExhausted(acc)
}

func (p *AccountPool) removeUniqueMatchingAccount(match func(*Account) bool) bool {
	p.mu.Lock()
	matchIndex := -1
	for i, a := range p.accounts {
		if match(a) {
			if matchIndex >= 0 {
				p.mu.Unlock()
				return false
			}
			matchIndex = i
		}
	}
	if matchIndex < 0 {
		p.mu.Unlock()
		return false
	}
	removed := p.accounts[matchIndex]
	p.accounts = append(p.accounts[:matchIndex], p.accounts[matchIndex+1:]...)
	remaining := append([]*Account(nil), p.accounts...)
	p.mu.Unlock()

	p.liveQuotaMu.Lock()
	delete(p.liveQuotaFlights, removed)
	delete(p.quotaGeneration, removed)
	delete(p.quotaApplied, removed)
	p.liveQuotaMu.Unlock()
	rebuildDynamicModelMap(remaining)
	globalSessionManager.DeleteByAccountID(removed.AccountID, removed.UserEmail)
	return true
}

// RemoveAccountByAccountID drops exactly one workspace profile from the
// in-memory pool. Disk deletion remains the caller's responsibility.
func (p *AccountPool) RemoveAccountByAccountID(accountID string) bool {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return false
	}
	return p.removeUniqueMatchingAccount(func(acc *Account) bool {
		acc.EnsureAccountID()
		return acc.AccountID == accountID
	})
}

// RemoveAccountByAccountIDIfToken atomically removes an exact live identity
// only when it is still backed by expectedToken. A concurrent re-login may
// replace the token after disk deletion; that replacement must remain live.
func (p *AccountPool) RemoveAccountByAccountIDIfToken(accountID, expectedToken string) bool {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return false
	}
	return p.removeUniqueMatchingAccount(func(acc *Account) bool {
		acc.EnsureAccountID()
		if acc.AccountID != accountID {
			return false
		}
		return expectedToken == "" || acc.TokenV2 == expectedToken
	})
}

// RemoveAccountByEmail drops the in-memory pool entry whose user_email
// matches (case-insensitive). Does NOT touch disk; callers are responsible
// for the file lifecycle (used by the dashboard delete endpoint).
// Ambiguous same-email profiles are refused rather than guessed.
func (p *AccountPool) RemoveAccountByEmail(email string) bool {
	return p.removeUniqueMatchingAccount(func(acc *Account) bool {
		return strings.EqualFold(acc.UserEmail, email)
	})
}

func (p *AccountPool) RemoveAccountByEmailIfToken(email, expectedToken string) bool {
	return p.removeUniqueMatchingAccount(func(acc *Account) bool {
		return strings.EqualFold(acc.UserEmail, email) &&
			(expectedToken == "" || acc.TokenV2 == expectedToken)
	})
}

// AvailableCount returns the number of accounts the pool can currently
// route traffic to (i.e. quota is healthy AND the workspace probe didn't
// flag the account as having zero accessible workspaces).
func (p *AccountPool) AvailableCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	count := 0
	for _, acc := range p.accounts {
		if !p.isUnusable(acc) {
			count++
		}
	}
	return count
}

func (p *AccountPool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.accounts)
}

// GetByEmail returns an available (non-exhausted, has workspace) account
// by email, or nil if not found / exhausted / has no workspace.
func (p *AccountPool) GetByEmail(email string) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, acc := range p.accounts {
		if strings.EqualFold(acc.UserEmail, email) && !p.isUnusable(acc) {
			return acc
		}
	}
	return nil
}

// FindByEmail returns the unique account matching the given email.
// Returns AmbiguousEmailError if multiple workspaces share the same email.
func (p *AccountPool) FindByEmail(email string) (*Account, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var matches []*Account
	for _, acc := range p.accounts {
		if strings.EqualFold(acc.UserEmail, email) {
			matches = append(matches, acc)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no account found for email %s", email)
	case 1:
		return matches[0], nil
	default:
		return nil, &AmbiguousEmailError{Email: email, Count: len(matches)}
	}
}

// FindByAccountID returns the account with the given AccountID, or nil.
func (p *AccountPool) FindByAccountID(id string) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, acc := range p.accounts {
		acc.EnsureAccountID()
		if acc.AccountID == id {
			return acc
		}
	}
	return nil
}

// FindUsableByAccountID returns an exact workspace only when normal routing
// would also consider it usable.
func (p *AccountPool) FindUsableByAccountID(id string) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, acc := range p.accounts {
		acc.EnsureAccountID()
		if acc.AccountID == id && !p.isUnusable(acc) {
			return acc
		}
	}
	return nil
}

// HasNoWorkspace returns true when the account has been probed and found
// to have zero accessible workspaces. Used by handlers that need to
// distinguish "no such account" from "account exists but is broken".
func (p *AccountPool) HasNoWorkspace(acc *Account) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.hasNoWorkspace(acc)
}

func (p *AccountPool) AllModels() []ModelEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	seen := map[string]bool{}
	var models []ModelEntry
	for _, acc := range p.accounts {
		for _, m := range acc.modelsSnapshot() {
			if !seen[m.ID] {
				seen[m.ID] = true
				models = append(models, m)
			}
		}
	}
	if len(models) == 0 {
		// Return default models if none loaded
		for name, id := range SnapshotModelMap() {
			models = append(models, ModelEntry{Name: name, ID: id})
		}
	}
	return models
}

// GetRefreshStatus returns the current refresh state for the API
func (p *AccountPool) GetRefreshStatus() map[string]interface{} {
	p.refreshMu.RLock()
	defer p.refreshMu.RUnlock()
	status := map[string]interface{}{
		"refreshing": p.refreshing,
		"done":       p.refreshDone,
		"total":      p.refreshTotal,
		"failed":     p.refreshFailed,
	}
	if p.lastRefreshAt != nil {
		status["last_refresh_at"] = p.lastRefreshAt.Format(time.RFC3339)
	}
	return status
}

// TriggerRefresh starts RefreshAll in a background goroutine if not already running.
// Returns true if a new refresh was started, false if one is already in progress.
func (p *AccountPool) TriggerRefresh(accountsDir string) bool {
	releaseMutation, ok := p.beginRefresh()
	if !ok {
		return false
	}
	go p.refreshAll(accountsDir, releaseMutation)
	return true
}

// RefreshAll proactively checks AI quota and fetches models for all accounts via Notion API.
// It also persists updated info back to account JSON files.
func (p *AccountPool) RefreshAll(accountsDir string) {
	releaseMutation, ok := p.beginRefresh()
	if !ok {
		return
	}
	p.refreshAll(accountsDir, releaseMutation)
}

func (p *AccountPool) beginRefresh() (func(), bool) {
	releaseMutation, ok := p.beginAccountMutation()
	if !ok {
		log.Printf("[refresh] skipped: %s", accountRestoreConflictMessage)
		return nil, false
	}
	p.refreshMu.Lock()
	if p.refreshing {
		p.refreshMu.Unlock()
		releaseMutation()
		return nil, false
	}
	p.refreshing = true
	p.refreshDone = 0
	p.refreshFailed = 0
	p.refreshMu.Unlock()
	return releaseMutation, true
}

func (p *AccountPool) recordRefreshAuthFailure(acc *Account, err error) bool {
	if acc == nil || err == nil {
		return false
	}
	if failure := classifyAccountAttemptError(err); failure.Reason == "auth_error" {
		if !p.isAuthInvalid(acc) {
			p.RecordAuthFailure(acc, 0)
		}
		return true
	}
	return false
}

func (p *AccountPool) refreshAll(accountsDir string, releaseMutation func()) {
	defer releaseMutation()
	defer func() {
		p.refreshMu.Lock()
		p.refreshing = false
		now := time.Now()
		p.lastRefreshAt = &now
		p.refreshMu.Unlock()
	}()

	p.mu.RLock()
	accs := make([]*Account, len(p.accounts))
	copy(accs, p.accounts)
	p.mu.RUnlock()

	p.refreshMu.Lock()
	p.refreshTotal = len(accs)
	p.refreshMu.Unlock()

	concurrency := 10
	if AppConfig != nil && AppConfig.Refresh.Concurrency > 0 {
		concurrency = AppConfig.Refresh.Concurrency
	}
	log.Printf("[refresh] refreshing %d accounts (quota + models, concurrency=%d)...", len(accs), concurrency)

	var (
		disabledNow   atomic.Int64
		recoveredNow  atomic.Int64
		workspaceLost atomic.Int64
	)
	var wg sync.WaitGroup
	sem := make(chan struct{}, concurrency)

	for _, acc := range accs {
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore slot
		go func(acc *Account) {
			defer wg.Done()
			defer func() { <-sem }() // release semaphore slot
			checkFailed := false
			defer func() {
				p.refreshMu.Lock()
				p.refreshDone++
				if checkFailed {
					p.refreshFailed++
				}
				p.refreshMu.Unlock()
			}()
			if p.isAuthInvalid(acc) {
				return
			}

			// 1. Refresh workspace accessibility and plan first. Quota policy
			// must use the current plan, not the value captured at import time.
			workspace, err := workspaceProbe(acc)
			if err != nil {
				checkFailed = true
				log.Printf("[refresh] %s (%s): workspace probe failed: %v", acc.UserName, acc.UserEmail, err)
				if p.recordRefreshAuthFailure(acc, err) {
					return
				}
			} else {
				prev, changed, planChanged := p.applyWorkspaceProfile(acc, workspace)
				if planChanged {
					log.Printf("[refresh] %s (%s): plan refreshed to %s", acc.UserName, acc.UserEmail, acc.planTypeSnapshot())
				}
				switch {
				case workspace.Count == 0 && (changed || prev == 0):
					log.Printf("[refresh] %s (%s): NO WORKSPACE — excluded from pool (selected workspace is no longer accessible)", acc.UserName, acc.UserEmail)
					workspaceLost.Add(1)
				case workspace.Count > 0 && changed:
					log.Printf("[refresh] %s (%s): %d workspace(s) accessible", acc.UserName, acc.UserEmail, workspace.Count)
				}
			}

			// 2. Check quota. Generations ensure that a slower older response
			// cannot overwrite a newer live-routing result.
			quotaGeneration := p.nextQuotaGeneration(acc)
			info, err := quotaFetcher(acc)
			if err != nil {
				checkFailed = true
				log.Printf("[refresh] %s (%s): quota check failed: %v", acc.UserName, acc.UserEmail, err)
				if p.recordRefreshAuthFailure(acc, err) {
					return
				}
			} else {
				res, applied := p.applyQuotaInfoIfCurrent(acc, info, quotaGeneration)
				if !applied {
					log.Printf("[refresh] %s (%s): discarded stale quota response", acc.UserName, acc.UserEmail)
				} else {
					premiumInfo := ""
					if info.HasPremium {
						premiumInfo = fmt.Sprintf(", premium %d/%d", info.PremiumUsage, info.PremiumLimit)
					}
					if info.ResearchModeUsage > 0 {
						premiumInfo += fmt.Sprintf(", research=%d", info.ResearchModeUsage)
					}
					switch {
					case res.Unlimited:
						log.Printf("[refresh] %s (%s): included-AI plan — legacy Basic %d/%d ignored (current unlimited mode%s)",
							acc.UserName, acc.UserEmail, info.SpaceUsage, info.SpaceLimit, premiumInfo)
					case res.NowPermanent:
						log.Printf("[refresh] %s (%s): NOT eligible — disabled permanently (free plan, space %d/%d, user %d/%d)",
							acc.UserName, acc.UserEmail, info.SpaceUsage, info.SpaceLimit, info.UserUsage, info.UserLimit)
						disabledNow.Add(1)
					case res.NowExhausted:
						log.Printf("[refresh] %s (%s): NOT eligible — disabled (space %d/%d, user %d/%d%s)",
							acc.UserName, acc.UserEmail, info.SpaceUsage, info.SpaceLimit, info.UserUsage, info.UserLimit, premiumInfo)
						disabledNow.Add(1)
					case res.Recovered:
						log.Printf("[refresh] %s (%s): RECOVERED (space %d/%d, user %d/%d, remaining ~%d%s)",
							acc.UserName, acc.UserEmail, info.SpaceUsage, info.SpaceLimit, info.UserUsage, info.UserLimit, res.BasicLeft, premiumInfo)
						recoveredNow.Add(1)
					default:
						log.Printf("[refresh] %s (%s): eligible (space %d/%d, user %d/%d, remaining ~%d%s)",
							acc.UserName, acc.UserEmail, info.SpaceUsage, info.SpaceLimit, info.UserUsage, info.UserLimit, res.BasicLeft, premiumInfo)
					}
				}
			}

			// 3. Fetch models
			models, err := modelsFetcher(acc)
			if err != nil {
				checkFailed = true
				log.Printf("[refresh] %s (%s): model fetch failed: %v", acc.UserName, acc.UserEmail, err)
				if p.recordRefreshAuthFailure(acc, err) {
					return
				}
			} else if len(models) > 0 {
				acc.setModels(models)
				log.Printf("[refresh] %s (%s): fetched %d models", acc.UserName, acc.UserEmail, len(models))
			}
		}(acc)
	}
	wg.Wait()

	// 3. Persist to disk
	if accountsDir != "" {
		p.SaveAccounts(accountsDir)
	}

	available := p.AvailableCount()
	log.Printf("[refresh] complete: %d/%d available, disabled=%d, recovered=%d, no_workspace=%d, check_errors=%d",
		available, len(accs), disabledNow.Load(), recoveredNow.Load(), workspaceLost.Load(), p.GetRefreshStatus()["failed"])
}

// normalizeModelName converts display name like "GPT-5.2" to a user-friendly alias like "gpt-5.2"
func normalizeModelName(displayName string) string {
	s := strings.ToLower(strings.TrimSpace(displayName))
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

func registerModelEntries(models []ModelEntry) {
	for _, model := range models {
		normalizedName := normalizeModelName(model.Name)
		if normalizedName != "" && strings.TrimSpace(model.ID) != "" {
			SetModelID(normalizedName, model.ID)
		}
	}
}

func publicFacingModelID(displayName, internalID string) string {
	name := normalizeModelName(displayName)
	if name == "" {
		name = friendlyModelNameByInternalID(internalID)
	}
	if name == "" {
		return ""
	}
	if isClaudeModel(displayName, internalID, "") || isClaudeFamilyName(name) {
		return ensureClaudeModelID(name)
	}
	return name
}

func displayModelName(displayName, internalID, family string) string {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = displayNameFromPublicModelID(friendlyModelNameByInternalID(internalID))
	}
	if name == "" {
		name = strings.TrimSpace(internalID)
	}
	if name == "" {
		return ""
	}
	if isClaudeModel(name, internalID, family) && !hasClaudeDisplayPrefix(name) {
		return "Claude " + name
	}
	return name
}

func ensureClaudeModelID(name string) string {
	normalized := normalizeModelName(name)
	if normalized == "" {
		return ""
	}
	base := strings.TrimPrefix(normalized, "claude-")
	base = strings.TrimPrefix(base, "anthropic-")
	if isClaudeFamilyName(base) {
		return "claude-" + base
	}
	return normalized
}

func displayNameFromPublicModelID(modelID string) string {
	switch strings.ToLower(strings.TrimSpace(modelID)) {
	case "claude-opus-4.6", "opus-4.6":
		return "Opus 4.6"
	case "claude-sonnet-4.6", "sonnet-4.6":
		return "Sonnet 4.6"
	case "claude-haiku-4.5", "haiku-4.5":
		return "Haiku 4.5"
	default:
		return strings.TrimSpace(modelID)
	}
}

func hasClaudeDisplayPrefix(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(normalized, "claude ") || strings.HasPrefix(normalized, "claude-")
}

func isClaudeModel(displayName, internalID, family string) bool {
	normalizedFamily := strings.ToLower(strings.TrimSpace(family))
	if strings.Contains(normalizedFamily, "anthropic") || strings.Contains(normalizedFamily, "claude") {
		return true
	}
	if isClaudeFamilyName(normalizeModelName(displayName)) {
		return true
	}
	return isClaudeInternalID(internalID)
}

func isClaudeFamilyName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.TrimPrefix(normalized, "claude-")
	normalized = strings.TrimPrefix(normalized, "anthropic-")
	return normalized == "opus" || strings.HasPrefix(normalized, "opus-") ||
		normalized == "sonnet" || strings.HasPrefix(normalized, "sonnet-") ||
		normalized == "haiku" || strings.HasPrefix(normalized, "haiku-")
}

func isClaudeInternalID(id string) bool {
	normalized := strings.ToLower(strings.TrimSpace(id))
	switch normalized {
	case "avocado-froyo-medium", "almond-croissant-low", "anthropic-haiku-4.5":
		return true
	default:
		return strings.HasPrefix(normalized, "anthropic-") && isClaudeFamilyName(normalized)
	}
}

// SaveAccounts persists current account state (models, quota) back to JSON files.
// Each account is matched to its disk file by account_id (not email).
// Missing files are not recreated: a concurrent delete must never be undone
// by a stale SaveAccounts snapshot.
func (p *AccountPool) SaveAccounts(dir string) {
	p.mu.RLock()
	accs := make([]*Account, len(p.accounts))
	copy(accs, p.accounts)
	p.mu.RUnlock()

	for _, acc := range accs {
		acc.EnsureAccountID()
		if err := saveAccountFile(dir, acc); err != nil {
			log.Printf("[account] save %s workspace=%s failed: %v",
				acc.UserEmail, acc.ShortSpaceID(), err)
		}
	}
}

// saveAccountFile rewrites a single account's JSON file so it carries the
// freshest quota_info / quota_checked_at / available_models, while
// preserving every other field (token, ids, browser/device, registered_via).
//
// The write is serialized per account path and atomic (unique temp file +
// os.Rename). The Account snapshot is captured under acc.mu, so concurrent
// quota/model/profile refreshes cannot race with persistence.
//
// Returns an error if no on-disk file matches acc.UserEmail; that is a
// real-world signal someone deleted the account file out from under us.
func saveAccountFile(dir string, acc *Account) error {
	if acc == nil {
		return fmt.Errorf("saveAccountFile: nil account")
	}
	if dir == "" {
		return fmt.Errorf("saveAccountFile: empty dir")
	}
	acc.EnsureAccountID()
	accountID := acc.AccountID
	unlockDirectory, err := accountstore.LockDirectory(dir)
	if err != nil {
		return err
	}
	defer unlockDirectory()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir: %w", err)
	}
	var matchPath string
	var existing map[string]interface{}
	matchCount := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}
		if accountID != "" {
			if accountIDFromRaw(raw) != accountID {
				continue
			}
		} else {
			email, _ := raw["user_email"].(string)
			token, _ := raw["token_v2"].(string)
			if !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(acc.UserEmail)) ||
				(acc.TokenV2 != "" && token != acc.TokenV2) {
				continue
			}
		}
		matchPath = path
		existing = raw
		matchCount++
	}
	if matchPath == "" {
		return fmt.Errorf("no account file matches identity for %s", acc.UserEmail)
	}
	if matchCount > 1 {
		return &AmbiguousEmailError{Email: acc.UserEmail, Count: matchCount}
	}

	unlockFile, err := lockAccountFilePath(matchPath)
	if err != nil {
		return err
	}
	defer unlockFile()

	// Re-read after acquiring the path lock so concurrent writers cannot lose
	// fields that were committed while this caller was locating the file.
	latest, err := os.ReadFile(matchPath)
	if err != nil {
		return fmt.Errorf("read account file: %w", err)
	}
	if err := json.Unmarshal(latest, &existing); err != nil {
		return fmt.Errorf("parse account file: %w", err)
	}
	if accountID != "" {
		if accountIDFromRaw(existing) != accountID {
			return fmt.Errorf("account identity changed while saving %s", acc.UserEmail)
		}
		if latestToken, _ := existing["token_v2"].(string); acc.TokenV2 != "" && latestToken != acc.TokenV2 {
			return fmt.Errorf("account credential changed while saving %s", acc.UserEmail)
		}
		// Migrate legacy files only after the locked re-read so the new field
		// is not lost and a same-path replacement cannot inherit another
		// account's refreshed state.
		existing["account_id"] = accountID
	} else {
		email, _ := existing["user_email"].(string)
		token, _ := existing["token_v2"].(string)
		if !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(acc.UserEmail)) ||
			(acc.TokenV2 != "" && token != acc.TokenV2) {
			return fmt.Errorf("account identity changed while saving %s", acc.UserEmail)
		}
	}

	state := acc.persistSnapshot()
	if len(state.Models) > 0 {
		modelEntries := make([]map[string]string, 0, len(state.Models))
		for _, m := range state.Models {
			modelEntries = append(modelEntries, map[string]string{"id": m.ID, "name": m.Name})
		}
		existing["available_models"] = modelEntries
	}
	if state.Quota.Info != nil {
		existing["quota_info"] = map[string]interface{}{
			"is_eligible":         state.Quota.Info.IsEligible,
			"space_usage":         state.Quota.Info.SpaceUsage,
			"space_limit":         state.Quota.Info.SpaceLimit,
			"user_usage":          state.Quota.Info.UserUsage,
			"user_limit":          state.Quota.Info.UserLimit,
			"last_usage_at":       state.Quota.Info.LastUsageAtMs,
			"research_mode_usage": state.Quota.Info.ResearchModeUsage,
			"has_premium":         state.Quota.Info.HasPremium,
			"premium_balance":     state.Quota.Info.PremiumBalance,
			"premium_usage":       state.Quota.Info.PremiumUsage,
			"premium_limit":       state.Quota.Info.PremiumLimit,
		}
	}
	if state.Quota.CheckedAt != nil {
		existing["quota_checked_at"] = state.Quota.CheckedAt.Format(time.RFC3339)
	}
	existing["plan_type"] = state.Profile.PlanType
	writePersonalInstructionsState(existing, state.PersonalInstructions)
	writeManualDisabledState(existing, state.Health.ManuallyDisabled)
	writePersistedHealthState(existing, state.Health)
	if state.Profile.WorkspaceCheckedAt != nil {
		existing["space_count"] = state.Profile.SpaceCount
		existing["workspace_checked_at"] = state.Profile.WorkspaceCheckedAt.Format(time.RFC3339)
		if state.Profile.AIEnabled != nil {
			existing["workspace_ai_enabled"] = *state.Profile.AIEnabled
		} else {
			delete(existing, "workspace_ai_enabled")
		}
	}

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	out = append(out, '\n')
	tmpFile, err := os.CreateTemp(filepath.Dir(matchPath), "."+filepath.Base(matchPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmp := tmpFile.Name()
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmp)
		}
	}()
	if err := tmpFile.Chmod(0o600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("chmod tmp: %w", err)
	}
	if _, err := tmpFile.Write(out); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, matchPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	cleanupTmp = false
	return nil
}

// RefreshAndPersistAccount runs a live quota + models check for a single
// account and persists the result to disk. Designed for one-off refreshes
// (e.g. immediately after a successful registration) so the dashboard
// sees real numbers without waiting for the next global refresh tick.
//
// Errors:
//   - email is unknown to the pool (no Account loaded yet)
//   - quota fetch failed (we don't persist nothing)
//   - ctx was already cancelled
//
// A models-fetch failure is logged but not returned, since quota is the
// higher-value half of the snapshot.
func (p *AccountPool) RefreshAndPersistAccount(ctx context.Context, accountsDir, email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return fmt.Errorf("RefreshAndPersistAccount: empty email")
	}
	acc, err := p.FindByEmail(email)
	if err != nil {
		return err
	}
	return p.refreshAndPersistResolvedAccount(ctx, accountsDir, acc)
}

// RefreshAndPersistAccountByID refreshes one exact workspace profile. This is
// the required path after registration because one email may own multiple
// workspaces.
func (p *AccountPool) RefreshAndPersistAccountByID(ctx context.Context, accountsDir, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return fmt.Errorf("RefreshAndPersistAccountByID: empty account_id")
	}
	acc := p.FindByAccountID(accountID)
	if acc == nil {
		return fmt.Errorf("account not in pool: %s", accountID)
	}
	return p.refreshAndPersistResolvedAccount(ctx, accountsDir, acc)
}

func (p *AccountPool) refreshAndPersistResolvedAccount(ctx context.Context, accountsDir string, acc *Account) error {
	releaseMutation, ok := p.beginAccountMutation()
	if !ok {
		return fmt.Errorf("%s", accountRestoreConflictMessage)
	}
	defer releaseMutation()

	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if acc == nil {
		return fmt.Errorf("account not in pool")
	}

	quotaGeneration := p.nextQuotaGeneration(acc)
	info, err := quotaFetcher(acc)
	if err != nil {
		return fmt.Errorf("quota check: %w", err)
	}
	if _, applied := p.applyQuotaInfoIfCurrent(acc, info, quotaGeneration); !applied {
		log.Printf("[post-register] %s: discarded stale quota response", acc.UserEmail)
	}

	models, mErr := modelsFetcher(acc)
	if mErr != nil {
		log.Printf("[post-register] %s: models fetch failed: %v (persisting quota only)", acc.UserEmail, mErr)
	} else if len(models) > 0 {
		acc.setModels(models)
	}

	// Workspace probe is best-effort. If it fails we still persist the
	// quota so the dashboard sees real numbers; the next /admin/refresh
	// retry will re-probe.
	if workspace, wErr := workspaceProbe(acc); wErr != nil {
		log.Printf("[post-register] %s: workspace probe failed: %v", acc.UserEmail, wErr)
	} else {
		p.applyWorkspaceProfile(acc, workspace)
		if workspace.Count == 0 {
			log.Printf("[post-register] %s: NO WORKSPACE detected immediately after registration — account will be excluded from the pool", acc.UserEmail)
		}
	}

	if accountsDir == "" {
		return nil
	}
	if err := saveAccountFile(accountsDir, acc); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	profile := acc.profileSnapshot()
	log.Printf("[post-register] %s: quota refreshed and persisted (eligible=%v, space %d/%d, workspaces=%d)",
		acc.UserEmail, info.IsEligible, info.SpaceUsage, info.SpaceLimit, profile.SpaceCount)
	return nil
}

// StartRefreshLoop runs a background goroutine that periodically refreshes all accounts
func (p *AccountPool) StartRefreshLoop(interval time.Duration, accountsDir string) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			p.RefreshAll(accountsDir)
		}
	}()
}

// GetAccountDetails returns detailed info for all accounts (for admin dashboard)
func (p *AccountPool) GetAccountDetails() []map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var details []map[string]interface{}
	for _, acc := range p.accounts {
		quota := acc.quotaSnapshot()
		health := acc.healthSnapshot()
		profile := acc.profileSnapshot()
		aiDisabled := profile.AIEnabled != nil && !*profile.AIEnabled
		unlimited := planIncludesFullNotionAI(profile.PlanType) && !aiDisabled
		models := acc.modelsSnapshot()
		personalInstructions := acc.personalInstructionsSnapshot()
		acc.EnsureAccountID()
		entry := map[string]interface{}{
			"account_id":      acc.AccountID,
			"login_id":        accountstore.ComputeLoginID(acc.UserID),
			"email":           acc.UserEmail,
			"name":            acc.UserName,
			"plan":            profile.PlanType,
			"space":           acc.SpaceName,
			"space_id_short":  acc.ShortSpaceID(),
			"exhausted":       p.isQuotaExhausted(acc),
			"permanent":       quota.PermanentlyExhausted && !unlimited,
			"no_workspace":    p.hasNoWorkspace(acc),
			"ai_disabled":     aiDisabled,
			"disabled":        health.ManuallyDisabled,
			"quota_unlimited": unlimited,
			// token_v2 is exposed only behind dashboard auth (the caller of
			// HandleAdminAccounts already gates on session). The dashboard
			// shows a "copy token" action and uses it for nothing else.
			"token_v2": acc.TokenV2,
		}
		if health.TemporaryUnavailableUntil != nil {
			entry["temporarily_unavailable"] = health.TemporaryUnavailableUntil.After(time.Now())
			entry["unavailable_until"] = health.TemporaryUnavailableUntil.Format(time.RFC3339)
		}
		if health.LastFailureReason != "" {
			entry["last_failure_reason"] = health.LastFailureReason
		}
		if health.LastFailureAt != nil {
			entry["last_failure_at"] = health.LastFailureAt.Format(time.RFC3339)
		}
		if health.AuthInvalid {
			entry["auth_invalid"] = true
		}
		if health.AuthFailureCount > 0 {
			entry["auth_failures"] = health.AuthFailureCount
		}
		if profile.WorkspaceCheckedAt != nil {
			entry["space_count"] = profile.SpaceCount
			entry["workspace_checked_at"] = profile.WorkspaceCheckedAt.Format(time.RFC3339)
		}
		if acc.RegisteredVia != "" {
			entry["registered_via"] = acc.RegisteredVia
		}
		if personalInstructions.Configured != nil {
			entry["personal_instructions_configured"] = *personalInstructions.Configured
		}
		if personalInstructions.CheckedAt != nil {
			entry["personal_instructions_checked_at"] = personalInstructions.CheckedAt.Format(time.RFC3339)
		}
		if personalInstructions.Error != "" {
			entry["personal_instructions_check_error"] = personalInstructions.Error
		}
		if quota.Info != nil {
			entry["eligible"] = quota.Info.IsEligible
			entry["usage"] = quota.Info.SpaceUsage
			entry["limit"] = quota.Info.SpaceLimit
			entry["space_usage"] = quota.Info.SpaceUsage
			entry["space_limit"] = quota.Info.SpaceLimit
			entry["space_remaining"] = quotaRemaining(quota.Info.SpaceLimit, quota.Info.SpaceUsage)
			entry["user_usage"] = quota.Info.UserUsage
			entry["user_limit"] = quota.Info.UserLimit
			entry["user_remaining"] = quotaRemaining(quota.Info.UserLimit, quota.Info.UserUsage)
			entry["remaining"] = basicRemaining(quota.Info)
			entry["last_usage_at"] = quota.Info.LastUsageAtMs
			// Research mode (V1)
			entry["research_usage"] = quota.Info.ResearchModeUsage
			// Premium credit data (V2)
			entry["has_premium"] = quota.Info.HasPremium
			entry["premium_balance"] = quota.Info.PremiumBalance
			entry["premium_usage"] = quota.Info.PremiumUsage
			entry["premium_limit"] = quota.Info.PremiumLimit
		}
		if quota.CheckedAt != nil {
			entry["checked_at"] = quota.CheckedAt.Format(time.RFC3339)
		}
		if p.isQuotaExhausted(acc) && quota.ExhaustedAt != nil {
			entry["exhausted_at"] = quota.ExhaustedAt.Format(time.RFC3339)
		}
		// Models
		var modelEntries []map[string]string
		for _, m := range models {
			modelEntries = append(modelEntries, map[string]string{"id": m.ID, "name": m.Name})
		}
		entry["models"] = modelEntries
		details = append(details, entry)
	}
	return details
}

// GetQuotaSummary returns quota summary for all accounts (for /health endpoint)
func (p *AccountPool) GetQuotaSummary() []map[string]interface{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var summary []map[string]interface{}
	for _, acc := range p.accounts {
		quota := acc.quotaSnapshot()
		health := acc.healthSnapshot()
		profile := acc.profileSnapshot()
		aiDisabled := profile.AIEnabled != nil && !*profile.AIEnabled
		unlimited := planIncludesFullNotionAI(profile.PlanType) && !aiDisabled
		entry := map[string]interface{}{
			"email":           acc.UserEmail,
			"name":            acc.UserName,
			"plan":            profile.PlanType,
			"exhausted":       p.isQuotaExhausted(acc),
			"permanent":       quota.PermanentlyExhausted && !unlimited,
			"no_workspace":    p.hasNoWorkspace(acc),
			"ai_disabled":     aiDisabled,
			"disabled":        health.ManuallyDisabled,
			"quota_unlimited": unlimited,
		}
		if health.TemporaryUnavailableUntil != nil {
			entry["temporarily_unavailable"] = health.TemporaryUnavailableUntil.After(time.Now())
			entry["unavailable_until"] = health.TemporaryUnavailableUntil.Format(time.RFC3339)
		}
		if health.LastFailureReason != "" {
			entry["last_failure_reason"] = health.LastFailureReason
		}
		if health.LastFailureAt != nil {
			entry["last_failure_at"] = health.LastFailureAt.Format(time.RFC3339)
		}
		if health.AuthInvalid {
			entry["auth_invalid"] = true
		}
		if health.AuthFailureCount > 0 {
			entry["auth_failures"] = health.AuthFailureCount
		}
		if profile.WorkspaceCheckedAt != nil {
			entry["space_count"] = profile.SpaceCount
			entry["workspace_checked_at"] = profile.WorkspaceCheckedAt.Format(time.RFC3339)
		}
		if quota.Info != nil {
			entry["eligible"] = quota.Info.IsEligible
			entry["usage"] = quota.Info.SpaceUsage
			entry["limit"] = quota.Info.SpaceLimit
			entry["space_usage"] = quota.Info.SpaceUsage
			entry["space_limit"] = quota.Info.SpaceLimit
			entry["space_remaining"] = quotaRemaining(quota.Info.SpaceLimit, quota.Info.SpaceUsage)
			entry["user_usage"] = quota.Info.UserUsage
			entry["user_limit"] = quota.Info.UserLimit
			entry["user_remaining"] = quotaRemaining(quota.Info.UserLimit, quota.Info.UserUsage)
			entry["remaining"] = basicRemaining(quota.Info)
			entry["last_usage_at"] = quota.Info.LastUsageAtMs
			// Research mode (V1)
			entry["research_usage"] = quota.Info.ResearchModeUsage
			// Premium credit data (V2)
			entry["has_premium"] = quota.Info.HasPremium
			entry["premium_balance"] = quota.Info.PremiumBalance
			entry["premium_usage"] = quota.Info.PremiumUsage
			entry["premium_limit"] = quota.Info.PremiumLimit
		}
		if quota.CheckedAt != nil {
			entry["checked_at"] = quota.CheckedAt.Format(time.RFC3339)
		}
		summary = append(summary, entry)
	}
	return summary
}

// loadPersistedWorkspace fills acc.SpaceCount / acc.WorkspaceCheckedAt
// from the persisted JSON. Absent fields leave the runtime values
// untouched (so the next probe still treats the account as unknown).
func loadPersistedWorkspace(data []byte, acc *Account) {
	var raw struct {
		SpaceCount         *int    `json:"space_count"`
		WorkspaceCheckedAt *string `json:"workspace_checked_at"`
		WorkspaceAIEnabled *bool   `json:"workspace_ai_enabled"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return
	}
	if raw.WorkspaceCheckedAt == nil || *raw.WorkspaceCheckedAt == "" {
		return
	}
	t, err := time.Parse(time.RFC3339, *raw.WorkspaceCheckedAt)
	if err != nil {
		return
	}
	acc.WorkspaceCheckedAt = &t
	acc.WorkspaceAIEnabled = cloneBoolPtr(raw.WorkspaceAIEnabled)
	if raw.SpaceCount != nil {
		acc.SpaceCount = *raw.SpaceCount
	}
}

// loadPersistedHealth restores only durable login failures. Temporary network,
// rate-limit, and upstream cooldowns deliberately disappear on restart.
func loadPersistedHealth(data []byte, acc *Account) {
	var raw struct {
		AuthInvalid       bool   `json:"auth_invalid"`
		LastFailureAt     string `json:"last_failure_at"`
		LastFailureReason string `json:"last_failure_reason"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || !raw.AuthInvalid {
		return
	}
	acc.AuthInvalid = true
	acc.AuthFailureCount = 1
	acc.LastFailureReason = "auth_invalid"
	if raw.LastFailureAt != "" {
		if failedAt, err := time.Parse(time.RFC3339, raw.LastFailureAt); err == nil {
			acc.LastFailureAt = &failedAt
		}
	}
}

// loadPersistedQuotaInfo parses the persisted quota_info (snake_case keys) from raw account JSON.
// Returns nil if quota_info is not present or cannot be parsed.
func loadPersistedQuotaInfo(data []byte) *QuotaInfo {
	var raw struct {
		QuotaInfo *struct {
			IsEligible        bool  `json:"is_eligible"`
			SpaceUsage        int   `json:"space_usage"`
			SpaceLimit        int   `json:"space_limit"`
			UserUsage         int   `json:"user_usage"`
			UserLimit         int   `json:"user_limit"`
			LastUsageAt       int64 `json:"last_usage_at"`
			ResearchModeUsage int   `json:"research_mode_usage"`
			HasPremium        bool  `json:"has_premium"`
			PremiumBalance    int   `json:"premium_balance"`
			PremiumUsage      int   `json:"premium_usage"`
			PremiumLimit      int   `json:"premium_limit"`
		} `json:"quota_info"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.QuotaInfo == nil {
		return nil
	}
	return &QuotaInfo{
		IsEligible:        raw.QuotaInfo.IsEligible,
		SpaceUsage:        raw.QuotaInfo.SpaceUsage,
		SpaceLimit:        raw.QuotaInfo.SpaceLimit,
		UserUsage:         raw.QuotaInfo.UserUsage,
		UserLimit:         raw.QuotaInfo.UserLimit,
		LastUsageAtMs:     raw.QuotaInfo.LastUsageAt,
		ResearchModeUsage: raw.QuotaInfo.ResearchModeUsage,
		HasPremium:        raw.QuotaInfo.HasPremium,
		PremiumBalance:    raw.QuotaInfo.PremiumBalance,
		PremiumUsage:      raw.QuotaInfo.PremiumUsage,
		PremiumLimit:      raw.QuotaInfo.PremiumLimit,
	}
}

func quotaRemaining(limit, usage int) int {
	if limit <= 0 {
		return 0
	}
	remaining := limit - usage
	if remaining < 0 {
		return 0
	}
	return remaining
}

func basicRemaining(info *QuotaInfo) int {
	if info == nil {
		return 0
	}
	remaining := []int{}
	if info.SpaceLimit > 0 {
		remaining = append(remaining, quotaRemaining(info.SpaceLimit, info.SpaceUsage))
	}
	if info.UserLimit > 0 {
		remaining = append(remaining, quotaRemaining(info.UserLimit, info.UserUsage))
	}
	if len(remaining) == 0 {
		return 0
	}
	best := remaining[0]
	for _, value := range remaining[1:] {
		if value < best {
			best = value
		}
	}
	return best
}

// accountQuotaPriority returns a coarse, evidence-backed service-tier score.
// Higher = more preferred when picking the "best" account.
//
//   - Unknown quota: -1 (fallback until refreshed).
//   - Included-AI paid plans: 100 + their product-tier rank.
//   - Other eligible trial accounts: 1 + their product-tier rank.
func accountQuotaPriority(acc *Account) int {
	if acc == nil {
		return -1
	}
	plan := acc.planTypeSnapshot()
	planPriority := workspacePlanPriority(plan)
	if planIncludesFullNotionAI(plan) {
		return 100 + planPriority
	}
	quota := acc.quotaInfoSnapshot()
	if quota == nil {
		return -1
	}
	return 1 + planPriority
}

func generateUUIDv4() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback
		for i := range b {
			b[i] = byte(mrand.Intn(256))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
