package yagocrawlcontract

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagomodel"
)

func TestCrawlOrderImportBatchIdentityIsStableAndContentBound(t *testing.T) {
	base := CrawlOrderImportBatch{
		Version:  1,
		SourceID: "source-a",
		Profile: CrawlProfile{
			Handle: "profile-a",
			SourceMetadata: NewSourceMetadata(
				map[string]string{"yacy.depth": "2", "yacy.name": "source"},
			),
		},
		Requests: []CrawlRequest{{
			URL:           "https://example.test/a",
			ProfileHandle: "profile-a",
			SourceMetadata: NewSourceMetadata(map[string]string{
				"yacy.stack": "LOCAL",
			}),
		}},
	}
	first, err := base.ComputeBatchID()
	if err != nil {
		t.Fatalf("compute base batch ID: %v", err)
	}
	base.BatchID = first

	mapOrderChanged := base
	mapOrderChanged.Profile.SourceMetadata = NewSourceMetadata(map[string]string{
		"yacy.name":  "source",
		"yacy.depth": "2",
	})
	mapOrderChanged.SourceID = "source-b"
	mapOrderChanged.DryRun = true
	second, err := mapOrderChanged.ComputeBatchID()
	if err != nil {
		t.Fatalf("compute equivalent batch ID: %v", err)
	}
	if first != second {
		t.Fatalf("equivalent payload IDs differ: %q != %q", first, second)
	}
	if got := base.IdempotencyKey(); got != "frontier:source-a:"+first {
		t.Fatalf("idempotency key = %q", got)
	}

	changed := base
	changed.Requests = append([]CrawlRequest(nil), base.Requests...)
	changed.Requests[0].URL = "https://example.test/b"
	third, err := changed.ComputeBatchID()
	if err != nil {
		t.Fatalf("compute changed batch ID: %v", err)
	}
	if first == third {
		t.Fatal("different crawl request reused the same batch ID")
	}
}

func TestCrawlOrderImportBatchValidateChecksIdentityAndBounds(t *testing.T) {
	batch := CrawlOrderImportBatch{
		Version:  1,
		SourceID: "source-a",
		Profile: CrawlProfile{
			Handle:          "profile-a",
			Name:            "fixture profile",
			URLMustMatch:    MatchAll,
			MaxPagesPerHost: UnlimitedPagesPerHost,
		},
		Requests: []CrawlRequest{{
			URL:           "https://example.test/page",
			ProfileHandle: "profile-a",
			Depth:         2,
		}},
	}
	batch.BatchID, _ = batch.ComputeBatchID()
	if err := batch.Validate(); err != nil {
		t.Fatalf("valid import batch rejected: %v", err)
	}

	changed := batch
	changed.Requests = append([]CrawlRequest(nil), batch.Requests...)
	changed.Requests[0].URL = "https://example.test/other"
	if err := changed.Validate(); err == nil {
		t.Fatal("payload with stale batch ID was accepted")
	}

	changed = batch
	changed.Requests = append([]CrawlRequest(nil), batch.Requests...)
	changed.Requests[0].URL = "https://user:secret@example.test/private"
	changed.BatchID, _ = changed.ComputeBatchID()
	if err := changed.Validate(); err == nil {
		t.Fatal("URL containing user credentials was accepted")
	}

	changed = batch
	changed.Requests = append([]CrawlRequest(nil), batch.Requests...)
	changed.Requests[0].Depth = MaxCrawlDepth + 1
	changed.BatchID, _ = changed.ComputeBatchID()
	if err := changed.Validate(); err == nil {
		t.Fatal("request depth above the supported maximum was accepted")
	}
}

func TestCrawlOrderImportBatchValidateRejectsInvalidFields(t *testing.T) {
	for _, test := range crawlOrderImportValidationCases() {
		t.Run(test.name, func(t *testing.T) {
			batch := validCrawlOrderImportBatch(t)
			test.mutate(&batch)
			if !test.preserveBatchID {
				batch.BatchID, _ = batch.ComputeBatchID()
			}
			if err := batch.Validate(); err == nil {
				t.Fatal("invalid import batch was accepted")
			}
		})
	}
}

type crawlOrderImportValidationCase struct {
	name            string
	preserveBatchID bool
	mutate          func(*CrawlOrderImportBatch)
}

func crawlOrderImportValidationCases() []crawlOrderImportValidationCase {
	cases := crawlOrderImportEnvelopeValidationCases()
	cases = append(cases, crawlOrderImportProfileValidationCases()...)
	cases = append(cases, crawlOrderImportRequestValidationCases()...)
	return append(cases, crawlOrderImportMetadataValidationCases()...)
}

