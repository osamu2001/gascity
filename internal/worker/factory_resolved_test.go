package worker

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

func TestSessionSpecForResolvedRuntimeRequiresCommand(t *testing.T) {
	_, err := SessionSpecForResolvedRuntime(ResolvedSessionConfig{})
	if err == nil {
		t.Fatal("SessionSpecForResolvedRuntime() error = nil, want error")
	}
}

func TestSessionSpecForResolvedRuntimeDerivesProviderAndCopiesFields(t *testing.T) {
	input := ResolvedSessionConfig{
		Alias:        "worker",
		ExplicitName: "worker-1",
		Template:     "probe",
		Title:        "Probe",
		Transport:    "acp",
		Metadata:     map[string]string{"kind": "named"},
		Runtime: ResolvedRuntime{
			Command:    "/bin/echo --verbose",
			WorkDir:    "/tmp/workdir",
			SessionEnv: map[string]string{"STUB_ENV": "present"},
			Resume: sessionpkg.ProviderResume{
				ResumeFlag:    "--resume",
				ResumeStyle:   "flag",
				ResumeCommand: "stub resume {{.SessionKey}}",
				SessionIDFlag: "--session-id",
			},
			Hints: runtime.Config{
				ReadyPromptPrefix: "stub-ready>",
				ReadyDelayMs:      250,
				Env:               map[string]string{"HINT_ENV": "present"},
			},
		},
	}

	spec, err := SessionSpecForResolvedRuntime(input)
	if err != nil {
		t.Fatalf("SessionSpecForResolvedRuntime: %v", err)
	}
	if spec.Provider != "/bin/echo" {
		t.Fatalf("Provider = %q, want /bin/echo", spec.Provider)
	}
	if spec.WorkDir != "/tmp/workdir" {
		t.Fatalf("WorkDir = %q, want /tmp/workdir", spec.WorkDir)
	}
	if spec.Transport != "acp" {
		t.Fatalf("Transport = %q, want acp", spec.Transport)
	}
	if spec.Env["STUB_ENV"] != "present" {
		t.Fatalf("Env[STUB_ENV] = %q, want present", spec.Env["STUB_ENV"])
	}
	if spec.Hints.Env["HINT_ENV"] != "present" {
		t.Fatalf("Hints.Env[HINT_ENV] = %q, want present", spec.Hints.Env["HINT_ENV"])
	}
	if spec.Resume.SessionIDFlag != "--session-id" {
		t.Fatalf("Resume.SessionIDFlag = %q, want --session-id", spec.Resume.SessionIDFlag)
	}
	if spec.Metadata["kind"] != "named" {
		t.Fatalf("Metadata[kind] = %q, want named", spec.Metadata["kind"])
	}

	input.Runtime.SessionEnv["STUB_ENV"] = "changed"
	input.Runtime.Hints.Env["HINT_ENV"] = "changed"
	input.Metadata["kind"] = "changed"
	if spec.Env["STUB_ENV"] != "present" {
		t.Fatalf("Env copy mutated to %q, want present", spec.Env["STUB_ENV"])
	}
	if spec.Hints.Env["HINT_ENV"] != "present" {
		t.Fatalf("Hints.Env copy mutated to %q, want present", spec.Hints.Env["HINT_ENV"])
	}
	if spec.Metadata["kind"] != "named" {
		t.Fatalf("Metadata copy mutated to %q, want named", spec.Metadata["kind"])
	}
}

func TestSessionSpecForResolvedRuntimeUsesHintsWorkDirFallback(t *testing.T) {
	spec, err := SessionSpecForResolvedRuntime(ResolvedSessionConfig{
		Runtime: ResolvedRuntime{
			Command: "/bin/echo",
			Hints: runtime.Config{
				WorkDir: "/tmp/hints-workdir",
			},
		},
	})
	if err != nil {
		t.Fatalf("SessionSpecForResolvedRuntime: %v", err)
	}
	if spec.WorkDir != "/tmp/hints-workdir" {
		t.Fatalf("WorkDir = %q, want /tmp/hints-workdir", spec.WorkDir)
	}
	if spec.Hints.WorkDir != "/tmp/hints-workdir" {
		t.Fatalf("Hints.WorkDir = %q, want /tmp/hints-workdir", spec.Hints.WorkDir)
	}
}

func TestFactorySessionForResolvedRuntimeStartsResolvedSession(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()

	factory, err := NewFactory(FactoryConfig{
		Store:    store,
		Provider: sp,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	handle, err := factory.SessionForResolvedRuntime(ResolvedSessionConfig{
		Alias:    "worker",
		Template: "probe",
		Title:    "Probe",
		Runtime: ResolvedRuntime{
			Command:    "/bin/echo",
			WorkDir:    t.TempDir(),
			Provider:   "stub",
			SessionEnv: map[string]string{"STUB_ENV": "present"},
			Resume: sessionpkg.ProviderResume{
				SessionIDFlag: "--session-id",
			},
			Hints: runtime.Config{
				ReadyPromptPrefix: "stub-ready>",
				ReadyDelayMs:      250,
			},
		},
	})
	if err != nil {
		t.Fatalf("SessionForResolvedRuntime: %v", err)
	}

	info, err := handle.Create(context.Background(), CreateModeStarted)
	if err != nil {
		t.Fatalf("Create(started): %v", err)
	}

	start := sp.LastStartConfig(info.SessionName)
	if start == nil {
		t.Fatalf("LastStartConfig(%q) = nil", info.SessionName)
	}
	wantArg := "--session-id " + info.SessionKey
	if got := start.Command; got != "/bin/echo "+wantArg {
		t.Fatalf("start command = %q, want %q", got, "/bin/echo "+wantArg)
	}
	if got := start.ReadyPromptPrefix; got != "stub-ready>" {
		t.Fatalf("ReadyPromptPrefix = %q, want stub-ready>", got)
	}
	if got := start.ReadyDelayMs; got != 250 {
		t.Fatalf("ReadyDelayMs = %d, want 250", got)
	}
	if got := start.Env["STUB_ENV"]; got != "present" {
		t.Fatalf("Env[STUB_ENV] = %q, want present", got)
	}
}

func TestFactorySessionForResolvedRuntimeCreatesDeferredSession(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()

	factory, err := NewFactory(FactoryConfig{
		Store:    store,
		Provider: sp,
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}

	handle, err := factory.SessionForResolvedRuntime(ResolvedSessionConfig{
		Alias:    "worker",
		Template: "probe",
		Title:    "Probe",
		Runtime: ResolvedRuntime{
			Command:  "/bin/echo",
			WorkDir:  t.TempDir(),
			Provider: "stub",
			Resume: sessionpkg.ProviderResume{
				SessionIDFlag: "--session-id",
			},
		},
	})
	if err != nil {
		t.Fatalf("SessionForResolvedRuntime: %v", err)
	}

	info, err := handle.Create(context.Background(), CreateModeDeferred)
	if err != nil {
		t.Fatalf("Create(deferred): %v", err)
	}
	if info.ID == "" {
		t.Fatal("Create(deferred) returned empty session ID")
	}
	if len(sp.Calls) != 0 {
		t.Fatalf("runtime calls = %#v, want none for deferred create", sp.Calls)
	}
}
