package personalsearch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/boltvault"
)

func openAccounts(t *testing.T, now func() time.Time) *Accounts {
	t.Helper()
	storage, err := boltvault.Open(filepath.Join(t.TempDir(), "state.db"), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	accounts, err := NewAccounts(storage, AccountsConfig{Now: now})
	if err != nil {
		t.Fatal(err)
	}
	return accounts
}

func claimTestAccount(t *testing.T, accounts *Accounts) Account {
	t.Helper()
	invitation, err := accounts.IssueInvitation(t.Context(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	account, err := accounts.ClaimInvitation(
		t.Context(),
		invitation.Token,
		"Test.User",
		"a sufficiently long test password",
	)
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func TestInvitationClaimIsExpiringAndOneTime(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	accounts := openAccounts(t, func() time.Time { return now })
	invitation, err := accounts.IssueInvitation(t.Context(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	account, err := accounts.ClaimInvitation(
		t.Context(),
		invitation.Token,
		"reader_one",
		"a sufficiently long password",
	)
	if err != nil || account.Personalization || !account.Active {
		t.Fatalf("claim result = %+v, %v", account, err)
	}
	if _, err := accounts.ClaimInvitation(
		t.Context(),
		invitation.Token,
		"reader_two",
		"a sufficiently long password",
	); !errors.Is(
		err,
		ErrInvitationInvalid,
	) {
		t.Fatalf("replayed invitation error = %v", err)
	}

	expiring, err := accounts.IssueInvitation(t.Context(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := accounts.ClaimInvitation(
		t.Context(),
		expiring.Token,
		"reader_three",
		"a sufficiently long password",
	); !errors.Is(
		err,
		ErrInvitationInvalid,
	) {
		t.Fatalf("expired invitation error = %v", err)
	}
}

func TestExpiredAccountSessionsAndInvitationsArePrunedInBoundedBatches(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	accounts := openAccounts(t, func() time.Time { return now })
	invitation, err := accounts.IssueInvitation(t.Context(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	account, err := accounts.ClaimInvitation(
		t.Context(),
		invitation.Token,
		"retention_user",
		"a sufficiently long password",
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := accounts.Login(
		t.Context(),
		account.LoginName,
		"a sufficiently long password",
	); err != nil {
		t.Fatal(err)
	}
	now = now.Add(13 * time.Hour)
	if removed, err := accounts.PruneExpiredSessions(t.Context(), 1); err != nil || removed != 1 {
		t.Fatalf("expired sessions pruned = %d, %v", removed, err)
	}
	if removed, err := accounts.PruneExpiredSessions(t.Context(), 1); err != nil || removed != 0 {
		t.Fatalf("second session cleanup = %d, %v", removed, err)
	}
	if removed, err := accounts.PruneExpiredInvitations(
		t.Context(),
		1,
	); err != nil ||
		removed != 1 {
		t.Fatalf("expired invitations pruned = %d, %v", removed, err)
	}
}

func TestAccountLoginSessionAndAccountOwnedAPIKey(t *testing.T) {
	accounts := openAccounts(t, time.Now)
	account := claimTestAccount(t, accounts)
	if _, _, err := accounts.Login(
		t.Context(),
		account.LoginName,
		"wrong password long enough",
	); !errors.Is(
		err,
		ErrLoginRejected,
	) {
		t.Fatalf("wrong-password error = %v", err)
	}
	session, loggedIn, err := accounts.Login(
		t.Context(),
		" TEST.USER ",
		"a sufficiently long test password",
	)
	if err != nil || loggedIn.ID != account.ID {
		t.Fatalf("login = %+v, %v", loggedIn, err)
	}
	principal, ok, err := accounts.AuthenticateSession(t.Context(), session.Token)
	if err != nil || !ok || principal.AccountID != account.ID || principal.CSRFToken != "" ||
		principal.csrfHash != secretHash(session.CSRFToken) {
		t.Fatalf("session principal = %+v, %v, %v", principal, ok, err)
	}
	assertAccountSessionMiddleware(t, accounts, session)

	key, err := accounts.CreateAPIKey(t.Context(), account.ID, "agent", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	apiPrincipal, ok, err := accounts.AuthenticateAPIKey(t.Context(), key.Secret, "read")
	if err != nil || !ok || apiPrincipal.AccountID != account.ID || !apiPrincipal.APIKey {
		t.Fatalf("API-key principal = %+v, %v, %v", apiPrincipal, ok, err)
	}
	if _, ok, err := accounts.AuthenticateAPIKey(t.Context(), key.Secret, "raw"); err != nil || ok {
		t.Fatalf("read-only key authorized raw content: %v, %v", ok, err)
	}
	if _, ok, err := accounts.AuthenticateAPIKey(
		t.Context(),
		key.Secret+"x",
		"read",
	); err != nil ||
		ok {
		t.Fatalf("altered API key authorized: %v, %v", ok, err)
	}

	if err := accounts.SetPersonalization(t.Context(), account.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := accounts.DisableAccount(t.Context(), account.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := accounts.AuthenticateSession(t.Context(), session.Token); err != nil || ok {
		t.Fatalf("disabled account session remained valid: %v, %v", ok, err)
	}
	if _, ok, err := accounts.AuthenticateAPIKey(
		t.Context(),
		key.Secret,
		"read",
	); err != nil ||
		ok {
		t.Fatalf("disabled account API key remained valid: %v, %v", ok, err)
	}
}

func TestMalformedAndUnknownAccountCredentialsFailClosed(t *testing.T) {
	accounts := openAccounts(t, time.Now)
	if _, _, err := accounts.Login(
		context.Background(),
		"missing",
		"a sufficiently long password",
	); !errors.Is(
		err,
		ErrLoginRejected,
	) {
		t.Fatalf("unknown-account error = %v", err)
	}
	if _, ok, err := accounts.AuthenticateAPIKey(
		t.Context(),
		"ygp_not_a_key",
		"read",
	); err != nil ||
		ok {
		t.Fatalf("malformed API key result = %v, %v", ok, err)
	}
	if _, err := accounts.ClaimInvitation(
		t.Context(),
		"not-a-real-invitation",
		"valid_user",
		"short",
	); !errors.Is(
		err,
		ErrCredentialInvalid,
	) {
		t.Fatalf("invalid signup fields error = %v", err)
	}
}

func TestInvitationAndLoginFormsRequireCSRF(t *testing.T) {
	accounts := openAccounts(t, time.Now)
	mux := http.NewServeMux()
	accounts.MountUserPages(mux, nil, nil)
	handler := accounts.Middleware(mux)

	for _, path := range []string{PathAccountClaim, PathAccountLogin} {
		get := httptest.NewRecorder()
		handler.ServeHTTP(
			get,
			httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil),
		)
		if get.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, get.Code)
		}
		var enrollmentCookie *http.Cookie
		for _, cookie := range get.Result().Cookies() {
			if cookie.Name == accountEnrollmentCookie {
				enrollmentCookie = cookie
			}
		}
		if enrollmentCookie == nil || !enrollmentCookie.Secure || !enrollmentCookie.HttpOnly ||
			enrollmentCookie.SameSite != http.SameSiteStrictMode {
			t.Fatalf("%s enrollment cookie is not protected: %+v", path, enrollmentCookie)
		}
		if !strings.Contains(get.Body.String(), `name="csrf"`) {
			t.Fatalf("GET %s did not render a form token", path)
		}

		form := url.Values{}
		if path == PathAccountClaim {
			form.Set("token", "not-a-real-invitation")
			form.Set("login", "reader")
			form.Set("password", "a sufficiently long password")
		} else {
			form.Set("login", "missing")
			form.Set("password", "a sufficiently long password")
		}
		post := httptest.NewRecorder()
		request := httptest.NewRequestWithContext(
			t.Context(),
			http.MethodPost,
			path,
			strings.NewReader(form.Encode()),
		)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.AddCookie(enrollmentCookie)
		handler.ServeHTTP(post, request)
		if post.Code != http.StatusForbidden {
			t.Fatalf(
				"POST %s without matching form token status = %d, want forbidden",
				path,
				post.Code,
			)
		}
	}
}

func assertAccountSessionMiddleware(t *testing.T, accounts *Accounts, session AccountSession) {
	t.Helper()
	var csrfFromMiddleware string
	handler := accounts.Middleware(
		http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			resolved, found := PrincipalFromContext(request.Context())
			if found {
				csrfFromMiddleware = resolved.CSRFToken
			}
		}),
	)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	request.AddCookie(&http.Cookie{Name: accountSessionCookie, Value: session.Token})
	request.AddCookie(&http.Cookie{Name: accountCSRFCookie, Value: session.CSRFToken})
	middlewareResponse := httptest.NewRecorder()
	handler.ServeHTTP(middlewareResponse, request)
	if csrfFromMiddleware != session.CSRFToken {
		t.Fatal("valid CSRF cookie was not restored to the authenticated request")
	}
	if middlewareResponse.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf(
			"authenticated response cache policy = %q",
			middlewareResponse.Header().Get("Cache-Control"),
		)
	}
}
