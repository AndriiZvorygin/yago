package searchcore

import "context"

type sessionScopeContextKey struct{}

func WithSessionScope(ctx context.Context, scope string) context.Context {
	if scope == "" {
		return ctx
	}

	return context.WithValue(ctx, sessionScopeContextKey{}, scope)
}

func SessionScope(ctx context.Context) string {
	scope, _ := ctx.Value(sessionScopeContextKey{}).(string)

	return scope
}
