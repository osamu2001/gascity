// Package dolt_test validates that the dolt pack's health.sh script
// completes within a bounded time even when the Dolt server is
// unresponsive. This is a regression guard for the hang reported in
// the atlas city (deacon patrol, 2026-04-17).
package dolt_test

import (
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Dir(filename)
}

// startDeadTCPListener accepts connections but never writes or reads —
// simulating a Dolt server whose goroutines are stuck before the MySQL
// handshake completes. Returns the port and a cleanup func.
func startDeadTCPListener(t *testing.T) (int, func()) {
	t.Helper()
	lc := net.ListenConfig{}
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			// Hold the connection open but send nothing. The Dolt
			// client blocks waiting for the server handshake, which
			// reproduces the hang mode the health script must tolerate.
			go func(c net.Conn) {
				<-stop
				_ = c.Close()
			}(c)
		}
	}()
	cleanup := func() {
		close(stop)
		_ = l.Close()
		wg.Wait()
	}
	return l.Addr().(*net.TCPAddr).Port, cleanup
}

// TestHealthScriptIsBounded runs commands/health.sh against a TCP
// listener that accepts connections but never speaks MySQL. The
// script used to hang indefinitely here because the per-database
// commit count ran `dolt log --oneline` directly against the on-disk
// database while the server held it open. The fix routes commit
// counts through SQL and wraps all dolt binary invocations in a
// timeout. We assert completion well under a minute.
func TestHealthScriptIsBounded(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("dolt not installed; skipping")
	}
	if _, errT := exec.LookPath("timeout"); errT != nil {
		if _, errG := exec.LookPath("gtimeout"); errG != nil {
			t.Skip("neither timeout nor gtimeout installed; skipping")
		}
	}

	port, cleanup := startDeadTCPListener(t)
	defer cleanup()

	cityPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityPath, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Minimal metadata file so metadata_files has a target.
	if err := os.WriteFile(filepath.Join(cityPath, ".beads", "metadata.json"),
		[]byte(`{"database":"dolt","backend":"dolt","dolt_database":"at"}`), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	root := repoRoot(t)
	script := filepath.Join(root, "commands", "health.sh")

	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(),
		"GC_CITY_PATH="+cityPath,
		"GC_PACK_DIR="+root,
		"GC_DOLT_HOST=127.0.0.1",
		"GC_DOLT_PORT="+strconv.Itoa(port),
		"GC_DOLT_USER=root",
		"GC_DOLT_PASSWORD=",
	)

	done := make(chan error, 1)
	stdout, stdoutW := io.Pipe()
	cmd.Stdout = stdoutW
	cmd.Stderr = stdoutW
	go func() {
		done <- cmd.Run()
		_ = stdoutW.Close()
	}()

	// Drain output so the pipe never fills.
	var buf strings.Builder
	go func() { _, _ = io.Copy(&buf, stdout) }()

	// The script has per-call 5s timeouts. Allow generous slack for
	// CI jitter, but fail hard well before "indefinite hang".
	const budget = 45 * time.Second
	select {
	case err := <-done:
		// Non-zero exit is expected here — the server isn't speaking
		// MySQL, so the health script should signal unhealthy. We
		// just assert that it exited (not hung) with some status.
		exitErr, ok := err.(*exec.ExitError)
		if err != nil && !ok {
			t.Fatalf("health.sh produced non-exit error: %v\n%s", err, buf.String())
		}
		if ok && exitErr.ExitCode() == 0 {
			t.Fatalf("health.sh exited 0 against unresponsive server; expected non-zero\n%s", buf.String())
		}
	case <-time.After(budget):
		_ = cmd.Process.Kill()
		t.Fatalf("health.sh exceeded %s budget against unresponsive server\n%s", budget, buf.String())
	}
}

// TestHealthScriptDoesNotInvokeDoltLog is a cheap regression guard
// for the specific bug: the old script ran `dolt log --oneline`
// locally against each on-disk database, which deadlocked with the
// running dolt sql-server. Routing commit counts through SQL is
// the only safe option. If a future refactor reintroduces `dolt log`,
// the hang comes back.
func TestHealthScriptDoesNotInvokeDoltLog(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "commands", "health.sh"))
	if err != nil {
		t.Fatalf("read health.sh: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Match `dolt log` as an executable call, not substrings
		// like `dolt_log` (the SQL table) or "dolt log" in prose.
		if strings.Contains(trimmed, "dolt log ") || strings.HasSuffix(trimmed, "dolt log") {
			t.Errorf("health.sh contains `dolt log` at non-comment line: %q\n"+
				"Commit counts must go through SQL (SELECT COUNT(*) FROM dolt_log) to avoid "+
				"deadlocking with the running sql-server.", trimmed)
		}
	}
}

