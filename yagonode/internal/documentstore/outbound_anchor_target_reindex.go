package documentstore

func (r outboundAnchorDocumentReplacement) affectsTargetAnchors(
	targetURL string,
	anchors []AnchorText,
) bool {
	if _, linked := r.previouslyLinkedTargets[targetURL]; linked {
		return true
	}
	for _, anchor := range anchors {
		if _, replaced := r.sources[anchor.URL]; replaced {
			return true
		}
	}

	return false
}
