package documentimport

import "github.com/D4rk4/yago/yagocrawlcontract"

// Import payload bounds are separate from crawler-ingest limits because YaCy
// can store larger extracted documents than the live crawler contract allows.
// Keep migration metadata below half the request cap so JSON framing and other
// document fields remain bounded inside one import request.
const (
	MaximumBatchRecords        = 256
	MaximumRequestBytes        = 64 << 20
	MaximumMigrationURLBytes   = 16 << 10
	MaximumRecordTextBytes     = 32 << 20
	MaximumRecordMetadataBytes = 8 << 20
	MaximumRecordHeadingBytes  = yagocrawlcontract.MaximumDocumentHeadingBytes
	MaximumRecordHeadings      = 4096
	MaximumRecordOutlinks      = 131_072
	MaximumRecordInlinks       = 65_536
)
