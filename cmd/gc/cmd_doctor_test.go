package main

import (
	"testing"

	"github.com/steveyegge/gascity/internal/config"
)

func TestCollectTopologyDirsEmpty(t *testing.T) {
	cfg := &config.City{}
	dirs := collectTopologyDirs(cfg)
	if len(dirs) != 0 {
		t.Errorf("expected no dirs, got %v", dirs)
	}
}

func TestCollectTopologyDirsCityLevel(t *testing.T) {
	cfg := &config.City{
		TopologyDirs: []string{"/a", "/b"},
	}
	dirs := collectTopologyDirs(cfg)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d: %v", len(dirs), dirs)
	}
	if dirs[0] != "/a" || dirs[1] != "/b" {
		t.Errorf("dirs = %v, want [/a /b]", dirs)
	}
}

func TestCollectTopologyDirsRigLevel(t *testing.T) {
	cfg := &config.City{
		RigTopologyDirs: map[string][]string{
			"rig1": {"/x", "/y"},
			"rig2": {"/z"},
		},
	}
	dirs := collectTopologyDirs(cfg)
	if len(dirs) != 3 {
		t.Fatalf("expected 3 dirs, got %d: %v", len(dirs), dirs)
	}
}

func TestCollectTopologyDirsDeduplicates(t *testing.T) {
	cfg := &config.City{
		TopologyDirs: []string{"/shared", "/a"},
		RigTopologyDirs: map[string][]string{
			"rig1": {"/shared", "/b"}, // /shared is a duplicate
		},
	}
	dirs := collectTopologyDirs(cfg)
	// /shared should appear only once.
	count := 0
	for _, d := range dirs {
		if d == "/shared" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("/shared appears %d times, want 1", count)
	}
	if len(dirs) != 3 {
		t.Fatalf("expected 3 unique dirs, got %d: %v", len(dirs), dirs)
	}
}

func TestCollectTopologyDirsMixed(t *testing.T) {
	cfg := &config.City{
		TopologyDirs: []string{"/city-topo"},
		RigTopologyDirs: map[string][]string{
			"rig1": {"/rig-topo"},
		},
	}
	dirs := collectTopologyDirs(cfg)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 dirs, got %d: %v", len(dirs), dirs)
	}
}
