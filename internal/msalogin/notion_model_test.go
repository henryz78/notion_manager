package msalogin

import "testing"

func TestNotionModelDisplayNameAddsClaudePrefix(t *testing.T) {
	tests := []struct {
		name       string
		internalID string
		family     string
		want       string
	}{
		{name: "Sonnet 4.6", internalID: "almond-croissant-low", family: "anthropic", want: "Claude Sonnet 4.6"},
		{name: "", internalID: "almond-croissant-low", family: "anthropic", want: "Claude Sonnet 4.6"},
		{name: "Claude Opus 4.6", internalID: "avocado-froyo-medium", family: "anthropic", want: "Claude Opus 4.6"},
		{name: "GPT 5.4", internalID: "oval-kumquat-medium", family: "openai", want: "GPT 5.4"},
	}

	for _, tc := range tests {
		if got := notionModelDisplayName(tc.name, tc.internalID, tc.family); got != tc.want {
			t.Fatalf("notionModelDisplayName(%q, %q, %q) = %q, want %q", tc.name, tc.internalID, tc.family, got, tc.want)
		}
	}
}
