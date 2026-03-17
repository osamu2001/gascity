package shellquote

import "testing"

func TestQuote(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{name: "simple", arg: "simple", want: "'simple'"},
		{name: "empty", arg: "", want: "''"},
		{name: "single quote", arg: "it's", want: "'it'\\''s'"},
		{name: "spaces", arg: "hello world", want: "'hello world'"},
		{name: "shell syntax", arg: "$(whoami)", want: "'$(whoami)'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Quote(tt.arg); got != tt.want {
				t.Fatalf("Quote(%q) = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}

func TestJoin(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "empty slice", args: nil, want: ""},
		{name: "single arg no metachar", args: []string{"--model"}, want: "--model"},
		{name: "two clean args", args: []string{"--model", "opus"}, want: "--model opus"},
		{name: "arg with space", args: []string{"hello world"}, want: "'hello world'"},
		{name: "arg with single quote", args: []string{"it's"}, want: "'it'\\''s'"},
		{name: "empty string arg", args: []string{""}, want: "''"},
		{name: "mixed clean and dirty", args: []string{"--flag", "value with space", "--other"}, want: "--flag 'value with space' --other"},
		{name: "arg with brackets", args: []string{"sonnet[1m]"}, want: "'sonnet[1m]'"},
		{name: "arg with semicolon", args: []string{"foo;bar"}, want: "'foo;bar'"},
		{name: "multiple special", args: []string{"a b", "c'd", "e|f"}, want: "'a b' 'c'\\''d' 'e|f'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Join(tt.args); got != tt.want {
				t.Fatalf("Join(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}
