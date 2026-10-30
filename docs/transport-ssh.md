# transport: git over ssh (phase 0)

Phase 0's whole deliverable. Objective: a clean `git clone / fetch / push`
against gite with a normal client and zero server-side configuration, then
access-denied paths that behave like GitHub's.

Design decision (see `architecture.md`): run an **in-process ssh server**
using `golang.org/x/crypto/ssh` instead of system sshd + AuthorizedKeysCommand.
Same logic either way; the built-in server means `gite serve` is all a machine
needs, no root. Keep the sshd variant documented below — it's the escape hatch
for admin setups and how Gitea/GitLab do it.

## The wire flow (what actually happens on `git push`)

```
git client                         gite (ssh server)              child proc
────────                          ─────────────────               ──────────
 TCP :2222 ──────────────────────────────────────► accept
 SSH banner ─────────────────────────────────────►
 KEYEXCHANGE (ed25519/curve25519) ───────────────► host key loaded from data dir
 publickey auth ─────────────────────────────────► ssh.PublicKeyCallback:
                                                    fingerprint → ssh_keys row → user
                                                   unknown key → failure, no user hint
 channel open "session" ─────────────────────────► accept (only type allowed)
 request "env" GIT_PROTOCOL=version=2 ───────────► record (whitelisted env)
 request "exec" payload: git-receive-pack '/you/widget.git' ─────► parse strictly
                                                    auth.Require(user, repo, write)
                                                    ✓ → exec git receive-pack <path> ──►
 stdin/stdout/stderr stream ◄────────────────────► pipe ch ↔ child
 close stdin (pack sent) ────────────────────────►
 exit-status request ◄─────────────────────────── child exit code (never faked)
 channel close
```

Between the exec and its EOF, everything is plumbing: gite copies bytes, never
inspects them. Protocol negotiation, capability lists, packfile transfer all
happen inside the `git upload-pack|receive-pack` child process. That isolation
is load-bearing: read `gitprotocol-pack` and `gitprotocol-capabilities` once to
understand what you're relaying, then delight in not implementing it:
<https://git-scm.com/docs/gitprotocol-pack>,
<https://git-scm.com/docs/gitprotocol-capabilities>

## Command parsing (the security core)

A client sends exactly one form (per git remote-ssh convention):

```
git-upload-pack '/owner/name.git'
git-receive-pack '/owner/name.git'
```

Our parser (`shSplit` + validation, unit-testable, no auth/db needed):

1. Split into words handling `'` and `"` quoting; no backslash support (git
   never sends it here; refuse anything else).
2. Exactly two words. First must be one of the two verbs above. Anything else
   (`bash`, `scp`, `rm`, `git-shell`) → refuse with
   `only git-upload-pack and git-receive-pack are allowed`.
3. Second word minus optional leading `/` and optional trailing `.git` must
   split into exactly two segments; both must match
   `^[a-z0-9][a-z0-9_.-]*$` (lowercased first). No `..` survives that regex.
4. Resolve `repos(owner, name)` (404-veiled per auth doc), then `Require`.
5. Only then compute the absolute path from catalog fields, verify
   `<path>/HEAD` exists (drift between db and disk, report honestly).

Whys:

- **argv exec, never `sh -c`**: the ssh command string must never touch a
  shell. `os/exec` builds argv; the filename is from our catalog, never from
  the wire (the wire string only *selects* a catalog row).
- **User-supplied path is validated before any db hit**: cheap DoS containment
  and fuzz-friendly (property tests in transport doc).
- **No usernames in URL determinations**: the key resolved user decides.
  A client claiming `you@host` with somebody else's key gets *them*.

## Built-in server design (internal/gateway/ssh.go)

```go
type SSHServer struct {
    cfg   config.Config   // .SSHAddr listen, .RepoRoot storage, .Home host key
    conn  *db.DB
    repos *repository.Store
    log   *slog.Logger
    priv  ssh.Signer       // ed25519, generated on first run, persisted
}

func (s *SSHServer) ListenAndServe(ctx context.Context) error
    // accept loop; SIGINT (signal.NotifyContext) closes listener; WaitGroup
    // drains live conns before returning nil. accept error after ctx.Done → nil.

func (s *SSHServer) serveConn(nc net.Conn)
    // ssh.ServerConfig{ PublicKeyCallback } → ssh.NewServerConn
    //   go ssh.DiscardRequests(reqs)         // global requests (hostkeys@ etc.)
    //   for nc := range chans: accept "session" only, Reject others
    //   per accepted channel: go serviceSession(...)

serviceSession:
    // drain request loop: "env" (record only GIT_PROTOCOL), "exec" (once!)
    //   shell/pty-req/subsystem refused; unknowns: reply false.
    // exec → go runGit(user, ch, cmdStr, envSnapshot)

runGit:
    // authorize (above) → exec.CommandContext(gitBinary, verb, absPath)
    //   cmd.Env = os.Environ() + envSnapshot (GIT_PROTOCOL forwarded)
    //   cmd.Stdin = ch; cmd.Stdout = ch; cmd.Stderr = ch.Stderr()
    // Start/Wait; exit code (mapping signal → 255) → SendRequest("exit-status")
    // → ch.Close(). Only code + one line of err prefix ever reaches clients.
```

