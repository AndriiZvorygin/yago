// Package documentimport defines the versioned document-import boundary shared
// by the node's admin API and offline migration tools.
package documentimport

import (
	"context"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
)

// Record is one source document plus optional source recrawl scheduling state.
type Record struct {
	ID          string                           `json:"id"`
	Document    yagocrawlcontract.DocumentIngest `json:"document"`
	NextVisitAt *time.Time                       `json:"nextVisitAt,omitempty"`
}

// Result is the idempotent import receipt for one record.
type Result struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Request is one bounded, versioned batch accepted by the node import API.
type Request struct {
	Version int      `json:"version"`
	DryRun  bool     `json:"dryRun,omitempty"`
	Records []Record `json:"records"`
}

// Importer stores documents through the node's ordinary ingest and store
// boundaries; implementations must make retries idempotent by record and URL.
type Importer interface {
	ImportDocuments(ctx context.Context, records []Record) ([]Result, error)
}
