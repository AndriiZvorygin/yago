package websearch

import (
	"context"
	"testing"
)

func TestKnownResultsWaitForPrimarySnapshot(t *testing.T) {
	primary := &primaryResultSnapshot{
		ready: make(chan struct{}),
		known: map[string]struct{}{"https://example.com/a": {}},
	}
	close(primary.ready)

	known := providerQuery{primary: primary}.known(t.Context())

	if _, ok := known["https://example.com/a"]; !ok || len(known) != 1 {
		t.Fatalf("known = %v, want the primary snapshot", known)
	}
}

func TestKnownResultsStopWaitingWhenContextEnds(t *testing.T) {
	primary := &primaryResultSnapshot{
		ready: make(chan struct{}),
		known: map[string]struct{}{"https://example.com/a": {}},
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if known := (providerQuery{primary: primary}).known(ctx); known != nil {
		t.Fatalf("known = %v, want nil after the context ends", known)
	}
}
