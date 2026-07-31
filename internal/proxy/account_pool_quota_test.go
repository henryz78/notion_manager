package proxy

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// newPool builds an AccountPool with deduplicated init for tests.
func newPool(accs ...*Account) *AccountPool {
	p := NewAccountPool()
	p.accounts = accs
	return p
}

func TestNextBestPrefersFullNotionAIPlan(t *testing.T) {
	low := &Account{
		UserEmail: "low@example.com",
		QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 180, UserLimit: 200, UserUsage: 180},
	}
	mid := &Account{
		UserEmail: "mid@example.com",
		QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 100, UserLimit: 200, UserUsage: 100},
	}
	high := &Account{
		UserEmail: "high@example.com",
		PlanType:  "business",
		QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 20, UserLimit: 200, UserUsage: 20},
	}
	pool := newPool(low, mid, high)

	got := pool.NextBest()
	if got == nil || got.UserEmail != "high@example.com" {
		t.Fatalf("expected Business account, got %#v", got)
	}
}

func TestNextBestUsesWorkspacePlanHierarchy(t *testing.T) {
	team := &Account{UserEmail: "team@example.com", PlanType: "team"}
	business := &Account{UserEmail: "business@example.com", PlanType: "business"}
	enterprise := &Account{UserEmail: "enterprise@example.com", PlanType: "enterprise"}
	pool := newPool(team, business, enterprise)

	if got := pool.NextBest(); got != enterprise {
		t.Fatalf("expected Enterprise first, got %#v", got)
	}
	if got := pool.NextBestExcluding(map[*Account]bool{enterprise: true}); got != business {
		t.Fatalf("expected Business after Enterprise, got %#v", got)
	}
	if got := pool.NextBestExcluding(map[*Account]bool{enterprise: true, business: true}); got != team {
		t.Fatalf("expected Team after Business, got %#v", got)
	}
}

func TestNextBestExcludingUsesServiceTiers(t *testing.T) {
	a := &Account{
		UserEmail: "a@example.com",
		PlanType:  "business",
		QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 20, UserLimit: 200, UserUsage: 20}, // remaining 180
	}
	b := &Account{
		UserEmail: "b@example.com",
		PlanType:  "plus",
		QuotaInfo: &QuotaInfo{IsEligible: true, HasPremium: true, SpaceLimit: 200, SpaceUsage: 100, UserLimit: 200, UserUsage: 100},
	}
	c := &Account{
		UserEmail: "c@example.com",
		QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 160, UserLimit: 200, UserUsage: 160}, // remaining 40
	}
	pool := newPool(a, b, c)

	// First pick is the full-AI Business plan.
	first := pool.NextBest()
	if first == nil || first.UserEmail != "a@example.com" {
		t.Fatalf("expected first NextBest to be 'a', got %#v", first)
	}

	// After excluding 'a', equal-tier trial accounts keep rotation order.
	tried := map[*Account]bool{a: true}
	second := pool.NextBestExcluding(tried)
	if second == nil || second.UserEmail != "b@example.com" {
		t.Fatalf("expected second NextBestExcluding to be 'b', got %#v", second)
	}

	// Excluding 'a' and 'b' leaves 'c'.
	tried[b] = true
	third := pool.NextBestExcluding(tried)
	if third == nil || third.UserEmail != "c@example.com" {
		t.Fatalf("expected third NextBestExcluding to be 'c', got %#v", third)
	}

	// All excluded → nil
	tried[c] = true
	if got := pool.NextBestExcluding(tried); got != nil {
		t.Fatalf("expected nil when all accounts excluded, got %#v", got)
	}
}

func TestNextBestFallsBackToUnknownQuota(t *testing.T) {
	// When no account has measurable quota (QuotaInfo == nil), the pool
	// should still return the first usable one rather than refusing to
	// serve.
	a := &Account{UserEmail: "a@example.com"}
	b := &Account{UserEmail: "b@example.com"}
	pool := newPool(a, b)

	got := pool.NextBest()
	if got == nil {
		t.Fatal("expected fallback when no quota info present, got nil")
	}
	if got != a && got != b {
		t.Fatalf("expected one of the seeded accounts, got %#v", got)
	}
}

