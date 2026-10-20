package searchsession

import (
	"context"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

type ScopedRecentWindow interface {
	RecentScoped(context.Context, searchcore.Request) (searchcore.Response, bool)
}

func recentCoverage(
	ctx context.Context,
	recent RecentWindow,
	request searchcore.Request,
) (searchcore.Response, bool) {
	response, found := recentWindowResponse(ctx, recent, request)
	if found || request.Source != searchcore.SourceGlobal {
		return response, found
	}
	localRequest := request
	localRequest.Source = searchcore.SourceLocal
	response, found = recentWindowResponse(ctx, recent, localRequest)
	if found {
		response.Request = request
	}

	return response, found
}

func recentWindowResponse(
	ctx context.Context,
	recent RecentWindow,
	request searchcore.Request,
) (searchcore.Response, bool) {
	if scoped, ok := recent.(ScopedRecentWindow); ok {
		return scoped.RecentScoped(ctx, request)
	}

	return recent.Recent(request)
}
