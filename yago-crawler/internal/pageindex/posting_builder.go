package pageindex

import (
	"fmt"
	"time"

	"github.com/D4rk4/yago/yago-crawler/internal/pageparse"
	"github.com/D4rk4/yago/yago-crawler/internal/stopwords"
	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagomodel"
)

func BuildRWIPageArtifacts(
	page pageparse.ParsedPage,
	stats pageparse.PageStats,
	loadedAt time.Time,
) (yagomodel.RWIPageArtifacts, error) {
	frequency := make(map[string]int)
	firstPosition := make(map[string]int)
	order := make([]string, 0)
	for position, token := range stats.Tokens {
		if stopwords.IsStopword(token) {
			continue
		}
		if _, seen := frequency[token]; !seen {
			if len(order) == yagocrawlcontract.MaximumIngestPostings {
				continue
			}
			firstPosition[token] = position
			order = append(order, token)
			frequency[token] = 1
			continue
		}
		frequency[token]++
	}
	words := make([]yagomodel.RWIWord, 0, len(order))
	for _, word := range order {
		words = append(words, yagomodel.RWIWord{
			Word:          word,
			Frequency:     frequency[word],
			FirstPosition: firstPosition[word],
		})
	}
	artifacts, err := yagomodel.BuildRWIPageArtifacts(yagomodel.RWIPage{
		URL:               page.URL,
		Title:             page.Title,
		Language:          NormalizeLanguage(page.Language),
		WordCount:         len(stats.Tokens),
		TitleWordCount:    len(stats.TitleTokens),
		TextBytes:         len(page.Text),
		LocalLinkCount:    len(stats.LocalLinks),
		ExternalLinkCount: len(stats.ExternalLinks),
		Words:             words,
		LoadedAt:          loadedAt,
		ModifiedAt:        loadedAt,
		FreshUntil:        loadedAt,
	}, yagocrawlcontract.MaximumIngestPostings)
	if err != nil {
		return yagomodel.RWIPageArtifacts{}, fmt.Errorf("build shared rwi artifacts: %w", err)
	}
	return artifacts, nil
}

func BuildPostings(page pageparse.ParsedPage, stats pageparse.PageStats) []yagomodel.RWIPosting {
	artifacts, err := BuildRWIPageArtifacts(page, stats, time.Time{})
	if err != nil {
		return nil
	}
	return artifacts.Postings
}

func BuildMetadata(
	page pageparse.ParsedPage,
	stats pageparse.PageStats,
	loadedAt time.Time,
) yagomodel.URIMetadataRow {
	artifacts, err := BuildRWIPageArtifacts(page, stats, loadedAt)
	if err != nil {
		return yagomodel.URIMetadataRow{}
	}
	return artifacts.Metadata
}
