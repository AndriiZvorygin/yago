package yagomodel

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type RWIPage struct {
	URL               string
	Title             string
	Language          string
	WordCount         int
	TitleWordCount    int
	TextBytes         int
	LocalLinkCount    int
	ExternalLinkCount int
	Words             []RWIWord
	LoadedAt          time.Time
	ModifiedAt        time.Time
	FreshUntil        time.Time
}

type RWIWord struct {
	Word          string
	Frequency     int
	FirstPosition int
}

type RWIPageArtifacts struct {
	Postings []RWIPosting
	Metadata URIMetadataRow
}

func BuildRWIPageArtifacts(page RWIPage, maximumPostings int) (RWIPageArtifacts, error) {
	if maximumPostings < 1 {
		return RWIPageArtifacts{}, fmt.Errorf("maximum postings must be positive")
	}
	parsedURL, err := url.Parse(page.URL)
	if err != nil || parsedURL.Scheme == "" ||
		(parsedURL.Host == "" && parsedURL.Scheme != "file") {
		return RWIPageArtifacts{}, fmt.Errorf("page URL is not absolute")
	}
	urlHash, err := HashURL(page.URL)
	if err != nil {
		return RWIPageArtifacts{}, fmt.Errorf("hash page URL: %w", err)
	}
	language := normalizeRWILanguage(page.Language)
	shared := map[string]string{
		ColURLHash:           urlHash.String(),
		ColLanguage:          language,
		ColDocType:           strconv.FormatUint(uint64(DocTypeText), 10),
		ColTextWordCount:     FormatRWICardinal(rwiCardinal(page.WordCount, 0xffff)),
		ColTitleWordCount:    FormatRWICardinal(rwiCardinal(page.TitleWordCount, 0xff)),
		ColLocalLinkCount:    FormatRWICardinal(rwiCardinal(page.LocalLinkCount, 0xff)),
		ColExternalLinkCount: FormatRWICardinal(rwiCardinal(page.ExternalLinkCount, 0xff)),
	}
	postings := make([]RWIPosting, 0, min(len(page.Words), maximumPostings))
	for _, word := range page.Words[:min(len(page.Words), maximumPostings)] {
		properties := make(map[string]string, len(shared)+2)
		for key, value := range shared {
			properties[key] = value
		}
		properties[ColHitCount] = FormatRWICardinal(rwiCardinal(word.Frequency, 0xff))
		properties[ColTextPosition] = FormatRWICardinal(rwiCardinal(word.FirstPosition, 0xffff))
		postings = append(postings, RWIPosting{
			WordHash:   WordHash(word.Word),
			Properties: properties,
		})
	}
	day := func(value time.Time) string {
		if value.IsZero() {
			return ""
		}
		return value.UTC().Format("20060102")
	}
	metadata := URIMetadataRow{Properties: map[string]string{
		URLMetaHash: urlHash.String(),
		URLMetaURL:  EncodeBase64WireForm(page.URL),
		"descr":     EncodeBase64WireForm(page.Title),
		"dt":        "t",
		"lang":      language,
		"mod":       day(page.ModifiedAt),
		"load":      day(page.LoadedAt),
		"fresh":     day(page.FreshUntil),
		"size":      strconv.Itoa(max(page.TextBytes, 0)),
		"wc":        strconv.Itoa(max(page.WordCount, 0)),
		"llocal":    strconv.Itoa(max(page.LocalLinkCount, 0)),
		"lother":    strconv.Itoa(max(page.ExternalLinkCount, 0)),
	}}
	if _, err := ParseURIMetadataRow(metadata.String()); err != nil {
		return RWIPageArtifacts{}, fmt.Errorf("validate generated URL metadata: %w", err)
	}
	return RWIPageArtifacts{Postings: postings, Metadata: metadata}, nil
}

func normalizeRWILanguage(language string) string {
	language = strings.ToLower(strings.TrimSpace(language))
	if len(language) >= 2 && language[0] >= 'a' && language[0] <= 'z' &&
		language[1] >= 'a' && language[1] <= 'z' {
		return language[:2]
	}
	return "en"
}

func rwiCardinal(value, maximum int) uint64 {
	if value < 0 || maximum <= 0 {
		return 0
	}
	if value > maximum {
		value = maximum
	}
	return uint64(value)
}
