package runtime

import (
	"context"
	"reflect"
	"testing"
)

func TestContainsWorkspaceTrustDialog(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "claude quick safety check",
			content: "Quick safety check\nYes, I trust this folder",
			want:    true,
		},
		{
			name:    "claude trust this folder",
			content: "Do you trust this folder?",
			want:    true,
		},
		{
			name:    "codex trust dialog",
			content: "> Do you trust the contents of this directory?",
			want:    true,
		},
		{
			name:    "normal prompt text",
			content: "> waiting for input",
			want:    false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := containsWorkspaceTrustDialog(tt.content); got != tt.want {
				t.Fatalf("containsWorkspaceTrustDialog(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestAcceptStartupDialogsAcceptsCodexTrustDialog(t *testing.T) {
	oldProbe := startupDialogProbeDelay
	oldAccept := startupDialogAcceptDelay
	oldBypass := bypassDialogConfirmDelay
	startupDialogProbeDelay = 0
	startupDialogAcceptDelay = 0
	bypassDialogConfirmDelay = 0
	t.Cleanup(func() {
		startupDialogProbeDelay = oldProbe
		startupDialogAcceptDelay = oldAccept
		bypassDialogConfirmDelay = oldBypass
	})

	var sent []string
	err := AcceptStartupDialogs(
		context.Background(),
		func(_ int) (string, error) {
			return "Do you trust the contents of this directory?", nil
		},
		func(keys ...string) error {
			sent = append(sent, keys...)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("AcceptStartupDialogs() error = %v", err)
	}
	if !reflect.DeepEqual(sent, []string{"Enter"}) {
		t.Fatalf("sent keys = %v, want [Enter]", sent)
	}
}

func TestAcceptStartupDialogsAcceptsBypassPermissionsWarning(t *testing.T) {
	oldProbe := startupDialogProbeDelay
	oldAccept := startupDialogAcceptDelay
	oldBypass := bypassDialogConfirmDelay
	startupDialogProbeDelay = 0
	startupDialogAcceptDelay = 0
	bypassDialogConfirmDelay = 0
	t.Cleanup(func() {
		startupDialogProbeDelay = oldProbe
		startupDialogAcceptDelay = oldAccept
		bypassDialogConfirmDelay = oldBypass
	})

	var sent []string
	call := 0
	err := AcceptStartupDialogs(
		context.Background(),
		func(_ int) (string, error) {
			call++
			if call == 1 {
				return "normal startup output", nil
			}
			return "Bypass Permissions mode", nil
		},
		func(keys ...string) error {
			sent = append(sent, keys...)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("AcceptStartupDialogs() error = %v", err)
	}
	if !reflect.DeepEqual(sent, []string{"Down", "Enter"}) {
		t.Fatalf("sent keys = %v, want [Down Enter]", sent)
	}
}