Details that bite if ignored:

- **`request "env"` arrives BEFORE exec.** Record at arrival, snapshot at exec.
- **Reply to requests only when `req.WantReply()`**. Reply-less requests are
  informational (window-change etc.);
- **Don't block the request loop with git.** runGit runs in its own goroutine
  after `exec` is answered; the loop keeps answering until channel close.
- **The exec request must be answered true before** running; some clients
  stall otherwise.
- **Host key**: bytes file `ssh_host_ed25519_key` in the home dir (base64 of
  the 64-byte private). World-visible fingerprint at startup, because tools
  (`ssh-keygen -l -f ssh_host_ed25519_key.pub`) must agree.
- **Known-host UX**: clients connecting to a dev port see
  `[host]:2222` keys; in the runbook, show them how to
  `ssh-keyscan -p 2222 host | ssh-keygen -lf -` once. A `/login`-level UX is not
  a transport problem; don't solve prompt-host key trust cleverly.
- **Backpressure reality**: `rep push` of a big repo saturates the channel;
  x/crypto handles channel windows; simply streaming io is correct.
- **`MaxAuthTries`-ish hygiene**:PublicKey callback returning error fails
  immediately; no brute-force loop loops beyond ssh's own window. Log every
  reject (fingerprint, remote, reason).

## Public-key auth internals (know just enough)

`ssh.PublicKeyCallback` fires on each auth attempt with the client's key; the
signature check has **already been done by the library** — the callback only
decides *whether this key is registered*. That's why fingerprint lookup is the
whole story and why the lookup must be by `ssh.FingerprintSHA256`.

Supported key types: whatever x/crypto supports (ed25519, rsa, ecdsa,
sk-ssh-ed25519). Do not pre-restrict; if someone shows up with a hardware key
(`sk-`), celebrate and let it work.

References:
- pkg: <https://pkg.go.dev/golang.org/x/crypto/ssh> (read ServerConfig, PublicKeyCallback, Channel docs once)
- study repo: `gliderlabs/ssh` (a wrapped API around the same primitives; read its source before writing your own abstractions): <https://github.com/gliderlabs/ssh>
- study repo: `charmbracelet/soft-serve` — read `ssh/` and `internal/transport/ssh`: <https://github.com/charmbracelet/soft-serve>
- OpenSSH `AUTHORIZED_KEYS`/forced command semantics (how gitolite/gitea/gitlab do it): `man 8 sshd` "COMMAND EXECUTION AND DATA-COPYING" section... (they all depend on `command=` prefix semantics)

## The sshd-wrapper alternative (documented, deliberately not default)

How Gitea does it today: generate `authorized_keys` lines like

```
command="gite serv key-15",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty ssh-ed25519 AAAA...
```

then `gite serv` reads `SSH_ORIGINAL_COMMAND` (set by sshd), makes an internal
authorization call, and execs git. If gite ever supports this mode, it's:

1. `gite keys shell <fingerprint>` prints the line (db-driven).
2. sshd_config:
   ```
   Match User gite
     AuthorizedKeysCommand gite keys shell %f
     AuthorizedKeysCommandUser gite
   ```
3. `gite serv` = same authorize+exec core as the built-in path, differing only
   in where the command string arrives from.

Keep the core identical so both modes can coexist (Gitea's again: `cmd/serv.go`
+ `routers/private/`). GitLab's evolution (their gitlab-sshd, written in Go in
`gitlab-shell`) is the evidence the built-in approach scales: <https://gitlab.com/gitlab-org/gitlab-shell>

## Error catalog over the wire (`runGit.finish`)

| Case | stderr text | exit |
| ---- | ----------- | ---- |
| unparseable command | `gite: expected: git-upload-pack '/owner/name.git' or git-receive-pack '/owner/name.git'` | 1 |
| unknown repo / unreadable private | `gite: repository not found` | 1 |
| public repo, write shortfall | `gite: write access denied on you/widget for you` | 1 |
| catalog row exists, disk gone | `gite: repository storage missing: <path>` | 1 |
| push args > 2 words | malformed message (above) | 1 |
| git child's own stderr | passed through untouched | child's |

Every user-visible ssh path stops at that set. Anything deeper (pack errors,
ref formatting) is git's stderr, passed through verbatim: do not paraphrase it.

## Test recipes (full scripts live in integration-and-testing.md)

Positive: known key → clone empty → commit → push → second clone shows commit.
Negative (each is a table row):

- unknown key rejected before any db hit beyond fingerprint
- 3-segment path refused; `..`-containing refused; `'`/`"` weirdness refused
- private repo + stranger key = same stderr as nonexistent repo
- public repo + stranger push = exit 1 with `write access denied...`
- granted `write` → the same push succeeds; grant again with no-op upsert → still succeeds
- `shell` request → refused; `git-shell`-style arbitrary exec → refused

Debugging helper of last resort: `GIT_TRACE_PACKET=1 git clone ...` shows every
pkt-line — read a trace once in your life; it demystifies everything above:
`gitprotocol-pack` + trace beats any blog post.
