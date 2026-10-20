package yagonode

import (
	"context"

	"github.com/D4rk4/yago/yagonode/internal/adminauth"
	"github.com/D4rk4/yago/yagonode/internal/personalsearch"
	"github.com/D4rk4/yago/yagonode/internal/tavilyapi"
)

func buildSearchScopeAuthorizer(service *adminauth.Service) tavilyapi.ScopeAuthorizer {
	return searchScopeAuthorizer{authorizer: service.APIKeyAuthorizer()}
}

func legacySearchAPIKeyFor(config nodeConfig) string {
	if config.SearchRequireAPIKey {
		return ""
	}

	return config.SearchAPIKey
}

type searchScopeAuthorizer struct {
	authorizer *adminauth.APIKeyAuthorizer
}

type accountSearchScopeAuthorizer struct {
	accounts *personalsearch.Accounts
	fallback tavilyapi.ScopeAuthorizer
}

func (a accountSearchScopeAuthorizer) Authorize(
	ctx context.Context,
	token string,
	scope tavilyapi.SearchScope,
) tavilyapi.AuthDecision {
	if a.accounts != nil {
		required := "read"
		if scope == tavilyapi.ScopeRaw {
			required = "raw"
		}
		_, known, allowed, err := a.accounts.AuthorizeAPIKey(ctx, token, required)
		if err != nil {
			return tavilyapi.DecisionUnavailable
		}
		if known {
			if allowed {
				return tavilyapi.DecisionAllow
			}
			return tavilyapi.DecisionForbidden
		}
	}
	if a.fallback != nil {
		return a.fallback.Authorize(ctx, token, scope)
	}
	return tavilyapi.DecisionUnauthenticated
}

func (a searchScopeAuthorizer) Authorize(
	ctx context.Context,
	token string,
	scope tavilyapi.SearchScope,
) tavilyapi.AuthDecision {
	return tavilyDecision(a.authorizer.Authorize(ctx, token, adminSearchScope(scope)))
}

func adminSearchScope(scope tavilyapi.SearchScope) adminauth.Scope {
	if scope == tavilyapi.ScopeRaw {
		return adminauth.ScopeSearchRaw
	}

	return adminauth.ScopeSearchRead
}

func tavilyDecision(outcome adminauth.APIKeyOutcome) tavilyapi.AuthDecision {
	switch outcome {
	case adminauth.APIKeyAuthorized:
		return tavilyapi.DecisionAllow
	case adminauth.APIKeyThrottled:
		return tavilyapi.DecisionThrottled
	case adminauth.APIKeyForbidden:
		return tavilyapi.DecisionForbidden
	case adminauth.APIKeyUnavailable:
		return tavilyapi.DecisionUnavailable
	case adminauth.APIKeyUnauthenticated:
		return tavilyapi.DecisionUnauthenticated
	default:
		return tavilyapi.DecisionUnauthenticated
	}
}
