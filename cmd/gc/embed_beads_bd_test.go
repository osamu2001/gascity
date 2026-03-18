package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterializeBeadsBdScript(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}

	path, err := MaterializeBeadsBdScript(dir)
	if err != nil {
		t.Fatalf("MaterializeBeadsBdScript() error: %v", err)
	}

	// Check file exists.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}

	// Check it's executable.
	if info.Mode()&0o111 == 0 {
		t.Errorf("script is not executable: mode %v", info.Mode())
	}

	// Check content is non-empty and starts with shebang.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 100 {
		t.Errorf("script too small: %d bytes", len(data))
	}
	if string(data[:2]) != "#!" {
		t.Error("script doesn't start with shebang")
	}
}

func TestMaterializeBeadsBdScript_idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}

	path1, err := MaterializeBeadsBdScript(dir)
	if err != nil {
		t.Fatal(err)
	}
	path2, err := MaterializeBeadsBdScript(dir)
	if err != nil {
		t.Fatal(err)
	}
	if path1 != path2 {
		t.Errorf("paths differ: %s != %s", path1, path2)
	}
}
