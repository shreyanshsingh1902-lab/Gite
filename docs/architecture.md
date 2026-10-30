# gite architecture

Read this before writing anything. It explains what the system is, where each
byte lives, and — crucially — how the same problem is solved by Gitea, GitLab,
SourceHut, Gitolite and soft-serve, so you can decide by precedent instead of
improvising.

## The core law

Git is the engine; gite is the catalog, the gatekeeper and the observer.

- The **object database** (commits, trees, blobs, tags) lives only in real,
  ordinary git repositories on disk. gite never synthesizes, parses or rewrites
  that format. It shells out to the `git` binary for everything.
- The **catalog** (accounts, ssh keys, repo names, roles, tokens, mirroring
  jobs, agent registry) lives in sqlite. This is metadata git has no place for.
- The **gate** decides who may run which git verb against which repo. All
  three doors (ssh transport, http transport, web viewer) funnel into one
  permission function.
- The **UI** is a reader over the object database plus a manager for the
  catalog. If gite died, `git clone ssh://...` on a server-hosted repo would
  keep working via plain sshd... and every repo remains a plain repo any tool
  can read.

## Component map

```
                             ┌──────────────────────────────┐
 git client (ssh) ──────────►│ gateway/ssh   (built-in)     │──► auth.RequireAccess
 git client (https) ────────►│ gateway/httpgit  (phase 1)   │──► exec git upload/receive-pack
                             └──────────────┬───────────────┘         │
                                    auth    │                         ▼
 web viewer (phase 1) ──────────► webui ────┤                  bare repo on disk
                                    │      │                  <repo-root>/owner/name.git
                                    ▼      ▼                         │
                              ┌─────────── sqlite catalog ───────┐   │
                              │ users, keys, repos, roles,       │   │
                              │ tokens, sessions, agent registry │   │
                              └──────────────────────────────────┘   │
                                                                     │ tunnel
 gite-agent (phase 2) ────────── outbound conn ──────────────────────┘
 (machine behind NAT holding the canonical bare repo)

 providers (phase 3) ──► github/gitlab API adapters, mirror jobs
```

One binary (`gite`) serves everything through phase 1. `gite-agent` joins in
phase 2. Deliberately small, deliberately boring, so the novel part stands.

## Storage model: where truth lives

Every repository has exactly one **authoritative copy** — one repo instance
whose accepting of a push is the moment history changes. Everything else is a
replica, a mirror, or a view.

| backend   | authoritative copy        | push accepted when...                  | offline behavior                        |
| --------- | ------------------------- | -------------------------------------- | --------------------------------------- |
| `server`  | gite's own disk           | receive-pack exits 0 on gite's disk    | never offline                           |
| `agent`   | a user's machine          | the agent's receive-pack exits 0 there | push **rejected honestly**; optional visible queue, never a silent accept |
| `external`| another forge (github...) | the provider's API confirms            | error from provider shown               |
| `mirror`  | designated primary + replicas | primary accepts                    | primary required; replicas lag          |

The invariant this buys us: **"push succeeded" always means the source of
truth accepted it.** GitHub, GitLab and Gitea never have to think about this
because their answer is always "our own disk". We generalize one question:
*whose disk?* — and refuse to answer vaguely.

The contract is captured by one interface (phase 3):

```go
// The storage backend that holds the authoritative copy.
type Backend interface {
    // Read path: serve a git fetch/clone (upload-pack side).
    Fetch(ctx context.Context, refspec []string) (RefsResult, error)
    // Write path: apply one push (receive-pack side). Returns nil only when
    // the authoritative copy has accepted.
    Receive(ctx context.Context, fr FetchRequest) ReceiveResult
    // Health: can the authoritative copy be reached right now?
    Alive(ctx context.Context) error
}

// serverBackend = local bare repo, exec git directly.
// agentBackend  = tunnel to a gite-agent, agent execs git there.
// externalBackend = provider adapter.
```

Phase 0 implements only `serverBackend`. The interface is written to not
need a rewrite when the others arrive.

## Request lifecycles

### Lifecycle A: `git clone ssh://you@gite.example:2222/you/widget.git`

1. TCP connect to the built-in ssh listener.
2. Key exchange; client's public key is looked up by SHA256 fingerprint in
   `ssh_keys`; the key resolves to exactly one user account. Unknown key = conn
   closed. (No usernames in ssh — the key *is* the identity.)
3. Client opens a `session` channel and sends an `exec` request.
4. The command string is strictly parsed: `git-upload-pack|git-receive-pack
   '<path>'` and nothing else. Path must be `owner/name[.git]`, two segments,
   no `..`, no shell metacharacters.
5. `auth.Evaluate(user, repo)` determines the role: owner/admin implies admin,
   collaborator row implied otherwise, public repo implies read for everyone.
   upload-pack needs `read`; receive-pack needs `write`.
