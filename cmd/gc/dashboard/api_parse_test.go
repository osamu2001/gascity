package dashboard

import "testing"

func TestParseCommandArgs(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{name: "simple", command: "status --json", want: []string{"status", "--json"}},
		{name: "single quoted", command: "mail send 'hello world'", want: []string{"mail", "send", "hello world"}},
		{name: "double quoted", command: "mail send \"hello world\"", want: []string{"mail", "send", "hello world"}},
		{name: "embedded single quote", command: "session new --title 'it'\\''s ready'", want: []string{"session", "new", "--title", "it's ready"}},
		{name: "empty quoted arg", command: "provider run ''", want: []string{"provider", "run", ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCommandArgs(tt.command)
			if len(got) != len(tt.want) {
				t.Fatalf("parseCommandArgs(%q) len = %d, want %d (%q)", tt.command, len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("parseCommandArgs(%q)[%d] = %q, want %q", tt.command, i, got[i], tt.want[i])
				}
			}
		})
	}
}
