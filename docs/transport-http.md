# transport: git smart HTTP (phase 1)

Phase 1's transport deliverable: `gite clone https://gite.example/you/widget.git`,
push with a personal access token, anonymous read for public repos, behind a
plain `net/http` server in the same `gite` binary.

## What "smart HTTP" means (and why dumb is out of scope)

- **Dumb HTTP** serves `.git` files statically; requires `update-server-info`
  maintenance; basically legacy. GitHub/GitLab/Gitea all serve smart only by
  default (gitea keeps a dumb fallback via `http-backend` toggles; we serve
  smart only and refuse the rest). Sourcehut same.
- **Smart HTTP** is two requests:
  1. `GET  /<repo>/info/refs?service=git-upload-pack[&version=2]` → ref
     advertisement as pkt-lines with a `# service=...` preamble.
  2. `POST /<repo>/git-upload-pack` (or `/git-receive-pack`) → body is the
     client's request stream (pkt-lines, maybe a packfile), response likewise
     pkt-lines + pack.
- Stateless by symmetry: every POST re-negotiates; no per-connection state on
  our side. That's why git-over-https survives flaky networks but transfers
  whole packs on re-fetch (fix is protocol v2 partial negotiation, which
  `git upload-pack --stateless-rpc` already handles for us).
- Formal spec, read once:
  <https://git-scm.com/docs/gitprotocol-http>,
  v2 additions: <https://git-scm.com/docs/gitprotocol-v2>

## Endpoints and routing (freeze these)

```
GET  /{owner}/{name}.git/info/refs?service=git-upload-pack   → read (auth: none-ok | PAT)
GET  /{owner}/{name}.git/info/refs?service=git-receive-pack  → write (auth: PAT), 405 pour dumb requests
POST /{owner}/{name}.git/git-upload-pack?service=... → read   → stream to `git upload-pack --stateless-rpc --advertise-refs? NO...`
POST /{owner}/{name}.git/git-receive-pack            → write  → stream to `git receive-pack --stateless-rpc`
```

- Route shape: `/{owner}/{name}.git/...` — the `.git` suffix keeps the
  namespace unambiguous with future REST routes. Gogs/Gitea/GitLab all spell
  it this way; GitLab additionally strips/normalizes `.git` (accept both, store one).
- `service` query param decides which read role is demanded on info/refs;
  upload → read, receive → write. On the POST endpoints, the leading path
  component decides; re-verify per request, never hand role state across requests.

## Auth middleware (before anything touches git)

```go
// Pseudocode for the http middleware chain on the /{owner}/{repo}.git/ subtree:
func gitAuth(w, r) (user *auth.User, repo *repository.Repo, ok bool) {
    user := fromBasicToken(r)            // username:=r.BasicAuth user; sha256(password)→pat_tokens
    if user == nil {
        user = anonymous                 // allowed ONLY if repo public AND request is read
    }
    repo := repos.GetByOwnerName(owner, name)   // router pre-parsed, validated segments
    if repo == nil { 404-notfound; return }
    want := readFor(service)/writeFor(path)
    err := auth.Require(user, repo, want)
    if err != nil { map per the 404 rule (transport-body text, not HTML); return }
    // REMOTE_USER etc for the child process
}
```

Rules:
- Basic-only credentials for git transport. **No session cookies over git**,
  ever (cookie tokens are weaker and get misrouted into clone URLs).
- Anonymous read = only for `public` repos; check `repo.Private` via Evaluate,
  don't duplicate the rule.
- Token scopes (v1): `git` → this transport. Later `api`, `mirror`. A token
  with zero match has no superpower (no privilege prototypes).
- 401 vs 403: missing/invalid creds → 401 with `WWW-Authenticate: Basic
  realm="gite"`; valid creds but unauthorized → 403 (NOT 401: git clients
  re-prompt forever on 401 cycles).
- GitHub/GitLab differ here: GitHub uses OAuth PATs; GitLab also allows PATs
  and OAuth. Straight Basic with a PAT covers git clients matter-of-factly —
  `git` does Basic natively.

## Implementation route: CGI handoff to `git http-backend` (documented pick)

Two viable paths; we take A. Both need the same auth middleware; only the
"stream-to-git" slot differs (see `architecture.md` for the exec-not-reimplement
doctrine).

### A. `git http-backend` as CGI subprocess (gogs/gitea lineage)

What Gogs does in `routers/repo/http.go` (and Gitea's `routers/web/githttp/`
near-equivalent): construct a CGI-style environment for `git http-backend`,
spawn it, pipe request body → process stdin, process stdout → response, with
two-way flow.

Env the CGI expects (verbatim, from `man git-http-backend`):

```
PATH_INFO=/{repo}.git/...          # full path after the gite-prefix router consumed auth
REQUEST_METHOD=GET|POST
QUERY_STRING=?service=git-upload-pack
CONTENT_TYPE=application/x-git-upload-pack-request
CONTENT_LENGTH=<n | omit for chunked>
REMOTE_USER=<gite username>
GIT_PROJECT_ROOT=<gite repo root>
GIT_HTTP_EXPORT_ALL=1
```

Placement gotchas (each is a classic bug):
- PATH_INFO must include the on-disk `.git` suffix and be relative to
  GIT_PROJECT_ROOT. Compute it from the catalog row, never from the URL.
