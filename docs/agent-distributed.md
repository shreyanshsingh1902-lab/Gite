# gite-agent: distributed repos (phase 2, the novel core)

The reason gite exists as more than forge #47. Repo storage is a choice: the
canon can live on your laptop, behind NAT, and people still clone and push
through gite with normal clients — while "push succeeded" continues to mean
*the laptop's git accepted it*.

## Problem statement

- Your machine holds the canonical bare repo (your rules, your hooks, your
  backups, your disk).
- Gite's server is public-facing; normal clients hit it.
- The machine is behind NAT; no inbound firewall changes, ever.
- It may be asleep. Pushes must then fail **honestly**. Nothing silent, ever.

Non-goals (record them so scope monsters stay out): no bidirectional sync
(proto-split-brain, see prior art), no automatic laptop wake-up, no mesh of
gite servers (phase 4 at the earliest).

## Prior art (read all of this before designing more)

| System | What it does | What we take / leave |
| ------ | ------------ | -------------------- |
| **Gitea/Forgejo mirrors** | pull-mirror (fetch on interval) + push-mirror (push after commits) — one-directional | **take**: the honesty that a mirror is *not* authoritative; every mirror row has a direction and an origin. **leave**: both directions — we want a tunnel, not a copy |
| **GitLab Geo** | multi-site replication with a primary+secondaries, promotion tooling | **take**: the single-writer assumption and "secondaries are read-only until promotion". **leave**: the enormous failover product; our v1 has exactly one primary |
| **radicle** | peer-to-peer git over a gossip protocol; nodes publish and exchange directly | **take**: the framing that git history is portable and signing can verify authorship anywhere. **leave**: elimination of a forge; we still want a host that isn't us |
| **rsync/syncthing-for-.git** | block-sync two copies of `.git` | **anti-pattern**: two independently-writable copies diverge and `.git` internal files (refs, packs, index locks) are not sync-safe. Never. |
| **ssh -R manual reverse tunnel + git daemon** | what solo devs do today: `ssh -R 0.0.0.0:2222:localhost:2222` to a VPS | **take**: proof that outbound-only works fine for git transport. **leave**: manual, one-repo, no identity model |

Radicle handbook for the p2p framing: <https://radicle.xyz> (docs links from there; also read their "signing and trust" essays).
Gitea mirror docs (mirror semantics): <https://docs.gitea.com/category/mirrors>
GitLab Geo architecture (if you want the deep end): <https://docs.gitlab.com/ee/administration/geo/>

## Transport choice: ssh-as-tunnel (decided, with reasons)

The agent dials **out** with a regular x/crypto/ssh client handshake to the
existing gite listener. Inside that single encrypted connection, multiple
*agent channels* carry:

1. `register` channel: the agent announces itself and its repos (db upserts).
2. `tunnel` channels, one per in-flight git operation.

Why ssh framing rather than websocket/gRPC:

- The code we already have (client vs server side of x/crypto/ssh) is
  symmetric; zero new wire stack.
- Channels multiplex for free with per-stream backpressure (window updates).
- The agent's identity is... an **ssh key** — the same `ssh_keys` machinery,
  same fingerprints, same revocation, same audit logging. One identity story.
- gRPC/WebSocket both fine, but they'd add a second identity model and a
  dependency; the win is small.

## Identity and enrollment

