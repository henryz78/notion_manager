package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckQuotaKeepsV1ResultWhenV2DiagnosticsFail(t *testing.T) {
	previousConfig := AppConfig
	previousBase := NotionAPIBase
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	t.Cleanup(func() {
		AppConfig = previousConfig
		NotionAPIBase = previousBase
		chromeHTTPClientForTest = previousClientOverride
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getAIUsageEligibility":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"isEligible": true,
				"spaceUsage": 12,
				"spaceLimit": 75,
				"userUsage": 7,
				"userLimit": 75,
				"researchModeUsage": 3
			}`))
		case "/getAIUsageEligibilityV2":
			http.Error(w, "temporary diagnostic failure", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client {
		return server.Client()
	}

	acc := &Account{
		UserEmail: "quota@example.com",
		SpaceID:   "space-id",
		TokenV2:   "test-token",
		QuotaInfo: &QuotaInfo{
			HasPremium:     true,
			PremiumBalance: 81,
			PremiumUsage:   19,
			PremiumLimit:   100,
		},
	}
	info, err := CheckQuota(acc)
	if err != nil {
		t.Fatalf("CheckQuota returned V2 diagnostic failure: %v", err)
	}
	if info == nil || !info.IsEligible {
		t.Fatalf("V1 eligibility was lost: %#v", info)
	}
	if info.SpaceUsage != 12 || info.SpaceLimit != 75 ||
		info.UserUsage != 7 || info.UserLimit != 75 ||
		info.ResearchModeUsage != 3 {
		t.Fatalf("unexpected V1 fields: %#v", info)
	}
	if !info.HasPremium || info.PremiumBalance != 81 || info.PremiumUsage != 19 || info.PremiumLimit != 100 {
		t.Fatalf("failed V2 diagnostics did not preserve the last known values: %#v", info)
	}
}

func TestCheckQuotaV1DoesNotCallV2Diagnostics(t *testing.T) {
	previousConfig := AppConfig
	previousBase := NotionAPIBase
	previousClientOverride := chromeHTTPClientForTest
	AppConfig = DefaultConfig()
	t.Cleanup(func() {
		AppConfig = previousConfig
		NotionAPIBase = previousBase
		chromeHTTPClientForTest = previousClientOverride
	})

	var v2Calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getAIUsageEligibility":
			_, _ = w.Write([]byte(`{"isEligible":true,"spaceUsage":1,"spaceLimit":75}`))
		case "/getAIUsageEligibilityV2":
			v2Calls++
			http.Error(w, "must not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NotionAPIBase = server.URL
	chromeHTTPClientForTest = func(time.Duration) *http.Client { return server.Client() }
	info, err := CheckQuotaV1(&Account{SpaceID: "space-id", TokenV2: "test-token"})
	if err != nil {
		t.Fatalf("CheckQuotaV1: %v", err)
	}
	if info == nil || !info.IsEligible {
		t.Fatalf("unexpected V1 result: %#v", info)
	}
	if v2Calls != 0 {
		t.Fatalf("routing V1 check called V2 %d time(s)", v2Calls)
	}
}

func TestCheckQuotaV1RejectsEmptySuccessSchema(t *testing.T) {
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

	info, err := CheckQuotaV1(&Account{SpaceID: "space-id", TokenV2: "test-token"})
	if err == nil {
		t.Fatalf("empty HTTP 200 schema was accepted as exhausted quota: %#v", info)
	}
	if info != nil {
		t.Fatalf("invalid V1 schema returned quota info: %#v", info)
	}
}
