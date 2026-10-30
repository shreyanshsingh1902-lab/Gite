# integration & testing

How gite gets tested so the transports can be trusted. The bias: black-box
through real `git` clients, table-driven units for the parsing/authorization
brains, and scripts you can run on a laptop while developing.

## The layers (and what each is for)

```
E2E scripts (scripts/e2e-*.sh)     real git binaries over real listeners  — the contract
   ↑
integration (internal/*/int_*)     temp GITE_HOME + httptest/spawn servers — the seams
   ↑
unit (internal/*/ *_test.go)       parsers, role math, migrations — the brains
```

Rules:

- Unit tests: stdlib `testing`, table-driven, no assert library required
  (if you add one, `github.com/stretchr/testify/require` at most).
- Integration tests open `db.Open(filepath.Join(t.TempDir(), "gite.db"))` +
  `Migrate()` + fill fixtures through exported methods only (same way the CLI
  does). Tests must not poke sqlite directly with INSERTs (they drift);
  `schema.md` notes the exceptions (only for modeling exotic legacy states).
- Never bind well-known ports in tests: listeners get `127.0.0.1:0` and the
  real addr is read back from the server handle.

## The access matrix, as code (per auth doc, mandatory rows)

| Case | Expected |
| ---- | -------- |
| owner clone public/private | ok |
| site admin clone private | ok |
| collaborator `read` fetch | ok |
| collaborator `read` push | deny, `write access required` |
| collaborator `write` push | ok |
| stranger fetch private | deny, message identical to a nonexistent repo |
| stranger push public | deny, `write access denied` |
| anonymous fetch public (phase 1, http) | ok |
| anonymous fetch private | deny, message identical to a nonexistent repo |
| revoked mid-session | current op may finish; next op denied (doc'd, phase-3 hook tightens) |

Test that stderr text is byte-identical for "no role + private" and
"genuinely missing" — that equality is the 404 veil, and it's the test most
forges skip.

## E2E smoke (phase 0 acceptance, script this exactly)

```sh
set -euo pipefail
TMP=$(mktemp -d)                                  # under macos: prefix with the opencode tmp dir
HOME_DIR=$TMP/gite-home
export GITE_HOME="$HOME_DIR"

gite migrate
gite admin create-user --username owner --email boat@example.com --password '-' <<EOF        # 8+ chars
EOF
ssh-keygen -t ed25519 -N '' -f "$TMP/owner_key" -C owner-smoke

gite keys add --user owner --title laptop --key "$TMP/owner_key.pub"
gite repos create owner/widget --private=false

(gite serve -addr 127.0.0.1:2222 &)               # background + trap kill
trap 'pkill -f "gite serve -addr 127.0.0.1:2222" || true' EXIT

export GIT_SSH_COMMAND="ssh -i $TMP/owner_key -p 2222 \
  -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null"

git clone  ssh://owner@127.0.0.1:2222/owner/widget.git "$TMP/wc"    # warns: empty repo
cd "$TMP/wc"
echo hello > README.md && git add . && git -c user.email=b@t -c user.name=b commit -m init
git push -u origin main
cd "$TMP" && rm -rf wc && git clone ssh://owner@127.0.0.1:2222/owner/widget.git wc2
grep -q hello wc2/README.md                        # the actual verification
```

Then the negatives (separate script, same fixture):

```sh
# stranger, no access to private repo:
gite repos create owner/valut --private=true       # note: misspell on purpose to test typo-tolerance? NO: typos are user bugs, keep name precise
gite admin create-user --username short-lived --email short@example.com --password long_enough_1
ssh-keygen -t ed25519 -N '' -f "$TMP/stranger_key"
gite keys add --user short-lived --title k --key "$TMP/stranger_key.pub"

ssh-keygen -R '[127.0.0.1]:2222' 2>/dev/null || true
GIT_SSH_COMMAND="ssh -i $TMP/stranger_key -p 2222 -o ..." \
  git clone ssh://short-lived@127.0.0.1:2222/owner/valut.git "$TMP/should-fail" \
  && echo "FAIL: clone should not have succeeded" \
  || grep -q "repository not found" <<< "$out"
```

And the grant then works path:

```sh
gite access grant owner/valut short-lived write
GIT_SSH_COMMAND=... $ git clone ssh://short-lived@127.0.0.1:2222/owner/valut.git "$TMP/ok"
git -C "$TMP/ok" commit; git push                    # now succeeds
gite access revoke owner/valut short-lived
... push fails: write access denied ...
```

## Phase 1 additions (http transport + webui)

```sh
gite serve                      # http listener up at cfg.HTTPAddr
curl -s localhost:<port>/owner/widget.git/info/refs?service=git-upload-pack | head -c 200
git clone http://owner@127.0.0.1:<port>/owner/widget.git
# PAT lifecycle: create → push ok → revoke → push 403
# CSRF test: POST /settings without token → 403
# README XSS fixture: push a README.md containing <script>alert(1)</script>;
# rendered page must contain it escaped.
```

## Unit fixtures worth writing before anything else

1. `shSplit` (quoted paths, unclosed quote error, multiple spaces, tab).
2. `parseGitCommand`: valid both verbs, wrong verb, extra args, three segments,
   `..`-containing segment, `.git` handling, absent path.
3. `Evaluate/Require`: every matrix row from the auth doc; identities: owner,
   site admin, reader, writer, stranger, anonymous (nil user).
4. Migration idempotence: `Migrate(); Migrate()` no error; `schema_migrations`
   one row version 1; drop tables → re-run full path with fixtures.
5. `config.Load` with `$GITE_HOME` unset/abs/relative variants (home override).
6. `git.RunIn("symbolic-ref", ...)` — run against a throwaway repo created in
   `t.TempDir()` per test rather than fixtures committed to the repo.

## Go best-practice reference set that applies to THIS repo (skim once, then enforce)

- Table-driven tests: <https://github.com/golang/go/wiki/TableDrivenTests> (and go.dev test guide),
  `t.Parallel()` only for truly independent cases.
- `httptest.ServeMux`-only handler tests (no extra routing deps).
- Error assertions: prefer `errors.Is` against package sentinels over string matching, except user-visible message equalities (which ARE a public contract here).
- Race-flag everything binding ports: `go test -race ./...` in CI once listeners exist.
- staticcheck for known foot-guns: <https://staticcheck.dev/>; `gofmt -s` baseline; no config file needed.
- Concurrency review: every `sync.WaitGroup` paired with explicit `Add` before spawn; every goroutine has an exit story (ctx, closed channel, or WaitGroup).

## CI sketch (later; don't block phase 0)

```yaml
# .gitea/workflows or any CI: three targets
#   make lint   → gofmt -s, go vet ./..., staticcheck
#   make unit   → go test ./...
#   make e2e    → scripts/e2e/ssh.sh + scripts/e2e/http.sh (targets real git)
```

Go test verbosity: keep `-run` scoped during dev; CI runs whole tree.

## Debugging recipes

| Symptom | Command chain |
| ------- | ------------- |
| clone hangs | `GIT_TRACE=1` then `GIT_TRACE_PACKET=1` (shows every pkt-line; the whole protocol visible) |
| auth claims ok, clone fails | check server log for the exec line (user, verb, repo, exit); run the same `git upload-pack /path` by hand |
| "repository not found" unexpectedly | `gite access list <owner>/<name>`; `sqlite3 gite.db 'select * from repos'` for the row |
| push works local, fails remote-side only | check `HEAD` symref mismatch (`git symbolic-ref HEAD <path>`), default branch drift |
| port in use | `lsof -i :2222`; `gite serve -addr 127.0.0.1:2299` for dev |
| webui renders nothing | dev: `curl -i` the route; template trust errors appear at parse (startup), data errors at render (404/500 page) |

## One doctrine (repeat before each PR)

**The transports are candidates for the internet. Write the negative tests
first for every new path** (unknown caller, malformed command, no access,
backend offline). Positives prove function; negatives prove the forge.
