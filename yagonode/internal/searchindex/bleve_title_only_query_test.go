package searchindex

import (
	"path/filepath"
	"testing"

	"github.com/D4rk4/yago/yagonode/internal/documentstore"
)

func TestTitleOnlySearchMatchesTitleAndNotBody(t *testing.T) {
	index, err := NewBleveMemoryIndex(t.Context(), nil)
	if err != nil {
		t.Fatalf("NewBleveMemoryIndex: %v", err)
	}
	for _, document := range []documentstore.Document{
		{
			NormalizedURL: "https://title.example/",
			Title:         "Needle reference",
			ExtractedText: "urban planning",
			Language:      "en",
		},
		{
			NormalizedURL: "https://body.example/",
			Title:         "Urban planning",
			ExtractedText: "Needle reference",
			Language:      "en",
		},
	} {
		if err := index.Index(t.Context(), document); err != nil {
			t.Fatalf("Index %s: %v", document.NormalizedURL, err)
		}
	}

	results, err := index.Search(t.Context(), SearchRequest{
		Query: "Needle", Terms: []string{"Needle"}, MaxResults: 10, TitleOnly: true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if results.Total != 1 || len(results.Results) != 1 ||
		results.Results[0].URL != "https://title.example/" {
		t.Fatalf("title-only results = %#v", results)
	}
}

func TestTitleOnlySearchUsesEnglishStemming(t *testing.T) {
	index, err := NewBleveMemoryIndex(t.Context(), nil)
	if err != nil {
		t.Fatalf("NewBleveMemoryIndex: %v", err)
	}
	if err := index.Index(t.Context(), documentstore.Document{
		NormalizedURL: "https://title.example/",
		Title:         "Running systems",
		Language:      "en",
	}); err != nil {
		t.Fatalf("Index: %v", err)
	}

	results, err := index.Search(t.Context(), SearchRequest{
		Query: "runs", Terms: []string{"runs"}, MaxResults: 10, TitleOnly: true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results.Results) != 1 || results.Results[0].URL != "https://title.example/" {
		t.Fatalf("stemmed title results = %#v", results)
	}
}

func TestBleveDiskTitleOnlySearchSkipsBodyOnlyMatches(t *testing.T) {
	title := documentstore.Document{
		NormalizedURL: "https://title.example/",
		Title:         "Needle reference",
		ExtractedText: "municipal planning",
		Language:      "en",
	}
	body := documentstore.Document{
		NormalizedURL: "https://body.example/",
		Title:         "Municipal planning",
		ExtractedText: "Needle reference",
		Language:      "en",
	}
	directory := newFakeDocumentDirectory(title, body)
	index, err := NewBleveDiskIndex(
		t.Context(),
		filepath.Join(t.TempDir(), "search.bleve"),
		directory,
		nil,
	)
	if err != nil {
		t.Fatalf("NewBleveDiskIndex: %v", err)
	}
	t.Cleanup(func() { _ = index.Close() })
	for _, document := range []documentstore.Document{title, body} {
		if err := index.Index(t.Context(), document); err != nil {
			t.Fatalf("Index %s: %v", document.NormalizedURL, err)
		}
	}

	results, err := index.Search(t.Context(), SearchRequest{
		Query: "Needle", Terms: []string{"Needle"}, MaxResults: 10, TitleOnly: true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if results.Total != 1 || len(results.Results) != 1 ||
		results.Results[0].URL != title.NormalizedURL {
		t.Fatalf("title-only disk results = %#v", results)
	}
}
