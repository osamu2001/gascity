package sessionscripts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	runtimepkg "github.com/gastownhall/gascity/internal/runtime"
	sessionexec "github.com/gastownhall/gascity/internal/runtime/exec"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func zmxScriptPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "contrib", "session-scripts", "gc-session-zmx")
}

func writeFakeZMX(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "zmx")
	const script = `#!/usr/bin/env bash
set -euo pipefail

STATE="${FAKE_ZMX_STATE_DIR:?}"
mkdir -p "$STATE"

write_session_json() {
  local name="$1"
  local pid="${2:-4242}"
  local cmd="${3:-}"
  local last="${FAKE_ZMX_LAST_ACTIVITY:-2026-04-09T01:02:03Z}"
  cat > "$STATE/$name.json" <<EOFJSON
{"name":"$name","pid":$pid,"clients":0,"cwd":"$PWD","cmd":"$cmd","created_at":"2026-04-09T00:00:00Z","task_ended_at":"","task_exit_code":0,"is_error":false,"error":"","last_activity_at":"$last"}
EOFJSON
}

append_history() {
  local name="$1"
  local text="$2"
  printf '%s' "$text" >> "$STATE/$name.history"
}

op="${1:-}"
case "$op" in
  run)
    name="${2:?missing name}"
    cmd="${3:-${SHELL:-/bin/sh}}"
    if [ -f "$STATE/$name.json" ]; then
      echo "session \"$name\" already exists" >&2
      exit 1
    fi
    printf '%s' "$PWD" > "$STATE/$name.cwd"
    env | sort > "$STATE/$name.env"
    printf '%s' "$cmd" > "$STATE/$name.command"
    printf 'startup line\n' > "$STATE/$name.history"
    write_session_json "$name" "${FAKE_ZMX_INFO_PID:-4242}" "$cmd"
    ;;
  info)
    name="${2:?missing name}"
    [ "${3:-}" = "--json" ] || exit 1
    [ -f "$STATE/$name.json" ] || {
      echo "session \"$name\" not found" >&2
      exit 1
    }
    cat "$STATE/$name.json"
    ;;
  send)
    name="${2:?missing name}"
    [ -f "$STATE/$name.json" ] || exit 1
    cat > "$STATE/$name.send"
    cat "$STATE/$name.send" >> "$STATE/$name.history"
    ;;
  send-keys)
    name="${2:?missing name}"
    shift 2
    [ -f "$STATE/$name.json" ] || exit 1
    printf '%s\n' "$*" > "$STATE/$name.keys"
    append_history "$name" "$*"
    ;;
  kill)
    name="${2:?missing name}"
    rm -f "$STATE/$name.json" "$STATE/$name.cwd" "$STATE/$name.env" "$STATE/$name.command" "$STATE/$name.history" "$STATE/$name.send" "$STATE/$name.keys"
    ;;
  list)
    flag="${2:-}"
    case "$flag" in
      --short)
        for file in "$STATE"/*.json; do
          [ -f "$file" ] || continue
          basename "$file" .json
        done
        ;;
      --json)
        files=("$STATE"/*.json)
        if [ "${files[0]}" = "$STATE/*.json" ]; then
          echo '[]'
        else
          jq -s '.' "${files[@]}"
        fi
        ;;
      *)
        exit 1
        ;;
    esac
    ;;
  history)
    name="${2:?missing name}"
    [ -f "$STATE/$name.history" ] || exit 1
    cat "$STATE/$name.history"
    ;;
  attach)
    name="${2:?missing name}"
    echo "attach:$name" > "$STATE/$name.attach"
    ;;
  *)
    exit 1
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func scriptEnv(pathDir, zmxStateDir, execStateDir string) []string {
	env := os.Environ()
	env = append(env,
		"PATH="+pathDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_ZMX_STATE_DIR="+zmxStateDir,
		"GC_EXEC_STATE_DIR="+execStateDir,
		"SHELL=/bin/sh",
	)
	return env
}

func runAdapter(t *testing.T, env []string, stdin []byte, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(zmxScriptPath(t), args...)
	cmd.Env = env
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), stderr.String(), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("runAdapter(%v): %v", args, err)
	}
	return stdout.String(), stderr.String(), exitErr.ExitCode()
}

func TestGCSessionZMXStartStagesWorkDirAndSupportsCopyOps(t *testing.T) {
	tmp := t.TempDir()
	zmxBinDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(zmxBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeZMX(t, zmxBinDir)

	zmxStateDir := filepath.Join(tmp, "zmx-state")
	execStateDir := filepath.Join(tmp, "exec-state")
	workDir := filepath.Join(tmp, "workdir")
	packOverlay := filepath.Join(tmp, "pack-overlay")
	overlayDir := filepath.Join(tmp, "overlay")
	copyDir := filepath.Join(tmp, "copy-dir")
	if err := os.MkdirAll(packOverlay, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(copyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packOverlay, "shared.txt"), []byte("pack"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "shared.txt"), []byte("overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copyDir, "copied.txt"), []byte("copy-dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	singleFile := filepath.Join(tmp, "single.txt")
	if err := os.WriteFile(singleFile, []byte("single"), 0o644); err != nil {
		t.Fatal(err)
	}
	setupScript := filepath.Join(tmp, "session-setup.sh")
	if err := os.WriteFile(setupScript, []byte("#!/bin/sh\nprintf '%s' \"$GC_SESSION\" > session-setup-script.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := map[string]any{
		"work_dir":             workDir,
		"command":              "agent-command --flag",
		"env":                  map[string]string{"GC_AGENT": "mayor", "EXTRA_ENV": "hello"},
		"nudge":                "inspect inbox",
		"pre_start":            []string{"printf '%s' \"$GC_AGENT\" > pre-start.txt"},
		"session_setup":        []string{"printf '%s' \"$GC_SESSION\" > session-setup.txt"},
		"session_setup_script": setupScript,
		"session_live":         []string{"printf 'live:%s' \"$GC_SESSION\" > session-live.txt"},
		"pack_overlay_dirs":    []string{packOverlay},
		"overlay_dir":          overlayDir,
		"copy_files": []map[string]string{
			{"src": copyDir},
			{"src": singleFile, "rel_dst": "nested/single.txt"},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	env := scriptEnv(zmxBinDir, zmxStateDir, execStateDir)
	stdout, stderr, code := runAdapter(t, env, data, "start", "gc-city-mayor")
	if code != 0 {
		t.Fatalf("start exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("start stderr = %q, want empty", stderr)
	}

	got, err := os.ReadFile(filepath.Join(zmxStateDir, "gc-city-mayor.cwd"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != workDir {
		t.Fatalf("zmx run cwd = %q, want %q", string(got), workDir)
	}

	got, err = os.ReadFile(filepath.Join(zmxStateDir, "gc-city-mayor.command"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "agent-command --flag" {
		t.Fatalf("zmx run command = %q", string(got))
	}

	got, err = os.ReadFile(filepath.Join(zmxStateDir, "gc-city-mayor.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "GC_AGENT=mayor") {
		t.Fatalf("zmx run env missing GC_AGENT: %s", string(got))
	}

	for path, want := range map[string]string{
		filepath.Join(workDir, "shared.txt"):                  "overlay",
		filepath.Join(workDir, "copied.txt"):                  "copy-dir",
		filepath.Join(workDir, "nested", "single.txt"):        "single",
		filepath.Join(workDir, "pre-start.txt"):               "mayor",
		filepath.Join(workDir, "session-setup.txt"):           "gc-city-mayor",
		filepath.Join(workDir, "session-setup-script.txt"):    "gc-city-mayor",
		filepath.Join(workDir, "session-live.txt"):            "live:gc-city-mayor",
		filepath.Join(execStateDir, "gc-city-mayor.work_dir"): workDir,
	} {
		got, err = os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, string(got), want)
		}
	}

	got, err = os.ReadFile(filepath.Join(zmxStateDir, "gc-city-mayor.send"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "inspect inbox\r" {
		t.Fatalf("nudge payload = %q", string(got))
	}

	extraSrc := filepath.Join(tmp, "extra.txt")
	if err := os.WriteFile(extraSrc, []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runAdapter(t, env, nil, "copy-to", "gc-city-mayor", extraSrc, "copied-later/extra.txt")
	if code != 0 {
		t.Fatalf("copy-to exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	got, err = os.ReadFile(filepath.Join(workDir, "copied-later", "extra.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "extra" {
		t.Fatalf("copied-later/extra.txt = %q", string(got))
	}

	stdout, stderr, code = runAdapter(t, env, nil, "copy-from", "gc-city-mayor", "copied-later/extra.txt")
	if code != 0 {
		t.Fatalf("copy-from exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout != "extra" {
		t.Fatalf("copy-from stdout = %q", stdout)
	}
}

func TestGCSessionZMXProcessAliveUsesInfoPID(t *testing.T) {
	tmp := t.TempDir()
	zmxBinDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(zmxBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeZMX(t, zmxBinDir)

	zmxStateDir := filepath.Join(tmp, "zmx-state")
	execStateDir := filepath.Join(tmp, "exec-state")
	env := scriptEnv(zmxBinDir, zmxStateDir, execStateDir)

	proc := exec.Command("sh", "-c", "sleep 30 & wait")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = proc.Process.Kill()
		_, _ = proc.Process.Wait()
	}()

	env = append(env, "FAKE_ZMX_INFO_PID="+strconv.Itoa(proc.Process.Pid))
	cfg := map[string]any{"work_dir": filepath.Join(tmp, "workdir"), "command": "dummy"}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runAdapter(t, env, data, "start", "gc-city-worker"); code != 0 {
		t.Fatalf("start exit=%d stderr=%q", code, stderr)
	}

	stdout, stderr, code := runAdapter(t, env, []byte("sleep\n"), "process-alive", "gc-city-worker")
	if code != 0 {
		t.Fatalf("process-alive exit=%d stderr=%q", code, stderr)
	}
	if strings.TrimSpace(stdout) != "true" {
		t.Fatalf("process-alive stdout = %q, want true", stdout)
	}
}

func TestGCSessionZMXExecProviderIntegration(t *testing.T) {
	tmp := t.TempDir()
	zmxBinDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(zmxBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeZMX(t, zmxBinDir)

	zmxStateDir := filepath.Join(tmp, "zmx-state")
	execStateDir := filepath.Join(tmp, "exec-state")
	t.Setenv("PATH", zmxBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_ZMX_STATE_DIR", zmxStateDir)
	t.Setenv("GC_EXEC_STATE_DIR", execStateDir)
	t.Setenv("SHELL", "/bin/sh")

	p := sessionexec.NewProvider(zmxScriptPath(t))
	workDir := filepath.Join(tmp, "workdir")
	name := "gc-city-mayor"

	if err := p.Start(context.Background(), name, runtimepkg.Config{
		WorkDir: workDir,
		Command: "agent-command",
		Env:     map[string]string{"GC_AGENT": "mayor"},
		Nudge:   "boot prompt",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !p.IsRunning(name) {
		t.Fatal("IsRunning returned false after Start")
	}

	output, err := p.Peek(name, 0)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if !strings.Contains(output, "startup line") {
		t.Fatalf("Peek output = %q", output)
	}

	if err := p.Nudge(name, runtimepkg.TextContent("follow up")); err != nil {
		t.Fatalf("Nudge: %v", err)
	}
	if err := p.SendKeys(name, "Down", "Enter"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	names, err := p.ListRunning("gc-city-")
	if err != nil {
		t.Fatalf("ListRunning: %v", err)
	}
	if len(names) != 1 || names[0] != name {
		t.Fatalf("ListRunning = %v", names)
	}

	lastActivity, err := p.GetLastActivity(name)
	if err != nil {
		t.Fatalf("GetLastActivity: %v", err)
	}
	if lastActivity.IsZero() {
		t.Fatal("GetLastActivity returned zero time")
	}

	if err := p.SetMeta(name, "GC_SESSION_ID", "sess-123"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	meta, err := p.GetMeta(name, "GC_SESSION_ID")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if meta != "sess-123" {
		t.Fatalf("GetMeta = %q", meta)
	}
	if err := p.RemoveMeta(name, "GC_SESSION_ID"); err != nil {
		t.Fatalf("RemoveMeta: %v", err)
	}
	meta, err = p.GetMeta(name, "GC_SESSION_ID")
	if err != nil {
		t.Fatalf("GetMeta after remove: %v", err)
	}
	if meta != "" {
		t.Fatalf("GetMeta after remove = %q", meta)
	}

	if err := p.ClearScrollback(name); err != nil {
		t.Fatalf("ClearScrollback should treat exit 2 as success: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(zmxStateDir, name+".send"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "follow up\r" {
		t.Fatalf("nudge payload = %q", string(got))
	}

	got, err = os.ReadFile(filepath.Join(zmxStateDir, name+".keys"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != "Down Enter" {
		t.Fatalf("keys payload = %q", string(got))
	}

	if err := p.Stop(name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.IsRunning(name) {
		t.Fatal("IsRunning returned true after Stop")
	}
}
