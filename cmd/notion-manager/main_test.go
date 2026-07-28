package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"notion-manager/internal/proxy"
)

func TestRequiresAPIKey(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v1/messages", want: true},
		{path: "/v1/chat/completions", want: true},
		{path: "/v1/responses", want: true},
		{path: "/v1/models", want: true},
		{path: "/models", want: true},
		{path: "/health", want: false},
		{path: "/dashboard/", want: false},
	}

	for _, tc := range tests {
		if got := requiresAPIKey(tc.path); got != tc.want {
			t.Fatalf("requiresAPIKey(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestResolveVolumeFile(t *testing.T) {
	if got := resolveVolumeFile("token.txt", "/app/accounts", true); got != filepath.Join("/app/accounts", "token.txt") {
		t.Fatalf("token path=%q", got)
	}
	if got := resolveVolumeFile("accounts/.register_history.json", "/app/accounts", true); got != filepath.Join("/app/accounts", ".register_history.json") {
		t.Fatalf("history path=%q", got)
	}
	if got := resolveVolumeFile("token.txt", "accounts", true); got != filepath.Join("accounts", "token.txt") {
		t.Fatalf("local accounts path=%q", got)
	}
	absoluteToken := filepath.Join(t.TempDir(), "token.txt")
	absoluteAccounts := filepath.Join(t.TempDir(), "accounts")
	if got := resolveVolumeFile(absoluteToken, absoluteAccounts, true); got != absoluteToken {
		t.Fatalf("absolute path=%q", got)
	}
}

func TestAPIKeyAuthMiddleware_ProtectsModelsRoutes(t *testing.T) {
	handler := apiKeyAuthMiddleware("sk-test", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name    string
		path    string
		headers map[string]string
		want    int
	}{
		{name: "models missing key", path: "/models", want: http.StatusUnauthorized},
		{name: "models wrong key", path: "/models", headers: map[string]string{"Authorization": "Bearer sk-wrong"}, want: http.StatusUnauthorized},
		{name: "models bearer", path: "/models", headers: map[string]string{"Authorization": "Bearer sk-test"}, want: http.StatusNoContent},
		{name: "v1 models x-api-key", path: "/v1/models", headers: map[string]string{"x-api-key": "sk-test"}, want: http.StatusNoContent},
		{name: "chat missing key", path: "/v1/chat/completions", want: http.StatusUnauthorized},
		{name: "responses x-api-key", path: "/v1/responses", headers: map[string]string{"x-api-key": "sk-test"}, want: http.StatusNoContent},
		{name: "health no auth", path: "/health", want: http.StatusNoContent},
	}

	for _, tc := range tests {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		for key, value := range tc.headers {
			req.Header.Set(key, value)
		}

		handler.ServeHTTP(rec, req)

		if rec.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d body=%s", tc.name, tc.want, rec.Code, rec.Body.String())
		}
	}
}

func TestNewMux_RegistersModelsRoutes(t *testing.T) {
	original := proxy.SnapshotModelMap()
	proxy.ReplaceModelMap(map[string]string{
		"opus-4.6": "avocado-froyo-medium",
	})
	t.Cleanup(func() {
		proxy.ReplaceModelMap(original)
	})

	pool := proxy.NewAccountPool()
	dashAuth := proxy.NewDashboardAuth("", "sk-test")
	usageStats := proxy.InitUsageStats("")
	requestHistory, _ := proxy.NewRequestHistoryStore("", 100)
	regDeps := &proxy.RegisterJobsDeps{Pool: pool, AccountsDir: "", Auth: dashAuth}
	batchManager, _ := proxy.NewAccountBatchManager(pool, "", "")
	mux := newMux(pool, "", "config.yaml", "sk-test", dashAuth, usageStats, requestHistory, regDeps, batchManager)
	handler := apiKeyAuthMiddleware("sk-test", mux)

	for _, path := range []string{"/v1/models", "/models"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer sk-test")
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestNewMux_RegistersOpenAIRoutes(t *testing.T) {
	originalConfig := proxy.AppConfig
	proxy.AppConfig = proxy.DefaultConfig()
	t.Cleanup(func() {
		proxy.AppConfig = originalConfig
	})

	pool := proxy.NewAccountPool()
	dashAuth := proxy.NewDashboardAuth("", "sk-test")
	usageStats := proxy.InitUsageStats("")
	requestHistory, _ := proxy.NewRequestHistoryStore("", 100)
	regDeps := &proxy.RegisterJobsDeps{Pool: pool, AccountsDir: "", Auth: dashAuth}
	batchManager, _ := proxy.NewAccountBatchManager(pool, "", "")
	mux := newMux(pool, "", "config.yaml", "sk-test", dashAuth, usageStats, requestHistory, regDeps, batchManager)
	handler := apiKeyAuthMiddleware("sk-test", mux)

	tests := []struct {
		path string
		body string
	}{
		{path: "/v1/chat/completions", body: `{"messages":[]}`},
		{path: "/v1/responses", body: `{"input":"ping"}`},
	}

	for _, tc := range tests {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer sk-test")
		req.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusNotFound {
			t.Fatalf("%s: expected registered handler, got 404", tc.path)
		}
	}
}

func TestNewMux_RegistersBackupRoute(t *testing.T) {
	originalConfig := proxy.AppConfig
	proxy.AppConfig = proxy.DefaultConfig()
	t.Cleanup(func() {
		proxy.AppConfig = originalConfig
	})

	pool := proxy.NewAccountPool()
	dashAuth := proxy.NewDashboardAuth("", "sk-test")
	usageStats := proxy.InitUsageStats("")
	requestHistory, _ := proxy.NewRequestHistoryStore("", 100)
	accountsDir := t.TempDir()
	regDeps := &proxy.RegisterJobsDeps{Pool: pool, AccountsDir: accountsDir, Auth: dashAuth}
	batchManager, _ := proxy.NewAccountBatchManager(pool, accountsDir, "")
	mux := newMux(pool, accountsDir, "config.yaml", "sk-test", dashAuth, usageStats, requestHistory, regDeps, batchManager)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/backup", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("backup route: expected 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("backup route: unexpected content type %q", contentType)
	}
}

func TestStartupAPIKeyLogValueNeverExposesKey(t *testing.T) {
	const secret = "sk-test-secret-value"
	if got, want := startupAPIKeyLogValue(secret), "configured (hidden)"; got != want {
		t.Fatalf("startup API key log value = %q, want %q", got, want)
	}
	if got, want := startupAPIKeyLogValue(""), "not configured"; got != want {
		t.Fatalf("empty startup API key log value = %q, want %q", got, want)
	}
}
