package personalsearch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestInvitationFormUsesAdminGuardCSRFField(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	renderAccountPage(response, http.StatusOK, accountPageData{
		Title: "Personal search invitations", CSRFToken: "admin-csrf-token",
	})

	body := response.Body.String()
	if !strings.Contains(body, `name="csrf_token" value="admin-csrf-token"`) {
		t.Fatal("invitation form does not submit the admin guard CSRF field")
	}
	if strings.Contains(body, `name="csrf" value="admin-csrf-token"`) {
		t.Fatal("invitation form still submits the account-page CSRF field")
	}
}

func TestAccountLoginAndLogoutProtectCSRFCookie(t *testing.T) {
	accounts := openAccounts(t, time.Now)
	account := claimTestAccount(t, accounts)
	mux := http.NewServeMux()
	accounts.MountUserPages(mux, nil, nil)
	handler := accounts.Middleware(mux)
	loginPage := httptest.NewRecorder()
	handler.ServeHTTP(
		loginPage,
		httpTestRequest(t, http.MethodGet, PathAccountLogin, ""),
	)
	enrollmentCookie := accountResponseCookie(loginPage, accountEnrollmentCookie)
	if enrollmentCookie == nil {
		t.Fatal("login page did not set the enrollment cookie")
	}
	form := url.Values{
		"csrf":     {enrollmentCookie.Value},
		"login":    {account.LoginName},
		"password": {"a sufficiently long test password"},
	}
	login := httptest.NewRecorder()
	loginRequest := httpTestRequest(t, http.MethodPost, PathAccountLogin, form.Encode())
	loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginRequest.AddCookie(enrollmentCookie)
	handler.ServeHTTP(login, loginRequest)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, body=%s", login.Code, login.Body.String())
	}
	sessionCookie := accountResponseCookie(login, accountSessionCookie)
	csrfCookie := accountResponseCookie(login, accountCSRFCookie)
	assertProtectedAccountCookie(t, sessionCookie)
	assertProtectedAccountCookie(t, csrfCookie)

	logout := httptest.NewRecorder()
	logoutForm := url.Values{"csrf": {csrfCookie.Value}}
	logoutRequest := httpTestRequest(t, http.MethodPost, PathAccountLogout, logoutForm.Encode())
	logoutRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	logoutRequest.AddCookie(sessionCookie)
	logoutRequest.AddCookie(csrfCookie)
	handler.ServeHTTP(logout, logoutRequest)
	if logout.Code != http.StatusSeeOther {
		t.Fatalf("logout status = %d, body=%s", logout.Code, logout.Body.String())
	}
	clearedCSRFCookie := accountResponseCookie(logout, accountCSRFCookie)
	assertProtectedAccountCookie(t, clearedCSRFCookie)
	if clearedCSRFCookie.MaxAge != -1 {
		t.Fatalf("CSRF cookie was not cleared: %+v", clearedCSRFCookie)
	}
}

func httpTestRequest(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	return httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
}

func accountResponseCookie(response *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func assertProtectedAccountCookie(t *testing.T, cookie *http.Cookie) {
	t.Helper()
	if cookie == nil || !cookie.Secure || !cookie.HttpOnly ||
		cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("account cookie is not protected: %+v", cookie)
	}
}

func TestAdminPasswordResetRouteUsesAccountsService(t *testing.T) {
	if PathPersonalCredentialReset != "/api/admin/v1/personal-search/password-reset" {
		t.Fatalf("credential reset API path changed: %q", PathPersonalCredentialReset)
	}
	accounts := openAccounts(t, time.Now)
	account := claimTestAccount(t, accounts)
	mux := http.NewServeMux()
	accounts.MountAdminPages(mux, nil)
	body, err := json.Marshal(passwordResetRequest{
		LoginName: account.LoginName,
		Password:  "a reset password long enough",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, PathPersonalCredentialReset, strings.NewReader(string(body)),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("password reset status = %d, body=%s", response.Code, response.Body.String())
	}
	if _, _, err := accounts.Login(
		t.Context(),
		account.LoginName,
		"a reset password long enough",
	); err != nil {
		t.Fatalf("login with reset password: %v", err)
	}
}

func TestHistoryExportAndClearAreOwnerBoundAndCSRFProtected(t *testing.T) {
	learning := openLearning(t, time.Now)
	owner := Principal{AccountID: "history-owner", Personalization: true, CSRFToken: "owner-csrf"}
	other := Principal{AccountID: "history-other", Personalization: true, CSRFToken: "other-csrf"}
	if err := learning.RecordSearch(t.Context(), owner, "owner-only query"); err != nil {
		t.Fatal(err)
	}
	if err := learning.RecordSearch(t.Context(), other, "other-only query"); err != nil {
		t.Fatal(err)
	}
	handler := accountPagesHandler{learning: learning}
	get := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, PathAccountHistory, nil)
	request = request.WithContext(WithPrincipal(request.Context(), owner))
	handler.history(get, request)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "owner-only query") ||
		strings.Contains(get.Body.String(), "other-only query") {
		t.Fatalf("owner history export status/body = %d, %s", get.Code, get.Body.String())
	}
	if got := get.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("history cache policy = %q", got)
	}

	apiKey := owner
	apiKey.APIKey = true
	apiRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		PathAccountHistory,
		nil,
	)
	apiRequest = apiRequest.WithContext(WithPrincipal(apiRequest.Context(), apiKey))
	apiResponse := httptest.NewRecorder()
	handler.history(apiResponse, apiRequest)
	if apiResponse.Code != http.StatusUnauthorized {
		t.Fatalf("API key accessed browser history export: %d", apiResponse.Code)
	}

	missingCSRF := httptest.NewRecorder()
	clearRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		PathAccountHistoryClear,
		strings.NewReader(""),
	)
	clearRequest = clearRequest.WithContext(WithPrincipal(clearRequest.Context(), owner))
	handler.clearHistory(missingCSRF, clearRequest)
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("history clear without CSRF status = %d", missingCSRF.Code)
	}

	form := url.Values{"csrf": {owner.CSRFToken}}
	clearRequest = httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		PathAccountHistoryClear,
		strings.NewReader(form.Encode()),
	)
	clearRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	clearRequest = clearRequest.WithContext(WithPrincipal(clearRequest.Context(), owner))
	cleared := httptest.NewRecorder()
	handler.clearHistory(cleared, clearRequest)
	if cleared.Code != http.StatusSeeOther {
		t.Fatalf("owner history clear status = %d, body=%s", cleared.Code, cleared.Body.String())
	}
	ownerRows, err := learning.History(t.Context(), owner.AccountID, 10)
	if err != nil || len(ownerRows) != 0 {
		t.Fatalf("owner history after clear = %+v, %v", ownerRows, err)
	}
	otherRows, err := learning.History(t.Context(), other.AccountID, 10)
	if err != nil || len(otherRows) != 1 || otherRows[0].Query != "other-only query" {
		t.Fatalf("other account history changed: %+v, %v", otherRows, err)
	}
}
