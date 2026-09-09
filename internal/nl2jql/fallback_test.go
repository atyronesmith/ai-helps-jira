package nl2jql

import (
	"context"
	"errors"
	"testing"
)

// genJSON drives the fallback engine with a canned model response so the
// JSON-envelope unwrapping (extractJQL) can be asserted in isolation, without a
// grammar or normalizer in the way. The empty schema is fine: renderSchema
// tolerates it.
func genJSON(t *testing.T, raw string) (string, error) {
	t.Helper()
	fb, err := NewFallbackEngine(&recordingProvider{jql: raw})
	if err != nil {
		t.Fatalf("NewFallbackEngine: %v", err)
	}
	return fb.Generate(context.Background(), "my open bugs", map[string]string{})
}

// TestGenerateUnwrapsJSONEnvelope pins the fix for the JSON-forcing collision:
// the LLM providers this fallback is wired to (Vertex prefills "{"; OpenAI sets
// response_format json_object) force the model to answer with a JSON object, so
// the strict prompt now emits {"jql": "..."} and extractJQL must unwrap it --
// while still accepting a raw (non-JSON) response from a non-forcing provider.
func TestGenerateUnwrapsJSONEnvelope(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want string
		err  error
	}{
		{"plain envelope", `{"jql": "status = Open"}`, "status = Open", nil},
		{"extra fields ignored", `{"jql": "status = Open", "days": 7}`, "status = Open", nil},
		{"alternate query key", `{"query": "status = Open"}`, "status = Open", nil},
		{"fenced json", "```json\n{\"jql\": \"status = Open\"}\n```", "status = Open", nil},
		{"prose then json", `Sure! {"jql": "status = Open"}`, "status = Open", nil},
		{"raw non-json still works", "status = Open", "status = Open", nil},
		{"empty envelope", `{"jql": ""}`, "", ErrEmptyResponse},
		{"unmappable envelope", `{"jql": "EMPTY"}`, "", ErrUnmappable},
		{"raw unmappable", "EMPTY", "", ErrUnmappable},
		{"malformed json", `{"jql": "status = Open`, "", ErrEmptyResponse},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := genJSON(t, tc.raw)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("Generate(%q) error = %v, want %v", tc.raw, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Generate(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("Generate(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestExtractJQLRawPath proves the historical raw-JQL sanitation is untouched:
// a response with no JSON object passes through sanitizeJQL verbatim, so a raw
// (non-JSON-forcing) provider behaves exactly as before.
func TestExtractJQLRawPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "status = Open", "status = Open"},
		{"quoted value", `issuetype = Bug AND priority = "High"`, `issuetype = Bug AND priority = "High"`},
		{"fenced", "```jql\nstatus = Open\n```", "status = Open"},
		{"jql label", "jql: status = Open", "status = Open"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := extractJQL(tc.in); got != tc.want {
				t.Errorf("extractJQL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
