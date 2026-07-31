package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspacePreferencePrioritizesUsableIncludedAIPlan(t *testing.T) {
	enabledTeam := workspacePreference(notionWorkspaceMetadata{PlanType: "team", AIEnabled: true})
	enabledPlus := workspacePreference(notionWorkspaceMetadata{PlanType: "plus", AIEnabled: true})
	disabledTeam := workspacePreference(notionWorkspaceMetadata{PlanType: "team", AIEnabled: false})
	if enabledTeam <= enabledPlus {
		t.Fatalf("enabled Team score=%d, want greater than enabled Plus=%d", enabledTeam, enabledPlus)
	}
	if enabledPlus <= disabledTeam {
		t.Fatalf("enabled Plus score=%d, want greater than disabled Team=%d", enabledPlus, disabledTeam)
	}
}

func TestCheckUserWorkspaceProfileRefreshesSelectedPlan(t *testing.T) {
	previousConfig := AppConfig
	previousBase := NotionAPIBase
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	t.Cleanup(func() {
		AppConfig = previousConfig
		NotionAPIBase = previousBase
		chromeHTTPClientForTest = previousClientOverride
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"recordMap": {
				"user_root": {
					"user-id": {"value":{"value":{
						"space_views":["view-a","view-b"],
						"space_view_pointers":[
							{"spaceId":"space-a","id":"view-a"},
							{"spaceId":"space-b","id":"view-b"}
						]
					}}}
				},
				"space": {
					"space-a": {"value":{"value":{
						"id":"space-a",
						"name":"Paid workspace",
						"plan_type":"team",
						"settings":{"disable_ai_feature":false}
					}}}
				}
			}
		}`))
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	result, err := CheckUserWorkspaceProfile(&Account{
		UserID:  "user-id",
		SpaceID: "space-a",
		TokenV2: "test-token",
	})
	if err != nil {
		t.Fatalf("CheckUserWorkspaceProfile: %v", err)
	}
	if result.Count != 2 || result.PlanType != "team" || !result.AIEnabled {
		t.Fatalf("unexpected workspace profile: %#v", result)
	}
}

func TestCheckUserWorkspaceProfileRejectsRemovedSelectedWorkspace(t *testing.T) {
	previousConfig := AppConfig
	previousBase := NotionAPIBase
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	t.Cleanup(func() {
		AppConfig = previousConfig
		NotionAPIBase = previousBase
		chromeHTTPClientForTest = previousClientOverride
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"recordMap": {
				"user_root": {
					"user-id": {"value":{"value":{
						"space_views":["view-b"],
						"space_view_pointers":[{"spaceId":"space-b","id":"view-b"}]
					}}}
				},
				"space": {}
			}
		}`))
	}))
	defer server.Close()
	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	result, err := CheckUserWorkspaceProfile(&Account{
		UserID:  "user-id",
		SpaceID: "removed-space",
		TokenV2: "test-token",
	})
	if err != nil {
		t.Fatalf("CheckUserWorkspaceProfile: %v", err)
	}
	if result.Count != 0 {
		t.Fatalf("removed selected workspace remained routable: %#v", result)
	}
}

func TestCheckUserWorkspaceProfileRejectsEmptySuccessSchema(t *testing.T) {
	previousConfig := AppConfig
	previousBase := NotionAPIBase
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	t.Cleanup(func() {
		AppConfig = previousConfig
		NotionAPIBase = previousBase
		chromeHTTPClientForTest = previousClientOverride
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }

	result, err := CheckUserWorkspaceProfile(&Account{
		UserID:  "user-id",
		SpaceID: "space-id",
		TokenV2: "test-token",
	})
	if err == nil {
		t.Fatalf("empty HTTP 200 schema was accepted as zero workspaces: %#v", result)
	}
}

func TestAIDisabledTeamIsNotRoutableOrReportedUnlimited(t *testing.T) {
	acc := &Account{
		UserEmail: "disabled-ai@example.com",
		PlanType:  "team",
		QuotaInfo: &QuotaInfo{IsEligible: true},
	}
	pool := newPool(acc)
	pool.applyWorkspaceProfile(acc, WorkspaceProbeResult{
		Count:          1,
		PlanType:       "team",
		AIEnabled:      false,
		AIEnabledKnown: true,
	})

	if selected := pool.Next(); selected != nil {
		t.Fatalf("AI-disabled Team account remained routable: %#v", selected)
	}
	if got := pool.AvailableCount(); got != 0 {
		t.Fatalf("available count=%d, want 0 for AI-disabled Team", got)
	}

	details := pool.GetAccountDetails()
	if len(details) != 1 {
		t.Fatalf("account details len=%d, want 1", len(details))
	}
	if disabled, _ := details[0]["ai_disabled"].(bool); !disabled {
		t.Fatalf("dashboard did not expose AI-disabled state: %#v", details[0])
	}
	if unlimited, _ := details[0]["quota_unlimited"].(bool); unlimited {
		t.Fatalf("AI-disabled Team was reported as unlimited: %#v", details[0])
	}
	if summary := summarizeAccounts(details); summary.UnlimitedAccounts != 0 {
		t.Fatalf("summary counted AI-disabled Team as unlimited: %+v", summary)
	}
}

func TestAIDisabledWorkspaceSurvivesSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "disabled-ai.json")
	if err := os.WriteFile(path, []byte(`{
  "token_v2": "token",
  "user_id": "user-id",
  "user_email": "disabled-ai@example.com",
  "space_id": "space-id"
}`), 0o600); err != nil {
		t.Fatalf("write account: %v", err)
	}

	acc := &Account{
		TokenV2:   "token",
		UserID:    "user-id",
		UserEmail: "disabled-ai@example.com",
		SpaceID:   "space-id",
		PlanType:  "team",
	}
	pool := newPool(acc)
	pool.applyWorkspaceProfile(acc, WorkspaceProbeResult{
		Count:          1,
		PlanType:       "team",
		AIEnabled:      false,
		AIEnabledKnown: true,
	})
	pool.SaveAccounts(dir)

	reloaded := NewAccountPool()
	if err := reloaded.LoadFromDir(dir); err != nil {
		t.Fatalf("reload accounts: %v", err)
	}
	if selected := reloaded.Next(); selected != nil {
		t.Fatalf("AI-disabled account became routable after reload: %#v", selected)
	}
	profile := reloaded.accounts[0].profileSnapshot()
	if profile.AIEnabled == nil || *profile.AIEnabled {
		t.Fatalf("AI-disabled state was not persisted: %#v", profile)
	}
}

func TestUnknownWorkspaceAIProbePreservesKnownDisabledState(t *testing.T) {
	disabled := false
	checkedAt := time.Now()
	account := &Account{
		UserEmail: "disabled@example.com", WorkspaceAIEnabled: &disabled,
		WorkspaceCheckedAt: &checkedAt, SpaceCount: 1,
	}
	pool := newPool(account)
	pool.applyWorkspaceProfile(account, WorkspaceProbeResult{Count: 1, PlanType: "team"})
	profile := account.profileSnapshot()
	if profile.AIEnabled == nil || *profile.AIEnabled {
		t.Fatalf("unknown probe cleared known disabled state: %#v", profile.AIEnabled)
	}
	if pool.GetByEmail(account.UserEmail) != nil {
		t.Fatal("known AI-disabled account became routable after an incomplete probe")
	}
}
