# Session Scripts

Community-maintained session provider scripts for Gas City's exec session
provider. These are real implementations we ship, but they have external
dependencies and aren't the same support tier as `gc` itself.

See [Exec Session Provider](../../docs/reference/exec-session-provider.md)
for the protocol specification.

## Scripts

### gc-session-zmx

Local `zmx` backend for the exec session provider. This adapter keeps Gas City
on its existing Go-side exec provider and translates each session operation to
the `zmx` CLI.

**Dependencies:** `zmx`, `jq`, `bash`

**Required zmx commands:** `run`, `kill`, `attach`, `history`, `list --short`,
`info --json`, `send`, `send-keys`

**Usage:**

```bash
export GC_SESSION=exec:/path/to/contrib/session-scripts/gc-session-zmx
gc start my-city
```

**Important:** Gas City ships the adapter, but `zmx` must supply the machine-
readable CLI surface. If your local `zmx` build does not yet expose
`info --json`, `send`, and `send-keys`, the adapter will not be fully usable.

**Current gaps:** `clear-scrollback` is intentionally unsupported in v1, and
`copy-to` / `copy-from` are implemented as local filesystem helpers rather than
native zmx transport commands.

### gc-session-screen

GNU screen backend. Creates screen sessions, sends keystrokes for nudge
and interrupt, captures output via `hardcopy`, and stores metadata in
sidecar files.

**Dependencies:** `screen`, `jq`, `bash`

**Usage:**

```bash
export GC_SESSION=exec:/path/to/contrib/session-scripts/gc-session-screen
gc start my-city
```

**Parity with tmux provider:** The script implements the full 13-operation
protocol but does not yet include Gas Town theming (status bar colors,
role emoji, keybindings) or lifecycle features (remain-on-exit, auto-respawn,
zombie detection). See comments in the script header for the full gap list.

### gc-session-k8s (reference — prefer native provider)

Kubernetes backend via exec protocol. Runs each agent session as a K8s
Pod using `kubectl` subprocesses. This script is now a **reference
implementation** — prefer the native K8s provider (`GC_SESSION=k8s` or
`[session] provider = "k8s"`) which uses client-go for direct API calls
and eliminates all subprocess overhead. Pod manifests are compatible
between the two for mixed-mode migration.

**Dependencies:** `kubectl`, `jq`, `bash`

**Usage (legacy):**

```bash
export GC_SESSION=exec:/path/to/contrib/session-scripts/gc-session-k8s
export GC_K8S_IMAGE=myregistry/gc-agent:latest
gc start my-city
```

**Native provider (recommended):**

```bash
export GC_SESSION=k8s
export GC_K8S_IMAGE=myregistry/gc-agent:latest
gc start my-city
```

See [docs/k8s-guide.md](../../docs/k8s-guide.md) for the full setup guide,
K8s manifests, and agent Dockerfile.
