package yagonode

import (
	"context"

	"github.com/D4rk4/yago/yagonode/internal/personalsearch"
	"github.com/D4rk4/yago/yagonode/internal/tavilyapi"
)

type personalTavilySearchFeedbackRecorder struct {
	learning *personalsearch.Learning
	enabled  func() bool
}

func newPersonalTavilySearchFeedbackRecorder(
	assembly publicSearchAssembly,
) tavilyapi.SearchFeedbackRecorder {
	if assembly.personalLearning == nil {
		return nil
	}
	return personalTavilySearchFeedbackRecorder{
		learning: assembly.personalLearning,
		enabled:  assembly.personalizationEnabled,
	}
}

func (r personalTavilySearchFeedbackRecorder) PrepareSearchImpression(
	ctx context.Context,
	query string,
	results []tavilyapi.SearchResult,
) string {
	if r.learning == nil || !personalsearch.PersonalizationEnabled(r.enabled) {
		return ""
	}
	principal, authenticated := personalsearch.PrincipalFromContext(ctx)
	if !authenticated || !principal.Personalization || len(results) == 0 {
		return ""
	}
	candidates := make([]personalsearch.ImpressionCandidate, len(results))
	for index, result := range results {
		candidates[index] = personalsearch.ImpressionCandidate{
			URLIdentity: result.URL, Position: index + 1, Rank: index + 1,
		}
	}
	token, err := r.learning.PrepareImpression(ctx, principal, query, candidates)
	if err != nil {
		return ""
	}
	return token
}
