package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVersionStatusServiceComparesLatestSuccessfulDockerRun(t *testing.T) {
	original := BuildVersion
	t.Cleanup(func() { BuildVersion = original })
	BuildVersion = "abcdef1234567890"
	t.Setenv("APP_VERSION", "")
	t.Setenv("RAILWAY_GIT_COMMIT_SHA", "")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workflow_runs":[{"head_sha":"abcdef1234567890","html_url":"https://example.test/run/1","updated_at":"2026-07-26T20:00:00Z"}]}`))
	}))
	t.Cleanup(server.Close)

	service := &VersionStatusService{
		client:      server.Client(),
		workflowURL: server.URL,
		cacheTTL:    time.Minute,
	}
	status := service.Get(t.Context(), false)
	if status.Status != "up_to_date" || status.LatestVersion != BuildVersion || status.RunURL == "" || status.Error != "" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestVersionStatusServiceReportsUpdateAndCachesResult(t *testing.T) {
	original := BuildVersion
	t.Cleanup(func() { BuildVersion = original })
	BuildVersion = "1111111"
	t.Setenv("APP_VERSION", "")
	t.Setenv("RAILWAY_GIT_COMMIT_SHA", "")

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workflow_runs":[{"head_sha":"2222222222222222","updated_at":"2026-07-26T20:00:00Z"}]}`))
	}))
	t.Cleanup(server.Close)

	service := &VersionStatusService{client: server.Client(), workflowURL: server.URL, cacheTTL: time.Minute}
	first := service.Get(t.Context(), false)
	second := service.Get(t.Context(), false)
	if first.Status != "update_available" || second.LatestVersion != first.LatestVersion || requests != 1 {
		t.Fatalf("unexpected cached status: first=%+v second=%+v requests=%d", first, second, requests)
	}
}

func TestHandleAdminVersionStatusRequiresSession(t *testing.T) {
	auth := NewDashboardAuth(HashAdminPassword("password"), "api-key", "session-secret")
	handler := HandleAdminVersionStatus(&VersionStatusService{}, auth)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/admin/version", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