func TestNextBestPrefersScoredOverUnknownQuota(t *testing.T) {
	known := &Account{
		UserEmail: "known@example.com",
		QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 50, UserLimit: 200, UserUsage: 50}, // remaining 150
	}
	unknown := &Account{UserEmail: "unknown@example.com"}
	pool := newPool(unknown, known) // unknown listed first to defeat ordering bias

	got := pool.NextBest()
	if got == nil || got.UserEmail != "known@example.com" {
		t.Fatalf("expected known-quota account to be preferred, got %#v", got)
	}
}

func TestNextBestSkipsExhaustedAccounts(t *testing.T) {
	exhausted := &Account{
		UserEmail: "exhausted@example.com",
		QuotaInfo: &QuotaInfo{IsEligible: false, SpaceLimit: 200, SpaceUsage: 200, UserLimit: 200, UserUsage: 200},
	}
	healthy := &Account{
		UserEmail: "healthy@example.com",
		QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 50, UserLimit: 200, UserUsage: 50},
	}
	pool := newPool(exhausted, healthy)

	got := pool.NextBest()
	if got == nil || got.UserEmail != "healthy@example.com" {
		t.Fatalf("expected exhausted account to be skipped, got %#v", got)
	}
}

func TestAccountQuotaPriorityIgnoresSeparatePremiumCredits(t *testing.T) {
	basicOnly := &Account{
		UserEmail: "basic@example.com",
		QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 50, UserLimit: 200, UserUsage: 50}, // 150
	}
	premiumLowBasic := &Account{
		UserEmail: "premium@example.com",
		QuotaInfo: &QuotaInfo{
			IsEligible:     true,
			HasPremium:     true,
			PremiumBalance: 10000,
			SpaceLimit:     200,
			SpaceUsage:     190,
			UserLimit:      200,
			UserUsage:      190, // basic remaining only 10
		},
	}

	if accountQuotaPriority(basicOnly) != 1 {
		t.Fatalf("trial score: want 1, got %d", accountQuotaPriority(basicOnly))
	}
	if got := accountQuotaPriority(premiumLowBasic); got != 1 {
		t.Fatalf("private Premium fields must not change the plan tier: want 1, got %d", got)
	}
}

func TestRefreshAccountQuotaCacheHitSkipsHTTP(t *testing.T) {
	// QuotaCheckedAt was set very recently, so RefreshAccountQuota must
	// take the cached fast path and never reach CheckQuota (which would
	// hit the network and panic since this account has no real token).
	now := time.Now()
	acc := &Account{
		UserEmail:      "cached@example.com",
		TokenV2:        "fake-token-do-not-use",
		QuotaCheckedAt: &now,
		QuotaInfo:      &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 0, UserLimit: 200, UserUsage: 0},
	}
	pool := newPool(acc)

	// 60s minInterval ensures the freshly recorded check counts as fresh.
	if !pool.RefreshAccountQuota(acc, 60*time.Second) {
		t.Fatal("expected cached eligible result to return true")
	}

	acc.QuotaInfo.IsEligible = false
	if pool.RefreshAccountQuota(acc, 60*time.Second) {
		t.Fatal("expected cached non-eligible result to return false")
	}
}

func TestRefreshAccountQuotaNilAccount(t *testing.T) {
	pool := newPool()
	if pool.RefreshAccountQuota(nil, 5*time.Second) {
		t.Fatal("nil account must not be considered eligible")
	}
}

func TestRefreshAccountQuotaSingleflightsConcurrentSyncAndAsyncChecks(t *testing.T) {
	previous := routingQuotaFetcher
	t.Cleanup(func() { routingQuotaFetcher = previous })

	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	routingQuotaFetcher = func(*Account) (*QuotaInfo, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return &QuotaInfo{IsEligible: true, SpaceLimit: 75, SpaceUsage: 1}, nil
	}

	acc := &Account{UserEmail: "singleflight@example.com", PlanType: "free"}
	pool := newPool(acc)
	pool.RefreshAccountQuotaAsync(acc)
	<-started

	result := make(chan bool, 1)
	go func() {
		result <- pool.RefreshAccountQuota(acc, 0)
	}()
	select {
	case got := <-result:
		t.Fatalf("synchronous follower returned before the shared check completed: %v", got)
	case <-time.After(50 * time.Millisecond):
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent checks made %d upstream calls, want 1", got)
	}
	close(release)
	if got := <-result; !got {
		t.Fatal("shared eligible result was not returned to the synchronous caller")
	}
}

