package yacysearch

import "testing"

func TestYacyJSONDisplayTitle(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "ordinary title", input: "A title", want: "A title"},
		{name: "array title", input: `["A title", "Alternate"]`, want: "A title"},
		{name: "skip empty array values", input: `["", "A title"]`, want: "A title"},
		{name: "empty array", input: `[]`, want: `[]`},
		{name: "non-string array", input: `[7]`, want: `[7]`},
		{name: "malformed array", input: `["A title"`, want: `["A title"`},
		{name: "all blank values", input: `["", "  "]`, want: `["", "  "]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := yacyJSONDisplayTitle(test.input); got != test.want {
				t.Fatalf("yacyJSONDisplayTitle(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}
