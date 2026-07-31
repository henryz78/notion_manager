package proxy

import (
	"testing"
	"time"
)

func TestNextSkipsIneligibleAccounts(t *testing.T) {
	pool := &AccountPool{
		accounts: []*Account{
			{
				PlanType:  "personal",
				UserEmail: "blocked@example.com",
				QuotaInfo: &QuotaInfo{
					IsEligible:     false,
					HasPremium:     true,
					PremiumBalance: 1300000,
					PremiumLimit:   1300000,
				},
			},
			{
				UserEmail: "eligible@example.com",
				QuotaInfo: &QuotaInfo{
					IsEligible: true,
				},
			},
		},
	}

	got := pool.Next()
	if got == nil || got.UserEmail != "eligible@example.com" {
		t.Fatalf("expected eligible account, got %#v", got)
	}
}

func TestGetBestAccountPrefersEligibleAccount(t *testing.T) {
	pool := &AccountPool{
		accounts: []*Account{
			{
				PlanType:  "personal",
				UserEmail: "blocked@example.com",
				QuotaInfo: &QuotaInfo{
					IsEligible:     false,
					HasPremium:     true,
					PremiumBalance: 1300000,
					PremiumLimit:   1300000,
					SpaceLimit:     200,
					SpaceUsage:     180,
				},
			},
			{
				UserEmail: "eligible@example.com",
				QuotaInfo: &QuotaInfo{
					IsEligible: true,
					SpaceLimit: 200,
					SpaceUsage: 20,
				},
			},
		},
	}

	got := pool.GetBestAccount()
	if got == nil || got.UserEmail != "eligible@example.com" {
		t.Fatalf("expected eligible account, got %#v", got)
	}
}

