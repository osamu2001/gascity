# Rig Management

A rig is a project directory registered with the city. Agents can be
scoped to rigs via the `dir` field.

## Beads

Each rig has its own `.beads/` database with a unique prefix (e.g.
`hw-` for hello-world). To create or query beads for a rig, run `bd`
from the rig directory or pass `--dir`:

```
bd create "title" --dir /path/to/rig   # Create in rig's database
bd list --dir /path/to/rig             # List rig's beads
```

Running `bd` from the city root hits the city-level `.beads/`, not
the rig's. Use `gc rig list` to find rig paths.

## Convention

Rigs should be created **inside the city directory** unless explicitly
given an absolute path. The default rig path is `<city-root>/<rig-name>`.
Do not create rigs as sibling directories of the city.

## Adding and listing

```
gc rig add <path>                      # Register a directory as a rig
gc rig list                            # List all registered rigs
```

## Status and inspection

```
gc rig status <name>                   # Show rig status, agents, health
gc status                              # City-wide overview (includes rigs)
```

## Suspending and resuming

```
gc rig suspend <name>                  # Suspend rig (all its agents stop)
gc rig resume <name>                   # Resume a suspended rig
```

## Restarting

```
gc rig restart <name>                  # Restart all agents in a rig
gc restart                             # Restart entire city
```
