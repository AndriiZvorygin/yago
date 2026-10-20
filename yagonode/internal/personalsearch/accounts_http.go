package personalsearch

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/adminauth"
)

const (
	PathAccountLogin        = "/account/login"
	PathAccountClaim        = "/account/claim"
	PathAccountHome         = "/account"
	PathAccountLogout       = "/account/logout"
	PathAccountPreference   = "/account/preference"
	PathAccountHistory      = "/account/history"
	PathAccountHistoryClear = "/account/history/clear"
	PathAccountAPIKeys      = "/account/api-keys"
	PathPersonalSearchAdmin = "/admin/personal-search"
	PathPersonalInvitations = "/api/admin/v1/personal-search/invitations"
	pathPersonalAccount     = "/api/admin/v1/personal-search/accounts/{id}"
	accountEnrollmentCookie = "__Host-yago_account_enrollment"
	accountCSRFCookie       = "__Host-yago_account_csrf"
)

type loginAttempt struct {
	window time.Time
	failed int
}

type accountPageData struct {
	Title           string
	Error           string
	Notice          string
	CSRFToken       string
	GlobalEnabled   bool
	Account         Account
	History         []personalEvent
	APIKeys         []AccountAPIKeyView
	NewAPIKey       string
	InvitationToken string
	InvitationUntil string
}

type invitationRequest struct {
	ExpiresInHours int `json:"expiresInHours"`
}

type invitationResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

