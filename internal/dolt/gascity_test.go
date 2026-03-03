package dolt

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gascity/internal/beads"
)

func TestGasCityConfig_Paths(t *testing.T) {
	cityPath := "/home/user/bright-lights"
	config := GasCityConfig(cityPath)

	if config.TownRoot != cityPath {
		t.Errorf("TownRoot = %q, want %q", config.TownRoot, cityPath)
	}
	if want := filepath.Join(cityPath, ".gc", "dolt-data"); config.DataDir != want {
		t.Errorf("DataDir = %q, want %q", config.DataDir, want)
	}
	if want := filepath.Join(cityPath, ".gc", "dolt.log"); config.LogFile != want {
		t.Errorf("LogFile = %q, want %q", config.LogFile, want)
	}
	if config.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", config.Port, DefaultPort)
	}
	if config.User != DefaultUser {
		t.Errorf("User = %q, want %q", config.User, DefaultUser)
	}
	if config.MaxConnections != DefaultMaxConnections {
		t.Errorf("MaxConnections = %d, want %d", config.MaxConnections, DefaultMaxConnections)
	}
}

func TestGasCityConfig_EnvOverrides(t *testing.T) {
	t.Setenv("GC_DOLT_HOST", "remote.example.com")
	t.Setenv("GC_DOLT_PORT", "3308")
	t.Setenv("GC_DOLT_USER", "testuser")
	t.Setenv("GC_DOLT_PASSWORD", "secret")

	config := GasCityConfig("/tmp/test-city")

	if config.Host != "remote.example.com" {
		t.Errorf("Host = %q, want %q", config.Host, "remote.example.com")
	}
	if config.Port != 3308 {
		t.Errorf("Port = %d, want 3308", config.Port)
	}
	if config.User != "testuser" {
		t.Errorf("User = %q, want %q", config.User, "testuser")
	}
	if config.Password != "secret" {
		t.Errorf("Password = %q, want %q", config.Password, "secret")
	}
}

func TestGasCityConfig_InvalidPort(t *testing.T) {
	t.Setenv("GC_DOLT_PORT", "not-a-number")

	config := GasCityConfig("/tmp/test-city")

	if config.Port != DefaultPort {
		t.Errorf("Port = %d, want default %d when env is invalid", config.Port, DefaultPort)
	}
}

func TestParseDataDir(t *testing.T) {
	tests := []struct {
		cmdline string
		want    string
	}{
		{"dolt sql-server --port 3307 --data-dir /home/user/city/.gc/dolt-data --max-connections 50", "/home/user/city/.gc/dolt-data"},
		{"dolt sql-server --data-dir=/tmp/data", "/tmp/data"},
		{"dolt sql-server --port 3307", ""},
		{"", ""},
		{"dolt sql-server --data-dir /a/b", "/a/b"},
	}
	for _, tt := range tests {
		got := parseDataDir(tt.cmdline)
		if got != tt.want {
			t.Errorf("parseDataDir(%q) = %q, want %q", tt.cmdline, got, tt.want)
		}
	}
}

func TestFindDoltServerForDataDir_NoServer(t *testing.T) {
	// Use a port that's definitely not in use.
	pid := findDoltServerForDataDir(19999, "/nonexistent")
	if pid != 0 {
		t.Errorf("findDoltServerForDataDir on unused port returned PID %d, want 0", pid)
	}
}

func TestWriteCityMetadata(t *testing.T) {
	cityPath := t.TempDir()
	cityName := "bright-lights"

	if err := writeCityMetadata(cityPath, cityName); err != nil {
		t.Fatalf("writeCityMetadata() error = %v", err)
	}

	metadataPath := filepath.Join(cityPath, ".beads", "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("reading metadata.json: %v", err)
	}

	var meta CityMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parsing metadata.json: %v", err)
	}

	if meta.Database != "dolt" {
		t.Errorf("database = %q, want %q", meta.Database, "dolt")
	}
	if meta.Backend != "dolt" {
		t.Errorf("backend = %q, want %q", meta.Backend, "dolt")
	}
	if meta.DoltMode != "server" {
		t.Errorf("dolt_mode = %q, want %q", meta.DoltMode, "server")
	}
	if meta.DoltDatabase != cityName {
		t.Errorf("dolt_database = %q, want %q", meta.DoltDatabase, cityName)
	}
}

