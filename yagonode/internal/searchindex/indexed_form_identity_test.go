package searchindex

import (
	"strings"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagonode/internal/documentstore"
)

func TestIndexedFormIncludesSearchFieldsAndCandidateEvidence(t *testing.T) {
	original := documentstore.Document{
		NormalizedURL: "https://example.org/stable",
		CanonicalURL:  "https://example.org/stable",
		Title:         "original",
		ExtractedText: strings.Repeat("ordinary text ", 200) + "ending",
		ContentHash:   "unchanged-content-identifier",
	}
	indexed, err := bleveDocumentFromStore(original)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*documentstore.Document){
		"url":   func(document *documentstore.Document) { document.NormalizedURL += "/new" },
		"title": func(document *documentstore.Document) { document.Title += " replacement" },
		"body beyond snippet": func(document *documentstore.Document) {
			document.ExtractedText = strings.TrimSuffix(document.ExtractedText, "ending") + "change"
		},
		"headings": func(document *documentstore.Document) { document.Headings = []string{"new heading"} },
		"anchor": func(document *documentstore.Document) {
			document.Inlinks = []documentstore.AnchorText{{Text: "new anchor"}}
		},
		"language": func(document *documentstore.Document) { document.Language = "de" },
		"cluster":  func(document *documentstore.Document) { document.ClusterID = "new cluster" },
		"representative": func(document *documentstore.Document) {
			document.RepresentativeURL = "https://example.org/representative"
		},
		"quality": func(document *documentstore.Document) { document.ContentQuality.Score = 0.5 },
		"safety":  func(document *documentstore.Document) { document.ContentSafety.Rating = documentstore.SafetyExplicit },
		"published": func(document *documentstore.Document) {
			document.PublishedAt = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
			document.DateConfidence = 1
		},
		"first seen": func(document *documentstore.Document) {
			document.FirstSeenAt = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
		},
		"author": func(document *documentstore.Document) {
			document.Metadata = map[string]string{"author": "Example author"}
		},
		"keywords": func(document *documentstore.Document) {
			document.Metadata = map[string]string{"keywords": "replacement"}
		},
		"publisher": func(document *documentstore.Document) {
			document.Metadata = map[string]string{"publisher": "Example publisher"}
		},
		"content type": func(document *documentstore.Document) { document.ContentType = "text/html" },
		"images": func(document *documentstore.Document) {
			document.Images = []documentstore.ImageMetadata{{URL: "https://example.org/image.png"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := original
			change(&changed)
			replacement, err := bleveDocumentFromStore(changed)
			if err != nil {
				t.Fatal(err)
			}
			if replacement.Candidate == indexed.Candidate {
				t.Fatal("changed indexed form has unchanged identity")
			}
		})
	}
	original.FetchedAt = time.Now().UTC()
	original.IndexedAt = original.FetchedAt
	original.Metadata = map[string]string{"not_indexed": "value"}
	unchanged, err := bleveDocumentFromStore(original)
	if err != nil || unchanged.Candidate != indexed.Candidate {
		t.Fatalf("non-indexed fields changed identity: %v", err)
	}
}
