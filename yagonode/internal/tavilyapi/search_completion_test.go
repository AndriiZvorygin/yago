package tavilyapi

import (
	"errors"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/searchcore"
)

func TestSearchCompletionUsesAvailabilityFailureForLostSources(t *testing.T) {
	completion := searchCompletion{
		response: searchcore.Response{PartialFailures: []searchcore.PartialFailure{{
			Source: searchcore.PartialFailureSourceRemoteYaCy,
			Reason: "peer unavailable",
		}}},
		err: errors.New("search returned partial results"),
	}
	if err := completion.errorForCaller(
		nil,
	); err == nil ||
		err.Error() == "search returned partial results" {
		t.Fatalf("lost-source availability error=%v", err)
	}
}
