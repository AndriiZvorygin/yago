package documentstore

func retainSubmittedInlinks(document Document) Document {
	if document.submittedInlinksKnown {
		return document
	}
	document.Inlinks = canonicalAnchorTexts(document.Inlinks)
	document.submittedInlinks = append([]AnchorText(nil), document.Inlinks...)
	document.submittedInlinksKnown = true

	return document
}

func restoreSubmittedInlinks(document Document) Document {
	if !document.submittedInlinksKnown {
		return document
	}
	document.Inlinks = append([]AnchorText(nil), document.submittedInlinks...)

	return document
}

func retainDocumentRevisionInlinks(
	document Document,
	materialized []AnchorText,
) Document {
	document.Inlinks = canonicalAnchorTexts(document.Inlinks)
	document.submittedInlinks = inlinksOutsideMaterializedAnchors(
		document.Inlinks,
		canonicalAnchorTexts(materialized),
	)
	document.submittedInlinksKnown = true

	return document
}

func inlinksAfterAnchorReplacement(
	inlinks []AnchorText,
	previousAnchors []AnchorText,
	replacedSources map[string]struct{},
	anchors []AnchorText,
) []AnchorText {
	replaced := append([]AnchorText(nil), anchors...)
	for _, anchor := range inlinksOutsideMaterializedAnchors(inlinks, previousAnchors) {
		if _, superseded := replacedSources[anchor.URL]; !superseded {
			replaced = append(replaced, anchor)
		}
	}

	return canonicalAnchorTexts(replaced)
}

func inlinksOutsideMaterializedAnchors(
	inlinks []AnchorText,
	materialized []AnchorText,
) []AnchorText {
	materializedEvidence := make(map[AnchorText]struct{}, len(materialized))
	for _, anchor := range materialized {
		materializedEvidence[anchor] = struct{}{}
	}
	outside := make([]AnchorText, 0, len(inlinks))
	for _, anchor := range inlinks {
		if _, found := materializedEvidence[anchor]; !found {
			outside = append(outside, anchor)
		}
	}

	return outside
}
