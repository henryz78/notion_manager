package proxy

import (
	"encoding/json"
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
	HandleDashboard(NewDashboardAuth("", "")).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `meta name="app-version" content="abcdef1234567890"`) {
		t.Fatalf("version meta missing from dashboard HTML: %s", body)
	}
	if strings.Contains(body, "sk-test") || strings.Contains(body, `meta name="api-key"`) {
		t.Fatalf("dashboard HTML exposed API credentials: %s", body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
		t.Fatalf("Cache-Control=%q", got)
	}
	if got := rec.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma=%q", got)
	}
}

func TestDashboardAPIKeyRequiresSessionAndOnlyRevealsOnDemand(t *testing.T) {
	const apiKey = "sk-secret-dashboard-key"
	auth := NewDashboardAuth(HashAdminPassword("password"), apiKey, "session-secret")
	handler := HandleAdminAPIKey(apiKey, auth)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/admin/api-key?reveal=1", nil))
	if unauthorized.Code != http.StatusUnauthorized || strings.Contains(unauthorized.Body.String(), apiKey) {
		t.Fatalf("unauthorized response=%d %s", unauthorized.Code, unauthorized.Body.String())
	}

	sessionRecorder := httptest.NewRecorder()
	auth.CreateSession(sessionRecorder)
	cookie := responseCookie(t, sessionRecorder, dashboardSessionCookieName)

	maskedRequest := httptest.NewRequest(http.MethodGet, "/admin/api-key", nil)
	maskedRequest.AddCookie(cookie)
	maskedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(maskedRecorder, maskedRequest)
	if maskedRecorder.Code != http.StatusOK || strings.Contains(maskedRecorder.Body.String(), apiKey) {
		t.Fatalf("masked response=%d %s", maskedRecorder.Code, maskedRecorder.Body.String())
	}
	var masked map[string]string
	if err := json.Unmarshal(maskedRecorder.Body.Bytes(), &masked); err != nil {
		t.Fatal(err)
	}
	if masked["masked"] != maskAPIKey(apiKey) || masked["value"] != "" {
		t.Fatalf("unexpected masked payload: %+v", masked)
	}

	revealRequest := httptest.NewRequest(http.MethodGet, "/admin/api-key?reveal=1", nil)
	revealRequest.AddCookie(cookie)
	revealRecorder := httptest.NewRecorder()
	handler.ServeHTTP(revealRecorder, revealRequest)
	var revealed map[string]string
	if err := json.Unmarshal(revealRecorder.Body.Bytes(), &revealed); err != nil {
		t.Fatal(err)
	}
	if revealed["value"] != apiKey || revealRecorder.Header().Get("Cache-Control") != "no-store, max-age=0" {
		t.Fatalf("unexpected reveal payload or cache policy: %+v headers=%v", revealed, revealRecorder.Header())
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
	for _, privateField := range []string{`"accounts"`, `"available"`, `"quota"`} {
		if strings.Contains(rec.Body.String(), privateField) {
			t.Fatalf("public health response exposed %s: %s", privateField, rec.Body.String())
		}
	}
}

func TestAdminHealthRequiresDashboardSession(t *testing.T) {
	auth := NewDashboardAuth(HashAdminPassword("password"), "api-key", "session-secret")
	handler := HandleAdminHealth(NewAccountPool(), auth)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/admin/health", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	sessionRecorder := httptest.NewRecorder()
	auth.CreateSession(sessionRecorder)
	req := httptest.NewRequest(http.MethodGet, "/admin/health", nil)
	req.AddCookie(responseCookie(t, sessionRecorder, dashboardSessionCookieName))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"accounts"`) {
		t.Fatalf("authorized status=%d body=%s", rec.Code, rec.Body.String())
	}
}
