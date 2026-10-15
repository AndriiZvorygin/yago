package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/D4rk4/yago/yagocrawlcontract"
	"github.com/D4rk4/yago/yagonode/internal/documentimport"
)

type interchangeHeader struct {
	Type     string `json:"type"`
	Version  int    `json:"version"`
	SourceID string `json:"sourceId"`
}

type interchangeRecord struct {
	Type    string                `json:"type"`
	Version int                   `json:"version"`
	Record  documentimport.Record `json:"record"`
}

func mapSolrDocument(
	source map[string]json.RawMessage,
	sourceID string,
) (documentimport.Record, error) {
	urls, err := mapSolrURLs(source)
	if err != nil {
		return documentimport.Record{}, err
	}
	content, err := mapSolrContent(source)
	if err != nil {
		return documentimport.Record{}, err
	}
	metadata, err := mapSolrMetadata(source)
	if err != nil {
		return documentimport.Record{}, err
	}
	links, err := mapSolrDocumentLinks(source)
	if err != nil {
		return documentimport.Record{}, err
	}
	contentHash := sha256.Sum256([]byte(content.text))
	document := yagocrawlcontract.DocumentIngest{
		CanonicalURL:    urls.canonical,
		NormalizedURL:   urls.normalized,
		Title:           content.title,
		Headings:        content.headings,
		ExtractedText:   content.text,
		Language:        solrString(source["language_s"]),
		ContentType:     solrString(source["content_type"]),
		FetchStatus:     solrString(source["httpstatus_i"]),
		FetchedAt:       solrTime(source["load_date_dt"]),
		ModifiedAt:      solrTime(source["last_modified"]),
		ContentHash:     hex.EncodeToString(contentHash[:]),
		Outlinks:        links.outbound,
		Inlinks:         links.inbound,
		OutboundAnchors: links.anchors,
		OutboundAnchorEvidenceKnown: len(source["outboundlinks_protocol_sxt"]) > 0 &&
			len(source["outboundlinks_urlstub_sxt"]) > 0,
		Metadata: metadata,
	}
	id := solrString(source["id"])
	if id == "" {
		id = urls.normalized + ":" + document.ContentHash
	}
	idHash := sha256.Sum256([]byte(sourceID + "\x00" + id))
	return documentimport.Record{
		ID:       hex.EncodeToString(idHash[:]),
		Document: document,
	}, nil
}

type solrDocumentURLs struct {
	normalized string
	canonical  string
}

func mapSolrURLs(source map[string]json.RawMessage) (solrDocumentURLs, error) {
	urlValue := solrString(source["sku"])
	if urlValue == "" {
		return solrDocumentURLs{}, errors.New("YaCy row has no sku URL")
	}
	parsed, err := url.Parse(urlValue)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil {
		return solrDocumentURLs{}, errors.New("YaCy sku is not a credential-free HTTP URL")
	}
	parsed.Fragment = ""
	normalizedURL := parsed.String()
	canonicalURL := solrString(source["canonical_s"])
	if canonicalURL == "" {
		canonicalURL = normalizedURL
	}
	canonicalParsed, canonicalErr := url.Parse(canonicalURL)
	if canonicalErr != nil ||
		(canonicalParsed.Scheme != "http" && canonicalParsed.Scheme != "https") ||
		canonicalParsed.Host == "" ||
		canonicalParsed.User != nil ||
		canonicalParsed.Fragment != "" {
		return solrDocumentURLs{}, errors.New(
			"YaCy canonical URL is not a credential-free absolute HTTP URL",
		)
	}
	if len(normalizedURL) > yagocrawlcontract.MaximumCrawlURLBytes {
		return solrDocumentURLs{}, errors.New("YaCy URL exceeds Yago's URL identity limit")
	}
	return solrDocumentURLs{normalized: normalizedURL, canonical: canonicalURL}, nil
}

type solrDocumentContent struct {
	title    string
	text     string
	headings []string
}

func mapSolrContent(source map[string]json.RawMessage) (solrDocumentContent, error) {
	text := strings.Join(solrStrings(source["text_t"]), "\n")
	if len(text) > yagocrawlcontract.MaximumDocumentTextBytes {
		return solrDocumentContent{}, fmt.Errorf(
			"YaCy document text is %d bytes; Yago accepts at most %d",
			len(text),
			yagocrawlcontract.MaximumDocumentTextBytes,
		)
	}
	headings := make([]string, 0)
	for level := 1; level <= 6; level++ {
		headings = append(headings, solrStrings(source[fmt.Sprintf("h%d_txt", level)])...)
	}
	if len(headings) > yagocrawlcontract.MaximumDocumentHeadings {
		return solrDocumentContent{}, errors.New(
			"YaCy document exceeds Yago heading count limit",
		)
	}
	for _, heading := range headings {
		if len(heading) > yagocrawlcontract.MaximumDocumentHeadingBytes {
			return solrDocumentContent{}, errors.New(
				"YaCy heading exceeds Yago heading size limit",
			)
		}
	}
	title := solrString(source["title"])
	if len(title) > yagocrawlcontract.MaximumDocumentTitleBytes {
		return solrDocumentContent{}, errors.New("YaCy title exceeds Yago title size limit")
	}
	return solrDocumentContent{title: title, text: text, headings: headings}, nil
}

