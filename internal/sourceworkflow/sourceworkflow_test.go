package sourceworkflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestWithLockHonorsContextWhileWaitingForLocalLock(t *testing.T) {
	cityPath := t.TempDir()
	locked := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)

	go func() {
		holderDone <- WithLock(context.Background(), cityPath, "city:test", "BL-42", func() error {
			close(locked)
			<-release
			return nil
		})
	}()

	<-locked
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := WithLock(ctx, cityPath, "city:test", "BL-42", func() error {
		t.Fatal("WithLock ran callback while lock was already held")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WithLock error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("WithLock waited %s after context deadline, want bounded wait", elapsed)
	}

	close(release)
	if err := <-holderDone; err != nil {
		t.Fatalf("holder WithLock: %v", err)
	}
}

func TestWithLockReleasesLocalLockEntryAfterUnlock(t *testing.T) {
	cityPath := t.TempDir()
	_, key, err := lockIdentity(cityPath, "city:test", "BL-42")
	if err != nil {
		t.Fatalf("lockIdentity: %v", err)
	}

	if err := WithLock(context.Background(), cityPath, "city:test", "BL-42", func() error {
		localLocksMu.Lock()
		_, ok := localLocks[key]
		localLocksMu.Unlock()
		if !ok {
			t.Fatal("local lock entry missing while lock held")
		}
		return nil
	}); err != nil {
		t.Fatalf("WithLock: %v", err)
	}

	localLocksMu.Lock()
	_, ok := localLocks[key]
	localLocksMu.Unlock()
	if ok {
		t.Fatal("local lock entry still present after unlock")
	}
}

func TestLockIdentityCanonicalizesScopeRefSymlinks(t *testing.T) {
	cityPath := t.TempDir()
	targetDir := filepath.Join(t.TempDir(), "rig")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(targetDir): %v", err)
	}
	linkDir := filepath.Join(t.TempDir(), "rig-link")
	if err := os.Symlink(targetDir, linkDir); err != nil {
		t.Fatalf("Symlink(linkDir): %v", err)
	}

	lockPathA, keyA, err := lockIdentity(cityPath, targetDir, "BL-42")
	if err != nil {
		t.Fatalf("lockIdentity(targetDir): %v", err)
	}
	lockPathB, keyB, err := lockIdentity(cityPath, linkDir, "BL-42")
	if err != nil {
		t.Fatalf("lockIdentity(linkDir): %v", err)
	}
	if lockPathA != lockPathB {
		t.Fatalf("lockPath mismatch = %q vs %q", lockPathA, lockPathB)
	}
	if keyA != keyB {
		t.Fatalf("key mismatch = %q vs %q", keyA, keyB)
	}
}

func TestWorkflowMatchesSourceUsesSourceStoreRefWhenPresent(t *testing.T) {
	root := beads.Bead{
		ID: "wf-1",
		Metadata: map[string]string{
			"gc.source_bead_id":       "BL-42",
			SourceStoreRefMetadataKey: "rig:alpha",
		},
	}
	if !WorkflowMatchesSource(root, "BL-42", "rig:alpha", "rig:beta") {
		t.Fatal("WorkflowMatchesSource() = false, want true for matching store ref")
	}
	if WorkflowMatchesSource(root, "BL-42", "rig:beta", "rig:alpha") {
		t.Fatal("WorkflowMatchesSource() = true, want false for mismatched store ref")
	}
}

func TestWorkflowMatchesSourceTreatsMissingSourceStoreRefAsLegacyMatchInOwningStore(t *testing.T) {
	root := beads.Bead{
		ID: "wf-legacy",
		Metadata: map[string]string{
			"gc.source_bead_id": "BL-42",
		},
	}
	if !WorkflowMatchesSource(root, "BL-42", "rig:alpha", "rig:alpha") {
		t.Fatal("WorkflowMatchesSource() = false, want true for legacy root in owning store")
	}
	if WorkflowMatchesSource(root, "BL-42", "rig:alpha", "rig:beta") {
		t.Fatal("WorkflowMatchesSource() = true, want false for legacy root in different store")
	}
}

func TestListLiveRootsFiltersBySourceStoreRef(t *testing.T) {
	store := beads.NewMemStore()
	if _, err := store.Create(beads.Bead{
		ID:     "wf-alpha",
		Title:  "alpha workflow",
		Type:   "task",
		Status: "in_progress",
		Metadata: map[string]string{
			"gc.kind":                 "workflow",
			"gc.source_bead_id":       "BL-42",
			SourceStoreRefMetadataKey: "rig:alpha",
		},
	}); err != nil {
		t.Fatalf("Create(alpha): %v", err)
	}
	if _, err := store.Create(beads.Bead{
		ID:     "wf-beta",
		Title:  "beta workflow",
		Type:   "task",
		Status: "in_progress",
		Metadata: map[string]string{
			"gc.kind":                 "workflow",
			"gc.source_bead_id":       "BL-42",
			SourceStoreRefMetadataKey: "rig:beta",
		},
	}); err != nil {
		t.Fatalf("Create(beta): %v", err)
	}

	roots, err := ListLiveRoots(store, "BL-42", "rig:alpha", "rig:alpha")
	if err != nil {
		t.Fatalf("ListLiveRoots: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("ListLiveRoots(...) = %#v, want 1 root", roots)
	}
	if got := roots[0].Metadata[SourceStoreRefMetadataKey]; got != "rig:alpha" {
		t.Fatalf("root %s = %q, want rig:alpha", SourceStoreRefMetadataKey, got)
	}
}

func TestListLiveRootsTreatsLegacyRootAsStoreScoped(t *testing.T) {
	store := beads.NewMemStore()
	if _, err := store.Create(beads.Bead{
		ID:     "wf-legacy",
		Title:  "legacy workflow",
		Type:   "task",
		Status: "in_progress",
		Metadata: map[string]string{
			"gc.kind":           "workflow",
			"gc.source_bead_id": "BL-42",
		},
	}); err != nil {
		t.Fatalf("Create(legacy): %v", err)
	}

	alphaRoots, err := ListLiveRoots(store, "BL-42", "rig:alpha", "rig:alpha")
	if err != nil {
		t.Fatalf("ListLiveRoots(alpha): %v", err)
	}
	if len(alphaRoots) != 1 {
		t.Fatalf("ListLiveRoots(alpha) = %#v, want 1 root", alphaRoots)
	}

	betaRoots, err := ListLiveRoots(store, "BL-42", "rig:alpha", "rig:beta")
	if err != nil {
		t.Fatalf("ListLiveRoots(beta): %v", err)
	}
	if len(betaRoots) != 0 {
		t.Fatalf("ListLiveRoots(beta) = %#v, want 0 roots", betaRoots)
	}
}