func TestQuotaGenerationRejectsOlderResponse(t *testing.T) {
	acc := &Account{UserEmail: "generation@example.com", PlanType: "free"}
	pool := newPool(acc)
	older := pool.nextQuotaGeneration(acc)
	newer := pool.nextQuotaGeneration(acc)
	if _, applied := pool.applyQuotaInfoIfCurrent(acc, &QuotaInfo{IsEligible: false}, newer); !applied {
		t.Fatal("newest quota response was unexpectedly rejected")
	}
	if _, applied := pool.applyQuotaInfoIfCurrent(acc, &QuotaInfo{IsEligible: true}, older); applied {
		t.Fatal("older quota response overwrote newer state")
	}
	if quota := acc.quotaInfoSnapshot(); quota == nil || quota.IsEligible {
		t.Fatalf("newest exhausted state was lost: %#v", quota)
	}
}

func TestQuotaGenerationNewerStartFailureDoesNotDiscardOlderSuccess(t *testing.T) {
	acc := &Account{
		UserEmail: "generation-failure@example.com",
		PlanType:  "free",
		QuotaInfo: &QuotaInfo{IsEligible: false},
	}
	pool := newPool(acc)

	older := pool.nextQuotaGeneration(acc)
	_ = pool.nextQuotaGeneration(acc) // newer request starts, then fails before applying

	if _, applied := pool.applyQuotaInfoIfCurrent(acc, &QuotaInfo{IsEligible: true}, older); !applied {
		t.Fatal("a newer failed check prevented an older successful response from being applied")
	}
	if quota := acc.quotaInfoSnapshot(); quota == nil || !quota.IsEligible {
		t.Fatalf("older successful response was not retained after newer failure: %#v", quota)
	}
}

func TestApplyWorkspaceProfileRefreshesIncludedAIPlan(t *testing.T) {
	now := time.Now()
	acc := &Account{
		UserEmail:            "upgraded@example.com",
		PlanType:             "plus",
		QuotaInfo:            &QuotaInfo{IsEligible: false},
		QuotaExhaustedAt:     &now,
		PermanentlyExhausted: true,
	}
	pool := newPool(acc)
	_, _, planChanged := pool.applyWorkspaceProfile(acc, WorkspaceProbeResult{
		Count:     1,
		PlanType:  "team",
		AIEnabled: true,
	})
	if !planChanged || acc.planTypeSnapshot() != "team" {
		t.Fatalf("plan was not refreshed: changed=%v plan=%q", planChanged, acc.planTypeSnapshot())
	}
	if pool.isQuotaExhausted(acc) {
		t.Fatal("upgraded included-AI plan remained exhausted")
	}
	quota := acc.quotaSnapshot()
	if quota.ExhaustedAt != nil || quota.PermanentlyExhausted {
		t.Fatalf("legacy trial flags were not cleared: %#v", quota)
	}
}