func mapSolrMetadata(source map[string]json.RawMessage) (map[string]string, error) {
	metadata := make(map[string]string)
	for sourceField, targetField := range map[string]string{
		"collection_sxt":              "yacy.collections",
		"exact_signature_l":           "yacy.exact_signature",
		"focused_policy_sxt":          "yacy.focused_policy_ids",
		"focused_profile_version_sxt": "yacy.focused_profile_versions",
		"focused_relevance_i":         "yacy.focused_relevance",
		"focused_priority_s":          "yacy.focused_priority",
		"focused_reason_sxt":          "yacy.focused_reasons",
		"httpstatus_redirect_s":       "yacy.redirect",
		"failreason_s":                "yacy.failure_reason",
		"failtype_s":                  "yacy.failure_type",
		"description_txt":             "yacy.description",
		"fresh_date_dt":               "yacy.fresh_until",
	} {
		if raw := source[sourceField]; len(raw) > 0 && string(raw) != "null" {
			metadata[targetField] = string(raw)
		}
	}
	if yacyID := solrString(source["id"]); yacyID != "" {
		metadata["yacy.source_id"] = yacyID
	}
	metadataBytes := 0
	for key, value := range metadata {
		metadataBytes += len(key) + len(value)
	}
	if len(metadata) > yagocrawlcontract.MaximumDocumentMetadata ||
		metadataBytes > yagocrawlcontract.MaximumDocumentMetadataBytes {
		return nil, errors.New(
			"YaCy metadata exceeds Yago's document metadata limits",
		)
	}
	return metadata, nil
}

type solrDocumentLinks struct {
	outbound []string
	inbound  []yagocrawlcontract.AnchorText
	anchors  []yagocrawlcontract.OutboundAnchor
}

func mapSolrDocumentLinks(source map[string]json.RawMessage) (solrDocumentLinks, error) {
	links, err := solrLinks(
		source["outboundlinks_protocol_sxt"],
		source["outboundlinks_urlstub_sxt"],
	)
	if err != nil {
		return solrDocumentLinks{}, fmt.Errorf("map YaCy outbound links: %w", err)
	}
	if len(links) > yagocrawlcontract.MaximumDocumentOutlinks {
		return solrDocumentLinks{}, errors.New(
			"YaCy document exceeds Yago outlink count limit",
		)
	}
	inlinks, err := solrLinks(
		source["inboundlinks_protocol_sxt"],
		source["inboundlinks_urlstub_sxt"],
	)
	if err != nil {
		return solrDocumentLinks{}, fmt.Errorf("map YaCy inbound links: %w", err)
	}
	if len(inlinks) > yagocrawlcontract.MaximumDocumentAnchors {
		return solrDocumentLinks{}, errors.New(
			"YaCy document exceeds Yago inbound-link count limit",
		)
	}
	anchors := make([]yagocrawlcontract.OutboundAnchor, 0, len(links))
	for _, link := range links {
		anchors = append(anchors, yagocrawlcontract.OutboundAnchor{TargetURL: link})
	}
	inbound := make([]yagocrawlcontract.AnchorText, 0, len(inlinks))
	for _, link := range inlinks {
		inbound = append(inbound, yagocrawlcontract.AnchorText{URL: link})
	}
	return solrDocumentLinks{outbound: links, inbound: inbound, anchors: anchors}, nil
}

func solrString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return strings.TrimSpace(string(raw))
}

func solrStrings(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err == nil {
		out := make([]string, 0, len(values))
		for _, value := range values {
			if item := solrString(value); item != "" {
				out = append(out, item)
			}
		}
		return out
	}
	return []string{solrString(raw)}
}

func solrLinks(protocolsRaw, stubsRaw json.RawMessage) ([]string, error) {
	protocols := solrStrings(protocolsRaw)
	stubs := solrStrings(stubsRaw)
	if len(protocols) != len(stubs) {
		return nil, errors.New("protocol and URL-stub arrays have different lengths")
	}
	links := make([]string, 0, len(stubs))
	for i, stub := range stubs {
		protocol := strings.TrimSuffix(protocols[i], ":")
		if protocol != "http" && protocol != "https" {
			continue
		}
		candidate := protocol + "://" + strings.TrimLeft(stub, "/")
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Host == "" || parsed.User != nil {
			continue
		}
		links = append(links, parsed.String())
	}
	return links, nil
}

func solrTime(raw json.RawMessage) time.Time {
	value := solrString(raw)
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}
