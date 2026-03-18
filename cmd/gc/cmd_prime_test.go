package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

func TestBuildPrimeContextFallsBackToConfiguredRigRoot(t *testing.T) {
	t.Setenv("GC_RIG", "demo")
	t.Setenv("GC_RIG_ROOT", "")
	t.Setenv("GC_DIR", "/tmp/demo-work")
	t.Setenv("GC_BRANCH", "")

	ctx := buildPrimeContext("/city", &config.Agent{Name: "polecat", Dir: "demo"}, []config.Rig{
		{Name: "demo", Path: "/repos/demo", Prefix: "dm"},
	})

	if ctx.RigName != "demo" {
		t.Fatalf("RigName = %q, want demo", ctx.RigName)
	}
	if ctx.RigRoot != "/repos/demo" {
		t.Fatalf("RigRoot = %q, want /repos/demo", ctx.RigRoot)
	}
}
