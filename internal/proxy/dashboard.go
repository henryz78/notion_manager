package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"notion-manager/internal/web"
)

// DashboardAuth manages dashboard session authentication.
type DashboardAuth struct {
	adminPasswordHash string // "$sha256$salt$hash" format
	sessionSecret     string // stable secret used to sign persistent sessions
}

const (
	dashboardSessionCookieName = "dashboard_session"
	dashboardSessionVersion    = "v1"
	dashboardSessionDuration   = 30 * 24 * time.Hour
)

// NewDashboardAuth creates a new auth manager.
func NewDashboardAuth(adminPasswordHash, _ string, sessionSecrets ...string) *DashboardAuth {
	sessionSecret := adminPasswordHash
	if len(sessionSecrets) > 0 && sessionSecrets[0] != "" {
		sessionSecret = sessionSecrets[0]
	}
	return &DashboardAuth{
		adminPasswordHash: adminPasswordHash,
		sessionSecret:     sessionSecret,
	}
}

// HasAdminPassword returns true if an admin password is configured.
func (da *DashboardAuth) HasAdminPassword() bool {
	return da.adminPasswordHash != "" && IsAdminPasswordHashed(da.adminPasswordHash)
}

// ValidateSession checks the signed dashboard cookie. The cookie is stateless,
// so it remains valid when Railway sleeps, restarts, or replaces the container.
func (da *DashboardAuth) ValidateSession(r *http.Request) bool {
	c, err := r.Cookie(dashboardSessionCookieName)
	if err != nil {
		return false
	}
	return da.validateSessionToken(c.Value, time.Now())
}

// CreateSession creates a new dashboard session and sets the cookie.
func (da *DashboardAuth) CreateSession(w http.ResponseWriter) {
	now := time.Now()
	expiry := now.Add(dashboardSessionDuration)
	http.SetCookie(w, &http.Cookie{
		Name:     dashboardSessionCookieName,
		Value:    da.newSessionToken(now),
		Path:     "/",
		HttpOnly: true,
		MaxAge:   int(dashboardSessionDuration / time.Second),
		Expires:  expiry,
		SameSite: http.SameSiteLaxMode,
	})
}

// DestroySession clears the browser's signed session cookie.
func (da *DashboardAuth) DestroySession(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: dashboardSessionCookieName, Value: "", Path: "/",
		HttpOnly: true, MaxAge: -1,
	})
}

func (da *DashboardAuth) newSessionToken(now time.Time) string {
	expiresAt := now.Add(dashboardSessionDuration).Unix()
	nonce := strings.ReplaceAll(generateUUIDv4(), "-", "")
	payload := fmt.Sprintf("%s.%d.%s", dashboardSessionVersion, expiresAt, nonce)
	return payload + "." + da.signSessionPayload(payload)
}

func (da *DashboardAuth) validateSessionToken(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != dashboardSessionVersion || parts[2] == "" || parts[3] == "" {
		return false
	}

	expiresAt, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expiresAt <= now.Unix() {
		return false
	}

	payload := strings.Join(parts[:3], ".")
	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	expectedSignature, err := base64.RawURLEncoding.DecodeString(da.signSessionPayload(payload))
	if err != nil {
		return false
	}
	return hmac.Equal(providedSignature, expectedSignature)
}

func (da *DashboardAuth) signSessionPayload(payload string) string {
	mac := hmac.New(sha256.New, da.sessionSigningKey())
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (da *DashboardAuth) sessionSigningKey() []byte {
	digest := sha256.Sum256([]byte("notion-manager/dashboard-session/v1\x00" + da.sessionSecret))
	return digest[:]
}

// RequireAuth is middleware that checks for valid dashboard session.
// Static assets (JS/CSS) are served without auth so the login page can load.
func (da *DashboardAuth) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/dashboard")

		// Always allow static assets (login page needs JS/CSS)
		if strings.HasPrefix(path, "/assets/") {
			next.ServeHTTP(w, r)
			return
		}
		// Always allow auth API endpoints
		if strings.HasPrefix(path, "/auth/") || path == "/auth" {
			next.ServeHTTP(w, r)
			return
		}

		// If no admin password configured, skip auth
		if !da.HasAdminPassword() {
			next.ServeHTTP(w, r)
			return
		}

		// Check session
		if !da.ValidateSession(r) {
			// For HTML page requests, serve index.html (React handles login routing)
			// For API requests, return 401
			accept := r.Header.Get("Accept")
			if strings.Contains(accept, "application/json") {
				w.Header().Set("Content-Type", "application/json")
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			// Serve the SPA — React will show login page based on auth state
			next.ServeHTTP(w, r)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// HandleAuthSalt returns the salt for client-side password hashing.
func (da *DashboardAuth) HandleAuthSalt() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		salt := AdminPasswordSalt(da.adminPasswordHash)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"salt":     salt,
			"required": da.HasAdminPassword(),
		})
	}
}

