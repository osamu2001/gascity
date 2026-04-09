---
title: "Exec Session Provider"
---

Gas City's exec session provider delegates each `runtime.Provider` operation
to a user-supplied script. This lets any terminal multiplexer, local session
manager, or remote control plane back Gas City without adding a Go provider to
this repository.

The Go-side contract lives in:

- [`internal/runtime/exec/exec.go`](../../internal/runtime/exec/exec.go)
- [`internal/runtime/exec/json.go`](../../internal/runtime/exec/json.go)

This document describes the wire contract those files implement today.

## Usage

Set `GC_SESSION` to `exec:<script>`:

```bash
# Absolute path
export GC_SESSION=exec:/path/to/gc-session-zmx

# PATH lookup
export GC_SESSION=exec:gc-session-zmx
```

## Calling Convention

The script receives the operation name as its first argument:

```text
<script> <operation> <session-name> [args...]
```

Gas City execs the script directly. There is no intermediate shell layer.

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | Failure (`stderr` should explain why) |
| `2` | Unsupported/unknown operation |

Exit code `2` is the forward-compatibility mechanism. Gas City's exec provider
treats it as a successful no-op, which lets older scripts survive newly added
operations.

## Core Operations

| Operation | Invocation | Stdin | Stdout |
|-----------|------------|-------|--------|
| `start` | `script start <name>` | JSON config | — |
| `stop` | `script stop <name>` | — | — |
| `interrupt` | `script interrupt <name>` | — | — |
| `is-running` | `script is-running <name>` | — | `true` or `false` |
| `attach` | `script attach <name>` | tty passthrough | tty passthrough |
| `process-alive` | `script process-alive <name>` | process names (1 per line) | `true` or `false` |
| `nudge` | `script nudge <name>` | message text | — |
| `set-meta` | `script set-meta <name> <key>` | value | — |
| `get-meta` | `script get-meta <name> <key>` | — | value or empty |
| `remove-meta` | `script remove-meta <name> <key>` | — | — |
| `peek` | `script peek <name> <lines>` | — | captured text |
| `list-running` | `script list-running <prefix>` | — | one name per line |
| `get-last-activity` | `script get-last-activity <name>` | — | RFC3339 timestamp or empty |
| `send-keys` | `script send-keys <name> <key>...` | — | — |
| `clear-scrollback` | `script clear-scrollback <name>` | — | — |
| `copy-to` | `script copy-to <name> <src> <rel-dst>` | — | — |

### Script-Specific Extensions

Some shipped scripts expose extra helper operations that are not part of the
core `runtime.Provider` interface. Today the main example is `copy-from`,
which some integration tests use to read a file from a remote/session-local
filesystem. Treat these as script-specific extensions, not as guaranteed parts
of the exec-provider contract.

## Start Config JSON

`start` receives one JSON object on stdin. All fields are optional.

```json
{
  "work_dir": "/path/to/working/directory",
  "command": "claude --dangerously-skip-permissions",
  "env": {"GC_AGENT": "mayor", "GC_CITY": "/home/user/bright-lights"},
  "process_names": ["claude", "node"],
  "nudge": "initial prompt text",
  "ready_prompt_prefix": "> ",
  "ready_delay_ms": 1000,
  "pre_start": ["mkdir -p /workspace"],
  "session_setup": ["./scripts/install-hooks.sh"],
  "session_setup_script": "/path/to/setup-script.sh",
  "session_live": ["./scripts/tmux-theme.sh"],
  "pack_overlay_dirs": ["/path/to/pack-overlay"],
  "overlay_dir": "/path/to/agent-overlay",
  "copy_files": [
    {"src": "/tmp/settings.json", "rel_dst": ".gc/settings.json"}
  ]
}
```

### Field Semantics

- `work_dir`: working directory for the session process.
- `command`: shell command (or equivalent backend command string) to start.
- `env`: extra environment variables to inject before startup.
- `process_names`: process names used by Gas City for liveness/readiness logic.
- `nudge`: initial text to type after the session is ready.
- `ready_prompt_prefix`: readiness hint surfaced to the script for adapters
  that want to participate in startup orchestration.
- `ready_delay_ms`: fixed readiness delay hint, also passed through as JSON.
- `pre_start`: host/target filesystem preparation commands that run before
  session creation. These should be treated as fatal when they fail.
- `session_setup`: post-start setup commands. Warnings are preferred over
  hard failure here so the session stays usable.
- `session_setup_script`: post-start script path on the controller filesystem.
- `session_live`: idempotent post-start commands for theming or live
  reconfiguration. Gas City does not currently send a separate re-apply
  operation for exec providers, but the field is present in the startup JSON.
- `pack_overlay_dirs`: lower-priority overlay directories contributed by packs.
- `overlay_dir`: higher-priority overlay directory for the specific agent/rig.
- `copy_files`: explicit file/directory copies to stage into the workdir before
  the main command starts.

## Conventions

- `stdin` carries structured values. `start`, `nudge`, `set-meta`, and
  `process-alive` all rely on stdin instead of shell-quoted arguments.
- `stdout` is reserved for machine-readable results only.
- `stderr` should contain diagnostics and warnings only.
- `stop` must be idempotent.
- `interrupt` and `nudge` should be best-effort. Missing sessions should not
  turn these into hard failures.
- `get-meta` returns empty stdout when the key is unset.
- `get-last-activity` returns empty stdout when unsupported or unknown.
- `clear-scrollback` or other optional operations should exit `2` when not
  supported.

## Writing Your Own Script

1. Start from a shipped script in `contrib/session-scripts/`.
2. Treat the Go files above as the source of truth for current JSON fields and
   operations.
3. Implement only the operations your backend supports.
4. Return exit `2` for unsupported ones.
5. Verify the script with `GC_SESSION=exec:/path/to/script gc start <city>`.

### Minimal Script

```bash
#!/bin/sh
op="$1"
name="$2"

case "$op" in
  start)
    cat > /dev/null
    my-backend start "$name"
    ;;
  stop)
    my-backend stop "$name" 2>/dev/null || true
    ;;
  is-running)
    if my-backend status "$name" >/dev/null 2>&1; then
      echo true
    else
      echo false
    fi
    ;;
  *)
    exit 2
    ;;
esac
```

## Environment Variables

Scripts may use `GC_EXEC_STATE_DIR` for sidecar state such as metadata files,
wrapper scripts, or remembered workdirs. If unset, scripts should choose a
reasonable default under `$TMPDIR` or `/tmp`.

## Shipped Scripts

- `contrib/session-scripts/gc-session-screen`: GNU screen backend.
- `contrib/session-scripts/gc-session-k8s`: reference exec-script K8s backend
  (prefer the native `k8s` provider when possible).
- `contrib/session-scripts/gc-session-zmx`: local zmx backend that maps Gas
  City's exec-provider contract onto zmx CLI commands.
