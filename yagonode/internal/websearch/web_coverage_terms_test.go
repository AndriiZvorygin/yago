package websearch

import "testing"

var installationQuestionTerms = []string{"how", "to", "install", "docker", "on", "ubuntu"}

func TestResultsMentioningTermsMeasureNaturalLanguageQuestionsByContentTerms(t *testing.T) {
	results := []Result{
		{Title: "Install Docker Engine", URL: "https://strong.example/"},
		{Title: "Docker on a boat: how to", URL: "https://weak.example/"},
	}

	got := resultsMentioningTerms(installationQuestionTerms, results)
	if len(got) != 1 || got[0].URL != "https://strong.example/" {
		t.Fatalf("results = %#v", got)
	}
}

func TestResultsMentioningTermsRejectFunctionWordOnlyEvidence(t *testing.T) {
	results := []Result{{Title: "How to cook on a stove", URL: "https://weak.example/"}}

	got := resultsMentioningTerms(installationQuestionTerms, results)
	if len(got) != 0 {
		t.Fatalf("results = %#v", got)
	}
}