- The agent owns an exclusive keypair (distinct from the user's personal key;
  it's a machine credential that can rotate independently).
- `gite agent enroll` on the machine: creates a pending registration row plus
  a one-time enrollment code; the owner approves it in the web UI (or, before
  there is a UI, through the same CLI shape used for ssh keys).
- The user account that owns the agent becomes the route owner: from the
  catalog's point of view the repo still has *its* owner; the agent row tracks
  which machine and which path the canonical copy is at (`agent_repos.path`).
- Revocation: delete the agent row → all its tunnels drop at next open
  attempt; the catalog's repos with `backend=agent` show a status badge when
  the route is offline but data stays untouched anywhere.

## Protocol draft (freeze the shape, adjust field names in implementation)

Frames are length-prefixed with a 4-byte big-endian length + JSON body for
control frames; `git` byte streams travel raw on the channel after a
`TunnelOpen` handshake, exactly akin to the ssh exec streaming we already do.

```
// agent → gite, channel "register"
{"type":"hello","agent_version":"0.1","key_fp":"SHA256:...","machine_name":"workstation","repos":[{"path":"/Users/me/repos/widget.git","default_branch":"main"}]}
// gite → agent
{"type":"welcome","agent_id":42,"accepted":[{"path":"/Users/me/repos/widget.git","repo_id":777,"status":"mapped"}],"rejected":[...]}

// gite → agent, channel "tunnel" (when a client push arrives)
{"type":"tunnel_open","frame_id":"uuid","repo_id":777,"verb":"git-receive-pack","actor_username":"collab","actor_role":"write"}
// gite → agent   (raw stream begins; agent pipes into its local
//                 `git receive-pack <path>` exactly like gateway/ssh does)
{"type":"tunnel_close","frame_id":"uuid","exit":0,"stderr_tail":"done in 2m1s"}
```

The **honesty rule** is protocol-level: gite only propagates success (exit 0)
to the git client. Anything else, or no answer before a deadline (default
`agent_time_to_fail: 10s` after `tunnel_open`, refreshed by stream bytes), is
a rejection to the *client*:

```
gite: repository lives on offline machine (agent 'workstation'); nothing was
accepted and nothing will be queued silently. Ask the owner to wake it up, or
wait.
```

Later (still phase 2, page-2): an explicit "submission mode" switch per repo
(`submit_mode=queue|reject`, default reject) — queue stores no packfile, only
a *request* for review that the owner pulls from the laptop when it's up. It
never reserves the word "accepted" for anything not landed on the canonical
copy. This is the exact failure mode that makes silent-accept designs
trustless, and GitHub+friends will never need to solve it because their disk
is always awake. This is also what makes gite "novel": implementing a HCI
honesty rule as protocol semantics.

## Read path (viewer + fetch)

The web viewer and anonymous clones on public agent-backend repos go through
the same tunnel channels (`verb: git-upload-pack`). There is no replica book-keeping,
no "last synced" lie: reading *is* reading the canonical object db. This makes
read paths for agent repos exactly like server repos except the storage hop,
keeping one code path in `gateway` with two `Backend` impls.

## Failure tables (implement exactly, test exactly)

| Agent state at tunnel_open | client experience | server status recorded |
| -------------------------- | ----------------- | ---------------------- |
| offline (no outbound conn) | honest rejection, no retry loop on our side (client may retry) | `status=offline` |
| online, git rejects (fast-forward, bad object) | child stderr passthrough + agent exit propagation | `status=online` |
| online, machine died mid-stream | rejection + `err=stream reset` | `status=degraded` |
| enrollment revoked | rejection `route revoked`, agent reconnect loop until re-enroll | row deleted |

## NAT and firewall notes

- Outbound TCP only; one ssh connection carries every repos' tunnels for the
  agent's lifetime; agents reconnect with exponential backoff (1s→30s, jitter,
  hard-complete on user cancel).
- Use `SSH_` keepalives so dead NAT mappings clear within one normal
  `ServerAliveInterval` (default 15s is fine).
- If two repos on the laptop map to two gite repos, no changes: one conn,
  many repos via `tunnel_open.repo_id`.

## Security model

- The agent's key authorizes tunnels; the *user's role* still authorizes the
  underlying git access rule (the same `auth.Require` as any other door).
- Client visible errors preserve the exact stderr of the agent-side git
  binary only for the canonical copy's verdict; nothing about the user's own
  machine leaks through gite.
- No inbound ports on the laptop; no remote exec beyond the fixed `git
  upload-pack|receive-pack` verbs bound to `agent_repos.path` (a full copy of
  the transport-ssh.go command parse discipline, byte for byte, on both sides).
- Rotated agent keys re-enroll with re-owner's approval (as with ssh keys).

## What stays out until the basics work

- secrets/cache/credentials mirroring between laptop/server sidecar
- laptop-side CI/event triggers (a `hooks/post-receive` on the laptop is the
  natural place; leave it to the owner's own git config)
- multi-agent one-repo leadership (mirroring, phase 3)

## Key files to study across ecosystems

- x/crypto/ssh client-side usage in agent: <https://pkg.go.dev/golang.org/x/crypto/ssh> (ClientConfig, Dial, NewClientConn reading)
- soft-serve's `internal/server` for how they do channel multiplexing patterns (no agent, but framing shape): <https://github.com/charmbracelet/soft-serve>
- Reverse-flow honest-error UX: Gitea's pull-mirror failure states in their UI as the calibration for "degraded" wording: <https://docs.gitea.com/category/mirrors>

## Kill criteria (echo of phase 0's rule)

If agent push round-trip isn't honest and correct across a machine sleep/wake
cycle within two weeks of build time, stop and re-design. No partial honesty.