6. If authorized: exec `git upload-pack /abs/path/to/bare.git` as a child
   process and pipe bytes between the ssh channel and the process.
7. Child exits; its exit code is forwarded as an ssh `exit-status` request;
   the channel closes. Exit status is honest: git's verdict, not ours.

### Lifecycle B: `git push origin main`

Same as A through step 6, except the verb is `git-receive-pack`, the wanted
role is `write`, and the client streams packfile data on its stdin. The
child process validates the update itself (fast-forward checks live inside
git, branch protection arrives via hooks in phase 3). We report success only
after that child's exit 0 — for agent-hosted repos, only after the agent's
child's exit 0.

### Lifecycle C (phase 1): `git clone https://gite.example/you/widget.git`

1. `GET /you/widget.git/info/refs?service=git-upload-pack` with Basic auth
   (username = account, password = personal access token) or anonymous for
   public repos.
2. Token verified against the hashed `pat_tokens` table, same `Evaluate`
   check, then the smart-HTTP dance begins (see `transport-http.md`): ref
   advertisement response, then `POST .../git-upload-pack` byte-streams.

### Lifecycle D (phase 2): push to a repo whose truth lives on a laptop

Steps 1-5 as in A; at step 6 the gateway finds `backend = agent` and instead
of exec'ing git locally, it opens a tunnel frame on the agent's outbound
connection, forward-streams the client's bytes, and waits for the agent's
`TunnelClose{exit}` frame. Laptop offline → the client gets a clear,
*honest* rejection: `gite: source of truth is offline; nothing was accepted
and nothing will be until the owner's machine is up`.

## Git internals crash course (the 20 minutes that make this codebase legible)

- **Objects.** Content-addressed, sha1-named. Four kinds: `blob` (file
  content), `tree` (directory), `commit` (tree + parents + metadata),
  `tag` (annotated tag). Content → hash, never the reverse.
- **Refs.** Plain text files under `refs/heads/...`, `refs/tags/...`, each
  containing one hash. `HEAD` is a symbolic ref, usually `ref: refs/heads/main`.
  In a bare repo this is the whole story; there is no working tree, no index.
- **Packfiles.** Objects get compressed together with deltas; refs stay as
  files. `git fsck`, `git cat-file`, `git verify-pack` inspect. We never parse
  these; the binary does.
- **Wire protocol.** Text lines prefixed with their hex length (`0011...`),
  `0000` = flush-pkt. A fetch starts with the server advertising refs
  (`refs/heads/main <sha>`-ish pkt-lines), the client sends wants, the
  server streams a pack reused from negotiation. A push is the reverse: client
  sends old-sha→new-sha ref-update lines plus a pack, server answers
  `unpack ok` / `ng @<ref> reason` lines. All of this is implemented by
  `git upload-pack` / `git receive-pack`, which we simply exec.
- **Why we never reimplement this.** The protocol has quirks accumulated over
  20 years (client capability negotiation, shallow pushes, partial clones,
  protocol v2). GitHub, GitLab and Gitea all end up shelling or delegating to
  git itself for the heavy lifting; a hobby rewrite of it is unpaid QA work
  for git's test suite. Our job starts *above* the protocol: identity and
  authorization.

Formal docs for when you need them:

- Pro Git, Git Internals: <https://git-scm.com/book/en/v2/Git-Internals-Git-Objects> then References, Packfiles, Transfer Protocols (same chapter series)
- `gitrepository-layout(5)`: <https://git-scm.com/docs/gitrepository-layout>
- `gitprotocol-pack`: <https://git-scm.com/docs/gitprotocol-pack>
- `gitprotocol-capabilities`: <https://git-scm.com/docs/gitprotocol-capabilities>
- `gitprotocol-v2`: <https://git-scm.com/docs/gitprotocol-v2>
- `gitprotocol-http`: <https://git-scm.com/docs/gitprotocol-http>
- Git source tree technical docs: <https://github.com/git/git/tree/master/Documentation/technical>

## The catalog (what lives in sqlite vs on disk)

| Concern                     | Lives in | Why |
| --------------------------- | -------- | --- |
| commits, branches, history  | disk (bare repo) | that IS git's format; portable, tool-readable |
| repo name, owner, visibility, backend kind | sqlite | policy, not content |
| users, ssh keys (fingerprints), PAT hashes | sqlite | identity is a forge concern |
| roles (collaborators)       | sqlite   | authorization input |
| repo *description*, topics  | sqlite   | display metadata |
| mirror/remote URLs, agent registrations | sqlite | plumbing metadata |

Failure mode to respect: catalog row exists, bare dir missing (or vice versa).
Treat as a broken state, report `gite: repository storage missing`, and offer
`gite admin repair` later — never silently recreate a directory a user thinks
is backed by 3 years of history.

## Decisions on record (make these again consciously, never by accident)

