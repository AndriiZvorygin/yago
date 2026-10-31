package yagonode

import (
	"context"
	"time"
)

var webFallbackSupplementDecisionCeiling = 600 * time.Millisecond

const (
	webFallbackAssemblyReserve   = 100 * time.Millisecond
	webFallbackMinimumWindow     = time.Second
	webFallbackSupplementReserve = webFallbackMinimumWindow + webFallbackAssemblyReserve -
		interactiveSearchCancellationGrace
)

func webFallbackSupplementDecisionDelay(ctx context.Context) time.Duration {
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return webFallbackSupplementDecisionCeiling
	}
	available := time.Until(deadline) - webFallbackSupplementReserve
	if available <= 0 {
		return webFallbackSupplementDecisionCeiling
	}

	return min(webFallbackSupplementDecisionCeiling, available)
}