func TestApplyQuotaInfoMarksAndClearsExhaustion(t *testing.T) {
	pool := NewAccountPool()
	now := time.Now()
	acc := &Account{
		UserEmail:        "victim@example.com",
		PlanType:         "personal",
		QuotaExhaustedAt: &now,
	}

	// Eligible result clears the exhaustion mark.
	pool.applyQuotaInfo(acc, &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 10, UserLimit: 200, UserUsage: 10})
	if acc.QuotaExhaustedAt != nil {
		t.Fatalf("expected QuotaExhaustedAt cleared after recovery, got %v", acc.QuotaExhaustedAt)
	}
	if acc.PermanentlyExhausted {
		t.Fatal("recovered free account should no longer be permanent")
	}
	if acc.QuotaInfo == nil || !acc.QuotaInfo.IsEligible {
		t.Fatal("expected QuotaInfo updated to eligible state")
	}

	// Non-eligible result on a complimentary plan flips the retained trial flag.
	pool.applyQuotaInfo(acc, &QuotaInfo{IsEligible: false, SpaceLimit: 200, SpaceUsage: 200, UserLimit: 200, UserUsage: 200})
	if acc.QuotaExhaustedAt == nil {
		t.Fatal("expected QuotaExhaustedAt set after exhaustion")
	}
	if !acc.PermanentlyExhausted {
		t.Fatal("complimentary trial exhaustion should be retained as unavailable")
	}

	// Notion's legacy/internal Team identifier is a paid included-AI plan.
	// Stale complimentary quota flags and the old 75-point counters must not
	// disable it.
	paidExhaustedAt := time.Now()
	paid := &Account{
		UserEmail:            "team@example.com",
		PlanType:             "team",
		QuotaExhaustedAt:     &paidExhaustedAt,
		PermanentlyExhausted: true,
	}
	res := pool.applyQuotaInfo(paid, &QuotaInfo{
		IsEligible: false,
		SpaceLimit: 75, SpaceUsage: 999,
		UserLimit: 75, UserUsage: 999,
	})
	if !res.Unlimited {
		t.Fatal("Team plan should be classified as current unlimited mode")
	}
	if paid.PermanentlyExhausted {
		t.Fatal("Team plan must not retain a complimentary-trial exhaustion flag")
	}
	if paid.QuotaExhaustedAt != nil {
		t.Fatal("Team plan must clear the old Basic exhaustion timestamp")
	}
	if pool.isQuotaExhausted(paid) {
		t.Fatal("Team plan must remain selectable despite legacy Basic counters")
	}
}

func TestRefreshAccountQuotaCachedTeamIgnoresLegacyBasicEligibility(t *testing.T) {
	originalRoutingQuotaFetcher := routingQuotaFetcher
	t.Cleanup(func() { routingQuotaFetcher = originalRoutingQuotaFetcher })
	var calls atomic.Int32
	routingQuotaFetcher = func(*Account) (*QuotaInfo, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	}
	acc := &Account{
		UserEmail: "team@example.com",
		PlanType:  "team",
		QuotaInfo: &QuotaInfo{
			IsEligible: false,
			SpaceLimit: 75,
			SpaceUsage: 999,
		},
	}
	pool := newPool(acc)
	if !pool.RefreshAccountQuota(acc, time.Minute) {
		t.Fatal("cached Team plan must ignore the legacy Basic eligibility flag")
	}
	if calls.Load() != 0 {
		t.Fatalf("included-AI plan made %d synchronous quota call(s)", calls.Load())
	}
	if got := accountQuotaPriority(&Account{PlanType: "team"}); got != 130 {
		t.Fatalf("Team without cached quota priority=%d, want 130", got)
	}
}

func TestRefreshAllRechecksTrialExhaustedAccountForUpgradeRecovery(t *testing.T) {
	originalQuotaFetcher := quotaFetcher
	originalModelsFetcher := modelsFetcher
	originalWorkspaceProbe := workspaceProbe
	t.Cleanup(func() {
		quotaFetcher = originalQuotaFetcher
		modelsFetcher = originalModelsFetcher
		workspaceProbe = originalWorkspaceProbe
	})

	var quotaCalls atomic.Int32
	quotaFetcher = func(*Account) (*QuotaInfo, error) {
		quotaCalls.Add(1)
		return &QuotaInfo{IsEligible: true}, nil
	}
	modelsFetcher = func(*Account) ([]ModelEntry, error) { return nil, nil }
	workspaceProbe = func(*Account) (WorkspaceProbeResult, error) {
		return WorkspaceProbeResult{Count: 1}, nil
	}

	now := time.Now()
	acc := &Account{
		UserEmail:            "upgraded@example.com",
		PlanType:             "plus",
		QuotaInfo:            &QuotaInfo{IsEligible: false},
		QuotaExhaustedAt:     &now,
		PermanentlyExhausted: true,
	}
	pool := newPool(acc)
	pool.RefreshAll("")

	if quotaCalls.Load() != 1 {
		t.Fatalf("expected trial-exhausted account to be rechecked once, got %d", quotaCalls.Load())
	}
	if acc.PermanentlyExhausted || acc.QuotaExhaustedAt != nil {
		t.Fatalf("expected upgraded account to recover, permanent=%v exhaustedAt=%v", acc.PermanentlyExhausted, acc.QuotaExhaustedAt)
	}
	if acc.QuotaInfo == nil || !acc.QuotaInfo.IsEligible {
		t.Fatalf("expected refreshed eligibility, got %#v", acc.QuotaInfo)
	}
}

