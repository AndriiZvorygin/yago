package crawlbroker

import "github.com/D4rk4/yago/yagocrawlcontract"

func (r *ControlRegistry) broadcastSettingLocked(
	directive yagocrawlcontract.CrawlControlDirective,
) int {
	signalled := 0
	for workerID := range r.workers {
		if r.enqueueLocked(workerID, directive) {
			signalled++
		} else {
			r.initialized[workerID] = false
		}
	}

	return signalled
}
