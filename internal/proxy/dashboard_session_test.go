package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDashboardSessionSurvivesAuthManagerRestart(t *testing.T) {
	passwordHash := HashAdminPassword("test-password")
	firstProcess := NewDashboardAuth(passwordHash, "stable-api-key", "stable-session-secret")

	rec := httptest.NewRecorder()
	firstProcess.CreateSession(rec)
	cookie := responseCookie(t, rec, dashboardSessionCookieName)

	// A fresh DashboardAuth represents a new Railway process/container. It has
	// no in-memory state from the process that issued the cookie.
	secondProcess := NewDashboardAuth(passwordHash, "stable-api-key", "stable-session-secret")
	req := httptest.NewRequest(http.MethodGet, "/admin/accounts", nil)
	req.AddCookie(cookie)

	if !secondProcess.ValidateSession(req) {
		t.Fatal("signed dashboard session did not survive process restart")
	}
	if cookie.MaxAge != int(dashboardSessionDuration/time.Second) {
		t.Fatalf("cookie MaxAge = %d, want %d", cookie.MaxAge, int(dashboardSessionDuration/time.Second))
	}
	if cookie.Expires.IsZero() {
		t.Fatal("persistent dashboard cookie must include Expires")
	}
}

func TestDashboardSessionRejectsTamperingAndSecretChanges(t *testing.T) {
	passwordHash := HashAdminPassword("test-password")
	auth := NewDashboardAuth(passwordHash, "stable-api-key", "stable-session-secret")
	token := auth.newSessionToken(time.Now())

	if !auth.validateSessionToken(token, time.Now()) {
		t.Fatal("fresh session token should be valid")
	}
	if auth.validateSessionToken(token+"tampered", time.Now()) {
		t.Fatal("tampered session token was accepted")
	}
	if NewDashboardAuth(passwordHash, "stable-api-key", "different-session-secret").validateSessionToken(token, time.Now()) {
		t.Fatal("session token survived signing-secret change")
	}
}

func TestDashboardSessionSurvivesPlaintextEnvPasswordRehash(t *testing.T) {
	firstProcess := NewDashboardAuth(HashAdminPassword("same-password"), "api-key", "same-password")
	token := firstProcess.newSessionToken(time.Now())

	// EnsureAdminPassword may create a different salted hash in a replacement
	// container, while the original ADMIN_PASSWORD environment value is stable.
	secondProcess := NewDashboardAuth(HashAdminPassword("same-password"), "api-key", "same-password")
	if !secondProcess.validateSessionToken(token, time.Now()) {
		t.Fatal("session did not survive rehashing the same environment password")
	}
}

func TestDashboardSessionRejectsExpiredToken(t *testing.T) {
	auth := NewDashboardAuth(HashAdminPassword("test-password"), "stable-api-key", "stable-session-secret")
	issuedAt := time.Now().Add(-dashboardSessionDuration - time.Minute)
	token := auth.newSessionToken(issuedAt)

	if auth.validateSessionToken(token, time.Now()) {
		t.Fatal("expired session token was accepted")
	}
}

func TestDashboardLogoutClearsPersistentCookie(t *testing.T) {
	auth := NewDashboardAuth(HashAdminPassword("test-password"), "stable-api-key", "stable-session-secret")
	rec := httptest.NewRecorder()
	auth.DestroySession(rec, httptest.NewRequest(http.MethodPost, "/dashboard/auth/logout", nil))
	cookie := responseCookie(t, rec, dashboardSessionCookieName)

	if cookie.MaxAge != -1 || cookie.Value != "" {
		t.Fatalf("logout cookie = value %q max-age %d, want cleared", cookie.Value, cookie.MaxAge)
	}
}

func responseCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response did not include cookie %q; Set-Cookie=%q", name, strings.Join(rec.Result().Header.Values("Set-Cookie"), "; "))
	return nil
}
