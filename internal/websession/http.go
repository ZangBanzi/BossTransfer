package websession

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"bosstransfer/internal/httpapi"
	"bosstransfer/internal/localauth"
)

const (
	loginFailureWindow     = 5 * time.Minute
	maxLoginFailures       = 8
	maxLoginFailureEntries = 4096
)

type Auth struct {
	Store        *localauth.Store
	CookieName   string
	LoginPath    string
	PasswordPath string

	mu       sync.Mutex
	failures map[string]failure
	now      func() time.Time
}

type failure struct {
	Count int
	Since time.Time
}

func New(store *localauth.Store, cookieName, loginPath string) *Auth {
	return &Auth{Store: store, CookieName: cookieName, LoginPath: loginPath, PasswordPath: "/password", failures: make(map[string]failure), now: time.Now}
}

func (a *Auth) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	remote := remoteIP(r)
	if a.rateLimited(remote) {
		httpapi.WriteJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_attempts", "message": "登录失败次数过多，请稍后再试"})
		return
	}
	var payload struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &payload); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	session, err := a.Store.Login(payload.Username, payload.Password)
	if err != nil {
		a.recordFailure(remote)
		httpapi.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_credentials", "message": "账号或密码错误"})
		return
	}
	a.clearFailures(remote)
	a.setCookie(w, r, session.Token, session.ExpiresAt)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": session.Username, "must_change": session.MustChange, "expires_at": session.ExpiresAt})
}

func (a *Auth) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if token := a.token(r); token != "" {
		a.Store.RevokeSession(token)
	}
	a.clearCookie(w, r)
	httpapi.WriteJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (a *Auth) SessionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	principal, err := a.Principal(r)
	if err != nil {
		httpapi.WriteJSON(w, http.StatusUnauthorized, map[string]any{"authenticated": false, "error": "authentication_required"})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": principal.Username, "must_change": principal.MustChange, "expires_at": principal.ExpiresAt})
}

func (a *Auth) CredentialsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		methodNotAllowed(w, http.MethodPut)
		return
	}
	principal, err := a.Principal(r)
	if err != nil {
		a.unauthorized(w, r)
		return
	}
	var payload struct {
		CurrentPassword string `json:"current_password"`
		Username        string `json:"username"`
		Password        string `json:"password"`
	}
	if err := decodeJSON(w, r, &payload); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}
	if !a.Store.VerifyCredentials(principal.Username, payload.CurrentPassword) {
		httpapi.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "current_password_invalid", "message": "当前密码错误"})
		return
	}
	if payload.Username == "" {
		payload.Username = principal.Username
	}
	if len([]rune(payload.Password)) < 8 {
		httpapi.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "weak_password", "message": "新密码至少需要 8 个字符"})
		return
	}
	if err := a.Store.UpdateCredentials(a.token(r), localauth.CredentialUpdate{Username: payload.Username, Password: payload.Password, MustChange: false}); err != nil {
		httpapi.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "credentials_update_failed", "message": err.Error()})
		return
	}
	a.clearCookie(w, r)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"updated": true, "login_required": true})
}

func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.Principal(r); err != nil {
			a.unauthorized(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePasswordChanged protects normal application routes while still
// allowing a freshly bootstrapped admin/password account to sign in and reach
// the dedicated credential-change flow.
func (a *Auth) RequirePasswordChanged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := a.Principal(r)
		if err != nil {
			a.unauthorized(w, r)
			return
		}
		if principal.MustChange {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				httpapi.WriteJSON(w, http.StatusPreconditionRequired, map[string]string{
					"error": "password_change_required", "message": "首次登录请先修改本地账号密码",
				})
				return
			}
			http.Redirect(w, r, a.PasswordPath, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Auth) Principal(r *http.Request) (localauth.Principal, error) {
	return a.Store.ValidateSession(a.token(r))
}

func (a *Auth) token(r *http.Request) string {
	cookie, err := r.Cookie(a.CookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (a *Auth) unauthorized(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		httpapi.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required", "message": "请先登录本地客户端"})
		return
	}
	http.Redirect(w, r, a.LoginPath, http.StatusSeeOther)
}

func (a *Auth) setCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: a.CookieName, Value: token, Path: "/", HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
}

func (a *Auth) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: a.CookieName, Value: "", Path: "/", HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (a *Auth) rateLimited(remote string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.pruneFailuresLocked(now)
	entry, ok := a.failures[remote]
	return ok && entry.Count >= maxLoginFailures
}

func (a *Auth) recordFailure(remote string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	a.pruneFailuresLocked(now)
	entry, exists := a.failures[remote]
	if !exists {
		for len(a.failures) >= maxLoginFailureEntries {
			a.evictOldestFailureLocked()
		}
		entry = failure{Since: now}
	}
	entry.Count++
	a.failures[remote] = entry
}

func (a *Auth) clearFailures(remote string) { a.mu.Lock(); delete(a.failures, remote); a.mu.Unlock() }

func (a *Auth) pruneFailuresLocked(now time.Time) {
	for remote, entry := range a.failures {
		if entry.Since.IsZero() || now.Before(entry.Since) || !now.Before(entry.Since.Add(loginFailureWindow)) {
			delete(a.failures, remote)
		}
	}
}

func (a *Auth) evictOldestFailureLocked() {
	oldestRemote := ""
	var oldestSince time.Time
	for remote, entry := range a.failures {
		if oldestRemote == "" || entry.Since.Before(oldestSince) || (entry.Since.Equal(oldestSince) && remote < oldestRemote) {
			oldestRemote = remote
			oldestSince = entry.Since
		}
	}
	if oldestRemote != "" {
		delete(a.failures, oldestRemote)
	}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		return errors.New("请求必须使用 application/json")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	httpapi.WriteJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
}