func TestRefreshAllUpdatesPlanClassificationFromWorkspaceProfile(t *testing.T) {
	originalQuotaFetcher := quotaFetcher
	originalModelsFetcher := modelsFetcher
	originalWorkspaceProbe := workspaceProbe
	t.Cleanup(func() {
		quotaFetcher = originalQuotaFetcher
		modelsFetcher = originalModelsFetcher
		workspaceProbe = originalWorkspaceProbe
	})
	quotaFetcher = func(*Account) (*QuotaInfo, error) {
		return &QuotaInfo{IsEligible: false, SpaceLimit: 75, SpaceUsage: 75}, nil
	}
	modelsFetcher = func(*Account) ([]ModelEntry, error) { return nil, nil }
	workspaceProbe = func(*Account) (WorkspaceProbeResult, error) {
		return WorkspaceProbeResult{Count: 1, PlanType: "team", AIEnabled: true}, nil
	}

	now := time.Now()
	acc := &Account{
		UserEmail:            "upgrade-plan@example.com",
		PlanType:             "plus",
		QuotaInfo:            &QuotaInfo{IsEligible: false},
		QuotaExhaustedAt:     &now,
		PermanentlyExhausted: true,
	}
	pool := newPool(acc)
	pool.RefreshAll("")
	if got := acc.planTypeSnapshot(); got != "team" {
		t.Fatalf("refreshed plan=%q, want team", got)
	}
	if pool.isQuotaExhausted(acc) {
		t.Fatal("refreshed Team account remained exhausted by legacy V1 counters")
	}
}

func TestRefreshAllStopsIgnoringLegacyQuotaAfterPlanDowngrade(t *testing.T) {
	originalQuotaFetcher := quotaFetcher
	originalModelsFetcher := modelsFetcher
	originalWorkspaceProbe := workspaceProbe
	t.Cleanup(func() {
		quotaFetcher = originalQuotaFetcher
		modelsFetcher = originalModelsFetcher
		workspaceProbe = originalWorkspaceProbe
	})
	quotaFetcher = func(*Account) (*QuotaInfo, error) {
		return &QuotaInfo{IsEligible: false, SpaceLimit: 75, SpaceUsage: 75}, nil
	}
	modelsFetcher = func(*Account) ([]ModelEntry, error) { return nil, nil }
	workspaceProbe = func(*Account) (WorkspaceProbeResult, error) {
		return WorkspaceProbeResult{Count: 1, PlanType: "plus", AIEnabled: true}, nil
	}

	acc := &Account{
		UserEmail: "downgrade-plan@example.com",
		PlanType:  "team",
		QuotaInfo: &QuotaInfo{IsEligible: true},
	}
	pool := newPool(acc)
	pool.RefreshAll("")
	if got := acc.planTypeSnapshot(); got != "plus" {
		t.Fatalf("refreshed plan=%q, want plus", got)
	}
	if !pool.isQuotaExhausted(acc) {
		t.Fatal("downgraded complimentary plan still ignored exhausted V1 state")
	}
}

