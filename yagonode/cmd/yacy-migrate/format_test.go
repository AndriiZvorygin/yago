package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
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

func TestMapSolrDocumentDerivesIDWhenYaCyIDIsAbsent(t *testing.T) {
	row := map[string]json.RawMessage{
		"sku":    json.RawMessage(`"https://fixture.example.test/page"`),
		"text_t": json.RawMessage(`"stable content"`),
	}
	first, err := mapSolrDocument(row, "fixture-source")
	if err != nil {
		t.Fatalf("map missing-ID row: %v", err)
	}
	second, err := mapSolrDocument(row, "fixture-source")
	if err != nil {
		t.Fatalf("map repeated missing-ID row: %v", err)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("fallback IDs are not stable: %q and %q", first.ID, second.ID)
	}
}

func TestMapSolrDocumentRejectsInvalidURLsAndOversizedContent(t *testing.T) {
	assertSolrDocumentsRejected(t, []solrMappingRejection{
		{name: "invalid source URL", change: func(row map[string]json.RawMessage) {
			row["sku"] = json.RawMessage(`"file:///tmp/private"`)
		}},
		{name: "invalid canonical URL", change: func(row map[string]json.RawMessage) {
			row["canonical_s"] = json.RawMessage(`"https://user:secret@example.test/"`)
		}},
		{name: "URL identity too long", change: func(row map[string]json.RawMessage) {
			row["sku"] = json.RawMessage(fmt.Sprintf(
				`"https://example.test/%s"`,
				strings.Repeat("x", yagocrawlcontract.MaximumCrawlURLBytes),
			))
		}},
		{name: "text too long", change: func(row map[string]json.RawMessage) {
			value, _ := json.Marshal(
				strings.Repeat("x", yagocrawlcontract.MaximumDocumentTextBytes+1),
			)
			row["text_t"] = value
		}},
		{name: "title too long", change: func(row map[string]json.RawMessage) {
			title, _ := json.Marshal(
				strings.Repeat("x", yagocrawlcontract.MaximumDocumentTitleBytes+1),
			)
			row["title"] = title
		}},
	})
}

func TestMapSolrDocumentRejectsOversizedHeadingsMetadataAndLinks(t *testing.T) {
	assertSolrDocumentsRejected(t, []solrMappingRejection{
		{name: "too many headings", change: func(row map[string]json.RawMessage) {
			values := make([]string, yagocrawlcontract.MaximumDocumentHeadings+1)
			for index := range values {
				values[index] = "heading"
			}
			row["h1_txt"], _ = json.Marshal(values)
		}},
		{name: "heading too long", change: func(row map[string]json.RawMessage) {
			row["h1_txt"], _ = json.Marshal([]string{
				strings.Repeat("x", yagocrawlcontract.MaximumDocumentHeadingBytes+1),
			})
		}},
		{name: "metadata too large", change: func(row map[string]json.RawMessage) {
			row["description_txt"], _ = json.Marshal(
				strings.Repeat("x", yagocrawlcontract.MaximumDocumentMetadataBytes),
			)
		}},
		{name: "too many outlinks", change: func(row map[string]json.RawMessage) {
			protocols := make([]string, yagocrawlcontract.MaximumDocumentOutlinks+1)
			stubs := make([]string, len(protocols))
			fillSolrLinkArrays(protocols, stubs)
			row["outboundlinks_protocol_sxt"], _ = json.Marshal(protocols)
			row["outboundlinks_urlstub_sxt"], _ = json.Marshal(stubs)
		}},
		{name: "too many inlinks", change: func(row map[string]json.RawMessage) {
			protocols := make([]string, yagocrawlcontract.MaximumDocumentAnchors+1)
			stubs := make([]string, len(protocols))
			fillSolrLinkArrays(protocols, stubs)
			row["inboundlinks_protocol_sxt"], _ = json.Marshal(protocols)
			row["inboundlinks_urlstub_sxt"], _ = json.Marshal(stubs)
		}},
		{name: "outbound link arrays mismatch", change: func(row map[string]json.RawMessage) {
			row["outboundlinks_protocol_sxt"] = json.RawMessage(`["https"]`)
			row["outboundlinks_urlstub_sxt"] = json.RawMessage(`[]`)
		}},
		{name: "inbound link arrays mismatch", change: func(row map[string]json.RawMessage) {
			row["inboundlinks_protocol_sxt"] = json.RawMessage(`["https"]`)
			row["inboundlinks_urlstub_sxt"] = json.RawMessage(`[]`)
		}},
	})
}

type solrMappingRejection struct {
	name   string
	change func(map[string]json.RawMessage)
}

func fillSolrLinkArrays(protocols, stubs []string) {
	for index := range protocols {
		protocols[index] = "https"
		stubs[index] = fmt.Sprintf("example.test/%d", index)
	}
}

func assertSolrDocumentsRejected(t *testing.T, tests []solrMappingRejection) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := map[string]json.RawMessage{"sku": json.RawMessage(`"https://example.test/"`)}
			test.change(row)
			if _, err := mapSolrDocument(row, "fixture"); err == nil {
				t.Fatal("unsupported source document was accepted")
			}
		})
	}
}

func TestSolrValueAdaptersHandleScalarMalformedAndUnsupportedValues(t *testing.T) {
	if got := solrString(json.RawMessage(` null `)); got != "" {
		t.Fatalf("null string = %q, want empty", got)
	}
	if got := solrString(json.RawMessage(` 42 `)); got != "42" {
		t.Fatalf("numeric string fallback = %q, want trimmed raw value", got)
	}
	if got := solrStrings(json.RawMessage(`null`)); got != nil {
		t.Fatalf("null string list = %#v, want nil", got)
	}
	if got := solrStrings(
		json.RawMessage(`" single value "`),
	); len(got) != 1 ||
		got[0] != " single value " {
		t.Fatalf("scalar string list = %#v", got)
	}
	if got := solrStrings(
		json.RawMessage(`["kept","",null,7]`),
	); len(got) != 2 || got[0] != "kept" ||
		got[1] != "7" {
		t.Fatalf("filtered string list = %#v", got)
	}
	if got := solrTime(json.RawMessage(`null`)); !got.IsZero() {
		t.Fatalf("missing time = %v, want zero", got)
	}
	if got := solrTime(json.RawMessage(`"not-a-date"`)); !got.IsZero() {
		t.Fatalf("malformed time = %v, want zero", got)
	}
	if got := solrTime(
		json.RawMessage(`"2026-10-01T10:00:00-04:00"`),
	); got.Location() != time.UTC ||
		got.Hour() != 14 {
		t.Fatalf("parsed time = %v, want equivalent UTC", got)
	}
}

func TestSolrLinksIgnoreUnsupportedAndMalformedTargets(t *testing.T) {
	links, err := solrLinks(
		json.RawMessage(`["ftp","https:","https","https"]`),
		json.RawMessage(
			`["example.test/file","example.test/page","example.test/%zz","user:pass@example.test"]`,
		),
	)
	if err != nil {
		t.Fatalf("solrLinks: %v", err)
	}
	if len(links) != 1 || links[0] != "https://example.test/page" {
		t.Fatalf("filtered links = %#v", links)
	}
}
