package yagocrawlcontract

import "testing"

func TestCrawlOrderImportBatchIdentityIsStableAndContentBound(t *testing.T) {
	base := CrawlOrderImportBatch{
		Version:  1,
		SourceID: "source-a",
		Profile: CrawlProfile{
			Handle:         "profile-a",
			SourceMetadata: map[string]string{"yacy.depth": "2", "yacy.name": "source"},
		},
		Requests: []CrawlRequest{{
			URL:           "https://example.test/a",
			ProfileHandle: "profile-a",
			SourceMetadata: map[string]string{
				"yacy.stack": "LOCAL",
			},
		}},
	}
	first, err := base.ComputeBatchID()
	if err != nil {
		t.Fatalf("compute base batch ID: %v", err)
	}
	base.BatchID = first

	mapOrderChanged := base
	mapOrderChanged.Profile.SourceMetadata = map[string]string{"yacy.name": "source", "yacy.depth": "2"}
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