func TestRefreshAllStopsAtFirst401AndReportsFailedAccount(t *testing.T) {
	originalQuotaFetcher := quotaFetcher
	originalModelsFetcher := modelsFetcher
	originalWorkspaceProbe := workspaceProbe
	t.Cleanup(func() {
		quotaFetcher = originalQuotaFetcher
		modelsFetcher = originalModelsFetcher
		workspaceProbe = originalWorkspaceProbe
	})

	tests := []struct {
		name          string
		workspaceErr  error
		quotaErr      error
		modelsErr     error
		wantWorkspace int32
		wantQuota     int32
		wantModels    int32
	}{
		{
			name:          "workspace endpoint",
			workspaceErr:  errors.New("loadUserContent API error 401: unauthorized"),
			wantWorkspace: 1,
		},
		{
			name:          "quota endpoint",
			quotaErr:      errors.New("V1 API error 401: unauthorized"),
			wantWorkspace: 1,
			wantQuota:     1,
		},
		{
			name:          "models endpoint",
			modelsErr:     errors.New("notion API error 401: unauthorized"),
			wantWorkspace: 1,
			wantQuota:     1,
			wantModels:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var workspaceCalls, quotaCalls, modelCalls atomic.Int32
			workspaceProbe = func(*Account) (WorkspaceProbeResult, error) {
				workspaceCalls.Add(1)
				if tt.workspaceErr != nil {
					return WorkspaceProbeResult{}, tt.workspaceErr
				}
				return WorkspaceProbeResult{Count: 1}, nil
			}
			quotaFetcher = func(*Account) (*QuotaInfo, error) {
				quotaCalls.Add(1)
				if tt.quotaErr != nil {
					return nil, tt.quotaErr
				}
				return &QuotaInfo{IsEligible: true}, nil
			}
			modelsFetcher = func(*Account) ([]ModelEntry, error) {
				modelCalls.Add(1)
				if tt.modelsErr != nil {
					return nil, tt.modelsErr
				}
				return []ModelEntry{{ID: "model-id", Name: "Model"}}, nil
			}

			acc := &Account{UserEmail: "expired@example.com", UserID: "user-1"}
			pool := newPool(acc)
			pool.RefreshAll("")

			if got := workspaceCalls.Load(); got != tt.wantWorkspace {
				t.Fatalf("workspace calls=%d want=%d", got, tt.wantWorkspace)
			}
			if got := quotaCalls.Load(); got != tt.wantQuota {
				t.Fatalf("quota calls=%d want=%d", got, tt.wantQuota)
			}
			if got := modelCalls.Load(); got != tt.wantModels {
				t.Fatalf("model calls=%d want=%d", got, tt.wantModels)
			}
			if !pool.isAuthInvalid(acc) {
				t.Fatal("first explicit 401 should mark the login auth-invalid")
			}
			status := pool.GetRefreshStatus()
			if got := status["failed"]; got != 1 {
				t.Fatalf("refresh failed=%#v want=1", got)
			}
		})
	}
}

func TestCloneChatMessagesIsDeepCopy(t *testing.T) {
	src := []ChatMessage{
		{Role: "system", Content: "you are helpful"},
		{Role: "user", Content: "hi"},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "lookup", Arguments: `{"q":"x"}`}},
				{ID: "call_2", Type: "function", Function: ToolCallFunction{Name: "noop", Arguments: `{}`}},
			},
		},
	}

	clone := cloneChatMessages(src)
	if len(clone) != len(src) {
		t.Fatalf("expected length %d, got %d", len(src), len(clone))
	}

	// Mutating the clone must not affect the source.
	clone[0].Content = "tampered"
	if src[0].Content != "you are helpful" {
		t.Fatalf("source content was mutated through clone: %q", src[0].Content)
	}

	// ToolCalls slice should be a fresh allocation.
	clone[2].ToolCalls[0].ID = "tampered_id"
	if src[2].ToolCalls[0].ID != "call_1" {
		t.Fatalf("source ToolCalls was mutated through clone: %q", src[2].ToolCalls[0].ID)
	}
	clone[2].ToolCalls = append(clone[2].ToolCalls, ToolCall{ID: "extra"})
	if len(src[2].ToolCalls) != 2 {
		t.Fatalf("source ToolCalls slice was extended through clone: len=%d", len(src[2].ToolCalls))
	}
}

func TestCloneChatMessagesNilInput(t *testing.T) {
	if got := cloneChatMessages(nil); got != nil {
		t.Fatalf("expected nil clone for nil input, got %v", got)
	}
}