func TestGetBestAccountReturnsNilWhenOnlyIneligibleAccounts(t *testing.T) {
	pool := &AccountPool{
		accounts: []*Account{
			{
				PlanType:  "personal",
				UserEmail: "blocked@example.com",
				QuotaInfo: &QuotaInfo{
					IsEligible:     false,
					HasPremium:     true,
					PremiumBalance: 1300000,
					PremiumLimit:   1300000,
				},
			},
		},
	}

	if got := pool.GetBestAccount(); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestTemporaryUnavailableAccountIsSkippedUntilCooldownExpires(t *testing.T) {
	blocked := &Account{
		UserEmail: "blocked@example.com",
		PlanType:  "business",
		QuotaInfo: &QuotaInfo{
			IsEligible: true,
			SpaceLimit: 200,
			SpaceUsage: 10,
			UserLimit:  200,
			UserUsage:  10,
		},
	}
	fallback := &Account{
		UserEmail: "fallback@example.com",
		QuotaInfo: &QuotaInfo{
			IsEligible: true,
			SpaceLimit: 200,
			SpaceUsage: 80,
			UserLimit:  200,
			UserUsage:  80,
		},
	}
	pool := &AccountPool{accounts: []*Account{blocked, fallback}}

	pool.MarkTemporarilyUnavailable(blocked, "auth_error", time.Hour)

	if got := pool.GetBestAccount(); got == nil || got.UserEmail != "fallback@example.com" {
		t.Fatalf("expected cooldown account to be skipped, got %#v", got)
	}

	past := time.Now().Add(-time.Second)
	blocked.mu.Lock()
	blocked.TemporaryUnavailableUntil = &past
	blocked.mu.Unlock()

	if got := pool.GetBestAccount(); got == nil || got.UserEmail != "blocked@example.com" {
		t.Fatalf("expected expired cooldown account to be usable again, got %#v", got)
	}
}

func TestGetAccountDetailsSurfacesTemporaryFailure(t *testing.T) {
	acc := &Account{
		UserEmail: "cooldown@example.com",
		QuotaInfo: &QuotaInfo{
			IsEligible: true,
		},
	}
	pool := &AccountPool{accounts: []*Account{acc}}

	pool.MarkTemporarilyUnavailable(acc, "upstream_5xx", time.Minute)

	details := pool.GetAccountDetails()
	if len(details) != 1 {
		t.Fatalf("details len=%d want 1", len(details))
	}
	if details[0]["temporarily_unavailable"] != true {
		t.Fatalf("temporarily_unavailable missing: %#v", details[0])
	}
	if details[0]["last_failure_reason"] != "upstream_5xx" {
		t.Fatalf("last_failure_reason=%#v", details[0]["last_failure_reason"])
	}
	if _, ok := details[0]["unavailable_until"].(string); !ok {
		t.Fatalf("unavailable_until missing: %#v", details[0])
	}
}

func TestFirstAuthErrorMarksAccountInvalid(t *testing.T) {
	acc := &Account{
		UserEmail: "invalid@example.com",
		QuotaInfo: &QuotaInfo{
			IsEligible: true,
		},
	}
	pool := &AccountPool{accounts: []*Account{acc}}

	if invalid := pool.RecordAuthFailure(acc, time.Minute); !invalid {
		t.Fatal("confirmed auth failure should mark the account invalid immediately")
	}

	if got := pool.GetBestAccount(); got != nil {
		t.Fatalf("auth-invalid account should be skipped, got %#v", got)
	}
	details := pool.GetAccountDetails()
	if details[0]["auth_invalid"] != true {
		t.Fatalf("auth_invalid missing: %#v", details[0])
	}
	if details[0]["auth_failures"] != 1 {
		t.Fatalf("auth_failures=%#v want 1", details[0]["auth_failures"])
	}
	if details[0]["last_failure_reason"] != "auth_invalid" {
		t.Fatalf("last_failure_reason=%#v", details[0]["last_failure_reason"])
	}
}

func TestSuccessfulAttemptCannotResurrectAuthInvalidState(t *testing.T) {
	acc := &Account{
		UserEmail: "recover@example.com",
		QuotaInfo: &QuotaInfo{
			IsEligible: true,
		},
	}
	pool := &AccountPool{accounts: []*Account{acc}}

	pool.RecordAuthFailure(acc, time.Minute)
	pool.ClearTemporaryUnavailable(acc)

	if got := pool.GetBestAccount(); got != nil {
		t.Fatalf("a late successful attempt must not resurrect an auth-invalid account, got %#v", got)
	}
	details := pool.GetAccountDetails()
	if details[0]["auth_invalid"] != true {
		t.Fatalf("auth_invalid should remain set: %#v", details[0])
	}
	if details[0]["auth_failures"] != 1 {
		t.Fatalf("auth_failures should remain set: %#v", details[0])
	}
}

func TestApplyQuotaInfoPreservesAuthInvalidState(t *testing.T) {
	acc := &Account{
		UserEmail: "quota-recover@example.com",
		QuotaInfo: &QuotaInfo{
			IsEligible: true,
		},
	}
	pool := &AccountPool{accounts: []*Account{acc}}

	pool.RecordAuthFailure(acc, time.Minute)
	pool.applyQuotaInfo(acc, &QuotaInfo{IsEligible: true})

	if got := pool.GetBestAccount(); got != nil {
		t.Fatalf("quota eligibility must not resurrect auth-invalid account, got %#v", got)
	}
	details := pool.GetAccountDetails()
	if details[0]["auth_invalid"] != true {
		t.Fatalf("auth_invalid should survive quota refresh: %#v", details[0])
	}
	if details[0]["auth_failures"] != 1 {
		t.Fatalf("auth_failures should survive quota refresh: %#v", details[0])
	}
}

func TestIsQuotaExhaustedUsesEligibilityFlag(t *testing.T) {
	pool := &AccountPool{
		accounts: []*Account{
			{
				PlanType:  "personal",
				UserEmail: "personal@example.com",
				QuotaInfo: &QuotaInfo{
					IsEligible:     false,
					HasPremium:     true,
					PremiumBalance: 1300000,
					PremiumLimit:   1300000,
				},
			},
		},
	}

	if got := pool.isQuotaExhausted(pool.accounts[0]); !got {
		t.Fatalf("expected account with is_eligible=false to be exhausted")
	}
}

func TestGetAccountDetailsMarksTeamQuotaUnlimited(t *testing.T) {
	now := time.Now()
	acc := &Account{
		UserEmail:            "team@example.com",
		PlanType:             "team",
		QuotaInfo:            &QuotaInfo{IsEligible: false, SpaceLimit: 75, SpaceUsage: 999},
		QuotaExhaustedAt:     &now,
		PermanentlyExhausted: true,
	}
	pool := &AccountPool{accounts: []*Account{acc}}

	details := pool.GetAccountDetails()
	if len(details) != 1 {
		t.Fatalf("details len=%d want 1", len(details))
	}
	if details[0]["quota_unlimited"] != true {
		t.Fatalf("quota_unlimited missing: %#v", details[0])
	}
	if details[0]["exhausted"] != false || details[0]["permanent"] != false {
		t.Fatalf("legacy Basic state must not disable Team plan: %#v", details[0])
	}
	if got := pool.GetBestAccount(); got != acc {
		t.Fatalf("Team account should remain selectable, got %#v", got)
	}
}

func TestGetBestAccountPrefersFullNotionAIPlanOverPrivateCounterMath(t *testing.T) {
	pool := &AccountPool{
		accounts: []*Account{
			{
				UserEmail: "trial-with-high-counters@example.com",
				PlanType:  "plus",
				QuotaInfo: &QuotaInfo{
					IsEligible: true,
					SpaceLimit: 200,
					SpaceUsage: 1,
					UserLimit:  200,
					UserUsage:  1,
				},
			},
			{
				UserEmail: "business@example.com",
				PlanType:  "business",
				QuotaInfo: &QuotaInfo{
					IsEligible: true,
					SpaceLimit: 200,
					SpaceUsage: 199,
					UserLimit:  200,
					UserUsage:  199,
				},
			},
		},
	}

	got := pool.GetBestAccount()
	if got == nil || got.UserEmail != "business@example.com" {
		t.Fatalf("expected full Notion AI plan to win, got %#v", got)
	}
}

func TestNextRoundRobinsRegardlessOfQuota(t *testing.T) {
	// Next() should rotate through accounts regardless of remaining quota.
	pool := &AccountPool{
		accounts: []*Account{
			{
				UserEmail: "low@example.com",
				QuotaInfo: &QuotaInfo{
					IsEligible: true,
					SpaceLimit: 200,
					SpaceUsage: 160,
					UserLimit:  200,
					UserUsage:  160,
				},
			},
			{
				UserEmail: "high@example.com",
				QuotaInfo: &QuotaInfo{
					IsEligible: true,
					SpaceLimit: 200,
					SpaceUsage: 40,
					UserLimit:  200,
					UserUsage:  40,
				},
			},
		},
	}

	first := pool.Next()
	second := pool.Next()
	if first == nil || second == nil {
		t.Fatal("expected non-nil accounts")
	}
	if first.UserEmail == second.UserEmail {
		t.Fatalf("expected different accounts on consecutive calls, both got %s", first.UserEmail)
	}
}

func TestNextExcludingRoundRobinsSkippingExcluded(t *testing.T) {
	low := &Account{
		UserEmail: "low@example.com",
		QuotaInfo: &QuotaInfo{
			IsEligible: true,
			SpaceLimit: 200,
			SpaceUsage: 170,
			UserLimit:  200,
			UserUsage:  170,
		},
	}
	mid := &Account{
		UserEmail: "mid@example.com",
		QuotaInfo: &QuotaInfo{
			IsEligible: true,
			SpaceLimit: 200,
			SpaceUsage: 80,
			UserLimit:  200,
			UserUsage:  80,
		},
	}
	high := &Account{
		UserEmail: "high@example.com",
		QuotaInfo: &QuotaInfo{
			IsEligible: true,
			SpaceLimit: 200,
			SpaceUsage: 20,
			UserLimit:  200,
			UserUsage:  20,
		},
	}
	pool := &AccountPool{
		accounts: []*Account{low, mid, high},
	}

	// With high excluded, NextExcluding should rotate between low and mid
	exclude := map[*Account]bool{high: true}
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		got := pool.NextExcluding(exclude)
		if got == nil {
			t.Fatal("expected non-nil account")
		}
		if got.UserEmail == "high@example.com" {
			t.Fatal("excluded account should not be returned")
		}
		seen[got.UserEmail]++
	}
	if seen["low@example.com"] == 0 || seen["mid@example.com"] == 0 {
		t.Fatalf("expected both low and mid to appear, got: %v", seen)
	}
}

