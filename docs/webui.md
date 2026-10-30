# webui (phase 1)

The gite web viewer + management UI. Server-rendered, no SPA, no build chain.
The design bar is SourceHut/gitea-plain: every page fast, every form a plain
POST, no JS required for any core interaction.

## Principles (with receipts)

1. **HTML is the API.** If a page needs to be screen-scraped, the page failed.
   Every write is a form POST handled by the CLI-parity code path (`repository`
   and `auth` packages), never a private codebase.
2. **Roles are decided before templates render.** The middleware decides
   view/act; templates only *format*.
3. **Default-secure**: `X-Frame-Options: DENY`, CSP starter (below), cookies
   HttpOnly+SameSite=Lax, CSRF token on every POST.
4. **Templates compile once, `go:embed`-ed**; static css/js in `web/` (minimal
   css file + a tiny script only for confirm-on-delete dialog).
5. Read everything below about README rendering — that's where forges hurt
   users (injection) most.

## Route table (method + handler + role)

| Route | Method | Handler sketch | Role |
| ----- | ------ | -------------- | ---- |
| `/` | GET | home: own repos + explore (public) | any/anon |
| `/register`, `/login` | GET/POST | no registration in phase 1 — CLI-created only; `/login` uses username+password | anon |
| `/logout` | POST | destroy session | user |
| `/settings/profile` | GET/POST | name, email (display fields only) | user |
| `/settings/keys` | GET/POST/DELETE | ssh keys: add (title+key), remove | user |
| `/settings/tokens` | GET/POST/DELETE | PATs: create (name, expiry, scope `git`), revoke; raw shown once | user |
| `/settings/password` | GET/POST | change password (requires current) | user |
| `/{owner}` | GET | user page: repos visible to viewer | based on repo rows |
| `/new` | GET/POST | create repo (owner=current user) | user |
| `/{owner}/{repo}` | GET | repo home: readme, file list, latest commit, clone URLs (ssh+http) | read |
| `/{owner}/{repo}/tree/{ref}/*` | GET | dir browse | read |
| `/{owner}/{repo}/blob/{ref}/*` | GET | file view (+ line anchors) | read |
| `/{owner}/{repo}/raw/{ref}/*` | GET | raw bytes (safe content-disposition) | read |
| `/{owner}/{repo}/commits/{ref}` | GET | log, paginated | read |
| `/{owner}/{repo}/commit/{sha}` | GET | one commit + diff | read |
| `/{owner}/{repo}/branches` · `/tags` | GET | lists | read |
| `/{owner}/{repo}/blame/{ref}/*` | GET | `git blame --porcelain` render (phase 1.5, skip at first) | read |
| `/{owner}/{repo}/settings` | GET | repo settings | admin |
| `/{owner}/{repo}/settings/collaborators` | GET/POST | add user, set role, remove | admin |
| `/{owner}/{repo}/settings/danger` | GET/POST | rename, visibility flip, delete (`--yes` parity: typed name confirm) | admin |
| `/admin` | GET | site admin: users list, force actions | site admin |

Middleware chain (order matters, top-down):

```
recoverPanic → logRequest → loadSession (cookie; optional) → route
route-scoped: requireLogin. Reponame routes: loadRepo (resolve owner/name →
Repo + currentRole) → requireRepoRole(read|write|admin) → handler.
```

`loadRepo` returns 404 (the auth rule) when the repo is invisible, which also
covers bad names too. Failure mapping again per `auth-and-permissions.md` (404
vs 403), never exceptions.

## Template layout

```
web/
├── templates/
│   ├── base.html            # <html>, nav (session-aware), flash messages
│   ├── home.html, login.html, error.html
│   ├── user.html
│   ├── settings/{keys,tokens,profile,password}.html
│   └── repo/
│       ├── layout.html      # repo header: name, vis badge, tabs (code/commits/branches)
│       ├── home.html, tree.html, blob.html, commits.html, commit.html
│       ├── branches.html, tags.html
│       └── settings/{home,collaborators,danger}.html
└── static/                  // a single css file; go:embed both dirs
```

`{{template "head" ...}}`-style composition; keep partials small; no template
logic beyond `if`/`range` (the data structs carry pre-shaped info: canonical
URLs, role flags, breadcrumbs).

Data context pattern (per route family):

```go
type repoContext struct {
    Repo *repository.Repo
    Role auth.Role              // viewer's
    Ref  string                  // resolved ref (sha or branch)
    PreComputed        // tree/commit slices per page, filled by handlers (see git pkg docs)
}
```