// applyQuotaInfo's return value drives the refresh-loop log lines that tell
// operators which accounts were just disabled. Lock the contract here so the
// startup "kick out exhausted accounts" behaviour stays observable.
func TestApplyQuotaInfoReturnsTransitionForExhaustedFreePlan(t *testing.T) {
	pool := NewAccountPool()
	acc := &Account{UserEmail: "free@example.com", PlanType: "personal"}

	res := pool.applyQuotaInfo(acc, &QuotaInfo{
		IsEligible: false,
		SpaceLimit: 200, SpaceUsage: 200,
		UserLimit: 200, UserUsage: 200,
	})

	if !res.NowExhausted {
		t.Fatal("expected NowExhausted=true")
	}
	if !res.NowPermanent {
		t.Fatal("expected NowPermanent=true for free plan exhaustion")
	}
	if res.Recovered {
		t.Fatal("expected Recovered=false on first exhaustion")
	}
	if !pool.isQuotaExhausted(acc) {
		t.Fatal("expected pool to consider account exhausted after applyQuotaInfo")
	}
}

func TestApplyQuotaInfoReturnsTransitionForRecovery(t *testing.T) {
	pool := NewAccountPool()
	now := time.Now()
	acc := &Account{
		UserEmail:        "back@example.com",
		PlanType:         "personal",
		QuotaExhaustedAt: &now,
	}

	res := pool.applyQuotaInfo(acc, &QuotaInfo{
		IsEligible: true,
		SpaceLimit: 200, SpaceUsage: 50,
		UserLimit: 200, UserUsage: 50,
	})

	if !res.Recovered {
		t.Fatal("expected Recovered=true when previously-exhausted account becomes eligible")
	}
	if res.NowExhausted || res.NowPermanent {
		t.Fatal("recovery transition must clear exhaustion flags")
	}
	if pool.isQuotaExhausted(acc) {
		t.Fatal("recovered account must not be reported exhausted")
	}
}

func TestApplyQuotaInfoNoTransitionWhenStillEligible(t *testing.T) {
	pool := NewAccountPool()
	acc := &Account{UserEmail: "stable@example.com", PlanType: "business"}

	res := pool.applyQuotaInfo(acc, &QuotaInfo{
		IsEligible: true,
		HasPremium: true,
		SpaceLimit: 200, SpaceUsage: 10,
		UserLimit: 200, UserUsage: 10,
	})

	if res.Recovered || res.NowExhausted || res.NowPermanent {
		t.Fatalf("expected no transitions, got %+v", res)
	}
	if res.BasicLeft != 190 {
		t.Fatalf("expected BasicLeft=190, got %d", res.BasicLeft)
	}
	if !res.HasPremium {
		t.Fatal("expected HasPremium=true echoed back")
	}
}

// Once an account is disabled by applyQuotaInfo, all selectors must skip it.
// This guards the startup "disable exhausted on refresh" promise: if a worker
// just marked an account exhausted, no concurrent NextBest/Next/etc. call may
// still hand it back to a request.
func TestSelectorsSkipAccountAfterApplyQuotaInfo(t *testing.T) {
	pool := NewAccountPool()
	exhausted := &Account{UserEmail: "drained@example.com", PlanType: "personal"}
	healthy := &Account{
		UserEmail: "healthy@example.com",
		QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 10, UserLimit: 200, UserUsage: 10},
	}
	pool.accounts = []*Account{exhausted, healthy}

	pool.applyQuotaInfo(exhausted, &QuotaInfo{
		IsEligible: false,
		SpaceLimit: 200, SpaceUsage: 200,
		UserLimit: 200, UserUsage: 200,
	})

	if got := pool.NextBest(); got == nil || got.UserEmail != "healthy@example.com" {
		t.Fatalf("NextBest should skip newly-disabled account, got %#v", got)
	}
	if got := pool.Next(); got == nil || got.UserEmail != "healthy@example.com" {
		t.Fatalf("Next should skip newly-disabled account, got %#v", got)
	}
	if got := pool.GetByEmail("drained@example.com"); got != nil {
		t.Fatalf("GetByEmail must not return disabled account, got %#v", got)
	}
	if got := pool.AvailableCount(); got != 1 {
		t.Fatalf("expected AvailableCount=1, got %d", got)
	}
}
