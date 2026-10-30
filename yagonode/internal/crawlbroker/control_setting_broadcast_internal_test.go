package crawlbroker

import (
	"slices"
	"testing"

	"github.com/D4rk4/yago/yagocrawlcontract"
)

func TestFleetSettingsAcceptTheirContractEdges(t *testing.T) {
	settings := []struct {
		name     string
		set      func(*ControlRegistry, int) int
		accepted []int
	}{
		{
			name:     "fetch-workers",
			set:      (*ControlRegistry).SetFetchWorkers,
			accepted: []int{1, yagocrawlcontract.MaximumFetchWorkerConcurrency},
		},
		{
			name:     "maximum-redirects",
			set:      (*ControlRegistry).SetMaximumRedirects,
			accepted: []int{0},
		},
		{
			name:     "active-runs",
			set:      (*ControlRegistry).SetMaximumActiveRuns,
			accepted: []int{1},
		},
	}
	for _, setting := range settings {
		t.Run(setting.name, func(t *testing.T) {
			registry := newControlRegistry()
			registry.register("crawler")
			for _, value := range setting.accepted {
				if signalled := setting.set(registry, value); signalled != 1 {
					t.Fatalf("%s %d signalled %d workers, want 1",
						setting.name, value, signalled)
				}
			}
		})
	}
}

func TestRuntimeFleetSettingsReachCrawlersThatConnectLater(t *testing.T) {
	registry := newControlRegistry()
	registry.SetProcessPagesPerSecond(7)
	registry.SetMaximumRedirects(3)
	registry.SetMaximumActiveRuns(5)
	registry.register("crawler")
	delivered := deliveredControls(t, registry, "crawler")
	for index := range delivered {
		delivered[index].DirectiveID = 0
	}
	want := []yagocrawlcontract.CrawlControlDirective{
		{Kind: yagocrawlcontract.CrawlControlSetProcessRate, ProcessPagesPerSecond: 7},
		{Kind: yagocrawlcontract.CrawlControlSetMaximumRedirects, MaximumRedirects: 3},
		{Kind: yagocrawlcontract.CrawlControlSetActiveRuns, MaximumActiveRuns: 5},
	}
	if !slices.Equal(delivered, want) {
		t.Fatalf("late crawler directives = %+v, want %+v", delivered, want)
	}
}
