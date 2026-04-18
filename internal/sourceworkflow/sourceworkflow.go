package sourceworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/citylayout"
)

type ConflictError struct {
	SourceBeadID string
	WorkflowIDs  []string
}

const SourceStoreRefMetadataKey = "gc.source_store_ref"

func (e *ConflictError) Error() string {
	if e == nil {
		return "source workflow conflict"
	}
	if len(e.WorkflowIDs) == 0 {
		return fmt.Sprintf("source bead %s already has a live workflow", e.SourceBeadID)
	}
	return fmt.Sprintf(
		"source bead %s already has live workflow(s): %s",
		e.SourceBeadID,
		strings.Join(e.WorkflowIDs, ","),
	)
}

func NormalizeSourceBeadID(sourceBeadID string) string {
	return strings.TrimSpace(sourceBeadID)
}

func NormalizeSourceStoreRef(sourceStoreRef string) string {
	return strings.TrimSpace(sourceStoreRef)
}

func WorkflowMatchesSource(root beads.Bead, sourceBeadID, sourceStoreRef, rootStoreRef string) bool {
	sourceBeadID = NormalizeSourceBeadID(sourceBeadID)
	if sourceBeadID == "" {
		return false
	}
	if NormalizeSourceBeadID(root.Metadata["gc.source_bead_id"]) != sourceBeadID {
		return false
	}
	sourceStoreRef = NormalizeSourceStoreRef(sourceStoreRef)
	if sourceStoreRef == "" {
		return true
	}
	rootSourceStoreRef := NormalizeSourceStoreRef(root.Metadata[SourceStoreRefMetadataKey])
	if rootSourceStoreRef != "" {
		return rootSourceStoreRef == sourceStoreRef
	}
	rootStoreRef = NormalizeSourceStoreRef(rootStoreRef)
	if rootStoreRef == "" {
		return false
	}
	return rootStoreRef == sourceStoreRef
}

func ListLiveRoots(store beads.Store, sourceBeadID, sourceStoreRef, rootStoreRef string) ([]beads.Bead, error) {
	sourceBeadID = NormalizeSourceBeadID(sourceBeadID)
	if store == nil || sourceBeadID == "" {
		return nil, nil
	}
	roots, err := store.List(beads.ListQuery{
		Metadata: map[string]string{
			"gc.kind":           "workflow",
			"gc.source_bead_id": sourceBeadID,
		},
	})
	if err != nil {
		return nil, err
	}
	roots = slices.DeleteFunc(roots, func(root beads.Bead) bool {
		return !WorkflowMatchesSource(root, sourceBeadID, sourceStoreRef, rootStoreRef)
	})
	slices.SortFunc(roots, func(a, b beads.Bead) int {
		return strings.Compare(a.ID, b.ID)
	})
	return roots, nil
}

func BlockingWorkflowIDs(roots []beads.Bead) []string {
	ids := make([]string, 0, len(roots))
	for _, root := range roots {
		if root.ID == "" {
			continue
		}
		ids = append(ids, root.ID)
	}
	slices.Sort(ids)
	return ids
}

var (
	localLocksMu sync.Mutex
	localLocks   = map[string]*localLock{}
)

const fileLockRetryInterval = 25 * time.Millisecond

type localLock struct {
	token chan struct{}
	refs  int
}

func WithLock(ctx context.Context, cityPath, scopeRef, sourceBeadID string, fn func() error) error {
	sourceBeadID = NormalizeSourceBeadID(sourceBeadID)
	if sourceBeadID == "" {
		return fn()
	}
	lockPath, key, err := lockIdentity(cityPath, scopeRef, sourceBeadID)
	if err != nil {
		return err
	}
	mu := inProcessMutex(key)
	defer releaseInProcessMutex(key, mu)
	if err := mu.Lock(ctx); err != nil {
		return err
	}
	defer mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return fmt.Errorf("create source workflow lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open source workflow lock: %w", err)
	}
	defer f.Close() //nolint:errcheck // best-effort cleanup
	if err := lockFile(ctx, f, sourceBeadID); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck // best-effort unlock
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func inProcessMutex(key string) *localLock {
	localLocksMu.Lock()
	defer localLocksMu.Unlock()
	mu := localLocks[key]
	if mu == nil {
		mu = newLocalLock()
		localLocks[key] = mu
	}
	mu.refs++
	return mu
}

func releaseInProcessMutex(key string, mu *localLock) {
	localLocksMu.Lock()
	defer localLocksMu.Unlock()
	current := localLocks[key]
	if current == nil || current != mu {
		return
	}
	if current.refs > 0 {
		current.refs--
	}
	if current.refs == 0 {
		delete(localLocks, key)
	}
}

