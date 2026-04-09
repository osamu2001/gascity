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
    shift 2
    if [ "$#" -eq 0 ]; then
      set -- "${SHELL:-/bin/sh}"
    fi
    cmd="$1"
    if [ -f "$STATE/$name.json" ]; then
      echo "session \"$name\" already exists" >&2
      exit 1
    fi
    printf '%s' "$PWD" > "$STATE/$name.cwd"
    env | sort > "$STATE/$name.env"
    printf '%s\n' "$@" > "$STATE/$name.argv"
    printf '%s' "$*" > "$STATE/$name.command"
    printf 'startup line\n' > "$STATE/$name.history"
    write_session_json "$name" "${FAKE_ZMX_INFO_PID:-4242}" "$cmd"
    if [ "${FAKE_ZMX_EXEC_RUN:-}" = "1" ]; then
      "$@" >/dev/null 2>&1
    fi
    if [ "${FAKE_ZMX_BACKGROUND_HOLD_STDIO:-}" = "1" ]; then
      sleep 5 &
    fi
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

type fakeZMXHarness struct {
	tmpDir       string
	zmxBinDir    string
	zmxStateDir  string
	execStateDir string
}

func newFakeZMXHarness(t *testing.T) fakeZMXHarness {
	t.Helper()
	tmpDir := t.TempDir()
	zmxBinDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(zmxBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFakeZMX(t, zmxBinDir)
	return fakeZMXHarness{
		tmpDir:       tmpDir,
		zmxBinDir:    zmxBinDir,
		zmxStateDir:  filepath.Join(tmpDir, "zmx-state"),
		execStateDir: filepath.Join(tmpDir, "exec-state"),
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

func (h fakeZMXHarness) env(extra ...string) []string {
	return append(scriptEnv(h.zmxBinDir, h.zmxStateDir, h.execStateDir), extra...)
}

func (h fakeZMXHarness) setProviderEnv(t *testing.T, extra ...string) {
	t.Helper()
	t.Setenv("PATH", h.zmxBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_ZMX_STATE_DIR", h.zmxStateDir)
	t.Setenv("GC_EXEC_STATE_DIR", h.execStateDir)
	t.Setenv("SHELL", "/bin/sh")
	for _, kv := range extra {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			t.Fatalf("invalid env entry %q", kv)
		}
		t.Setenv(key, value)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
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
	h := newFakeZMXHarness(t)
	workDir := filepath.Join(h.tmpDir, "workdir")
	packOverlay := filepath.Join(h.tmpDir, "pack-overlay")
	overlayDir := filepath.Join(h.tmpDir, "overlay")
	copyDir := filepath.Join(h.tmpDir, "copy-dir")
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
	singleFile := filepath.Join(h.tmpDir, "single.txt")
	if err := os.WriteFile(singleFile, []byte("single"), 0o644); err != nil {
		t.Fatal(err)
	}
	sameDstFile := filepath.Join(workDir, ".opencode", "plugins", "gascity.js")
	if err := os.MkdirAll(filepath.Dir(sameDstFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sameDstFile, []byte("same-path"), 0o644); err != nil {
		t.Fatal(err)
	}
	setupScript := filepath.Join(h.tmpDir, "session-setup.sh")
	if err := os.WriteFile(setupScript, []byte("#!/bin/sh\nprintf '%s' \"$GC_SESSION\" > session-setup-script.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := map[string]any{
		"work_dir":             workDir,
		"command":              "agent-command --flag",
		"prompt_suffix":        "'You are the mayor.'",
		"prompt_flag":          "--prompt",
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
			{"src": sameDstFile, "rel_dst": ".opencode/plugins/gascity.js"},
		},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	env := h.env()
	stdout, stderr, code := runAdapter(t, env, data, "start", "gc-city-mayor")
	if code != 0 {
		t.Fatalf("start exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("start stderr = %q, want empty", stderr)
	}

	got := mustReadFile(t, filepath.Join(h.zmxStateDir, "gc-city-mayor.cwd"))
	if string(got) != workDir {
		t.Fatalf("zmx run cwd = %q, want %q", got, workDir)
	}

	got = mustReadFile(t, filepath.Join(h.zmxStateDir, "gc-city-mayor.argv"))
	if got != "sh\n-c\nagent-command --flag --prompt 'You are the mayor.'\n" {
		t.Fatalf("zmx run argv = %q", got)
	}

	got = mustReadFile(t, filepath.Join(h.zmxStateDir, "gc-city-mayor.env"))
	if !strings.Contains(got, "GC_AGENT=mayor") {
		t.Fatalf("zmx run env missing GC_AGENT: %s", got)
	}

	for path, want := range map[string]string{
		filepath.Join(workDir, "shared.txt"):                         "overlay",
		filepath.Join(workDir, "copied.txt"):                         "copy-dir",
		filepath.Join(workDir, "nested", "single.txt"):               "single",
		filepath.Join(workDir, ".opencode", "plugins", "gascity.js"): "same-path",
		filepath.Join(workDir, "pre-start.txt"):                      "mayor",
		filepath.Join(workDir, "session-setup.txt"):                  "gc-city-mayor",
		filepath.Join(workDir, "session-setup-script.txt"):           "gc-city-mayor",
		filepath.Join(workDir, "session-live.txt"):                   "live:gc-city-mayor",
		filepath.Join(h.execStateDir, "gc-city-mayor.work_dir"):      workDir,
	} {
		got = mustReadFile(t, path)
		if got != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}

	got = mustReadFile(t, filepath.Join(h.zmxStateDir, "gc-city-mayor.send"))
	if got != "inspect inbox\r" {
		t.Fatalf("nudge payload = %q", got)
	}

	extraSrc := filepath.Join(h.tmpDir, "extra.txt")
	if err := os.WriteFile(extraSrc, []byte("extra"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code = runAdapter(t, env, nil, "copy-to", "gc-city-mayor", extraSrc, "copied-later/extra.txt")
	if code != 0 {
		t.Fatalf("copy-to exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	got = mustReadFile(t, filepath.Join(workDir, "copied-later", "extra.txt"))
	if got != "extra" {
		t.Fatalf("copied-later/extra.txt = %q", got)
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
	h := newFakeZMXHarness(t)
	env := h.env()

	proc := exec.Command("sh", "-c", "sleep 30 & wait")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = proc.Process.Kill()
		_, _ = proc.Process.Wait()
	}()

	env = append(env, "FAKE_ZMX_INFO_PID="+strconv.Itoa(proc.Process.Pid))
	cfg := map[string]any{"work_dir": filepath.Join(h.tmpDir, "workdir"), "command": "dummy"}
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

func TestGCSessionZMXStartPreservesShellCommandSemantics(t *testing.T) {
	h := newFakeZMXHarness(t)
	workDir := filepath.Join(h.tmpDir, "workdir")
	env := h.env("FAKE_ZMX_EXEC_RUN=1")

	cfg := map[string]any{
		"work_dir": workDir,
		"command":  "printf shell > shell.txt && printf second > second.txt",
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	if _, stderr, code := runAdapter(t, env, data, "start", "gc-city-shell"); code != 0 {
		t.Fatalf("start exit=%d stderr=%q", code, stderr)
	}

	got := mustReadFile(t, filepath.Join(h.zmxStateDir, "gc-city-shell.argv"))
	if got != "sh\n-c\nprintf shell > shell.txt && printf second > second.txt\n" {
		t.Fatalf("zmx run argv = %q", got)
	}

	for path, want := range map[string]string{
		filepath.Join(workDir, "shell.txt"):  "shell",
		filepath.Join(workDir, "second.txt"): "second",
	} {
		got = mustReadFile(t, path)
		if got != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestGCSessionZMXExecProviderIntegration(t *testing.T) {
	h := newFakeZMXHarness(t)
	h.setProviderEnv(t)

	p := sessionexec.NewProvider(zmxScriptPath(t))
	workDir := filepath.Join(h.tmpDir, "workdir")
	name := "gc-city-mayor"

	if err := p.Start(context.Background(), name, runtimepkg.Config{
		WorkDir:      workDir,
		Command:      "agent-command",
		PromptSuffix: "'You are the mayor.'",
		PromptFlag:   "--prompt",
		Env:          map[string]string{"GC_AGENT": "mayor"},
		Nudge:        "boot prompt",
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	got := mustReadFile(t, filepath.Join(h.zmxStateDir, name+".argv"))
	if got != "sh\n-c\nagent-command --prompt 'You are the mayor.'\n" {
		t.Fatalf("zmx argv = %q", got)
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

	got = mustReadFile(t, filepath.Join(h.zmxStateDir, name+".send"))
	if got != "follow up\r" {
		t.Fatalf("nudge payload = %q", got)
	}

	got = mustReadFile(t, filepath.Join(h.zmxStateDir, name+".keys"))
	if strings.TrimSpace(got) != "Down Enter" {
		t.Fatalf("keys payload = %q", got)
	}

	if err := p.Stop(name); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if p.IsRunning(name) {
		t.Fatal("IsRunning returned true after Stop")
	}
}

func TestGCSessionZMXExecProviderStartDetachesZMXRunStdio(t *testing.T) {
	h := newFakeZMXHarness(t)
	h.setProviderEnv(t, "FAKE_ZMX_BACKGROUND_HOLD_STDIO=1")

	p := sessionexec.NewProvider(zmxScriptPath(t))
	if err := p.Start(context.Background(), "gc-city-detach", runtimepkg.Config{
		WorkDir: filepath.Join(h.tmpDir, "workdir"),
		Command: "agent-command",
	}); err != nil {
		t.Fatalf("Start with background stdio holder: %v", err)
	}
}