var accountPages = template.Must(template.New("account-pages").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>{{.Title}} · YagoSeek</title>
<style>
:root{color-scheme:light dark;font:16px/1.55 system-ui,sans-serif;background:#f4f6f8;color:#18212b}body{margin:0}.wrap{max-width:760px;margin:6vh auto;padding:0 20px}.card{background:#fff;border:1px solid #d8dee5;border-radius:12px;padding:28px;box-shadow:0 8px 26px #10203012}h1,h2{line-height:1.2}h1{margin-top:0}h2{margin-top:30px}.muted{color:#526171}.notice{padding:12px;border-radius:8px;background:#edf6ff}.error{padding:12px;border-radius:8px;background:#ffeded;color:#7b1e1e}label{display:block;margin:12px 0 5px}input[type=text],input[type=password],input[type=number]{box-sizing:border-box;width:100%;padding:11px;border:1px solid #9ba8b5;border-radius:6px;font:inherit}input[type=checkbox]{width:18px;height:18px;vertical-align:middle}button{padding:10px 16px;border:0;border-radius:6px;background:#155eef;color:#fff;font:inherit;font-weight:650;cursor:pointer;margin-top:12px}button.secondary{background:#4b5563}button.danger{background:#a51f2b}form.inline{display:inline}code,pre{overflow-wrap:anywhere;white-space:pre-wrap;background:#eef1f4;padding:8px;border-radius:6px}li{margin:12px 0}a{color:#155eef}header{display:flex;justify-content:space-between;align-items:center;gap:16px}.divider{border:0;border-top:1px solid #d8dee5;margin:24px 0}@media(prefers-color-scheme:dark){:root{background:#111820;color:#e5edf5}.card{background:#1c2732;border-color:#34414e}.muted{color:#aab8c6}.notice{background:#182f45}.error{background:#4b2529;color:#ffd8dc}code,pre{background:#263341}a{color:#8ab4ff}}
</style></head><body><main class="wrap"><section class="card">
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}{{if .Notice}}<p class="notice" role="status">{{.Notice}}</p>{{end}}
{{if eq .Title "Sign in"}}<h1>Sign in to YagoSeek</h1><p class="muted">Accounts are invitation-only. Search remains available without signing in.</p><form method="post" action="/account/login"><input type="hidden" name="csrf" value="{{.CSRFToken}}"><label for="login">Account name</label><input id="login" name="login" type="text" autocomplete="username" required maxlength="40"><label for="password">Password</label><input id="password" name="password" type="password" autocomplete="current-password" required><button type="submit">Sign in</button></form><p><a href="/account/claim">Have an invitation?</a></p>
{{else if eq .Title "Claim invitation"}}<h1>Claim your invitation</h1><p class="muted">Choose a unique account name and a password of at least 12 characters.</p><form method="post" action="/account/claim"><input type="hidden" name="csrf" value="{{.CSRFToken}}"><label for="invite">Invitation token</label><input id="invite" name="token" type="text" autocomplete="off" required><label for="login">Account name</label><input id="login" name="login" type="text" autocomplete="username" required maxlength="40"><label for="password">Password</label><input id="password" name="password" type="password" autocomplete="new-password" required minlength="12"><button type="submit">Create account</button></form><p><a href="/account/login">Return to sign in</a></p>
{{else if eq .Title "Personal search"}}<header><div><h1>Personal search</h1><p class="muted">Signed in as {{.Account.LoginName}}</p></div><form method="post" action="/account/logout"><input type="hidden" name="csrf" value="{{.CSRFToken}}"><button class="secondary" type="submit">Sign out</button></form></header>
<h2>Learning preference</h2><p class="muted">When enabled, your recent searches and clicked results are used only on this node to gently reorder your results. Your profile is never sent to peers or web providers.</p>{{if not .GlobalEnabled}}<p class="notice">Personalized search is currently disabled by the operator. Your preference is saved, but no personal history is recorded and results are not personalized.</p>{{end}}<form method="post" action="/account/preference"><input type="hidden" name="csrf" value="{{.CSRFToken}}"><label><input type="checkbox" name="enabled" value="true" {{if .Account.Personalization}}checked{{end}}> Learn from my searches and clicks</label><button type="submit">Save preference</button></form>
<h2>Recent history</h2><p><a href="/account/history?format=json">Download history as JSON</a></p>{{if .History}}<ol>{{range .History}}<li><time>{{.CreatedAt.Format "2006-01-02 15:04 MST"}}</time> — {{if eq .Kind "click"}}clicked <a href="{{.URL}}" rel="noreferrer">{{.Host}}</a> after searching {{else}}searched {{end}}<q>{{.Query}}</q></li>{{end}}</ol>{{else}}<p class="muted">No personal search history is stored.</p>{{end}}<form method="post" action="/account/history/clear"><input type="hidden" name="csrf" value="{{.CSRFToken}}"><button class="danger" type="submit">Clear my history</button></form>
<h2>Agent API keys</h2><p class="muted">Keys are shown only once when created. The read scope supports personalized search; raw-content access is optional.</p>{{if .NewAPIKey}}<p class="notice">Copy this key now; it will not be shown again:<br><code>{{.NewAPIKey}}</code></p>{{end}}{{if .APIKeys}}<ul>{{range .APIKeys}}<li>{{.Label}} ({{range $index,$scope := .Scopes}}{{if $index}}, {{end}}{{$scope}}{{end}}) <form class="inline" method="post" action="/account/api-keys/revoke"><input type="hidden" name="csrf" value="{{$.CSRFToken}}"><input type="hidden" name="id" value="{{.ID}}"><button class="danger" type="submit">Revoke</button></form></li>{{end}}</ul>{{else}}<p class="muted">No active keys.</p>{{end}}<form method="post" action="/account/api-keys"><input type="hidden" name="csrf" value="{{.CSRFToken}}"><label for="key-label">Key label</label><input id="key-label" name="label" type="text" maxlength="80" required><label><input type="checkbox" name="raw" value="true"> Allow raw page content</label><button type="submit">Create API key</button></form>
{{else if eq .Title "Personal search invitations"}}<h1>Personal search invitations</h1><p class="muted">Create one-use account invitations. This page never shows user search history.</p>{{if .InvitationToken}}<p class="notice">Copy this invitation token and give it to the user. It is shown once and expires {{.InvitationUntil}}.<br><code>{{.InvitationToken}}</code></p>{{end}}<form method="post" action="/admin/personal-search"><input type="hidden" name="csrf" value="{{.CSRFToken}}"><label for="hours">Lifetime in hours (1–168)</label><input id="hours" type="number" name="hours" min="1" max="168" value="24"><button type="submit">Create invitation</button></form>{{end}}
</section></main></body></html>`))

func (a *Accounts) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, authenticated, err := a.principalFromRequest(r)
		if err != nil {
			if errors.Is(err, ErrCredentialInvalid) {
				http.Error(w, "invalid account API key", http.StatusUnauthorized)
				return
			}
			http.Error(w, "account authentication unavailable", http.StatusServiceUnavailable)
			return
		}
		if authenticated {
			w.Header().Set("Cache-Control", "private, no-store")
			next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Accounts) principalFromRequest(r *http.Request) (Principal, bool, error) {
	if cookie, err := r.Cookie(accountSessionCookie); err == nil {
		principal, ok, authErr := a.AuthenticateSession(r.Context(), cookie.Value)
		if authErr != nil {
			return Principal{}, false, authErr
		}
		if ok {
			if csrfCookie, csrfErr := r.Cookie(
				accountCSRFCookie,
			); csrfErr == nil &&
				csrfCookie.Value != "" &&
				subtle.ConstantTimeCompare(
					[]byte(secretHash(csrfCookie.Value)),
					[]byte(principal.csrfHash),
				) == 1 {
				principal.CSRFToken = csrfCookie.Value
			}

			return principal, true, nil
		}
	}
	token := bearerValue(r.Header.Get("Authorization"))
	if token == "" || !strings.HasPrefix(token, "ygp_") {
		return Principal{}, false, nil
	}
	principal, ok, err := a.AuthenticateAPIKey(r.Context(), token, "read")
	if err != nil {
		return Principal{}, false, err
	}
	if !ok {
		return Principal{}, false, ErrCredentialInvalid
	}

	return principal, true, nil
}

func (a *Accounts) MountUserPages(
	mux *http.ServeMux,
	learning *Learning,
	globalEnabled func() bool,
) {
	handler := accountPagesHandler{accounts: a, learning: learning, globalEnabled: globalEnabled}
	mux.HandleFunc("GET "+PathAccountLogin, handler.loginPage)
	mux.HandleFunc("POST "+PathAccountLogin, handler.login)
	mux.HandleFunc("GET "+PathAccountClaim, handler.claimPage)
	mux.HandleFunc("POST "+PathAccountClaim, handler.claim)
	mux.HandleFunc("GET "+PathAccountHome, handler.home)
	mux.HandleFunc("POST "+PathAccountLogout, handler.logout)
	mux.HandleFunc("POST "+PathAccountPreference, handler.preference)
	mux.HandleFunc("GET "+PathAccountHistory, handler.history)
	mux.HandleFunc("POST "+PathAccountHistoryClear, handler.clearHistory)
	mux.HandleFunc("POST "+PathAccountAPIKeys, handler.createAPIKey)
	mux.HandleFunc("POST "+PathAccountAPIKeys+"/revoke", handler.revokeAPIKey)
}

func (a *Accounts) MountAdminPages(mux *http.ServeMux, learning *Learning) {
	admin := accountAdminPages{accounts: a, learning: learning}
	mux.HandleFunc("GET "+PathPersonalSearchAdmin, admin.page)
	mux.HandleFunc("POST "+PathPersonalSearchAdmin, admin.create)
	mux.HandleFunc("POST "+PathPersonalInvitations, admin.createJSON)
	mux.HandleFunc("DELETE "+pathPersonalAccount, admin.disableAccount)
}

type accountPagesHandler struct {
	accounts      *Accounts
	learning      *Learning
	globalEnabled func() bool
}

func (h accountPagesHandler) loginPage(w http.ResponseWriter, r *http.Request) {
	renderAccountPage(
		w,
		http.StatusOK,
		accountPageData{Title: "Sign in", CSRFToken: setEnrollmentCSRF(w)},
	)
}

func (h accountPagesHandler) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !allowAccountLogin(r) {
		renderEnrollmentPage(
			w,
			http.StatusTooManyRequests,
			"Sign in",
			"Too many sign-in attempts. Try again later.",
		)
		return
	}
	if err := r.ParseForm(); err != nil {
		renderEnrollmentPage(w, http.StatusBadRequest, "Sign in", "Invalid sign-in request.")
		return
	}
	if !validEnrollmentCSRF(r) {
		renderEnrollmentPage(
			w,
			http.StatusForbidden,
			"Sign in",
			"Invalid sign-in form. Reload the page and try again.",
		)
		return
	}
	session, _, err := h.accounts.Login(r.Context(), r.Form.Get("login"), r.Form.Get("password"))
	if err != nil {
		recordAccountLoginFailure(r)
		status := http.StatusUnauthorized
		message := "The account name or password was not accepted."
		if !errors.Is(err, ErrLoginRejected) {
			status = http.StatusServiceUnavailable
			message = "Sign-in is temporarily unavailable."
		}
		renderEnrollmentPage(w, status, "Sign in", message)
		return
	}
	clearAccountLoginFailures(r)
	http.SetCookie(w, &http.Cookie{
		Name: accountSessionCookie, Value: session.Token, Path: "/", Secure: true,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt,
		MaxAge: int(time.Until(session.ExpiresAt).Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name: accountCSRFCookie, Value: session.CSRFToken, Path: "/", Secure: true,
		SameSite: http.SameSiteStrictMode, Expires: session.ExpiresAt,
		MaxAge: int(time.Until(session.ExpiresAt).Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name: accountEnrollmentCookie, Value: "", Path: "/", Secure: true,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	http.Redirect(w, r, PathAccountHome, http.StatusSeeOther)
}

func (h accountPagesHandler) claimPage(w http.ResponseWriter, _ *http.Request) {
	renderAccountPage(
		w,
		http.StatusOK,
		accountPageData{Title: "Claim invitation", CSRFToken: setEnrollmentCSRF(w)},
	)
}

func (h accountPagesHandler) claim(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if !allowAccountAction(r, "claim") {
		renderEnrollmentPage(
			w,
			http.StatusTooManyRequests,
			"Claim invitation",
			"Too many invitation attempts. Try again later.",
		)
		return
	}
	if err := r.ParseForm(); err != nil {
		renderEnrollmentPage(
			w,
			http.StatusBadRequest,
			"Claim invitation",
			"Invalid invitation request.",
		)
		return
	}
	if !validEnrollmentCSRF(r) {
		renderEnrollmentPage(
			w,
			http.StatusForbidden,
			"Claim invitation",
			"Invalid invitation form. Reload the page and try again.",
		)
		return
	}
	_, err := h.accounts.ClaimInvitation(
		r.Context(),
		r.Form.Get("token"),
		r.Form.Get("login"),
		r.Form.Get("password"),
	)
	if err != nil {
		recordAccountActionFailure(r, "claim")
		message := "The invitation was invalid, expired, or already used."
		if errors.Is(err, ErrCredentialInvalid) || errors.Is(err, ErrAccountExists) {
			message = "Choose an available account name and a password of at least 12 characters."
		}
		renderEnrollmentPage(w, http.StatusBadRequest, "Claim invitation", message)
		return
	}
	clearAccountActionFailures(r, "claim")
	renderAccountPage(w, http.StatusOK, accountPageData{
		Title:     "Sign in",
		Notice:    "Account created. Sign in to manage your personal search settings.",
		CSRFToken: setEnrollmentCSRF(w),
	})
}

func (h accountPagesHandler) home(w http.ResponseWriter, r *http.Request) {
	principal, ok := pagePrincipal(r)
	if !ok || principal.APIKey {
		http.Redirect(w, r, PathAccountLogin, http.StatusSeeOther)
		return
	}
	account, found, err := h.accounts.AccountByID(r.Context(), principal.AccountID)
	if err != nil || !found || !account.Active {
		http.Error(w, "account unavailable", http.StatusServiceUnavailable)
		return
	}
	if principal.CSRFToken == "" {
		http.Redirect(w, r, PathAccountLogin, http.StatusSeeOther)
		return
	}
	history, err := h.learning.History(r.Context(), principal.AccountID, 50)
	if err != nil {
		http.Error(w, "history unavailable", http.StatusServiceUnavailable)
		return
	}
	keys, err := h.accounts.ListAPIKeys(r.Context(), principal.AccountID)
	if err != nil {
		http.Error(w, "API keys unavailable", http.StatusServiceUnavailable)
		return
	}
	data := accountPageData{
		Title: "Personal search", Account: account, History: history, APIKeys: keys,
		CSRFToken: principal.CSRFToken, GlobalEnabled: h.globalEnabled != nil && h.globalEnabled(),
	}
	renderAccountPage(w, http.StatusOK, data)
}

func (h accountPagesHandler) logout(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireAccountForm(w, r)
	if !ok {
		return
	}
	cookie, _ := r.Cookie(accountSessionCookie)
	if cookie != nil {
		_ = h.accounts.Logout(r.Context(), cookie.Value)
	}
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     accountSessionCookie,
			Value:    "",
			Path:     "/",
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   -1,
		},
	)
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     accountCSRFCookie,
			Value:    "",
			Path:     "/",
			Secure:   true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   -1,
		},
	)
	_ = principal
	http.Redirect(w, r, PathAccountLogin, http.StatusSeeOther)
}

func (h accountPagesHandler) preference(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireAccountForm(w, r)
	if !ok {
		return
	}
	if err := h.accounts.SetPersonalization(
		r.Context(),
		principal.AccountID,
		r.Form.Get("enabled") == "true",
	); err != nil {
		http.Error(w, "could not update personal search preference", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, PathAccountHome, http.StatusSeeOther)
}

func (h accountPagesHandler) history(w http.ResponseWriter, r *http.Request) {
	principal, ok := pagePrincipal(r)
	if !ok || principal.APIKey {
		http.Error(w, "account session required", http.StatusUnauthorized)
		return
	}
	rows, err := h.learning.History(r.Context(), principal.AccountID, maximumPersonalHistoryPage)
	if err != nil {
		http.Error(w, "history unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=personal-search-history.json")
	_ = json.NewEncoder(w).Encode(rows)
}

func (h accountPagesHandler) clearHistory(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireAccountForm(w, r)
	if !ok {
		return
	}
	if err := h.learning.ClearHistory(r.Context(), principal.AccountID); err != nil {
		http.Error(w, "could not clear personal search history", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, PathAccountHome, http.StatusSeeOther)
}

func (h accountPagesHandler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireAccountForm(w, r)
	if !ok {
		return
	}
	scopes := []string{"read"}
	if r.Form.Get("raw") == "true" {
		scopes = append(scopes, "raw")
	}
	key, err := h.accounts.CreateAPIKey(
		r.Context(),
		principal.AccountID,
		r.Form.Get("label"),
		scopes,
	)
	if err != nil {
		http.Error(w, "could not create account API key", http.StatusServiceUnavailable)
		return
	}
	account, _, _ := h.accounts.AccountByID(r.Context(), principal.AccountID)
	history, _ := h.learning.History(r.Context(), principal.AccountID, 50)
	keys, _ := h.accounts.ListAPIKeys(r.Context(), principal.AccountID)
	renderAccountPage(w, http.StatusCreated, accountPageData{
		Title: "Personal search", Account: account, History: history, APIKeys: keys,
		CSRFToken: principal.CSRFToken, NewAPIKey: key.Secret,
	})
}

func (h accountPagesHandler) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	principal, ok := requireAccountForm(w, r)
	if !ok {
		return
	}
	if err := h.accounts.RevokeAPIKey(
		r.Context(),
		principal.AccountID,
		r.Form.Get("id"),
	); err != nil {
		http.Error(w, "account API key not found", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, PathAccountHome, http.StatusSeeOther)
}

type accountAdminPages struct {
	accounts *Accounts
	learning *Learning
}

func (h accountAdminPages) page(w http.ResponseWriter, r *http.Request) {
	csrf, _ := adminauth.CSRFTokenFromContext(r.Context())
	renderAccountPage(w, http.StatusOK, accountPageData{
		Title: "Personal search invitations", Notice: r.URL.Query().Get("notice"), CSRFToken: csrf,
	})
}

func (h accountAdminPages) create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := r.ParseForm(); err != nil {
		renderAdminInvitationPage(
			w,
			r,
			http.StatusBadRequest,
			accountPageData{Error: "Invalid invitation lifetime."},
		)
		return
	}
	hours, err := strconv.Atoi(r.Form.Get("hours"))
	if err != nil || hours < 1 || hours > 168 {
		renderAdminInvitationPage(
			w,
			r,
			http.StatusBadRequest,
			accountPageData{Error: "Invitation lifetime must be from 1 to 168 hours."},
		)
		return
	}
	invitation, err := h.accounts.IssueInvitation(r.Context(), time.Duration(hours)*time.Hour)
	if err != nil {
		http.Error(w, "could not create invitation", http.StatusServiceUnavailable)
		return
	}
	renderAdminInvitationPage(
		w,
		r,
		http.StatusCreated,
		accountPageData{
			InvitationToken: invitation.Token,
			InvitationUntil: invitation.ExpiresAt.Format(time.RFC1123),
		},
	)
}

func renderAdminInvitationPage(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	data accountPageData,
) {
	csrf, _ := adminauth.CSRFTokenFromContext(r.Context())
	data.Title = "Personal search invitations"
	data.CSRFToken = csrf
	renderAccountPage(w, status, data)
}

func (h accountAdminPages) createJSON(w http.ResponseWriter, r *http.Request) {
	var request invitationRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if request.ExpiresInHours < 1 || request.ExpiresInHours > 168 {
		http.Error(w, "expiresInHours must be between 1 and 168", http.StatusBadRequest)
		return
	}
	invitation, err := h.accounts.IssueInvitation(
		r.Context(),
		time.Duration(request.ExpiresInHours)*time.Hour,
	)
	if err != nil {
		http.Error(w, "could not create invitation", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).
		Encode(invitationResponse(invitation))
}

func (h accountAdminPages) disableAccount(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("id")
	if err := h.accounts.DisableAccount(r.Context(), accountID); err != nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
	}
	if h.learning != nil {
		if err := h.learning.ClearHistory(r.Context(), accountID); err != nil {
			http.Error(
				w,
				"account disabled but personal history cleanup failed",
				http.StatusServiceUnavailable,
			)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func setEnrollmentCSRF(w http.ResponseWriter) string {
	token, err := randomSecret(24)
	if err != nil {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: accountEnrollmentCookie, Value: token, Path: "/", Secure: true,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 15 * 60,
	})
	return token
}

func validEnrollmentCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(accountEnrollmentCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(r.Form.Get("csrf"))) == 1
}

func renderEnrollmentPage(w http.ResponseWriter, status int, title, message string) {
	renderAccountPage(w, status, accountPageData{
		Title: title, Error: message, CSRFToken: setEnrollmentCSRF(w),
	})
}

func renderAccountPage(w http.ResponseWriter, status int, data accountPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	if err := accountPages.Execute(w, data); err != nil {
		return
	}
}

func pagePrincipal(r *http.Request) (Principal, bool) {
	return PrincipalFromContext(r.Context())
}

func requireAccountForm(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	principal, ok := pagePrincipal(r)
	if !ok || principal.APIKey {
		http.Error(w, "account session required", http.StatusUnauthorized)
		return Principal{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form request", http.StatusBadRequest)
		return Principal{}, false
	}
	csrf := r.Form.Get("csrf")
	if csrf == "" {
		csrf = r.Header.Get("X-CSRF-Token")
	}
	if csrf == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(principal.CSRFToken)) != 1 {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return Principal{}, false
	}
	return principal, true
}

func bearerValue(header string) string {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

var loginWindowState = struct {
	sync.Mutex
	entries map[string]loginAttempt
}{entries: map[string]loginAttempt{}}

const maximumLoginAddresses = 2048

func allowAccountLogin(r *http.Request) bool {
	return allowAccountAction(r, "login")
}

func allowAccountAction(r *http.Request, action string) bool {
	key := accountActionKey(r, action)
	now := time.Now()
	loginWindowState.Lock()
	defer loginWindowState.Unlock()
	for address, attempt := range loginWindowState.entries {
		if now.Sub(attempt.window) >= 15*time.Minute {
			delete(loginWindowState.entries, address)
		}
	}
	attempt, exists := loginWindowState.entries[key]
	if attempt.failed >= 5 {
		return false
	}
	if !exists && len(loginWindowState.entries) >= maximumLoginAddresses {
		return false
	}
	return true
}

func recordAccountLoginFailure(r *http.Request) {
	recordAccountActionFailure(r, "login")
}

func recordAccountActionFailure(r *http.Request, action string) {
	key := accountActionKey(r, action)
	loginWindowState.Lock()
	defer loginWindowState.Unlock()
	attempt := loginWindowState.entries[key]
	if attempt.window.IsZero() || time.Since(attempt.window) >= 15*time.Minute {
		attempt.window = time.Now()
	}
	attempt.failed++
	loginWindowState.entries[key] = attempt
}

func clearAccountLoginFailures(r *http.Request) {
	clearAccountActionFailures(r, "login")
}

func clearAccountActionFailures(r *http.Request, action string) {
	loginWindowState.Lock()
	delete(loginWindowState.entries, accountActionKey(r, action))
	loginWindowState.Unlock()
}

func accountActionKey(r *http.Request, action string) string {
	return action + ":" + accountClientKey(r)
}

func accountClientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}
