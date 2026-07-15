package proxy

import (
	"sort"
	"strings"
)

// AccountSummary aggregates pool-wide counts and quota sums used by the
// dashboard. We compute it server-side so the dashboard does not need
// to download the full account list to render headline numbers — that's
// what `/admin/accounts?page=...` is for.
//
// JSON tags use snake_case to match the rest of the admin API.
type AccountSummary struct {
	ExhaustedOnly       int   `json:"exhausted_only"`
	NoWorkspace         int   `json:"no_workspace"`
	AuthInvalid         int   `json:"auth_invalid"`
	PremiumAccounts     int   `json:"premium_accounts"`
	ExhaustedTrials     int   `json:"exhausted_trials"`
	ResearchLimited     int   `json:"research_limited"` // deprecated: kept as 0 for API compatibility
	TotalResearchUsage  int   `json:"total_research_usage"`
	TotalRemaining      int   `json:"total_remaining"`
	TotalSpaceUsage     int   `json:"total_space_usage"`
	TotalSpaceLimit     int   `json:"total_space_limit"`
	TotalSpaceRemaining int   `json:"total_space_remaining"`
	TotalUserUsage      int   `json:"total_user_usage"`
	TotalUserLimit      int   `json:"total_user_limit"`
	TotalUserRemaining  int   `json:"total_user_remaining"`
	TotalPremiumBalance int64 `json:"total_premium_balance"`
	TotalPremiumLimit   int64 `json:"total_premium_limit"`
}

// summarizeAccounts walks the full account-detail list and produces the
// summary used by the dashboard headline cards. It treats missing fields
// as zero.
func summarizeAccounts(accounts []map[string]interface{}) AccountSummary {
	var s AccountSummary
	for _, a := range accounts {
		exh := mapBool(a, "exhausted")
		perm := mapBool(a, "permanent")
		nws := mapBool(a, "no_workspace")
		authInvalid := mapBool(a, "auth_invalid")
		if nws {
			s.NoWorkspace++
		}
		if authInvalid {
			s.AuthInvalid++
		}
		if (exh || perm) && !nws && !authInvalid {
			s.ExhaustedOnly++
		}
		if hasPremiumMap(a) {
			s.PremiumAccounts++
		}
		if isExhaustedComplimentaryMap(a) {
			s.ExhaustedTrials++
		}
		s.TotalResearchUsage += mapInt(a, "research_usage")
		s.TotalRemaining += mapInt(a, "remaining")
		s.TotalSpaceUsage += mapInt(a, "space_usage")
		s.TotalSpaceLimit += mapInt(a, "space_limit")
		s.TotalSpaceRemaining += mapInt(a, "space_remaining")
		s.TotalUserUsage += mapInt(a, "user_usage")
		s.TotalUserLimit += mapInt(a, "user_limit")
		s.TotalUserRemaining += mapInt(a, "user_remaining")
		s.TotalPremiumBalance += int64(mapInt(a, "premium_balance"))
		s.TotalPremiumLimit += int64(mapInt(a, "premium_limit"))
	}
	return s
}

func isExhaustedComplimentaryMap(a map[string]interface{}) bool {
	if !isComplimentaryPlanType(mapString(a, "plan")) || hasPremiumMap(a) {
		return false
	}
	if mapBool(a, "auth_invalid") || mapBool(a, "temporarily_unavailable") || mapBool(a, "no_workspace") {
		return false
	}
	return mapBool(a, "exhausted") || mapBool(a, "permanent")
}

// filterAccountDetails keeps only entries whose email/name/plan/space
// contains q (case-insensitive). An empty q is a no-op.
func filterAccountDetails(accounts []map[string]interface{}, q string) []map[string]interface{} {
	q = strings.TrimSpace(strings.ToLower(q))
	if q == "" {
		return accounts
	}
	out := make([]map[string]interface{}, 0, len(accounts))
	for _, a := range accounts {
		if matchAccountQuery(a, q) {
			out = append(out, a)
		}
	}
	return out
}

func matchAccountQuery(a map[string]interface{}, qLower string) bool {
	for _, k := range []string{"email", "name", "plan", "space"} {
		if s, ok := a[k].(string); ok && s != "" {
			if strings.Contains(strings.ToLower(s), qLower) {
				return true
			}
		}
	}
	return false
}

// sortAccountDetails sorts in-place using the same criteria the dashboard
// previously applied client-side:
//  1. Permanently exhausted accounts to the bottom.
//  2. Auth-invalid accounts to the bottom.
//  3. Accounts with no accessible workspace to the bottom.
//  4. Quota-exhausted accounts to the bottom.
//  5. Full Notion AI plans, then a live premium signal, then trial plans.
//  6. Stable fallback by name (lower-cased). Private quota counters are not
//     used for ordering because their public semantics are undocumented.
func sortAccountDetails(accounts []map[string]interface{}) {
	sort.SliceStable(accounts, func(i, j int) bool {
		ai, aj := mapBool(accounts[i], "permanent"), mapBool(accounts[j], "permanent")
		if ai != aj {
			return !ai
		}
		ai, aj = mapBool(accounts[i], "auth_invalid"), mapBool(accounts[j], "auth_invalid")
		if ai != aj {
			return !ai
		}
		ai, aj = mapBool(accounts[i], "no_workspace"), mapBool(accounts[j], "no_workspace")
		if ai != aj {
			return !ai
		}
		ai, aj = mapBool(accounts[i], "exhausted"), mapBool(accounts[j], "exhausted")
		if ai != aj {
			return !ai
		}
		ti, tj := notionAITierMap(accounts[i]), notionAITierMap(accounts[j])
		if ti != tj {
			return ti > tj
		}
		return strings.ToLower(mapString(accounts[i], "name")) <
			strings.ToLower(mapString(accounts[j], "name"))
	})
}

func notionAITierMap(a map[string]interface{}) int {
	if planIncludesFullNotionAI(mapString(a, "plan")) {
		return 2
	}
	if hasPremiumMap(a) {
		return 1
	}
	return 0
}

// paginateAccounts returns the slice [page*pageSize : page*pageSize+pageSize].
// pageSize <= 0 returns the input unchanged. Out-of-range pages return an
// empty (non-nil) slice so JSON encodes as `[]` rather than `null`.
func paginateAccounts(accounts []map[string]interface{}, page, pageSize int) []map[string]interface{} {
	if pageSize <= 0 {
		return accounts
	}
	if page < 0 {
		page = 0
	}
	start := page * pageSize
	if start >= len(accounts) {
		return []map[string]interface{}{}
	}
	end := start + pageSize
	if end > len(accounts) {
		end = len(accounts)
	}
	return accounts[start:end]
}

// hasPremiumMap mirrors the frontend's hasPremiumAccess: any of has_premium,
// premium_limit > 0, or premium_balance > 0 marks the account as having
// premium credits.
func hasPremiumMap(a map[string]interface{}) bool {
	if v, _ := a["has_premium"].(bool); v {
		return true
	}
	if mapInt(a, "premium_limit") > 0 {
		return true
	}
	if mapInt(a, "premium_balance") > 0 {
		return true
	}
	return false
}

// mapBool / mapInt / mapString are tiny accessors that paper over the
// fact that GetAccountDetails stores values as interface{} (we round-trip
// JSON numbers through float64 in tests, but Go-native ints in prod).
func mapBool(a map[string]interface{}, k string) bool {
	v, _ := a[k].(bool)
	return v
}

func mapString(a map[string]interface{}, k string) string {
	v, _ := a[k].(string)
	return v
}

func mapInt(a map[string]interface{}, k string) int {
	switch v := a[k].(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	}
	return 0
}