func crawlOrderImportEnvelopeValidationCases() []crawlOrderImportValidationCase {
	return []crawlOrderImportValidationCase{
		{
			name: "unsupported version",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Version = 2
			},
		},
		{
			name: "invalid source ID",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.SourceID = "source id"
			},
		},
		{
			name:            "malformed batch ID",
			preserveBatchID: true,
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.BatchID = "!" + batch.BatchID[1:]
			},
		},
		{
			name:            "batch ID has wrong length",
			preserveBatchID: true,
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.BatchID = batch.BatchID[:len(batch.BatchID)-1]
			},
		},
		{
			name:            "batch ID is not canonical lowercase",
			preserveBatchID: true,
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.BatchID = strings.ToUpper(batch.BatchID)
			},
		},
		{
			name: "empty request list",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests = nil
			},
		},
		{
			name: "request list exceeds limit",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests = make([]CrawlRequest, MaximumCrawlOrderImportBatchRecords+1)
			},
		},
	}
}

func crawlOrderImportProfileValidationCases() []crawlOrderImportValidationCase {
	return []crawlOrderImportValidationCase{
		{
			name: "invalid crawl profile",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Profile.MaxDepth = MaxCrawlDepth + 1
			},
		},
		{
			name: "invalid UTF-8 profile name",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Profile.Name = string([]byte{0xff})
			},
		},
		{
			name: "invalid profile metadata",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Profile.SourceMetadata = NewSourceMetadata(map[string]string{"": "value"})
			},
		},
		{
			name: "too many profile metadata fields",
			mutate: func(batch *CrawlOrderImportBatch) {
				metadata := make(map[string]string, MaximumCrawlOrderImportMetadata+1)
				for index := range MaximumCrawlOrderImportMetadata + 1 {
					metadata[fmt.Sprintf("field-%d", index)] = "value"
				}
				batch.Profile.SourceMetadata = NewSourceMetadata(metadata)
			},
		},
	}
}

func crawlOrderImportRequestValidationCases() []crawlOrderImportValidationCase {
	return []crawlOrderImportValidationCase{
		{
			name: "credential URL",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].URL = "https://user:secret@example.test/page"
			},
		},
		{
			name: "empty URL",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].URL = ""
			},
		},
		{
			name: "unsupported URL scheme",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].URL = "file://example.test/private"
			},
		},
		{
			name: "unsupported request mode",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].Mode = "archive"
			},
		},
		{
			name: "invalid referrer URL",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].ReferrerURL = "mailto:person@example.test"
			},
		},
		{
			name: "unsupported referrer scheme",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].ReferrerURL = "file://example.test/source"
			},
		},
		{
			name: "invalid initiator",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].Initiator = yagomodel.Hash("bad")
			},
		},
		{
			name: "duplicate URL and profile",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests = append(batch.Requests, batch.Requests[0])
			},
		},
	}
}

func crawlOrderImportMetadataValidationCases() []crawlOrderImportValidationCase {
	return []crawlOrderImportValidationCase{
		{
			name: "invalid request metadata",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].SourceMetadata = NewSourceMetadata(map[string]string{"": "value"})
			},
		},
		{
			name: "invalid UTF-8 metadata",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].SourceMetadata = NewSourceMetadata(
					map[string]string{"field": string([]byte{0xff})},
				)
			},
		},
		{
			name: "metadata exceeds byte budget",
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].SourceMetadata = NewSourceMetadata(map[string]string{
					"field": strings.Repeat("x", MaximumCrawlOrderImportMetadataBytes),
				})
			},
		},
		{
			name:            "unencodable batch identity",
			preserveBatchID: true,
			mutate: func(batch *CrawlOrderImportBatch) {
				batch.Requests[0].AppDate = time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
			},
		},
	}
}

func TestCrawlOrderImportBatchValidatePreservesValidOptionalMetadata(t *testing.T) {
	batch := validCrawlOrderImportBatch(t)
	batch.Requests[0].Mode = CrawlRequestModeSitemap
	batch.Requests[0].ReferrerURL = "https://example.test/parent"
	batch.Requests[0].Initiator = yagomodel.WordHash("migration")
	batch.Profile.SourceMetadata = NewSourceMetadata(map[string]string{"source": "fixture"})
	batch.Requests[0].SourceMetadata = NewSourceMetadata(map[string]string{"state": "queued"})
	var err error
	batch.BatchID, err = batch.ComputeBatchID()
	if err != nil {
		t.Fatalf("compute batch ID: %v", err)
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("valid optional import metadata rejected: %v", err)
	}
}

func validCrawlOrderImportBatch(t *testing.T) CrawlOrderImportBatch {
	t.Helper()
	batch := CrawlOrderImportBatch{
		Version:  1,
		SourceID: "source-a",
		Profile: CrawlProfile{
			Handle:          "profile-a",
			Name:            "fixture profile",
			URLMustMatch:    MatchAll,
			MaxPagesPerHost: UnlimitedPagesPerHost,
		},
		Requests: []CrawlRequest{{
			URL:           "https://example.test/page",
			ProfileHandle: "profile-a",
			Depth:         2,
		}},
	}
	var err error
	batch.BatchID, err = batch.ComputeBatchID()
	if err != nil {
		t.Fatalf("compute fixture batch ID: %v", err)
	}
	return batch
}
