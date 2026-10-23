package searchcore

import "testing"

func TestDisplayTitle(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "ordinary title", input: "A title", want: "A title"},
		{name: "array title", input: `["A title", "Alternate"]`, want: "A title"},
		{name: "skip empty values", input: `["", "A title"]`, want: "A title"},
		{name: "empty array remains unchanged", input: `[]`, want: `[]`},
		{name: "non-string array remains unchanged", input: `[7]`, want: `[7]`},
		{name: "malformed array remains unchanged", input: `["A title"`, want: `["A title"`},
		{name: "all blank values remain unchanged", input: `["", "  "]`, want: `["", "  "]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := DisplayTitle(test.input); got != test.want {
				t.Fatalf("DisplayTitle(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestDisplayDescription(t *testing.T) {
	for _, test := range []struct {
		name        string
		title       string
		description string
		want        string
	}{
		{
			name:        "duplicate title array",
			title:       `["Page title", "Alternate"]`,
			description: `["Page title"]`,
			want:        "Page title",
		},
		{
			name:        "ordinary description",
			title:       "Page title",
			description: "A useful summary",
			want:        "A useful summary",
		},
		{
			name:        "unrelated JSON description",
			title:       "Page title",
			description: `["one", "two"]`,
			want:        `["one", "two"]`,
		},
		{
			name:        "different array description",
			title:       `["Page title"]`,
			description: `["Different summary"]`,
			want:        `["Different summary"]`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := DisplayDescription(test.title, test.description); got != test.want {
				t.Fatalf(
					"DisplayDescription(%q, %q) = %q, want %q",
					test.title,
					test.description,
					got,
					test.want,
				)
			}
		})
	}
}