func TestWriteCityMetadata_Idempotent(t *testing.T) {
	cityPath := t.TempDir()
	cityName := "test-city"

	// Write twice.
	if err := writeCityMetadata(cityPath, cityName); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeCityMetadata(cityPath, cityName); err != nil {
		t.Fatalf("second write: %v", err)
	}

	// Verify content is correct after second write.
	data, err := os.ReadFile(filepath.Join(cityPath, ".beads", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta CityMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.DoltDatabase != cityName {
		t.Errorf("dolt_database = %q, want %q", meta.DoltDatabase, cityName)
	}
}

func TestWriteCityMetadata_CreatesBeadsDir(t *testing.T) {
	cityPath := t.TempDir()

	if err := writeCityMetadata(cityPath, "test"); err != nil {
		t.Fatalf("writeCityMetadata() error = %v", err)
	}

	beadsDir := filepath.Join(cityPath, ".beads")
	fi, err := os.Stat(beadsDir)
	if err != nil {
		t.Fatalf(".beads dir not created: %v", err)
	}
	if !fi.IsDir() {
		t.Error(".beads is not a directory")
	}
}

func TestRunBdInit_Idempotent(t *testing.T) {
	cityPath := t.TempDir()

	// Pre-create .beads/metadata.json to simulate already-initialized state.
	beadsDir := filepath.Join(cityPath, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"),
		[]byte(`{"backend":"dolt"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// The runner should never be called (idempotent — already initialized).
	neverCalled := func(_, _ string, _ ...string) ([]byte, error) {
		t.Fatal("runner should not be called for idempotent init")
		return nil, nil
	}
	store := beads.NewBdStore(cityPath, neverCalled)

	// Should be a no-op (idempotent) — doesn't need bd installed.
	if err := runBdInit(store, cityPath, "test", "localhost", 3307); err != nil {
		t.Errorf("runBdInit() error = %v, want nil (idempotent)", err)
	}
}

// Regression: Bug 2 — patchMetadataConnection must NOT set dolt_database.
// The dolt_database field is owned by bd init (set via -p flag). If
// patchMetadataConnection overwrites it, rigs get the wrong database name.
func TestPatchMetadataConnection_PreservesDoltDatabase(t *testing.T) {
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate bd init having written metadata with its own dolt_database.
	existing := `{"dolt_database":"fe","issue_prefix":"fe"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"),
		[]byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := patchMetadataConnection(dir); err != nil {
		t.Fatalf("patchMetadataConnection() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}

	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}

	// dolt_database must be preserved (owned by bd).
	if meta["dolt_database"] != "fe" {
		t.Errorf("dolt_database = %v, want %q (bd's value must be preserved)", meta["dolt_database"], "fe")
	}
	// issue_prefix must be preserved (owned by bd).
	if meta["issue_prefix"] != "fe" {
		t.Errorf("issue_prefix = %v, want %q (bd's value must be preserved)", meta["issue_prefix"], "fe")
	}
	// Connection fields must be set.
	if meta["database"] != "dolt" {
		t.Errorf("database = %v, want %q", meta["database"], "dolt")
	}
	if meta["backend"] != "dolt" {
		t.Errorf("backend = %v, want %q", meta["backend"], "dolt")
	}
	if meta["dolt_mode"] != "server" {
		t.Errorf("dolt_mode = %v, want %q", meta["dolt_mode"], "server")
	}
}

func TestPatchMetadataConnection_CreatesBeadsDir(t *testing.T) {
	dir := t.TempDir()

	if err := patchMetadataConnection(dir); err != nil {
		t.Fatalf("patchMetadataConnection() error = %v", err)
	}

	// Verify .beads dir was created.
	fi, err := os.Stat(filepath.Join(dir, ".beads"))
	if err != nil {
		t.Fatalf(".beads dir not created: %v", err)
	}
	if !fi.IsDir() {
		t.Error(".beads is not a directory")
	}

	// Verify metadata.json has connection fields but no dolt_database.
	data, err := os.ReadFile(filepath.Join(dir, ".beads", "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["database"] != "dolt" {
		t.Errorf("database = %v, want %q", meta["database"], "dolt")
	}
	// dolt_database should NOT be set by patchMetadataConnection.
	if _, ok := meta["dolt_database"]; ok {
		t.Errorf("dolt_database should not be set by patchMetadataConnection, got %v", meta["dolt_database"])
	}
}

func TestEnsureRunning_remote(t *testing.T) {
	cityPath := t.TempDir()

	// Unreachable remote host — EnsureRunning should return an error.
	t.Setenv("GC_DOLT_HOST", "192.0.2.1") // TEST-NET-1, not routable
	t.Setenv("GC_DOLT_PORT", "39999")

	err := EnsureRunning(cityPath)
	if err == nil {
		t.Error("EnsureRunning() should fail for unreachable remote host")
	}
	if !strings.Contains(err.Error(), "remote dolt") {
		t.Errorf("error should mention 'remote dolt', got: %v", err)
	}
}

func TestIsRunningCity_remote(t *testing.T) {
	cityPath := t.TempDir()

	// Unreachable remote → not running, no error.
	t.Setenv("GC_DOLT_HOST", "192.0.2.1") // TEST-NET-1, not routable
	t.Setenv("GC_DOLT_PORT", "39999")

	running, pid, err := IsRunningCity(cityPath)
	if err != nil {
		t.Fatalf("IsRunningCity() unexpected error: %v", err)
	}
	if running {
		t.Error("IsRunningCity() = true for unreachable remote, want false")
	}
	if pid != 0 {
		t.Errorf("pid = %d, want 0 for unreachable remote", pid)
	}
}

func TestStopCity_remote_noop(t *testing.T) {
	cityPath := t.TempDir()

	// Remote host — StopCity should be a no-op.
	t.Setenv("GC_DOLT_HOST", "remote.example.com")
	t.Setenv("GC_DOLT_PORT", "3307")

	err := StopCity(cityPath)
	if err != nil {
		t.Errorf("StopCity() = %v for remote, want nil (no-op)", err)
	}
}

func TestInitRigBeads_Idempotent(t *testing.T) {
	rigPath := t.TempDir()

	// Pre-create .beads/metadata.json to simulate already-initialized state.
	beadsDir := filepath.Join(rigPath, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"),
		[]byte(`{"backend":"dolt","dolt_database":"fe"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// The runner should never be called (idempotent — already initialized).
	neverCalled := func(_, _ string, _ ...string) ([]byte, error) {
		t.Fatal("runner should not be called for idempotent init")
		return nil, nil
	}
	store := beads.NewBdStore(rigPath, neverCalled)

	// Should be a no-op (idempotent) — doesn't need bd installed.
	if err := InitRigBeads(store, rigPath, "fe", "localhost", 3307); err != nil {
		t.Errorf("InitRigBeads() error = %v, want nil (idempotent)", err)
	}
}

// ── Orphan detection and cleanup tests ───────────────────────────────

// helper: create a fake dolt database dir with a .dolt marker.
func createFakeDB(t *testing.T, dataDir, name string) {
	t.Helper()
	dbDir := filepath.Join(dataDir, name)
	if err := os.MkdirAll(filepath.Join(dbDir, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a small file so dirSize returns non-zero.
	if err := os.WriteFile(filepath.Join(dbDir, "data.bin"), []byte("testdata"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// helper: write metadata.json with a dolt_database field.
func writeMetadataDB(t *testing.T, beadsDir, dbName string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := map[string]string{"dolt_database": dbName}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectReferencedDatabasesCity(t *testing.T) {
	cityPath := t.TempDir()

	// HQ metadata
	writeMetadataDB(t, filepath.Join(cityPath, ".beads"), "hq")

	// Rig metadata via top-level dir scan
	writeMetadataDB(t, filepath.Join(cityPath, "frontend", ".beads"), "frontend")

	// Route that references a database
	routesDir := filepath.Join(cityPath, ".beads")
	routesLine := `{"path":"api"}` + "\n"
	if err := os.WriteFile(filepath.Join(routesDir, "routes.jsonl"), []byte(routesLine), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMetadataDB(t, filepath.Join(cityPath, "api", ".beads"), "api-db")

	refs := collectReferencedDatabasesCity(cityPath)

	for _, want := range []string{"hq", "frontend", "api-db"} {
		if !refs[want] {
			t.Errorf("collectReferencedDatabasesCity missing %q, got %v", want, refs)
		}
	}
}

func TestFindOrphanedDatabasesCity(t *testing.T) {
	cityPath := t.TempDir()

	// Set GC_DOLT=skip so ListDatabasesCity reads from disk.
	t.Setenv("GC_DOLT", "skip")

	dataDir := filepath.Join(cityPath, ".gc", "dolt-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create databases: hq (referenced), frontend (referenced), orphan (not referenced).
	createFakeDB(t, dataDir, "hq")
	createFakeDB(t, dataDir, "frontend")
	createFakeDB(t, dataDir, "orphan")

	// Reference hq and frontend via metadata.
	writeMetadataDB(t, filepath.Join(cityPath, ".beads"), "hq")
	writeMetadataDB(t, filepath.Join(cityPath, "frontend", ".beads"), "frontend")

	orphans, err := FindOrphanedDatabasesCity(cityPath)
	if err != nil {
		t.Fatalf("FindOrphanedDatabasesCity() error = %v", err)
	}

	if len(orphans) != 1 {
		t.Fatalf("got %d orphans, want 1", len(orphans))
	}
	if orphans[0].Name != "orphan" {
		t.Errorf("orphan name = %q, want %q", orphans[0].Name, "orphan")
	}
	if orphans[0].SizeBytes <= 0 {
		t.Errorf("orphan size = %d, want > 0", orphans[0].SizeBytes)
	}
}

func TestFindOrphanedDatabasesCity_NoDatabases(t *testing.T) {
	cityPath := t.TempDir()
	t.Setenv("GC_DOLT", "skip")

	orphans, err := FindOrphanedDatabasesCity(cityPath)
	if err != nil {
		t.Fatalf("FindOrphanedDatabasesCity() error = %v", err)
	}
	if len(orphans) != 0 {
		t.Errorf("got %d orphans, want 0 for empty city", len(orphans))
	}
}

func TestRemoveDatabaseCity(t *testing.T) {
	cityPath := t.TempDir()
	t.Setenv("GC_DOLT", "skip")

	dataDir := filepath.Join(cityPath, ".gc", "dolt-data")
	createFakeDB(t, dataDir, "orphan")

	// Verify the database exists.
	if _, err := os.Stat(filepath.Join(dataDir, "orphan", ".dolt")); err != nil {
		t.Fatalf("setup failed: orphan DB not created: %v", err)
	}

	// Server is not running, so force=true skips SQL checks.
	if err := RemoveDatabaseCity(cityPath, "orphan", true); err != nil {
		t.Fatalf("RemoveDatabaseCity() error = %v", err)
	}

	// Verify removed.
	if _, err := os.Stat(filepath.Join(dataDir, "orphan")); !os.IsNotExist(err) {
		t.Error("orphan database directory still exists after removal")
	}
}

func TestRemoveDatabaseCity_NotFound(t *testing.T) {
	cityPath := t.TempDir()
	t.Setenv("GC_DOLT", "skip")

	if err := os.MkdirAll(filepath.Join(cityPath, ".gc", "dolt-data"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := RemoveDatabaseCity(cityPath, "nonexistent", true)
	if err == nil {
		t.Fatal("RemoveDatabaseCity() should fail for nonexistent database")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

// ── Backoff tracker tests ────────────────────────────────────────────

func TestBackoffLoosenedParams(t *testing.T) {
	if doltBackoffWindow != 10*time.Minute {
		t.Errorf("doltBackoffWindow = %v, want 10m", doltBackoffWindow)
	}
	if doltBackoffMaxStarts != 5 {
		t.Errorf("doltBackoffMaxStarts = %d, want 5", doltBackoffMaxStarts)
	}
}

func TestBackoffReset(t *testing.T) {
	tracker := &doltBackoffTracker{
		starts: make(map[string][]time.Time),
	}
	city := "/tmp/test-reset"
	now := time.Now()

	// Record some starts within the window.
	tracker.recordStart(city, now.Add(-5*time.Minute))
	tracker.recordStart(city, now.Add(-3*time.Minute))

	// Should have 2 starts.
	tracker.mu.Lock()
	count := len(tracker.starts[city])
	tracker.mu.Unlock()
	if count != 2 {
		t.Fatalf("expected 2 starts, got %d", count)
	}

	// resetIfHealthy should NOT clear because starts are within the window.
	tracker.resetIfHealthy(city, now)
	tracker.mu.Lock()
	count = len(tracker.starts[city])
	tracker.mu.Unlock()
	if count != 2 {
		t.Fatalf("expected 2 starts still present, got %d", count)
	}

	// Advance past the backoff window — all starts are now expired.
	future := now.Add(doltBackoffWindow + time.Second)
	tracker.resetIfHealthy(city, future)
	tracker.mu.Lock()
	_, exists := tracker.starts[city]
	tracker.mu.Unlock()
	if exists {
		t.Error("expected city entry to be deleted after resetIfHealthy past window")
	}
}

func TestStartCityServer_LookPath(t *testing.T) {
	// Use an empty PATH so dolt won't be found.
	t.Setenv("PATH", t.TempDir())

	config := &Config{
		TownRoot: t.TempDir(),
		Port:     39999,
		DataDir:  filepath.Join(t.TempDir(), "data"),
		LogFile:  filepath.Join(t.TempDir(), "dolt.log"),
	}

	err := startCityServer(config, os.Stderr)
	if err == nil {
		t.Fatal("startCityServer should fail when dolt is not in PATH")
	}
	if !strings.Contains(err.Error(), "dolt not found") {
		t.Errorf("error should mention 'dolt not found', got: %v", err)
	}

	// Verify exec.LookPath actually fails with the empty PATH.
	if _, lookErr := exec.LookPath("dolt"); lookErr == nil {
		t.Skip("dolt unexpectedly found in empty PATH, cannot verify LookPath check")
	}
}

func TestQuarantineCorruptDB(t *testing.T) {
	// Verify the quarantine logic by calling startCityServer with a real
	// dolt binary (or failing fast at LookPath). We test quarantine as a
	// side effect: startCityServer creates data dir, quarantines, then
	// either proceeds (dolt installed) or fails at LookPath/Start.
	dataDir := t.TempDir()

	// Create a valid DB dir with noms/manifest.
	validDB := filepath.Join(dataDir, "valid-db")
	if err := os.MkdirAll(filepath.Join(validDB, ".dolt", "noms"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(validDB, ".dolt", "noms", "manifest"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a corrupt DB dir with .dolt but NO noms/manifest.
	corruptDB := filepath.Join(dataDir, "corrupt-db")
	if err := os.MkdirAll(filepath.Join(corruptDB, ".dolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a non-dolt dir (should be left alone).
	plainDir := filepath.Join(dataDir, "plain-dir")
	if err := os.MkdirAll(plainDir, 0o755); err != nil {
		t.Fatal(err)
	}

	logDir := t.TempDir()
	config := &Config{
		TownRoot: t.TempDir(),
		Port:     39999,
		DataDir:  dataDir,
		LogFile:  filepath.Join(logDir, "dolt.log"),
	}

	// startCityServer will quarantine corrupt DBs then either fail at
	// LookPath (no dolt) or at cmd.Start / port bind. We only care about
	// the quarantine side effect.
	_ = startCityServer(config, os.Stderr)

	// Valid DB should still exist.
	if _, err := os.Stat(filepath.Join(validDB, ".dolt", "noms", "manifest")); err != nil {
		t.Errorf("valid DB was incorrectly removed: %v", err)
	}

	// Corrupt DB should be removed.
	if _, err := os.Stat(corruptDB); !os.IsNotExist(err) {
		t.Error("corrupt DB was not quarantined (still exists)")
	}

	// Plain dir should still exist.
	if _, err := os.Stat(plainDir); err != nil {
		t.Errorf("plain dir was incorrectly removed: %v", err)
	}
}

// ── City wrapper tests ───────────────────────────────────────────────

func TestRigDatabaseDirCity(t *testing.T) {
	cityPath := "/home/user/my-city"
	got := RigDatabaseDirCity(cityPath, "frontend")
	want := filepath.Join(cityPath, ".gc", "dolt-data", "frontend")
	if got != want {
		t.Errorf("RigDatabaseDirCity() = %q, want %q", got, want)
	}
}

func TestFindRigBeadsDirCity_HQ(t *testing.T) {
	cityPath := t.TempDir()

	// Create HQ .beads with metadata pointing to "hq" database.
	writeMetadataDB(t, filepath.Join(cityPath, ".beads"), "hq")

	got := FindRigBeadsDirCity(cityPath, "hq")
	want := filepath.Join(cityPath, ".beads")
	if got != want {
		t.Errorf("FindRigBeadsDirCity(hq) = %q, want %q", got, want)
	}
}

func TestFindRigBeadsDirCity_Rig(t *testing.T) {
	cityPath := t.TempDir()

	// Create a rig dir with .beads metadata.
	writeMetadataDB(t, filepath.Join(cityPath, "frontend", ".beads"), "fe")

	got := FindRigBeadsDirCity(cityPath, "fe")
	want := filepath.Join(cityPath, "frontend", ".beads")
	if got != want {
		t.Errorf("FindRigBeadsDirCity(fe) = %q, want %q", got, want)
	}
}

func TestFindRigBeadsDirCity_Route(t *testing.T) {
	cityPath := t.TempDir()

	// Create a rig referenced via routes.jsonl.
	writeMetadataDB(t, filepath.Join(cityPath, ".beads"), "hq")
	writeMetadataDB(t, filepath.Join(cityPath, "api-service", ".beads"), "api")

	// Write route pointing to the rig.
	routesDir := filepath.Join(cityPath, ".beads")
	routeLine := `{"path":"api-service"}` + "\n"
	if err := os.WriteFile(filepath.Join(routesDir, "routes.jsonl"), []byte(routeLine), 0o644); err != nil {
		t.Fatal(err)
	}

	got := FindRigBeadsDirCity(cityPath, "api")
	want := filepath.Join(cityPath, "api-service", ".beads")
	if got != want {
		t.Errorf("FindRigBeadsDirCity(api) = %q, want %q", got, want)
	}
}

func TestFindRigBeadsDirCity_Fallback(t *testing.T) {
	cityPath := t.TempDir()

	// No matching database — should fall back to city-root .beads.
	got := FindRigBeadsDirCity(cityPath, "nonexistent")
	want := filepath.Join(cityPath, ".beads")
	if got != want {
		t.Errorf("FindRigBeadsDirCity(nonexistent) = %q, want %q", got, want)
	}
}

func TestCheckReadOnlyCity_NoDatabases(t *testing.T) {
	cityPath := t.TempDir()
	t.Setenv("GC_DOLT", "skip")

	// No databases → should return (false, nil), not probe.
	readOnly, err := CheckReadOnlyCity(cityPath)
	if err != nil {
		t.Fatalf("CheckReadOnlyCity() error = %v", err)
	}
	if readOnly {
		t.Error("CheckReadOnlyCity() = true with no databases, want false")
	}
}

func TestSyncDatabasesCity_NoDatabases(t *testing.T) {
	cityPath := t.TempDir()
	t.Setenv("GC_DOLT", "skip")

	results := SyncDatabasesCity(cityPath, SyncOptions{})
	if len(results) != 0 {
		t.Errorf("SyncDatabasesCity() returned %d results, want 0 for empty city", len(results))
	}
}
