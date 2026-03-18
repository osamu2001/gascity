# Dashboard

The dashboard is a web UI compiled into the `gc` binary for monitoring
convoys, agents, mail, rigs, sessions, and events in real time.

## Prerequisites

The dashboard requires the GC API server. Add an `[api]` section to
`city.toml`:

```toml
[api]
port = 4280
```

Without this, the API server won't start and the dashboard has no data
source. The API server starts automatically with `gc start` when the
`[api]` section is present.

## Starting the dashboard

```
gc dashboard serve                     # Start on default port (8080)
gc dashboard serve --port 3000         # Start on custom port
gc dashboard serve --api http://127.0.0.1:4280  # Explicit API URL
```

The `--api` flag is required. It points to the GC API server URL
(matching the `[api]` port in city.toml).

## Features

The dashboard provides:

- **Convoys** — progress tracking, tracked issues, create new convoys
- **Crew** — named worker status with activity detection
- **Polecats** — ephemeral worker activity and work status
- **Activity timeline** — categorized event feed with filters
- **Mail** — inbox with threading, compose, and all-traffic view
- **Merge queue** — open PRs with CI and mergeable status
- **Escalations** — priority-colored escalation list
- **Ready work** — items available for assignment
- **Health** — system heartbeat and agent counts
- **Issues** — backlog with priority, age, labels, assignment
- **Command palette** (Cmd+K) — execute gc commands from the browser

Real-time updates via SSE (Server-Sent Events) from the API server.
