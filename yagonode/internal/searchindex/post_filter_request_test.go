package searchindex

import (
	"path/filepath"
	"testing"
	"time"
)

func TestEveryPostFilterForcesTheCompleteHitScan(t *testing.T) {
	bound := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	for name, req := range map[string]SearchRequest{
		"safe search":          {SafeSearch: true},
		"language":             {Language: "en"},
		"site host":            {SiteHost: "a.example"},
		"included domain":      {IncludeDomain: []string{"a.example"}},
		"excluded domain":      {ExcludeDomain: []string{"a.example"}},
		"since":                {Since: bound},
		"until":                {Until: bound},
		"minimum date":         {MinDate: bound},
		"maximum date":         {MaxDate: bound},
		"minimum first seen":   {MinFirstSeen: bound},
		"maximum first seen":   {MaxFirstSeen: bound},
		"author":               {Author: "writer"},
		"near":                 {Near: true},
		"image content domain": {ContentDomain: "image"},
		"file type":            {FileType: "pdf"},
		"text inside the URL":  {InURL: "download"},
		"top-level domain":     {TLD: "example"},
	} {
		if !hasPostFilters(req) {
			t.Errorf("%s must force post-filtering", name)
		}
	}
}

func TestRequestsWithoutAPostFilterKeepThePagedHitScan(t *testing.T) {
	for name, req := range map[string]SearchRequest{
		"plain query":                {Query: "golang", MaxResults: 10},
		"text content domain":        {ContentDomain: "text"},
		"empty included domain list": {IncludeDomain: []string{}},
		"empty excluded domain list": {ExcludeDomain: []string{}},
	} {
		if hasPostFilters(req) {
			t.Errorf("%s must not force post-filtering", name)
		}
	}
}

func TestBleveDiskPostFilteredSearchNeverExceedsThePageSize(t *testing.T) {
	index := openFirstSeenBoundDiskIndex(t)
	results, err := index.Search(t.Context(), SearchRequest{
		Query: "golang", MaxResults: 1,
		MinFirstSeen: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if results.Total != 2 || len(results.Results) != 1 {
		t.Fatalf("post-filtered page = total %d, %d results", results.Total, len(results.Results))
	}
}

func TestBleveDiskSearchAnswersBlankQueriesAndEmptyPagesWithNothing(t *testing.T) {
	index := openFirstSeenBoundDiskIndex(t)
	for name, req := range map[string]SearchRequest{
		"blank query":        {Query: "   ", MaxResults: 1},
		"zero page size":     {Query: "golang", MaxResults: 0},
		"negative page size": {Query: "golang", MaxResults: -1},
	} {
		results, err := index.Search(t.Context(), req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if results.Total != 0 || len(results.Results) != 0 {
			t.Fatalf("%s = total %d, %d results", name, results.Total, len(results.Results))
		}
	}
	results, err := index.Search(t.Context(), SearchRequest{Query: "  golang  ", MaxResults: 1})
	if err != nil {
		t.Fatal(err)
	}
	if results.Total != 2 || len(results.Results) != 1 {
		t.Fatalf("padded query = total %d, %d results", results.Total, len(results.Results))
	}
}

func openFirstSeenBoundDiskIndex(t *testing.T) *BleveDiskIndex {
	t.Helper()
	documents := firstSeenBoundDocuments()
	index, err := NewBleveDiskIndex(
		t.Context(), filepath.Join(t.TempDir(), "page-size.bleve"),
		newFakeDocumentDirectory(documents...),
		&fakeStoredDocuments{documents: documents},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })

	return index
}
