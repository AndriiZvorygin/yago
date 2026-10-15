package main

import (
	"encoding/json"
	"testing"
)

func TestMapSolrDocumentPreservesFieldsAndStableHashes(t *testing.T) {
	source := map[string]json.RawMessage{
		"id":  json.RawMessage(`"yacy-id-1"`),
		"sku": json.RawMessage(`"https://library.example.test/item/7"`),
		"canonical_s": json.RawMessage(
			`"https://library.example.test/item/7?canonical=1"`,
		),
		"title":                      json.RawMessage(`"Example title"`),
		"text_t":                     json.RawMessage(`"sanitized archival fixture"`),
		"h1_txt":                     json.RawMessage(`["Main heading"]`),
		"language_s":                 json.RawMessage(`"en"`),
		"content_type":               json.RawMessage(`"text/html"`),
		"httpstatus_i":               json.RawMessage(`200`),
		"load_date_dt":               json.RawMessage(`"2026-10-01T10:00:00Z"`),
		"last_modified":              json.RawMessage(`"2026-09-30T10:00:00Z"`),
		"collection_sxt":             json.RawMessage(`["canada-reference"]`),
		"focused_policy_sxt":         json.RawMessage(`["policy-a"]`),
		"outboundlinks_protocol_sxt": json.RawMessage(`["https"]`),
		"outboundlinks_urlstub_sxt":  json.RawMessage(`["archive.example.test/related"]`),
		"inboundlinks_protocol_sxt":  json.RawMessage(`["https"]`),
		"inboundlinks_urlstub_sxt":   json.RawMessage(`["catalog.example.test/ref"]`),
		"exact_signature_l":          json.RawMessage(`734`),
		"fresh_date_dt":              json.RawMessage(`"2026-10-03T10:00:00Z"`),
		"httpstatus_redirect_s":      json.RawMessage(`"https://library.example.test/new"`),
	}
	first, err := mapSolrDocument(source, "fixture-export")
	if err != nil {
		t.Fatalf("mapSolrDocument: %v", err)
	}
	second, err := mapSolrDocument(source, "fixture-export")
	if err != nil {
		t.Fatalf("repeat mapSolrDocument: %v", err)
	}
	if first.ID != second.ID || first.Document.ContentHash != second.Document.ContentHash {
		t.Fatal("same source row did not retain a stable ID and content hash")
	}
	doc := first.Document
	if doc.NormalizedURL != "https://library.example.test/item/7" ||
		doc.CanonicalURL != "https://library.example.test/item/7?canonical=1" ||
		doc.Title != "Example title" || doc.Language != "en" || doc.FetchStatus != "200" ||
		len(doc.Headings) != 1 || len(doc.Outlinks) != 1 || len(doc.Inlinks) != 1 {
		t.Fatalf("mapped document lost fields: %+v", doc)
	}
	if doc.Metadata["yacy.collections"] != `["canada-reference"]` {
		t.Fatalf("collection metadata missing: %q", doc.Metadata["yacy.collections"])
	}
	if doc.Metadata["yacy.redirect"] != `"https://library.example.test/new"` ||
		doc.Metadata["yacy.exact_signature"] != "734" {
		t.Fatalf("YaCy metadata gaps: %#v", doc.Metadata)
	}
}

func TestMapSolrDocumentRejectsIncompleteAndAmbiguousRows(t *testing.T) {
	if _, err := mapSolrDocument(map[string]json.RawMessage{}, "fixture"); err == nil {
		t.Fatal("row without sku was accepted")
	}
	if _, err := mapSolrDocument(map[string]json.RawMessage{
		"sku":                        json.RawMessage(`"https://example.test/"`),
		"outboundlinks_protocol_sxt": json.RawMessage(`["https"]`),
	}, "fixture"); err == nil {
		t.Fatal("mismatched link arrays were guessed instead of rejected")
	}
	if _, err := mapSolrDocument(map[string]json.RawMessage{
		"sku": json.RawMessage(`"https://user:secret@example.test/"`),
	}, "fixture"); err == nil {
		t.Fatal("credential-bearing URL was accepted")
	}
}
