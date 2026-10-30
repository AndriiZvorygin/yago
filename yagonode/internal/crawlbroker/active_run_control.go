package crawlbroker

import "github.com/D4rk4/yago/yagocrawlcontract"

func (r *ControlRegistry) MaximumActiveRuns() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return int(r.maximumActiveRuns)
}

func (r *ControlRegistry) SetMaximumActiveRuns(maximumActiveRuns int) int {
	if maximumActiveRuns < 1 ||
		maximumActiveRuns > yagocrawlcontract.MaximumActiveCrawlRunConcurrency {
		return 0
	}

	directive := yagocrawlcontract.CrawlControlDirective{
		Kind:              yagocrawlcontract.CrawlControlSetActiveRuns,
		MaximumActiveRuns: uint32(maximumActiveRuns),
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.maximumActiveRuns = uint32(maximumActiveRuns)
	r.maximumActiveRunsSet = true

	return r.broadcastSettingLocked(directive)
}
