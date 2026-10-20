package yagonode

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/adminauth"
	"github.com/D4rk4/yago/yagonode/internal/memvault"
	"github.com/D4rk4/yago/yagonode/internal/personalsearch"
	"github.com/D4rk4/yago/yagonode/internal/tavilyapi"
)

func TestBuildSearchScopeAuthorizer(t *testing.T) {
	storage, err := memvault.Open(0)
	if err != nil {
		t.Fatalf("memvault.Open: %v", err)
	}
	service, err := adminauth.New(storage, adminauth.Config{})
	if err != nil {
		t.Fatalf("adminauth.New: %v", err)
	}
	if buildSearchScopeAuthorizer(service) == nil {
		t.Fatal("authorizer must be built")
	}
}

func TestLegacySearchAPIKeyPolicy(t *testing.T) {
	config := nodeConfig{SearchAPIKey: "static"}
	if got := legacySearchAPIKeyFor(config); got != "static" {
		t.Fatalf("legacy token = %q", got)
	}
	config.SearchRequireAPIKey = true
	if got := legacySearchAPIKeyFor(config); got != "" {
		t.Fatalf("scoped-only legacy token = %q, want empty", got)
	}
}

func TestTavilyDecisionMapping(t *testing.T) {
	for outcome, want := range map[adminauth.APIKeyOutcome]tavilyapi.AuthDecision{
		adminauth.APIKeyAuthorized:      tavilyapi.DecisionAllow,
		adminauth.APIKeyThrottled:       tavilyapi.DecisionThrottled,
		adminauth.APIKeyForbidden:       tavilyapi.DecisionForbidden,
		adminauth.APIKeyUnavailable:     tavilyapi.DecisionUnavailable,
		adminauth.APIKeyUnauthenticated: tavilyapi.DecisionUnauthenticated,
	} {
		if got := tavilyDecision(outcome); got != want {
			t.Errorf("tavilyDecision(%v) = %v, want %v", outcome, got, want)
		}
	}
}

func TestAdminSearchScopeMapping(t *testing.T) {
	if got := adminSearchScope(tavilyapi.ScopeRaw); got != adminauth.ScopeSearchRaw {
		t.Errorf("raw -> %v", got)
	}
	if got := adminSearchScope(tavilyapi.ScopeRead); got != adminauth.ScopeSearchRead {
		t.Errorf("read -> %v", got)
	}
}

func TestSearchAccessPolicyCombinesAuthorizerAndStaticToken(t *testing.T) {
	scoped := searchAccessPolicy(publicSearchAssembly{
		searchAuthorizer: searchScopeAuthorizer{},
		searchAPIKey:     "static",
	})
	if scoped.Authorizer == nil || scoped.BearerToken != "static" {
		t.Fatalf("scoped policy = %#v", scoped)
	}

	static := searchAccessPolicy(publicSearchAssembly{searchAPIKey: "static"})
	if static.Authorizer != nil || static.BearerToken != "static" {
		t.Fatalf("static policy = %#v", static)
	}
}

func TestPersonalSearchAPIKeyUsesItsAccountScopes(t *testing.T) {
	accounts, accountID := createScopedTestAccount(t)
	key, err := accounts.CreateAPIKey(t.Context(), accountID, "read-only", []string{"read"})
	if err != nil {
		t.Fatal(err)
	}
	authorizer := accountSearchScopeAuthorizer{accounts: accounts}
	if got := authorizer.Authorize(
		t.Context(),
		key.Secret,
		tavilyapi.ScopeRead,
	); got != tavilyapi.DecisionAllow {
		t.Fatalf("read scope decision = %v", got)
	}
	if got := authorizer.Authorize(
		t.Context(),
		key.Secret,
		tavilyapi.ScopeRaw,
	); got != tavilyapi.DecisionForbidden {
		t.Fatalf("raw scope decision = %v, want forbidden", got)
	}
	if got := authorizer.Authorize(
		t.Context(),
		"ygp_unknown",
		tavilyapi.ScopeRead,
	); got != tavilyapi.DecisionUnauthenticated {
		t.Fatalf("unknown account-key decision = %v", got)
	}
}

func TestPersonalRawScopeDoesNotAuthorizeCrawl(t *testing.T) {
	accounts, accountID := createScopedTestAccount(t)
	rawKey, err := accounts.CreateAPIKey(
		t.Context(), accountID, "raw-content", []string{"read", "raw"},
	)
	if err != nil {
		t.Fatal(err)
	}
	assembly := publicSearchAssembly{personalAccounts: accounts}
	searchPolicy := searchAccessPolicy(assembly)
	if got := searchPolicy.Authorizer.Authorize(
		t.Context(), rawKey.Secret, tavilyapi.ScopeRaw,
	); got != tavilyapi.DecisionAllow {
		t.Fatalf("account raw-content access decision = %v", got)
	}
	assertAccountKeyCannotCrawl(t, rawKey.Secret, crawlAccessPolicy(assembly))
}

func createScopedTestAccount(t *testing.T) (*personalsearch.Accounts, string) {
	t.Helper()
	storage, err := memvault.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	accounts, err := personalsearch.NewAccounts(storage, personalsearch.AccountsConfig{})
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := accounts.IssueInvitation(t.Context(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	account, err := accounts.ClaimInvitation(
		t.Context(),
		invitation.Token,
		"agent_reader",
		"a sufficiently long password",
	)
	if err != nil {
		t.Fatal(err)
	}
	return accounts, account.ID
}

func assertAccountKeyCannotCrawl(
	t *testing.T,
	secret string,
	policy tavilyapi.SearchAccessPolicy,
) {
	t.Helper()
	crawlMux := http.NewServeMux()
	tavilyapi.MountCrawl(crawlMux, policy, nil)
	crawlResponse := httptest.NewRecorder()
	crawlRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		tavilyapi.PathCrawl,
		strings.NewReader(`{"url":"https://example.org/"}`),
	)
	crawlRequest.Header.Set("Authorization", "Bearer "+secret)
	crawlRequest.Header.Set("Content-Type", "application/json")
	crawlMux.ServeHTTP(crawlResponse, crawlRequest)
	if crawlResponse.Code != http.StatusUnauthorized {
		t.Fatalf(
			"account raw key accessed remote crawl: status=%d body=%s",
			crawlResponse.Code,
			crawlResponse.Body.String(),
		)
	}
}

func TestLoadDerivedConfigsRequireAPIKey(t *testing.T) {
	getenv := func(key string) string {
		if key == envSearchRequireAPIKey {
			return "true"
		}

		return ""
	}
	derived, err := loadDerivedConfigs(getenv)
	if err != nil {
		t.Fatalf("loadDerivedConfigs: %v", err)
	}
	if !derived.requireAPIKey {
		t.Fatal("YAGO_SEARCH_REQUIRE_API_KEY=true should disable the legacy token")
	}
}

func TestScopedOnlySettingExplainsLegacyCompatibility(t *testing.T) {
	definition := settingByKey(
		t,
		storageAndAccessDefinitions(),
		"search.api.scoped_access",
	)
	if definition.title != "Require scoped API keys only" {
		t.Fatalf("title = %q", definition.title)
	}
	for _, phrase := range []string{"holding the required scope", "legacy static"} {
		if !strings.Contains(definition.description, phrase) {
			t.Fatalf("description missing %q: %q", phrase, definition.description)
		}
	}
}
