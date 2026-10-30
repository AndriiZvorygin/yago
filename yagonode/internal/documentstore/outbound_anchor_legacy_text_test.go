package documentstore

import (
	"slices"
	"testing"
)

func TestOutboundAnchorReplacementRewritesStaleTextOfATargetWithoutAnchorRecord(t *testing.T) {
	for name, replace := range targetAnchorReplacements() {
		t.Run(name, func(t *testing.T) {
			directory, receiver, _ := openPagedDocuments(t)
			target := "https://target.example/stale"
			source := "https://source.example/page"
			receiveTargetWithInlinks(t, receiver, target, AnchorText{URL: source, Text: "stale"})

			replace(t, receiver, source, target)
			want := []AnchorText{{URL: source, Text: "hub"}}
			if got := storedInlinks(t, directory, target); !slices.Equal(got, want) {
				t.Fatalf("target inlinks = %#v, want %#v", got, want)
			}
		})
	}
}
