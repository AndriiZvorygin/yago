package pageindex_test

import (
	"reflect"
	"testing"

	"github.com/D4rk4/yago/yago-crawler/internal/pageindex"
	"github.com/D4rk4/yago/yago-crawler/internal/pageparse"
	"github.com/D4rk4/yago/yagomodel"
)

func TestRWIBackfillTokenizerMatchesCrawlerTokenizer(t *testing.T) {
	for _, text := range []string{
		"Hello WORLD a 42 golang",
		"go-lang 1,234 4.7Ohm",
		"word-word word . word.word@word.word ....word... word,word word word",
		"Café Montréal Québec 1.5e3",
	} {
		crawlerTokens := pageparse.Tokenize(text)
		backfillTokens := yagomodel.TokenizeRWIText(text, 0)
		if !reflect.DeepEqual(backfillTokens, crawlerTokens) {
			t.Errorf(
				"tokenizers disagree for %q: backfill=%v crawler=%v",
				text,
				backfillTokens,
				crawlerTokens,
			)
		}
	}
}

func buildPostings(t *testing.T, page pageparse.ParsedPage) []yagomodel.RWIPosting {
	t.Helper()
	return pageindex.BuildPostings(page, pageparse.BuildPageStats(page))
}

func TestBuildPostingsUsesYaCyWordHash(t *testing.T) {
	page := pageparse.ParsedPage{
		URL:      "http://example.com/",
		Title:    "Kangaroo facts",
		Language: "en",
		Text:     "Kangaroo kangaroo hops across the outback",
		Links:    nil,
	}
	postings := buildPostings(t, page)

	byHash := make(map[yagomodel.Hash]yagomodel.RWIPosting, len(postings))
	for _, entry := range postings {
		byHash[entry.WordHash] = entry
	}

	want := yagomodel.WordHash("kangaroo")
	entry, ok := byHash[want]
	if !ok {
		t.Fatalf("no posting for word hash %q", want)
	}
	if _, ok := byHash[yagomodel.WordHash("the")]; ok {
		t.Fatal("stopword unexpectedly has an RWI posting")
	}
	if got := entry.Properties[yagomodel.ColHitCount]; got == "" {
		t.Errorf("missing hit count on kangaroo posting")
	}
}

func TestBuildPostingsPreservesRelativeURLCompatibility(t *testing.T) {
	page := pageparse.ParsedPage{
		URL:  "relative/path",
		Text: "indexable words",
	}
	if postings := buildPostings(t, page); len(postings) == 0 {
		t.Fatal("relative URL unexpectedly dropped its postings")
	}
}

func TestBuildPostingsAreAcceptableRWILines(t *testing.T) {
	page := pageparse.ParsedPage{
		URL:      "http://example.com/path?q=a&b=c",
		Title:    "Title",
		Language: "en",
		Text:     "indexable words here",
		Links:    []string{"http://example.com/local", "http://other.com/x"},
	}
	postings := buildPostings(t, page)
	if len(postings) == 0 {
		t.Fatal("expected postings")
	}
	for _, entry := range postings {
		line := entry.String()
		parsed, err := yagomodel.ParseRWIPosting(line)
		if err != nil {
			t.Errorf("ParseRWIPosting(%q): %v", line, err)
			continue
		}
		if _, err := parsed.URLHash(); err != nil {
			t.Errorf("URLHash(%q): %v", line, err)
		}
		if parsed.WordHash != entry.WordHash {
			t.Errorf("round trip word hash %q != %q", parsed.WordHash, entry.WordHash)
		}
		hits, err := parsed.Cardinal(yagomodel.ColHitCount)
		if err != nil {
			t.Errorf("hit count cardinal: %v", err)
		}
		if hits == 0 {
			t.Errorf("hit count must survive parser normalization: %q", line)
		}
	}
}
