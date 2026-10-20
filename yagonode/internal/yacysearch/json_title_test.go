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

func TestYacyJSONDisplayDescription(t *testing.T) {
	for _, test := range []struct {
		name        string
		title       string
		description string
		want        string
	}{
		{
			name:        "same imported title array",
			title:       `["Page title", "Alternate"]`,
			description: `["Page title"]`,
			want:        "Page title",
		},
		{
			name:        "ordinary description",
			title:       "Page title",
			description: "A useful page summary",
			want:        "A useful page summary",
		},
		{
			name:        "unrelated JSON snippet",
			title:       "Page title",
			description: `["item one", "item two"]`,
			want:        `["item one", "item two"]`,
		},
		{
			name:        "nonmatching array description",
			title:       `["Page title"]`,
			description: `["Different summary"]`,
			want:        `["Different summary"]`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := yacyJSONDisplayDescription(test.title, test.description); got != test.want {
				t.Fatalf(
					"yacyJSONDisplayDescription(%q, %q) = %q, want %q",
					test.title,
					test.description,
					got,
					test.want,
				)
			}
		})
	}
}
