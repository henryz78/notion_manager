package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardIndexInjectsVersionAndDisablesDocumentCaching(t *testing.T) {
	original := BuildVersion
	t.Cleanup(func() { BuildVersion = original })
	BuildVersion = "abcdef1234567890"
	t.Setenv("APP_VERSION", "")
	t.Setenv("RAILWAY_GIT_COMMIT_SHA", "")

	req := httptest.NewRequest(http.MethodGet, "/dashboard/", nil)
	rec := httptest.NewRecorder()
	HandleDashboard("sk-test", NewDashboardAuth("", "")).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `meta name="app-version" content="abcdef1234567890"`) {
		t.Fatalf("version meta missing from dashboard HTML: %s", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma=%q", got)
	}
}

func TestHealthExposesRunningVersionHeader(t *testing.T) {
	original := BuildVersion
	t.Cleanup(func() { BuildVersion = original })
	BuildVersion = "health-version"
	t.Setenv("APP_VERSION", "")
	t.Setenv("RAILWAY_GIT_COMMIT_SHA", "")

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	HandleHealth(NewAccountPool()).ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Notion-Manager-Version"); got != "health-version" {
		t.Fatalf("version header=%q", got)
	}
}
