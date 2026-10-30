package documentstore

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestOutboundAnchorReplacementReindexesOnlyTargetsWhoseAnchorsItChanges(t *testing.T) {
	directory, receiver, _ := openPagedDocuments(t)
	replacer := receiver.(OutboundAnchorDocumentReplacer)
	hub := "https://hub.example/"
	if _, err := receiver.Receive(t.Context(), []Document{{NormalizedURL: hub}}); err != nil {
		t.Fatal(err)
	}
	fillInboundAnchorCapacity(t, replacer, hub)
	before := storedInlinks(t, directory, hub)

	late := "https://z.example/late"
	if visited := replaceAndRecordVisits(t, replacer, late, hub); len(visited) != 0 {
		t.Fatalf("source outside the anchor capacity re-indexed %v", visited)
	}
	if after := storedInlinks(t, directory, hub); !slices.Equal(after, before) {
		t.Fatalf(
			"hub anchors changed by a source outside capacity: %d -> %d",
			len(before),
			len(after),
		)
	}
	documents := receiver.(documentVault)
	publication := readOutboundAnchorReplacementPublication(t, documents, late)
	if !slices.Equal(publication.Targets, []string{hub}) {
		t.Fatalf("late source publication = %#v", publication.Targets)
	}

	early := "https://0.example/early"
	visited := replaceAndRecordVisits(t, replacer, early, hub)
	if len(visited) != 1 || !documentHasInboundAnchorSource(visited[0], early) {
		t.Fatalf("source inside the anchor capacity re-indexed %#v", visited)
	}
}

func TestOutboundAnchorReplacementReplayReindexesTargetsCommittedBeforeVisitFailure(
	t *testing.T,
) {
	directory, receiver, _ := openPagedDocuments(t)
	replacer := receiver.(OutboundAnchorDocumentReplacer)
	dropped := "https://target.example/dropped"
	added := "https://target.example/added"
	if _, err := receiver.Receive(
		t.Context(),
		[]Document{{NormalizedURL: dropped}, {NormalizedURL: added}},
	); err != nil {
		t.Fatal(err)
	}
	source := "https://source.example/replay"
	if _, err := replacer.ReplaceOutboundAnchorDocuments(
		t.Context(),
		[]OutboundAnchorSet{{
			SourceURL: source,
			Anchors:   []OutboundAnchor{{TargetURL: dropped, Text: "first wording"}},
		}},
		func([]Document) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	changed := []OutboundAnchorSet{{
		SourceURL: source,
		Anchors:   []OutboundAnchor{{TargetURL: added, Text: "second wording"}},
	}}
	wantFailure := errors.New("index unavailable")
	if _, err := replacer.ReplaceOutboundAnchorDocuments(
		t.Context(),
		changed,
		func([]Document) error { return wantFailure },
	); !errors.Is(err, wantFailure) {
		t.Fatalf("failed visit = %v", err)
	}
	if inlinks := storedInlinks(t, directory, dropped); len(inlinks) != 0 {
		t.Fatalf("dropped target kept anchors after the committed page: %#v", inlinks)
	}
	if inlinks := storedInlinks(t, directory, added); len(inlinks) != 1 {
		t.Fatalf("added target missed anchors after the committed page: %#v", inlinks)
	}

	var replayed []string
	if _, err := replacer.ReplaceOutboundAnchorDocuments(
		t.Context(),
		changed,
		func(documents []Document) error {
			for _, document := range documents {
				replayed = append(replayed, document.NormalizedURL)
			}

			return nil
		},
	); err != nil {
		t.Fatalf("replay: %v", err)
	}
	slices.Sort(replayed)
	if !slices.Equal(replayed, []string{added, dropped}) {
		t.Fatalf("replay re-indexed %v, want both committed targets", replayed)
	}
}

func fillInboundAnchorCapacity(
	t *testing.T,
	replacer OutboundAnchorDocumentReplacer,
	targetURL string,
) {
	t.Helper()
	for first := 0; first < maximumInboundAnchors; first += MaximumOutboundAnchorSourcesPerReplacement {
		sets := make([]OutboundAnchorSet, 0, MaximumOutboundAnchorSourcesPerReplacement)
		for sequence := first; sequence < first+MaximumOutboundAnchorSourcesPerReplacement; sequence++ {
			sets = append(sets, OutboundAnchorSet{
				SourceURL: fmt.Sprintf("https://a.example/%04d", sequence),
				Anchors:   []OutboundAnchor{{TargetURL: targetURL, Text: "hub"}},
			})
		}
		if _, err := replacer.ReplaceOutboundAnchorDocuments(
			t.Context(),
			sets,
			func([]Document) error { return nil },
		); err != nil {
			t.Fatal(err)
		}
	}
}

func replaceAndRecordVisits(
	t *testing.T,
	replacer OutboundAnchorDocumentReplacer,
	sourceURL string,
	targetURL string,
) []Document {
	t.Helper()
	var visited []Document
	if _, err := replacer.ReplaceOutboundAnchorDocuments(
		t.Context(),
		[]OutboundAnchorSet{{
			SourceURL: sourceURL,
			Anchors:   []OutboundAnchor{{TargetURL: targetURL, Text: "hub"}},
		}},
		func(documents []Document) error {
			visited = append(visited, documents...)

			return nil
		},
	); err != nil {
		t.Fatal(err)
	}

	return visited
}

func storedInlinks(t *testing.T, directory DocumentDirectory, normalizedURL string) []AnchorText {
	t.Helper()
	document, found, err := directory.Document(t.Context(), normalizedURL)
	if err != nil || !found {
		t.Fatalf("stored document %s = %t/%v", normalizedURL, found, err)
	}

	return document.Inlinks
}
