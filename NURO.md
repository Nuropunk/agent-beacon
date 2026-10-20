# Beacon, nuro edition

This repository is a fork of [Beacon](https://github.com/Asymptote-Labs/agent-beacon) by
[Asymptote Labs](https://asymptotelabs.ai), made for Nuro and nue. Everything Beacon does, from
capturing 27 agent runtimes to the dashboard, threat rules, lenses and MCP server, is Asymptote
Labs' work, released under the MIT License in [LICENSE](LICENSE). That notice stays as it is, and
the changes in this fork are offered under the same license. Thank you, Asymptote Labs.

The fork adds one thing on top: a **nuro edition**, built by default, in which nothing Beacon
captures leaves the machine. What follows is what that edition changes and what to know before
installing it.

## What the nuro edition changes

**A local-only lock.** `cli/beacon/internal/localonly` turns on when the build sets
`version.Edition=nuro` (the default in both Makefiles, the macOS package job and goreleaser's
archives, Linux packages and MSI) or when `BEACON_LOCAL_ONLY=1` is set for any other build. A plain
`go build` makes the community edition, so build with `make build`. Nothing at run time turns the
lock off; that takes a build with `EDITION=community`. Under the lock:

- Commands that would send data off the machine, or hand private traces to another vendor's agent,
  refuse with a message instead of running: signing in to beacon.sh, sharing, Beacon Cloud and
  cloud-agent traces, onboarding signups, the enterprise build download, self-updates and release
  checks, Cursor usage sync, the SIEM and bucket forwarding packs, Splunk and Falcon LogScale
  forwarding from the collector (the `--splunk-*` and `--falcon-*` install and repair flags), CI
  forwarding, the Beacon Cloud MCP server and its tokens, rule downloads, third-party memory
  evaluations, handoffs to other vendors' agents, git note pushes that skip hooks (`git notes push`
  and the pre-push hook, which runs as `|| true` so the person's own push goes on; `beacon git setup
  --share-notes=false` removes one an earlier install left), session sharing
  on git push, public tunnels (the Claude Cowork `--ngrok` flag), and installing Beacon skills
  where other vendors' agents read them. `beacon mcp serve` runs over stdio only, and installing
  hooks for "all" runtimes is refused so each runtime is named. `cli/beacon/cmd/localonly.go` lists
  all 45 rules, and a test checks that each one names a real command.
- The collector config leaves out any Splunk or Falcon destination, including one an earlier
  install left in `config.json`, so the collector writes only the local log. A collector still
  running on a config from a community install keeps it until `beacon endpoint repair` rewrites it.
- The process's default HTTP transport dials loopback addresses only, so a path the rules miss
  still cannot reach the network.
- The hooks binary never uploads (`cli/beacon-hooks/internal/cloudshuttle`).
- Grok is held: Beacon does not run its binary during discovery and refuses to install hooks for
  it, sync it or backfill from it.

**Pulse.** `beacon pulse` summarizes the runtime log as metadata-only JSON: for each runtime, when
Beacon last saw an event, counts for the last 6 and 24 hours split by observed and inferred
fidelity, an hourly histogram, and a grade of LIVE (an event within 6 hours), STALE (within 24),
DARK (older, or expected and never seen) or HELD. It holds no prompts, commands, paths, users,
hostnames or repositories, never reads the hostname (`--machine` or `BEACON_PULSE_MACHINE` names
the machine), and writes a 0600 file with `--out`. A harness name or collection method in the log
that is not shaped like a name is counted as `unknown` or dropped, never copied. It never sends
anything on its own.

**Two rule packs**, both in detect posture, so they flag and never block:

- `rules/owner-gates`: merges and deploys, chain and wallet actions, and any held engine at work.
  These are steps that wait for the owner's go.
- `rules/private-data`: bypassing a hook (`--no-verify` and the like), another vendor's agent
  reading a private store, a private read followed by network egress, and voice or biosignal
  files leaving the machine.

**The dashboard in nue's palette.** The nuro edition serves the dashboard with a dark void
background, turquoise, aqua, violet and gold accents, and fonts referenced with `local()` only,
so the page loads nothing from the network. Lenses get the same palette through their tokens.
Community builds serve the upstream pages unchanged. Neither of the two candidate nue color pairs
is used; the palette is the general NUE one until Nuro picks.

**The Nuro Brain lens.** A built-in lens that draws a trace as a brain: each kind of event is a
region that lights only from the trace's own events, inferred events glow dim, lines join regions
where one kind of event followed another, and a trace with no events stays quiet. Nothing
animates on its own.

## Build

```sh
cd cli/beacon
make build                     # nuro edition, the default
make build EDITION=community   # upstream behavior, no lock
```

Build from source. `cli/beacon/beacon.zip` is a prebuilt binary committed upstream, with nothing
to verify it against; this fork does not use it, and neither should you.

## Before installing

These come from reading the upstream code and docs for this fork. They are notes, not changes:

- The lock covers the processes this build runs. If the machine ever ran another Beacon build,
  run that build's `beacon endpoint uninstall` first and remove any forwarding service it set up
  (a Vector pack or Beacon Cloud connection): those run as their own processes, outside the lock.

- Prefer user mode. In system mode the runtime log is mode 0666, so any local account can read
  the prompts, commands and paths in it (`docs/platforms/linux-deployment-profile.mdx`). User
  mode keeps logs at 0600.
- `beacon status --json` carries the latest full event under `last_event`. Treat its output as
  private and never pipe it to a cloud log or CI output.
- The browser extension defaults to full prompt and response retention
  (`browser-extension/src/shared/types.ts`). Leave it uninstalled, or set it to metadata only.
- Some runtimes can take Beacon's hooks without a trust prompt: Hermes documents
  `HERMES_ACCEPT_HOOKS=1` for it, and Muse's managed hooks run without a trust step. Install
  hooks only for runtimes you name.
- The macOS package's postinstall rewrites the console user's Claude and Codex settings,
  `~/.claude/settings.json` included, on every upgrade. If another tool manages that file, keep
  Beacon's hook entries in that tool's source so the two do not fight.
- `beacon-sandbox` runs real agent sessions on Modal with the Anthropic API, which costs money,
  and runs Claude Code with `--dangerously-skip-permissions`. Its free unit tests are fine; do
  not run it live without the owner's go.

Installing on a machine, turning on anything that sends data, and merging this fork's changes
all wait on the owner's explicit go.