func TestNextForResearchPrefersFullAIPlanWithoutNumericCap(t *testing.T) {
	pool := &AccountPool{
		accounts: []*Account{
			{
				UserEmail: "trial@example.com",
				PlanType:  "plus",
				QuotaInfo: &QuotaInfo{IsEligible: true, ResearchModeUsage: 99},
			},
			{
				UserEmail: "business@example.com",
				PlanType:  "business",
				QuotaInfo: &QuotaInfo{
					IsEligible:        true,
					ResearchModeUsage: 99,
				},
			},
		},
	}

	got := pool.NextForResearch()
	if got == nil || got.UserEmail != "business@example.com" {
		t.Fatalf("expected Business account regardless of raw research usage, got %#v", got)
	}
}

func TestNextForResearchUsesExactPaidPlanHierarchy(t *testing.T) {
	team := &Account{UserEmail: "team@example.com", PlanType: "team"}
	business := &Account{UserEmail: "business@example.com", PlanType: "business"}
	enterprise := &Account{UserEmail: "enterprise@example.com", PlanType: "enterprise"}
	pool := &AccountPool{accounts: []*Account{team, business, enterprise}}

	if got := pool.NextForResearch(); got != enterprise {
		t.Fatalf("research picker=%#v, want Enterprise", got)
	}
	if got := pool.NextBestExcluding(map[*Account]bool{enterprise: true}); got != business {
		t.Fatalf("research failover picker=%#v, want Business", got)
	}
}

// ── Round-Robin distribution tests ──

