package websearch

import "github.com/D4rk4/yago/yagonode/internal/stopwords"

func webCoverageTerms(terms []string) []string {
	content := stopwords.ContentTerms(terms)
	if len(distinctVerificationTerms(content)) < 2 {
		return terms
	}

	return content
}
