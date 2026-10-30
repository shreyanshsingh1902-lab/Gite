# gite docs

Self-hosted git forge where repo storage is a choice. Native git is the engine,
gite is the gatekeeper and the manager. A repo can live on the gite server, on
another provider, or on your own machine behind NAT, reached through an agent
that dials out. The web UI manages; git stays the truth.

Pitch line: **"the git forge that doesn't own your code."**

The novel part is placement. Gitea, Forgejo, GitLab, SourceHut all assume repos
live on their servers. That assumption is the thing we're removing.

## What this folder is

Working design docs, not a paper trail. Write code with these open. Every doc
has: the design, the exact methods/tables needed, how other forges solved the
same problem (with file pointers into their source), a reading list, and
pitfall checklists.

## Reading order by phase

| You're building...        | Read                          |
| ------------------------- | ----------------------------- |
| Anything                  | architecture, README          |
| Phase 0 (forge floor)     | schema, auth, transport-ssh, repository-lifecycle |
| Phase 1 (web + smart HTTP)| transport-http, webui         |
| Phase 2 (agent)           | agent-distributed             |
| Phase 3 (placement)       | agent-distributed (half), providers section in modules |
| Before every PR           | integration-and-testing       |

## Doc index

- `architecture.md` — components, request lifecycles, git internals crash course, decisions, how Gitea/GitLab/SourceHut/Gitolite/soft-serve structure the same problem
- `schema.md` — every table, every column, why, plus Gitea/GitLab schema comparisons and sqlite production practices
- `auth-and-permissions.md` — roles, owner semantics, 404-vs-403 rule, auth doors (ssh key / PAT / session), method contract, OWASP references
- `transport-ssh.md` — git-over-ssh wire flow, built-in ssh server design, exit-status honesty, Gitea `serv.go` and GitLab `gitlab-shell` comparisons
- `transport-http.md` — git smart HTTP, `http-backend` CGI route vs hand-rolled, PAT auth, chunked-encoding pitfalls
- `repository-lifecycle.md` — bare repo layout, create/delete/rename/visibility, hooks, housekeeping, what GitHub/Gitea do
- `modules-and-methods.md` — the full method catalog per internal package with signatures, error semantics, and which phase each belongs to
- `webui.md` — route table, page inventory, README rendering pipeline, XSS/CSRF checklist
- `agent-distributed.md` — outbound-only agent protocol, honesty rules, radicle/GitLab-Geo/gitea-mirror prior art
- `integration-and-testing.md` — test pyramid, smoke scripts, access-matrix tests, debugging recipes

The overall phase plan lives in `.opencode/plan`. Note: `.opencode/drafts/`
holds a code sketch from an earlier round; consult it if useful, but the
authoritative design is these docs.

## Project ground rules

1. Native `git` binary does all git work. No packfile parsing, ever.
2. Never report a push as accepted until it hit that repo's source of truth.
3. Transport before pixels: SSH + smart HTTP working beats a pretty UI.
4. Single binary, sqlite for v1, templates and assets embedded via `go:embed`.

## Code conventions (summary, details in each doc)

- Module path: `gite` (already renamed in `go.mod`).
- Layout: `cmd/gite`, `cmd/gite-agent`, `internal/*`. See `modules-and-methods.md` for the full tree and tree rationale.
- Errors: sentinel errors per package (`ErrNotFound`, `ErrConflict`, `ErrInvalid`, `ErrAccessDenied`, `ErrOffline`, `ErrStorageMissing`), always wrapped with `%w`, user-safe wording only at the transport boundary. Internal details go to logs, never to clients.
- Context: first argument `ctx context.Context` everywhere that can block; transports pass the request ctx down cleanly.
- Config: load once at startup, pass explicitly. Flag > env > default. No globals except the loaded config.
- Logging: `log/slog`, structured, one line per user-visible network event (auth ok/reject, command handled).
- DB: parameterized queries only, `COLLATE NOCASE` for username/name columns, timestamps as UTC text via sqlite `datetime('now')`.
- Tests: stdlib `testing` only, table-driven, plus black-box E2E scripts that drive a real `git` client.

Style references you should skim once:

- Effective Go: <https://go.dev/doc/effective_go>
- Google Go style guide: <https://google.github.io/styleguide/go/>
- Uber Go style guide: <https://github.com/uber-go/guide/blob/master/style.md>
- Mat Ryer, "How I write HTTP services in Go after 13 years": <https://grafana.com/blog/2024/02/09/how-i-write-http-services-in-go-after-13-years/>
- database/sql tutorial: <http://go-database-sql.org/>

## How to read other forges' code (the skill this project teaches you)

Forges are readable. The pattern everywhere is: transport in -> resolve who
you are -> resolve which repo -> permission check -> exec the right git verb. When
you open a forge, look for those five hops:

- Gitea (Go, most similar to us): <https://github.com/go-gitea/gitea> — start at `cmd/serv.go` (ssh forced command), `routers/private/` (internal permission API), `routers/web/githttp/` (smart http), `models/perm/` (role model).
- Gogs (Go, gitea's parent): <https://github.com/gogs/gogs> — `routers/repo/http.go`, `cmd/serv.go`.
- GitLab (rails+go, scaled-out): <https://gitlab.com/gitlab-org/gitlab> — dev docs first: Development > Gitaly, Development > GitLab Shell. `gitlab-shell` repo: <https://gitlab.com/gitlab-org/gitlab-shell>.
- SourceHut (python, service-sliced): <https://man.sr.ht/git.sr.ht/> — API-first design, separate meta service for identity.
- soft-serve (Go, tiny ssh forge): <https://github.com/charmbracelet/soft-serve> — best single-file study of x/crypto/ssh serving git.
- Gitolite (perl, auth-only): <https://gitolite.com/gitolite/> — the minimal forced-command pattern we're reconsidering in phase 2+.

Rule of thumb: when a doc here says "how does Gitea do X?", grep its repo for
the verb (`upload-pack`, `receive-pack`, `AuthorizeKeys`, `pre-receive`) and
read the two files around the hit. Ten minutes per question beats an hour of
guessing.