func TestNextDistributesEvenly(t *testing.T) {
	// Three accounts with identical quota — Next() must rotate across all three.
	pool := &AccountPool{
		accounts: []*Account{
			{UserEmail: "a@example.com", QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 100, UserLimit: 200, UserUsage: 100}},
			{UserEmail: "b@example.com", QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 100, UserLimit: 200, UserUsage: 100}},
			{UserEmail: "c@example.com", QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 100, UserLimit: 200, UserUsage: 100}},
		},
	}
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		acc := pool.Next()
		if acc == nil {
			t.Fatal("expected non-nil account")
		}
		seen[acc.UserEmail]++
	}
	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		if seen[email] != 2 {
			t.Fatalf("expected each account called 2 times in 6 calls, got distribution: %v", seen)
		}
	}
}

func TestNextDistributesWithUnequalQuota(t *testing.T) {
	// Even with different remaining quotas, Next() should still round-robin.
	pool := &AccountPool{
		accounts: []*Account{
			{UserEmail: "low@example.com", QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 160, UserLimit: 200, UserUsage: 160}},
			{UserEmail: "high@example.com", QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 40, UserLimit: 200, UserUsage: 40}},
		},
	}
	seen := map[string]int{}
	for i := 0; i < 4; i++ {
		acc := pool.Next()
		if acc == nil {
			t.Fatal("expected non-nil account")
		}
		seen[acc.UserEmail]++
	}
	for _, email := range []string{"low@example.com", "high@example.com"} {
		if seen[email] != 2 {
			t.Fatalf("expected each account called 2 times in 4 calls, got distribution: %v", seen)
		}
	}
}

func TestNextExcludingRoundRobinsAmongUntried(t *testing.T) {
	a := &Account{UserEmail: "a@example.com", QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 20, UserLimit: 200, UserUsage: 20}}
	b := &Account{UserEmail: "b@example.com", QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 80, UserLimit: 200, UserUsage: 80}}
	c := &Account{UserEmail: "c@example.com", QuotaInfo: &QuotaInfo{IsEligible: true, SpaceLimit: 200, SpaceUsage: 170, UserLimit: 200, UserUsage: 170}}
	pool := &AccountPool{
		accounts: []*Account{a, b, c},
	}

	// Exclude 'a': both b and c must appear, 'a' must never appear
	exclude := map[*Account]bool{a: true}
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		acc := pool.NextExcluding(exclude)
		if acc == nil {
			t.Fatal("expected non-nil account")
		}
		if acc.UserEmail == "a@example.com" {
			t.Fatal("excluded account should not be returned")
		}
		seen[acc.UserEmail]++
	}
	if seen["b@example.com"] == 0 || seen["c@example.com"] == 0 {
		t.Fatalf("expected both b and c to appear, got: %v", seen)
	}
}

func TestBasicRemainingUsesMostConstrainedQuota(t *testing.T) {
	info := &QuotaInfo{
		SpaceLimit: 200,
		SpaceUsage: 20,
		UserLimit:  200,
		UserUsage:  190,
	}

	if got := basicRemaining(info); got != 10 {
		t.Fatalf("expected effective remaining 10, got %d", got)
	}
}

func TestIsFreePlanDoesNotTreatPremiumCreditsAsPlanEvidence(t *testing.T) {
	acc := &Account{
		PlanType: "personal",
		QuotaInfo: &QuotaInfo{
			HasPremium:     true,
			PremiumBalance: 1300000,
			PremiumLimit:   1300000,
		},
	}

	if !isFreePlan(acc) {
		t.Fatal("Personal plan must remain a complimentary plan despite private Premium fields")
	}
}

func TestCurrentNotionAIPlanClassification(t *testing.T) {
	if !isFreePlan(&Account{PlanType: "plus", QuotaInfo: &QuotaInfo{}}) {
		t.Fatal("Plus should be treated as a complimentary trial")
	}
	if !isFreePlan(&Account{PlanType: "plus", QuotaInfo: &QuotaInfo{HasPremium: true, PremiumBalance: 300}}) {
		t.Fatal("Plus Premium diagnostics must not be treated as workspace-plan evidence")
	}
	if isFreePlan(&Account{PlanType: "team", QuotaInfo: &QuotaInfo{}}) {
		t.Fatal("Team should be treated as an included-AI paid plan")
	}
	if isFreePlan(&Account{PlanType: "business", QuotaInfo: &QuotaInfo{}}) {
		t.Fatal("Business should be treated as including full Notion AI")
	}
	if isFreePlan(&Account{PlanType: "enterprise", QuotaInfo: &QuotaInfo{}}) {
		t.Fatal("Enterprise should be treated as including full Notion AI")
	}
}