## README rendering pipeline (the one that forks copy, badly)

Order of operations for the repo home page:

1. Locate readme: file root list lookup case-insensitive among the standard
   names (`README.md`, `README.markdown`, `README.txt`, `README`) — cache by
   `(repo, ref)` only *at runtime*, no persisted cache.
2. `.md`/`.markdown` → **goldmark** (GFM, tables, yaml front matter off),
   then rewrite in-repo image/src links to `/raw/{ref}/...` (relative path
   resolution keyed by the source file's directory).
3. **Sanitize with bluemonday UGCPolicy** + extensions to allow class lists —
   before storing into template context. Never templating the raw HTML.
   <https://github.com/microcosmos-cc/bluemonday>
4. Syntax highlight code fences with **chroma** (class-based, css is our one
   stylesheet): <https://github.com/alecthomas/chroma>
5. Cap rendering sizes: blobs > 512 KiB are linked to raw instead of rendered
   (github's rule, avoids CPU amplification: <https://docs.github.com/en/repositories/working-with-files/using-files/viewing-a-file>).
6. Keep `javascript:` hrefs out (bluemonday does); add `rel="nofollow noreferrer"`
   on external `<a>` links; constrain rendered `<img>` heights via css.

Gitea's markup service (services/markup) is the reference implementation of
this order; they've had a decade of CVE fun reinforcing the lesson:
use goldmark + bluemonday, not markdown-the-legacy-lib.

## Diff and blame rendering (phase 1 late)

- `git diff --no-color <a> <b>` parse or better: run
  `git diff-tree --no-commit-id --r -p <sha>`? — either way, render server-side
  line-numbered with unified-split views. Libraries: sourcehut does this in
  ~200 lines of python templates; ours fits.

## Security checklist (pin into tests; webui is the internet-visible face)

- [ ] **CSRF**: session cookie SameSite=Lax + per-session CSRF token in every
      hidden form field; `test:` POST without token → 403.
- [ ] **Cookies**: HttpOnly, Secure (when TLS), Path=/, rotate cookie value at
      login; logout deletes db session. `test:`/login sets, /logout clears.
- [ ] **CSP**: `default-src 'self'; img-src 'self' data:; style-src 'self';
      script-src 'self'` — inline scripts absent from templates is the point.
- [ ] `test:` XSS: commit message containing `<script>` renders as text in
      commits page and as sanitized content in readme (fixture in tests).
- [ ] `test:` path traversal: `/tree/main/../...` and `%2e%2e` → handled by git
      ref parsing + our router which never passes raw URL segments to fs; each
      attempt → 404.
- [ ] `test:` anonymous access of private repo → identical 404 shape as fake repo
      (byte-equal blank page), no leak of "this repo exists".
- [ ] Clickjacking headers; no referrer-leak on external links (or do the only
      sane thing: noreferrer on all external anchors).
- [ ] Upload size caps on POST bodies (e.g. 1 MiB) except the git transport
      subtree, which has its own (see transport-http).
- [ ] Admin actions audit-logged (slog with actor field) — phase 1 keep it to
      one table row insert when audit table arrives.

## What the forges do differently (reference for future phases)

- **Gitea**: base templates + composable partials (`templates/base/`),
  per-context `ctx.Data[...]` maps (fragile but fast). Read
  `templates/base/head_navbar.tmpl` once; it's the best short example of
  "everything is a partial" done tolerably.
- **GitLab**: rails templates + haml, heavy webpack SPA for newer bits.
  Explicitly not our path; but their *repo home page inertia* (readme +
  commit list + stats) is a good UX bar.
- **SourceHut**: the whole UI is server-rendered python templates with almost
  no css; every page is a table + links. The most readable example: <https://man.sr.ht/git.sr.ht/>
- **Forgejo / git.sr.ht blame pages**: they render blame server-side, line by
  annotated with latest commit — it's just `blame --porcelain` json parsed;
  our phase 1.5 step is the same thing, no more.

## Implementation order (so code review can track it)

1. server skeleton + base templates + login/logout + session cookie
2. home + user page + repo create/list (CLI parity on the same packages)
3. repo home: file tree, blob, README pipeline
4. commits, commit, branches, tags
5. repo settings: collaborator CRUD, visibility, danger zone; delete flows
6. raw + blame (delayed) + error page consistency pass
7. CSP + CSRF test matrix green; doc update

Everything served from one binary on `HTTPAddr` alongside the git transport
routes (same mux, prefix-differentiated). `gite serve` runs both listeners.