- Chunked request bodies: CGI has no way to express `Transfer-Encoding:
  chunked`, and `git http-backend` expects a length. Two practical options:
  1. Buffer the request body to a temp file and set `CONTENT_LENGTH` to its
     size (this is what Gogs' `http.go` style implementations do when the
     client sends chunked data).
  2. Introduce a `HTTP_TRANSFER_ENCODING`-style workaround only if option 1
     measurably hurts big pushes; gitea's `routers/web/githttp` shows their
     final approach, read it before inventing anything.
- Kill the CGI if the client goes away (r.Context().Done() → cmd cancel),
  else you leak `upload-pack` processes.
- The response content-type comes back as CGI header lines from the child; relay
  `Status:`, `Content-Type:` verbatim (do not re-derive).

Pros: git writes every protocol edge case. Cons: plumbing, and process-per-request.

### B. Direct `upload-pack|receive-pack --stateless-rpc` handlers (what GitLab's workhorse does over gRPC to gitaly)

`git upload-pack --advertise-refs <dir>` for GET,
`git upload-pack --stateless-rpc <dir>` for POST, receiving the body as stdin
and emitting stdout (see the v2 docs for the exact flag pairing
`--advertise-refs` vs `--stateless-rpc --advertise-refs` for the info/refs
request). This skips CGI env; you write the two endpoints yourself.
More code you own; fewer surprises in env plumbing. **Phase 3+ option if CGI
friction shows up.**

Reference impls to read before choosing:
- `man git-http-backend`: <https://git-scm.com/docs/git-http-backend> (the env list above is from here)
- Gogs `routers/repo/http.go` — the humane CGI plumbing
- Gitea `routers/web/githttp/` + `internal/simulate?` — their modern layout with tests
- GitLab `gitlab-shell` + workhorse for the gRPC flavor

## HTTP handler skeleton (stdlib first, zero deps)

```go
mux := http.NewServeMux()
// {owner}/{repo}.git/* subtree → git gitAuthmw → CGI bridge
// app routes (web viewer) → session authm → template handlers
srv := &http.Server{
    Addr:     cfg.HTTPAddr,
    Handler:  recoverPanic(logRequest(mux)),
    ReadHeaderTimeout: 5 * time.Second, // slowloris: you will be scanned
    IdleTimeout:       120 * time.Second,
}
```

Practices (Mat Ryer's whole essay applies; one pass, then code): each handler
returns its implementation (make-handler pattern), dependencies are structs
not globals, one error-handling helper per handler family:
<https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years/>

Body limits: `http.MaxBytesReader(w, r.Body, 2<<30)` (2 GiB, configurable) —
a hostile client uploading junk shouldn't have unbounded feed to a child
process that pipes through gite's memory. Legit big pushes stay pipes, not
buffers: bytes flow stdin→child; we never accumulate a pack in RAM.

## Response contract per endpoint (smoke-testable with curl)

```
# refs advertisement (a real one, abbreviated):
$ curl -i https://gite.example/you/widget.git/info/refs?service=git-upload-pack
HTTP/1.1 200 OK
Content-Type: application/x-git-upload-pack-advertisement

001e# service=git-upload-pack
0000
0001 protocol      ... (v2 shape differs; do not expect byte-exactness)
...

# push needs auth:
$ curl -i https://gite.example/you/widget.git/info/refs?service=git-receive-pack
HTTP/1.1 401 Unauthorized
WWW-Authenticate: Basic realm="gite"

# with a bad token: still 401 (distinguish invalid cred from anonymous? NO: same
# 401 keeps the oracle quiet; log which case it was internally instead).
```

Smoke recipe:
```
GIT_CURL_VERBOSE=1 git ls-remote https://gite.example/you/widget.git
git -C /tmp/w push https://you:gite_pat_xxx@gite.example/you/widget.git main
```

## Test matrix (scripted in integration-and-testing.md)

- [ ] anon clone public (2 requests)
- [ ] anon fetch private → 404 text identical to a missing repo
- [ ] token readonly (scopes: git) clone ok; push → 403 `write access denied...`
- [ ] token write push ok, then revoke → next push 403
- [ ] huge push (>1GiB spoofable) — define limit, confirm cleanup child killed
- [ ] `file://` remotes are irrelevant to this transport — skip
- [ ] multiple concurrent pushes to same ref (git last-wins + atomic ref swap);
      no partial-state error message oddity
- [ ] protocol v2 `GIT_PROTOCOL` on info/refs: served when client offers version=2 —
      comes free via http-backend; assert via `GIT_TRACE_PACKET` that `/v2` was used.

## References

- `gitprotocol-http`: <https://git-scm.com/docs/gitprotocol-http>
- `git-http-backend` man: <https://git-scm.com/docs/git-http-backend> (env contract)
- GitLab dev docs on workhorse/gitaly relationship: <https://docs.gitlab.com/ee/development/gitaly.html>
- Gogs http.go: <https://github.com/gogs/gogs/blob/main/routers/repo/http.go>
- x/crypto not needed here; stdlib `net/http` with a `cgi` handhold (unlike python-cgi, Go's stdlib `net/http/cgi` package has a *client*-side helper `http/httputil`-style; note: server-side CGI means you run the handler yourself — the `http-backend` child IS the app)
