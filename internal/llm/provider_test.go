package llm

import "testing"

func TestCleanJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain JSON",
			input: `{"key": "value"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "json code fence",
			input: "```json\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "plain code fence",
			input: "```\n{\"key\": \"value\"}\n```",
			want:  `{"key": "value"}`,
		},
		{
			name:  "with leading whitespace",
			input: "  ```json\n{\"key\": \"value\"}\n```  ",
			want:  `{"key": "value"}`,
		},
		{
			name:  "multiline JSON in fence",
			input: "```json\n{\n  \"key\": \"value\",\n  \"arr\": [1, 2]\n}\n```",
			want:  "{\n  \"key\": \"value\",\n  \"arr\": [1, 2]\n}",
		},
		{
			name:  "no fence just whitespace",
			input: "  {\"key\": \"value\"}  ",
			want:  `{"key": "value"}`,
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "fence with language tag",
			input: "```JSON\n{\"a\": 1}\n```",
			want:  `{"a": 1}`,
		},
		{
			name:  "nested backticks in content",
			input: "```json\n{\"code\": \"use `x`\"}\n```",
			want:  "{\"code\": \"use `x`\"}",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanJSON(tc.input)
			if got != tc.want {
				t.Errorf("cleanJSON(%q)\n  got:  %q\n  want: %q", tc.input, got, tc.want)
			}
		})
	}
}