// HandleAuthLogin validates the client's hash and creates a session.
func (da *DashboardAuth) HandleAuthLogin() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			Hash string `json:"hash"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}

		if !VerifyAdminPassword(da.adminPasswordHash, body.Hash) {
			log.Printf("[dashboard] failed login attempt")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid password"})
			return
		}

		da.CreateSession(w)
		log.Printf("[dashboard] login success")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// HandleAuthLogout destroys the dashboard session.
func (da *DashboardAuth) HandleAuthLogout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		da.DestroySession(w, r)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// HandleAuthCheck returns whether the current session is valid.
func (da *DashboardAuth) HandleAuthCheck() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"authenticated": da.ValidateSession(r),
			"required":      da.HasAdminPassword(),
		})
	}
}

// --- Account Pool helpers ---

// GetAccountByEmail returns a specific account by email regardless of
// usability (callers like the dashboard "copy token" action want the raw
// record even for accounts that the picker would skip).
func (p *AccountPool) GetAccountByEmail(email string) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, acc := range p.accounts {
		if acc.UserEmail == email {
			return acc
		}
	}
	return nil
}

// GetBestAccount returns the best available account for a new conversation.
// Prefer accounts with remaining basic quota.
func (p *AccountPool) GetBestAccount() *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pickBestAccountLocked(nil)
}

// --- Reverse Proxy helpers ---

// CreateTargetedSession creates a proxy session for a specific account
func (rp *ReverseProxy) CreateTargetedSession(w http.ResponseWriter, acc *Account) {
	id := generateUUIDv4()
	sess := &ProxySession{Account: acc, CreatedAt: time.Now()}
	rp.sessions.Store(id, sess)
	http.SetCookie(w, &http.Cookie{
		Name: "np_session", Value: id, Path: "/",
		HttpOnly: true, MaxAge: 86400,
	})
}

// --- HTTP Handlers ---

// HandleDashboard serves the React SPA dashboard.
// It injects the API key into index.html via a <meta> tag so the frontend
// can authenticate against /admin/* endpoints.
// Auth endpoints are nested under /dashboard/auth/*.
func HandleDashboard(apiKey string, auth *DashboardAuth) http.Handler {
	// Serve from embedded dist/ filesystem
	distFS, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		panic("failed to get dist sub-filesystem: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(distFS))

	// Inner handler that serves files and auth endpoints
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/dashboard")
		if path == "" || path == "/" {
			path = "/index.html"
		}

		// Auth API endpoints
		switch path {
		case "/auth/salt":
			auth.HandleAuthSalt()(w, r)
			return
		case "/auth/login":
			auth.HandleAuthLogin()(w, r)
			return
		case "/auth/logout":
			auth.HandleAuthLogout()(w, r)
			return
		case "/auth/check":
			auth.HandleAuthCheck()(w, r)
			return
		}

		// For index.html, inject the API key meta tag
		if path == "/index.html" {
			data, err := fs.ReadFile(distFS, "index.html")
			if err != nil {
				http.Error(w, "index.html not found", http.StatusInternalServerError)
				return
			}
			html := strings.Replace(string(data), "<head>",
				`<head><meta name="api-key" content="`+apiKey+`">`, 1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			w.Write([]byte(html))
			return
		}

		// Serve static assets (JS, CSS) with caching
		if strings.HasPrefix(path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}

		// Serve file from embedded FS
		r.URL.Path = path
		fileServer.ServeHTTP(w, r)
	})

	// Wrap with auth middleware
	return auth.RequireAuth(inner)
}

// HandleProxyStart creates a session for a specific account and redirects to /ai.
// Requires valid dashboard session.
func HandleProxyStart(pool *AccountPool, rp *ReverseProxy, auth *DashboardAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Check dashboard auth
		if auth.HasAdminPassword() && !auth.ValidateSession(r) {
			http.Redirect(w, r, "/dashboard/", http.StatusFound)
			return
		}

		email := r.URL.Query().Get("email")
		best := r.URL.Query().Get("best")

		var acc *Account
		if best == "true" {
			acc = pool.GetBestAccount()
		} else if email != "" {
			acc = pool.GetAccountByEmail(email)
		}

		if acc == nil {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"account not found or all exhausted"}`, http.StatusNotFound)
			return
		}

		// Refuse to redirect into an account whose Notion workspace is
		// missing — the SPA loops on a skeleton screen forever and the
		// user perceives it as a reverse-proxy hang. Surface a clear
		// error so the dashboard can show "this account has no
		// workspace" instead of opening a dead tab.
		if pool.HasNoWorkspace(acc) {
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"account has no accessible workspace; pick another or re-register"}`, http.StatusConflict)
			return
		}

		rp.CreateTargetedSession(w, acc)
		http.Redirect(w, r, "/ai", http.StatusFound)
	}
}
