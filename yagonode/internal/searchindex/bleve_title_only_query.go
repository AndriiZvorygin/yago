package searchindex

import (
	"github.com/blevesearch/bleve/v2"
	blevequery "github.com/blevesearch/bleve/v2/search/query"
)

func bleveTitleOnlyQuery(
	req SearchRequest,
	multilingual bool,
	analyzerScope bool,
	weights RankingWeights,
) blevequery.Query {
	terms := queryTermWords(req)
	if len(terms) == 0 {
		return bleve.NewMatchNoneQuery()
	}
	analyzers := []string{standardTextAnalyzer}
	if multilingual {
		analyzer := searchTextAnalyzer
		if req.Language != "" {
			analyzer = languageToAnalyzer(req.Language)
		}
		if analyzer != standardTextAnalyzer {
			analyzers = append(analyzers, analyzer)
		}
	}
	branches := make([]blevequery.Query, 0, len(analyzers))
	for _, analyzer := range analyzers {
		requiredTerms := requirableTermsForAnalyzer(terms, analyzer)
		if len(requiredTerms) == 0 {
			continue
		}
		clauses := make([]blevequery.Query, 0, len(requiredTerms)+1)
		if analyzerScope && analyzer != standardTextAnalyzer {
			clauses = append(clauses, analyzerScopeClause(analyzer))
		}
		for _, term := range requiredTerms {
			clauses = append(clauses, fieldMatchWithAnalyzer(
				"title",
				term,
				weights.Title,
				analyzer,
			))
		}
		branches = append(branches, conjunctionQuery(clauses))
	}
	if len(branches) == 0 {
		return bleve.NewMatchNoneQuery()
	}
	if len(branches) == 1 {
		return branches[0]
	}

	return bleve.NewDisjunctionQuery(branches...)
}

func conjunctionQuery(queries []blevequery.Query) blevequery.Query {
	if len(queries) == 1 {
		return queries[0]
	}

	return bleve.NewConjunctionQuery(queries...)
}
