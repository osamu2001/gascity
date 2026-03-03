package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gascity/internal/config"
	"github.com/steveyegge/gascity/internal/session"
)

func TestBuildOneAgentMinimal(t *testing.T) {
	sp := session.NewFake()
	p := testBuildParams(sp)
	cfgAgent := &config.Agent{
		Name:         "worker",
		StartCommand: "echo hello",
	}

	a, err := buildOneAgent(p, cfgAgent, "worker", nil)
	if err != nil {
		t.Fatalf("buildOneAgent: %v", err)
	}
	if a.Name() != "worker" {
		t.Errorf("Name() = %q, want %q", a.Name(), "worker")
	}
	if a.SessionName() != "gc-city-worker" {
		t.Errorf("SessionName() = %q, want %q", a.SessionName(), "gc-city-worker")
	}
}

func TestBuildOneAgentStartCommandBypassesProvider(t *testing.T) {
	sp := session.NewFake()
	p := testBuildParams(sp)
	cfgAgent := &config.Agent{
		Name:         "custom",
		StartCommand: "/usr/local/bin/my-agent",
		Provider:     "nonexistent-provider",
	}

	a, err := buildOneAgent(p, cfgAgent, "custom", nil)
	if err != nil {
		t.Fatalf("buildOneAgent should succeed with start_command: %v", err)
	}

	cfg := a.SessionConfig()
	if !strings.Contains(cfg.Command, "/usr/local/bin/my-agent") {
		t.Errorf("command should contain start_command, got %q", cfg.Command)
	}
}

func TestBuildOneAgentUnknownProviderError(t *testing.T) {
	sp := session.NewFake()
	p := testBuildParams(sp)
	// Override lookPath to fail for the unknown provider.
	p.lookPath = func(name string) (string, error) {
		return "", &lookPathErr{name}
	}

	cfgAgent := &config.Agent{
		Name:     "bad",
		Provider: "missing-agent",
	}

	_, err := buildOneAgent(p, cfgAgent, "bad", nil)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error should mention agent name: %v", err)
	}
}

type lookPathErr struct{ name string }

func (e *lookPathErr) Error() string { return e.name + ": not found" }

func TestBuildOneAgentSetsEnvironment(t *testing.T) {
	sp := session.NewFake()
	p := testBuildParams(sp)
	cfgAgent := &config.Agent{
		Name:         "envtest",
		StartCommand: "echo",
	}

	a, err := buildOneAgent(p, cfgAgent, "envtest", nil)
	if err != nil {
		t.Fatalf("buildOneAgent: %v", err)
	}

	cfg := a.SessionConfig()
	if cfg.Env["GC_AGENT"] != "envtest" {
		t.Errorf("GC_AGENT = %q, want %q", cfg.Env["GC_AGENT"], "envtest")
	}
	if cfg.Env["GC_CITY"] != p.cityPath {
		t.Errorf("GC_CITY = %q, want %q", cfg.Env["GC_CITY"], p.cityPath)
	}
}

func TestBuildOneAgentPromptModeNoneSkipsPrompt(t *testing.T) {
	sp := session.NewFake()
	p := testBuildParams(sp)
	// Use a built-in provider with prompt_mode overridden to "none"
	// via city-level providers.
	p.providers = map[string]config.ProviderSpec{
		"silent-provider": {
			Command:    "echo",
			PromptMode: "none",
		},
	}
	cfgAgent := &config.Agent{
		Name:     "silent",
		Provider: "silent-provider",
	}

	a, err := buildOneAgent(p, cfgAgent, "silent", nil)
	if err != nil {
		t.Fatalf("buildOneAgent: %v", err)
	}

	// With prompt_mode=none from provider, the command should NOT have
	// a quoted prompt appended.
	cfg := a.SessionConfig()
	if strings.Contains(cfg.Command, "silent •") {
		t.Errorf("command should not contain beacon with prompt_mode=none, got %q", cfg.Command)
	}
}

func TestBuildOneAgentFingerprintExtra(t *testing.T) {
	sp := session.NewFake()
	p := testBuildParams(sp)
	cfgAgent := &config.Agent{
		Name:         "pooled",
		StartCommand: "echo",
	}
	fpExtra := map[string]string{"pool_min": "2", "pool_max": "5"}

	a, err := buildOneAgent(p, cfgAgent, "pooled", fpExtra)
	if err != nil {
		t.Fatalf("buildOneAgent: %v", err)
	}

	cfg := a.SessionConfig()
	if cfg.FingerprintExtra["pool_min"] != "2" {
		t.Errorf("FingerprintExtra[pool_min] = %q, want %q", cfg.FingerprintExtra["pool_min"], "2")
	}
}

func TestBuildOneAgentQualifiedName(t *testing.T) {
	sp := session.NewFake()
	p := testBuildParams(sp)
	cfgAgent := &config.Agent{
		Name:         "polecat",
		Dir:          "myrig",
		StartCommand: "echo",
	}

	a, err := buildOneAgent(p, cfgAgent, "myrig/polecat", nil)
	if err != nil {
		t.Fatalf("buildOneAgent: %v", err)
	}
	if a.Name() != "myrig/polecat" {
		t.Errorf("Name() = %q, want %q", a.Name(), "myrig/polecat")
	}
	if a.SessionName() != "gc-city-myrig--polecat" {
		t.Errorf("SessionName() = %q, want %q", a.SessionName(), "gc-city-myrig--polecat")
	}
}

func TestBuildOneAgentStartsAndRuns(t *testing.T) {
	sp := session.NewFake()
	p := testBuildParams(sp)
	cfgAgent := &config.Agent{
		Name:         "runner",
		StartCommand: "echo hello",
	}

	a, err := buildOneAgent(p, cfgAgent, "runner", nil)
	if err != nil {
		t.Fatalf("buildOneAgent: %v", err)
	}

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !a.IsRunning() {
		t.Error("agent should be running after Start")
	}
}

func TestBuildOneAgentCustomSessionTemplate(t *testing.T) {
	sp := session.NewFake()
	p := testBuildParams(sp)
	p.sessionTemplate = "custom-{{.City}}-{{.Agent}}"
	cfgAgent := &config.Agent{
		Name:         "mayor",
		StartCommand: "echo",
	}

	a, err := buildOneAgent(p, cfgAgent, "mayor", nil)
	if err != nil {
		t.Fatalf("buildOneAgent: %v", err)
	}
	if a.SessionName() != "custom-city-mayor" {
		t.Errorf("SessionName() = %q, want %q", a.SessionName(), "custom-city-mayor")
	}
}

func TestNewAgentBuildParams(t *testing.T) {
	sp := session.NewFake()
	cfg := &config.City{
		Workspace: config.Workspace{
			Name:            "my-city",
			SessionTemplate: "gc-{{.City}}-{{.Agent}}",
		},
		Rigs: []config.Rig{
			{Name: "rig1", Path: "/tmp/rig1"},
		},
	}
	var stderr bytes.Buffer

	bp := newAgentBuildParams("my-city", "/tmp/city", cfg, sp, time.Time{}, &stderr)

	if bp.cityName != "my-city" {
		t.Errorf("cityName = %q, want %q", bp.cityName, "my-city")
	}
	if bp.cityPath != "/tmp/city" {
		t.Errorf("cityPath = %q, want %q", bp.cityPath, "/tmp/city")
	}
	if len(bp.rigs) != 1 {
		t.Errorf("rigs = %d, want 1", len(bp.rigs))
	}
	if bp.sessionTemplate != "gc-{{.City}}-{{.Agent}}" {
		t.Errorf("sessionTemplate = %q, want config value", bp.sessionTemplate)
	}
}