| Decision | Choice | Alternative rejected | Why |
| -------- | ------ | -------------------- | --- |
| Git engine | exec native `git` | go-git library | go-git reimplements git; server-side receive-pack handling lags; binary truth keeps repos tool-portable. go-git is fine for *client-side* tricks later |
| SSH transport | built-in server in-process (x/crypto/ssh) | system sshd + AuthorizedKeysCommand wrapper | zero root configuration, single binary ships and runs as one process; auth logic in-process instead of a `serv` subcommand shelling trap. sshd wrapper remains a documented escape hatch (see transport-ssh) |
| DB | sqlite + WAL, pure-Go driver `modernc.org/sqlite` | postgres early | one file, immediate cold-start, no ops burden; 10k users-scale is a rounding error for sqlite. Migration to postgres would be mechanical but is NOT the plan |
| Web | server-rendered templates, `go:embed` | SPA | sourcehut bar: fast, plain, testable; no build toolchain |
| Authz | one function `auth.Evaluate/Require` called from every door | per-door logic | doors drift exactly when they perform differently; single choke point |
| Identity on ssh | key fingerprint → user, no ssh usernames | username in URL then trusted | username in URL is display only: authorization must derive from the *key*, or key theft |
| Errors | sentinels + user-safe boundary wording | raw driver errors to clients | leaks schema, confuses users |

## How other forges solve the same problem

Read at minimum the Gitea and soft-serve pointers. The others are for taste.

### Gitea (+ Forgejo, its fork) — closest relative, start here

- Single Go binary, repos are bare on disk under `data/git/repositories/`, catalog in a real rdbms.
- SSH: writes `authorized_keys` lines whose `command="gitea serv key-15,..."`
  routes to `cmd/serv.go`, which parses `SSH_ORIGINAL_COMMAND`, then asks the
  running web process over its *internal private API* (`routers/private/`) to
  authorize before exec'ing git. Two-process trust pattern. Study exactly how
  serv.go enforces read vs write per verb — that's our `authorize()`.
- Supports both sshd wrapper mode **and** a built-in ssh server (`START_SSH_SERVER`) — precedent for our built-in choice.
- HTTP: `routers/web/githttp/` + services layer exec git for upload/receive stacks.
- Files to read: `cmd/serv.go`, `routers/private/`, `services/repository/`, `models/perm/access/`.
- Dev docs: <https://docs.gitea.com/category/development>

### GitLab — what gite looks like at 10M scale

- Rails app owns identity/authz; `gitlab-shell` (Go) owns transport; `gitaly` (Go, gRPC) owns git storage as an API; `workhorse` (Go) fronts smart HTTP.
- Complexity exists for HA and scale, not clarity. Nothing to copy for v1, but worth understanding the **question each service answers** (identity / transport / storage / arena).
- Authorization always re-checked server-side per request ("internal/allowed"), never cached in the transport. That discipline is portable.
- Dev docs: Gitaly architecture and GitLab Shell pages: <https://docs.gitlab.com/ee/development/gitaly.html>

### SourceHut — service-per-organ, API-first

- Identity is a separate service (meta.sr.ht); git web and git ssh are separate; the web app is mostly a shell over APIs.
- Study it for: minimal HTML, no frills, API-first. <https://man.sr.ht/git.sr.ht/>

### Gitolite — auth-only reference

- If you want to see how little machinery correct ssh git authorization needs, Gitolite is the classic: forced command, a two-key (user+repo+permission) rule table synced to `authorized_keys`, then plain exec.
- <https://gitolite.com/gitolite/>

### soft-serve — small go-native study case

- charmbracelet's ssh-first forge: x/crypto/ssh server, git served, bubbletea TUI over the same transport. The most concise real codebase demonstrating the exact server shape we're writing.
- <https://github.com/charmbracelet/soft-serve>

### What we deliberately do NOT follow

- Storing derived state as truth (e.g. GitHub issues/PR state in their db while git holds *only* content is fine; but GitLab "repository storage" moving between nodes is HA scenery we can skip).
- Reimplementing git in-process (`libgit2`-style) — buying sophistication we don't need.
- Regex-based permission rule files (gitolite.conf flexibility) — overkill before teams exist.

## Pitfalls and practices for this architecture

1. **Never combine `sh -c` with user data.** Exec with argv arrays (os/exec does this), validate paths via a segment allowlist regex, reject `..` — `transport-ssh.md` has the tests to pin it.
2. **One permission function.** Adding a door = adding a caller, not new logic.
3. **Nevér let the web viewer become the storage layer.** If the viewer stops shelling to git and grows its own cache of truth, we've reinvented a worse git.
4. **Honest failure.** If a backend is gone, say `ErrOffline`, offer nothing quietly.
5. **Index the catalog for the queries the UI does**, not the ones it might: owner listings, fingerprint lookups, name→id for transport (hot path, called per push).
6. **Timebox: if the ssh round-trip doesn't clone-and-push within a week of build time, stop shipping features and fix that.** (Kill criteria in `.opencode/plan`.)