func newLocalLock() *localLock {
	lock := &localLock{token: make(chan struct{}, 1)}
	lock.token <- struct{}{}
	return lock
}

func (l *localLock) Lock(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.token:
		return nil
	}
}

func (l *localLock) Unlock() {
	l.token <- struct{}{}
}

func lockFile(ctx context.Context, f *os.File, sourceBeadID string) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("lock source workflow %q: %w", sourceBeadID, err)
		}
		timer := time.NewTimer(fileLockRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func lockIdentity(cityPath, scopeRef, sourceBeadID string) (lockPath, key string, _ error) {
	cityPath, err := canonicalCityPath(cityPath)
	if err != nil {
		return "", "", err
	}
	scopeRef = canonicalScopeRef(scopeRef)
	if scopeRef == "" {
		scopeRef = "city"
	}
	hash := sha256.Sum256([]byte(scopeRef + "\x00" + sourceBeadID))
	key = cityPath + "\x00" + scopeRef + "\x00" + sourceBeadID
	lockPath = filepath.Join(
		citylayout.RuntimeDataDir(cityPath),
		"sling-source-locks",
		hex.EncodeToString(hash[:])+".lock",
	)
	return lockPath, key, nil
}

func canonicalScopeRef(scopeRef string) string {
	scopeRef = strings.TrimSpace(scopeRef)
	if scopeRef == "" {
		return ""
	}
	scopeRef = filepath.Clean(scopeRef)
	if resolved, err := filepath.EvalSymlinks(scopeRef); err == nil && strings.TrimSpace(resolved) != "" {
		return resolved
	}
	return scopeRef
}

func ListWorkflowBeads(store beads.Store, rootID string) ([]beads.Bead, error) {
	rootID = strings.TrimSpace(rootID)
	if store == nil || rootID == "" {
		return nil, nil
	}
	root, err := store.Get(rootID)
	if err != nil {
		return nil, err
	}
	descendants, err := store.List(beads.ListQuery{
		IncludeClosed: true,
		Metadata: map[string]string{
			"gc.root_bead_id": rootID,
		},
	})
	if err != nil {
		return nil, err
	}
	beadsByID := map[string]beads.Bead{
		root.ID: root,
	}
	for _, bead := range descendants {
		beadsByID[bead.ID] = bead
	}
	out := make([]beads.Bead, 0, len(beadsByID))
	for _, bead := range beadsByID {
		out = append(out, bead)
	}
	slices.SortFunc(out, func(a, b beads.Bead) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func CloseWorkflowSubtree(store beads.Store, rootID string) (int, error) {
	matched, err := ListWorkflowBeads(store, rootID)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(matched))
	for _, bead := range matched {
		if bead.ID == "" || bead.Status == "closed" {
			continue
		}
		ids = append(ids, bead.ID)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	return store.CloseAll(ids, map[string]string{"gc.outcome": "skipped"})
}

type WorkflowBeadSnapshot struct {
	ID       string
	Status   string
	Assignee string
	Outcome  string
}

func SnapshotOpenWorkflowBeads(store beads.Store, rootID string) ([]WorkflowBeadSnapshot, error) {
	matched, err := ListWorkflowBeads(store, rootID)
	if err != nil {
		return nil, err
	}
	out := make([]WorkflowBeadSnapshot, 0, len(matched))
	for _, bead := range matched {
		if bead.ID == "" || bead.Status == "closed" {
			continue
		}
		out = append(out, WorkflowBeadSnapshot{
			ID:       bead.ID,
			Status:   bead.Status,
			Assignee: bead.Assignee,
			Outcome:  bead.Metadata["gc.outcome"],
		})
	}
	return out, nil
}

func RestoreWorkflowBeads(store beads.Store, snapshots []WorkflowBeadSnapshot) error {
	var restoreErr error
	for _, snapshot := range snapshots {
		if strings.TrimSpace(snapshot.ID) == "" {
			continue
		}
		status := snapshot.Status
		assignee := snapshot.Assignee
		if err := store.Update(snapshot.ID, beads.UpdateOpts{
			Status:   &status,
			Assignee: &assignee,
		}); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore bead %s: %w", snapshot.ID, err))
			continue
		}
		if err := store.SetMetadata(snapshot.ID, "gc.outcome", snapshot.Outcome); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore bead %s outcome: %w", snapshot.ID, err))
		}
	}
	return restoreErr
}

func canonicalCityPath(cityPath string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(cityPath))
	if cleaned == "" || cleaned == "." {
		return "", fmt.Errorf("source workflow lock requires city path")
	}
	abs, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("canonicalize city path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(resolved) != "" {
		return resolved, nil
	}
	return abs, nil
}
