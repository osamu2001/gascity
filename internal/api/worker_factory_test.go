package api

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session"
)

func TestWorkerFactorySessionByIDUsesResolvedTemplateRuntime(t *testing.T) {
	fs := newSessionFakeState(t)
	fs.cfg.Agents[0].Provider = "resolved-worker"
	fs.cfg.Providers["resolved-worker"] = config.ProviderSpec{
		DisplayName:       "Resolved Worker",
		Command:           "/bin/echo",
		ReadyPromptPrefix: "resolved-ready>",
		ReadyDelayMs:      321,
		ResumeFlag:        "--resume-resolved",
		ResumeStyle:       "flag",
		SessionIDFlag:     "--session-id-resolved",
	}

	srv := New(fs)
	mgr := session.NewManager(fs.cityBeadStore, fs.sp)
	info, err := mgr.CreateBeadOnly(
		"myrig/worker",
		"Chat",
		"",
		t.TempDir(),
		"",
		"",
		nil,
		session.ProviderResume{SessionIDFlag: "--stale-session-id"},
	)
	if err != nil {
		t.Fatalf("CreateBeadOnly: %v", err)
	}

	factory, err := srv.workerFactory(fs.cityBeadStore)
	if err != nil {
		t.Fatalf("workerFactory: %v", err)
	}
	handle, err := factory.SessionByID(info.ID)
	if err != nil {
		t.Fatalf("SessionByID(%q): %v", info.ID, err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := fs.sp.LastStartConfig(info.SessionName)
	if start == nil {
		t.Fatal("LastStartConfig() = nil")
	}
	if got, want := start.Command, "/bin/echo --session-id-resolved "+info.SessionKey; got != want {
		t.Fatalf("start command = %q, want %q", got, want)
	}
	if got, want := start.ReadyPromptPrefix, "resolved-ready>"; got != want {
		t.Fatalf("ReadyPromptPrefix = %q, want %q", got, want)
	}
	if got, want := start.ReadyDelayMs, 321; got != want {
		t.Fatalf("ReadyDelayMs = %d, want %d", got, want)
	}
}

func TestWorkerFactoryHandleForTargetUsesResolvedTemplateRuntimeForSessionMeta(t *testing.T) {
	fs := newSessionFakeState(t)
	fs.cfg.Agents[0].Provider = "resolved-worker"
	fs.cfg.Providers["resolved-worker"] = config.ProviderSpec{
		DisplayName:       "Resolved Worker",
		Command:           "/bin/echo",
		ReadyPromptPrefix: "resolved-ready>",
		ReadyDelayMs:      321,
		ResumeFlag:        "--resume-resolved",
		ResumeStyle:       "flag",
		SessionIDFlag:     "--session-id-resolved",
	}

	srv := New(fs)
	mgr := session.NewManager(fs.cityBeadStore, fs.sp)
	info, err := mgr.CreateBeadOnly(
		"myrig/worker",
		"Chat",
		"",
		t.TempDir(),
		"",
		"",
		nil,
		session.ProviderResume{SessionIDFlag: "--stale-session-id"},
	)
	if err != nil {
		t.Fatalf("CreateBeadOnly: %v", err)
	}
	if err := fs.sp.SetMeta("legacy-runtime-name", "GC_SESSION_ID", info.ID); err != nil {
		t.Fatalf("SetMeta(GC_SESSION_ID): %v", err)
	}

	factory, err := srv.workerFactory(fs.cityBeadStore)
	if err != nil {
		t.Fatalf("workerFactory: %v", err)
	}
	handle, err := factory.HandleForTarget("legacy-runtime-name", nil)
	if err != nil {
		t.Fatalf("HandleForTarget: %v", err)
	}
	if err := handle.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := fs.sp.LastStartConfig(info.SessionName)
	if start == nil {
		t.Fatal("LastStartConfig() = nil")
	}
	if got, want := start.Command, "/bin/echo --session-id-resolved "+info.SessionKey; got != want {
		t.Fatalf("start command = %q, want %q", got, want)
	}
	if got, want := start.ReadyPromptPrefix, "resolved-ready>"; got != want {
		t.Fatalf("ReadyPromptPrefix = %q, want %q", got, want)
	}
	if got, want := start.ReadyDelayMs, 321; got != want {
		t.Fatalf("ReadyDelayMs = %d, want %d", got, want)
	}
}
