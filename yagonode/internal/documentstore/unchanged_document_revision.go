package documentstore

import (
	"maps"
	"slices"
)

type documentRevisionKind uint8

const (
	documentRevisionRejected documentRevisionKind = iota
	documentRevisionFirst
	documentRevisionChanged
	documentRevisionUnchanged
)

type canonicalDocumentRevision struct {
	document Document
	location storedDocumentLocation
	kind     documentRevisionKind
}

func classifiedDocumentRevision(
	previous Document,
	found bool,
	incoming Document,
	location storedDocumentLocation,
) canonicalDocumentRevision {
	revision := canonicalDocumentRevision{document: incoming, location: location}
	switch {
	case !found:
		revision.kind = documentRevisionFirst
	case sameDurableDocument(previous, incoming):
		revision.document.FetchedAt = previous.FetchedAt
		revision.document.IndexedAt = previous.IndexedAt
		revision.kind = documentRevisionUnchanged
	default:
		revision.kind = documentRevisionChanged
	}

	return revision
}

func sameDurableDocument(previous Document, incoming Document) bool {
	return previous.ContentHash == incoming.ContentHash &&
		previous.ExtractionGeneration == incoming.ExtractionGeneration &&
		previous.CanonicalURL == incoming.CanonicalURL &&
		previous.NormalizedURL == incoming.NormalizedURL &&
		previous.Title == incoming.Title &&
		previous.ContentQuality == incoming.ContentQuality &&
		previous.ContentSafety == incoming.ContentSafety &&
		previous.RawContentReference == incoming.RawContentReference &&
		previous.Language == incoming.Language &&
		previous.ContentType == incoming.ContentType &&
		previous.FetchStatus == incoming.FetchStatus &&
		previous.PublishedAt.Equal(incoming.PublishedAt) &&
		previous.ModifiedAt.Equal(incoming.ModifiedAt) &&
		previous.FirstSeenAt.Equal(incoming.FirstSeenAt) &&
		previous.ContentChangedAt.Equal(incoming.ContentChangedAt) &&
		previous.DateConfidence == incoming.DateConfidence &&
		previous.DateSource == incoming.DateSource &&
		previous.ClusterID == incoming.ClusterID &&
		previous.RepresentativeURL == incoming.RepresentativeURL &&
		previous.OutboundAnchorEvidenceKnown == incoming.OutboundAnchorEvidenceKnown &&
		previous.ExtractedText == incoming.ExtractedText &&
		slices.Equal(previous.Headings, incoming.Headings) &&
		slices.Equal(previous.Outlinks, incoming.Outlinks) &&
		slices.Equal(previous.Inlinks, incoming.Inlinks) &&
		slices.Equal(previous.OutboundAnchors, incoming.OutboundAnchors) &&
		slices.Equal(previous.Images, incoming.Images) &&
		maps.Equal(previous.Metadata, incoming.Metadata)
}
