# modules & methods (the catalog you'll implement)

This is the contract you code against. Signatures here are normative: keep
names, keep the shape, adjust details as reality demands — then update this
doc in the same commit so juniors can trust it as ground truth.

Package map:

```
gite/                          # module (go.mod: module gite)
├── cmd/gite/                  # server binary
│   └── main.go                # dispatch: serve, migrate, admin, keys, repos, access
├── cmd/gite-agent/            # phase 2 (separate binary, agent)
├── internal/
│   ├── config/                # paths, listen addrs, flags
│   ├── db/                    # sqlite open, migrations
│   ├── auth/                  # users, keys, tokens, sessions, permissions
│   ├── repository/            # repo catalog lifecycle (bare dir management)
│   ├── git/                   # thin exec wrapper over the git binary
│   ├── gateway/               # transport doors: ssh (phase 0), http (phase 1)
│   ├── webui/                 # phase 1: server-rendered viewer/site
│   ├── agent/                 # phase 2: outbound client + its server side
│   └── providers/             # phase 3: github/gitlab adapters, mirrors
├── docs/                      # these files
├── scripts/                   # smoke/e2e scripts (executable docs, see testing doc)
└── web/                        (kept empty until webui embeds templates)
```

Dependency rule (make it a lint note, then enforce by review):
`config → db → auth ⇄ repository → git → gateway → webui`, with `agent`,
`providers` depending only downwards. `auth` defines the `RepoInfo` interface
precisely so `repository` implements it without importing auth (except error
sentinels — permitted, they're values).

Wait, correction, make the graph purely downward:

```
providers → agent → webui → gateway → repository → auth → db → config
                                  ↘ git (used by repository & gateway & webui)
```

`git` is a leaf package (imports nothing internal). `auth` imports `db` only.
`repository` imports `auth` for sentinels + user model passed in. Each
internal package may import its own parent only in the downward direction
(each file's import list stays readable by eye).

## internal/config

```go
type Config struct {
    Home     string // data dir; also default location for ssh host key
    DBPath   string // sqlite file
    RepoRoot string // bare repos: <home>/repos/<owner>/<name>.git
    SSHAddr  string // "127.0.0.1:2222";  env GITE_SSH_ADDR; flag -addr
    HTTPAddr string // phase 1: web+git-http listener; env GITE_HTTP_ADDR
}

func EnvHome() string                                  // $GITE_HOME or ~/.gite
func Load(home string) (*Config, error)                // resolves absolutes; flag/env/defaults
func (c *Config) Ensure() error                        // mkdirall home + repoRoot
```

## internal/db

```go
type DB struct{ *sql.DB }

func Open(path string) (*DB, error)         // pragmas: wal, foreign_keys, busy_timeout
func (d *DB) Migrate() error                // embedded migrations v1..n; tx per step
func (d *DB) Close() error
func (d *DB) WithTx(ctx, fn func(tx *sql.Tx) error) error   // helper: Begin/defer Rollback/Commit

func IsUnique(err error) bool    // driver-agnostic UNIQUE-constraint test
// migrations as []string consts inside db.go (or migrate.go), versioned in
// schema_migrations(version) — the pattern gitea uses under models/migrations/
```

## internal/git

Thin, no state, no sqlite. Everything is safe argv exec; inputs validated by
callers (gateway validates external input; repository passes only catalog-
derived paths).

```go
func Binary() (string, error)
func Run(ctx context.Context, args ...string) (string, error)          // cwd=none
func RunIn(ctx context.Context, dir string, args ...string) (string, error)  // cwd=dir
func Version(ctx context.Context) (string, error)                      // `git --version` parse

// phase 0/1 readers (used by webui + repair + mirror plumbing):
func ForEachRef(ctx, repoPath, pattern string) ([]Ref, error)   // for-each-ref --format=...(z-safe format)
func IsEmpty(ctx, repoPath string) (bool, error)   // ForEachRef(refs/heads) has zero rows
func DefaultBranch(ctx, repoPath string) (string, error)        // read HEAD symref (symbolic-ref --short HEAD)
func HeadCommit(ctx, repoPath string) (*Commit, error)

// phase 1 web viewer, listed here because the *method* belongs to git pkg:
func Tree(ctx, repoPath, ref, dir string, recursive bool) ([]TreeEntry, error) // ls-tree -z
func Blob(ctx, repoPath, ref, path string, max int) ([]byte, error)            // cat-file blob; obey max
func Log(ctx, repoPath, ref string, limit, skip int) ([]Commit, error)         // log --pretty=<z-format>
func Commit(ctx, repoPath, sha string) (*Commit, error)                        // cat-file commit + parse
func Diff(ctx, repoPath, a, b string) ([]byte, error)                          // diff a b (unsafe-free; use fixed args)
```

Type sketches (keep fields minimal, porcelain-parse in this package only):

```go
type Ref struct { NameShort, ObjectName, Kind string /* kind: branch|tag */ }
type Commit struct { SHA, Tree, Parent string; Author, Committer Signature; Message string }
type Signature struct { Name, Email string; When time.Time }
type TreeEntry struct { Mode string; ObjectName; Name; Type /* blob|tree|commit(submodule) */ }
```

Parsing doctrine: prefer `-z` (NUL) porcelain outputs; never split on spaces;
never shell (`git log --pretty=format:%H%x00%an%x00...`).

Reference reading once: Pro Git "Git References" and "Packfiles" chapters:
<https://git-scm.com/book/en/v2/Git-Internals-Git-References>

## internal/auth

Full contract lives in `auth-and-permissions.md` — that file is the spec;
signatures are reproduced there and not repeated here to avoid drift. Files:
`users.go`, `keys.go`, `perm.go`, `token.go` (phase 1), `session.go` (phase 1).

Sentinel errors defined by this package and consumed everywhere downstream:
`ErrNotFound, ErrConflict, ErrInvalid, ErrAccessDenied`. Error wrapping style:

```go
return nil, fmt.Errorf("%w: user", ErrNotFound)                          // veil-safe wording
return nil, fmt.Errorf("hash password: %w", err)                          // ops context for logs
```

## internal/repository

```go
type Repo struct { ... }         // with RepoID()/OwnerUserID()/IsPrivate() satisfying auth.RepoInfo

type Store struct{ DB *db.DB; RepoRoot string }

func NewStore(d *db.DB, repoRoot string) *Store
func (s *Store) Path(r *Repo) string                  // <root>/<owner>/<name>.git
func (s *Store) HeadPath(r *Repo) string              // <path>/HEAD (drift check)

// phase 0
func (s *Store) Create(ctx, owner *User, name, desc string, private bool, branch string) (*Repo, error)
func (s *Store) GetByOwnerName(ctx, owner, name string) (*Repo, error)   // ErrNotFound
func (s *Store) Delete(ctx, *Repo) error                              // row first, dir after
func (s *Store) List(ctx, ownerOptional string) ([]*Repo, error)

// phase 1
func (s *Store) GetByID(ctx, id int64) (*Repo, error)
func (s *Store) SetVisibility(ctx, *Repo, private bool) error          // single UPDATE
func (s *Store) SetDescription(ctx, *Repo, desc string) error
func (s *Store) Rename(ctx, *Repo, newName string) error               // dir+row atomic-ish; see lifecycle doc
func (s *Store) CountByOwner(ctx, ownerID int64) (int, error)

// phase 2/3 (stubs now, implemented when the backend interface lands)
func (s *Store) SetBackend(ctx, *Repo, backend string) error
```

Invariants (engineer-contract): create/delete/rename are the only writers of
disk layout; disk ops always use `s.Path(r)`; no other package ever constructs
repo paths from raw strings.

## internal/gateway

### ssh  (phase 0 — detailed design in transport-ssh.md)

```go
type SSHServer struct{ ... }

func NewSSHServer(cfg config.Config, d *db.DB, log *slog.Logger) (*SSHServer, error) // host key gen/load
func (s *SSHServer) ListenAndServe(ctx) error

// internal, unit-tested as pure functions (see testing doc):
func shSplit(s string) ([]string, error)                       // ' and " handling
func parseGitCommand(raw string) (verb, owner, name string, err error)
     // "|<owner>/<name>.git" only two segments; regex checks; no fs access
func (s *SSHServer) authorize(user, raw string) (*repository.Repo, string /*verb*/, error)
     // 404-veil mapping, role decision, disk drift check
func (s *SSHServer) runGit(user string, ch ssh.Channel, raw string, env []string)
```

### http  (phase 1 — detailed design in transport-http.md)

```go
type HTTPServer struct{ ... }

func NewHTTPServer(cfg config.Config, d *db.DB, log *slog.Logger) *HTTPServer
func (h *HTTPServer) Handler() http.Handler       // composable; used both by `gite serve` and tests (httptest)

routes served (all under /{owner}/{repo}.git/):
    infoRefs (GET, honors ?service=), uploadPack (POST), receivePack (POST)
// auth middleware: Basic token → PAT; anonymous allowed iff public + read
// the git child is re-executed fresh per request (stateless); CGI handoff env
// contract lives in transport-http.md
```

## internal/webui (phase 1 — full spec in webui.md)

Route table with role requirements is in `webui.md`; method shape here:

```go
type Server struct { ... }   // cfg, db, repos store, templates, repoGit service
func New(cfg, d, log) *Server
func (s *Server) Handler() http.Handler
// handler pattern (makeHandler, Mat Ryer style):
func (s *Server) repoHome(repo *Repo | loadmw) http.HandlerFunc
```

## internal/agent (phase 2 — full design in agent-distributed.md)

```go
// client (the machine hosting canonical repos)
func Run(ctx, cfg) error     // dials out, holds tunnel, execs receive/upload locally

// server side, consumed by gateway:
type Registry struct{ ... }  // agents, agent_repos
func (r *Registry) resolve(repoID) (*AgentRoute, error)      // ErrOffline when agent down
// protocol messages are in agent-distributed.md tables (framing, envelopes)
```

## internal/providers (phase 3 sketch)

```go
type Provider interface {
    ParseURL(ctx, rawURL string) (*RemoteRepo, error)     // kind inference: github/gitlab/generic
    FetchRefs(ctx, remote RemoteRepo) ([]git.Ref, error)
    PushRefs(ctx, remote RemoteRepo, updates []RefUpdate) error
    Exists(ctx, remote RemoteRepo) (bool, error)
}
// Mirror service uses it inside a per-repo worker; never in request paths.
```

## Cross-cutting contracts every package honors

1. **ctx** travels; no `context.TODO()` in committed code except `main()`.
2. **Errors**: wrap with `%w`; boundary maps to user wording once; logs get
   the details (`slog` fields `user`, `repo`, `verb`, `duration`, `exit`).
3. **Time** as `time.Time` (UTC) until it touches sqlite (`created_at` string
   is a db-layer artifact; apps don't parse it except display).
4. **No package does its own config loading** — cfg comes in as a value.
5. **No `init()`** anywhere. Manual construction in cmd/gite keeps the
   call graph obvious and testable.
6. **Concurrency at the process level**: ssh/http servers own goroutines per
   conn; db serializes writes (single conn); repos never mutated concurrently
   except through git (which is internally consistent). The design freedom is
   deliberate (see sqlite doc: one writer).
7. **Exported = documented**; docs here refer to exported names, never to
   internal variable names, to keep this file authoritative without churn.

## Testing hooks of note (see integration-and-testing.md for the drivers)

- Each `internal/*` keeps pure functions pure (parsing, role math) for
  fixture-free unit tests.
- Integration layers (Store, gateway servers, webui) zip up with a temp
  `GITE_HOME` per test via `config.Load(t.TempDir())`.
- Transports are black-box tested through `scripts/` running real git clients,
  because the wire is the thing we're judged by.
