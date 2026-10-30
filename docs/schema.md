# gite schema

The sqlite catalog. One migration file per version, forward-only, embedded
into the binary. Everything here is policy metadata — the git history itself
never lives in this database.

## Global conventions

- Driver: `modernc.org/sqlite` (pure Go, no cgo — keeps the single-binary property). Driver docs: <https://pkg.go.dev/modernc.org/sqlite>
- Enable pragmas per connection: `WAL` (concurrent readers, single writer),
  `foreign_keys(1)` (gite relies on cascade deletes), `busy_timeout(10000)` (checkpoint contention must not live-fail).
- Timestamps: `TEXT`, UTC, produced by `datetime('now')` — human-readable in `sqlite3`, sorts correctly, zero driver hoop-jumping for phase 0. If it ever hurts, a tiny view/`darwin` upgrade swaps to integer epoch; not now.
- Identifiers: `INTEGER PRIMARY KEY` (sqlite rowid alias — free, fast).
- Usernames, repo names, emails: `COLLATE NOCASE` so `Alice` and `alice` collide at the database layer. Auth decisions and on-disk paths must agree with this: canonical casing is lowercase.
- Blob-ish secrets: never stored raw. PATs: sha256 of `gite_pat_<random>`; sessions: sha256 of cookie content.
- Every table exists for a named query or a transport decision. If you can't name one, don't create the table.

## Version 1

```sql
CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
    email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT NOT NULL,              -- bcrypt, never plaintext, never used by transports yet
    is_admin      INTEGER NOT NULL DEFAULT 0, -- site administrator
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE ssh_keys (
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,                -- "laptop", "ci-runner"
    fingerprint TEXT NOT NULL UNIQUE,         -- SHA256:... — THE auth handle for the ssh door
    key_type    TEXT NOT NULL,                -- ssh-ed25519, ecdsa-sha2-nistp256...
    pubkey      TEXT NOT NULL,                -- canonical "<type> <base64 blob>" (authorized_keys compatible)
    comment     TEXT NOT NULL DEFAULT '',
    added_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE pat_tokens (                    -- phase 1 (smart http + api)
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,             -- "ci"
    token_hash   TEXT NOT NULL UNIQUE,      -- sha256 hex of the token; the raw token is shown once
    scopes       TEXT NOT NULL DEFAULT '',  -- csv of scopes; v1: 'git' only, then 'api'
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at   TEXT,                      -- null = non-expiring (req: show warning in UI)
    last_used_at TEXT
);

CREATE TABLE sessions (                      -- phase 1 (web viewer)
    id          INTEGER PRIMARY KEY,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    cookie_hash TEXT NOT NULL UNIQUE,
    expires_at  TEXT NOT NULL
);

CREATE TABLE repos (
    id             INTEGER PRIMARY KEY,
    owner_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name           TEXT NOT NULL COLLATE NOCASE,
    description    TEXT NOT NULL DEFAULT '',
    private        INTEGER NOT NULL DEFAULT 1, -- fail closed: default-private
    backend        TEXT NOT NULL DEFAULT 'server', -- server | agent | external | mirror
    default_branch TEXT NOT NULL DEFAULT 'main',
    created_at     TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (owner_id, name)
);

CREATE TABLE repo_collabs (
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role    TEXT NOT NULL CHECK (role IN ('read','write','admin')),
    PRIMARY KEY (repo_id, user_id)
);
-- note: owner row is intentionally NOT present; owner => admin is computed,
-- so renaming the owner role semantics later never requires a data migration.

CREATE TABLE agents (                        -- phase 2
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,             -- "workstation"
    status       TEXT NOT NULL DEFAULT 'offline', -- offline | online | degraded
    last_seen_at TEXT,
    UNIQUE (user_id, name)
);

CREATE TABLE agent_repos (                   -- phase 2
    agent_id INTEGER NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    path     TEXT NOT NULL DEFAULT '',      -- bare repo path on the agent machine
    PRIMARY KEY (agent_id, repo_id)
);

CREATE TABLE repo_remotes (                  -- phase 3
    id         INTEGER PRIMARY KEY,
    repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    url        TEXT NOT NULL,
    kind       TEXT NOT NULL DEFAULT 'generic', -- generic | github | gitlab | ...
    is_primary INTEGER NOT NULL DEFAULT 0,
    UNIQUE (repo_id, url)
);

CREATE INDEX idx_ssh_keys_fingerprint ON ssh_keys (fingerprint);
CREATE INDEX idx_repos_owner          ON repos (owner_id);
```

## Phase 3 candidates (park until needed)

- `webhooks(id, repo_id, url, events, secret_hash, enabled, last_result)` — phase 3/4.
- `protected_branches(repo_id, name, patterns, min_role, force_hook_id)` — phase 3, enforced via pre-receive hook.
- `mirrors(repo_id, source_remote_id, direction, interval, last_synced_at, state)` — phase 3.
- `audit_log(id, actor_id, action, target_kind, target_id, at, detail)` — add when the second admin exists.

## Hot queries (know what you're optimizing)

| Query | Caller | Index serving it |
| ----- | ------ | ---------------- |
| user by ssh fingerprint | every ssh conn (hot) | `ssh_keys.fingerprint` unique |
| repo by (owner username, name) | every transport op (hot) | the join through `users.username` (NOCASE) + `repos(owner_id, name)` unique |
| repos owned by user | web user page | `idx_repos_owner` |
| collaborators of repo | transport authz + settings page | PK `(repo_id, user_id)` |
| token by hash | every http git call | `pat_tokens.token_hash` unique |

The transport-resolve query is the hot one: one join, always. Later (if
pushes/second reach five figures/sec — they won't for a while) you can denormalize
`repos.owner_username` to skip the join; don't pre-optimize this.

## Invariants the code must hold

1. `ssh_keys.fingerprint` unique across ALL users: one key = one identity,
   revoking a user kills their keys via cascade and simultaneously revokes transport access.
2. `repos.backend='server'` ⇔ bare dir exists under `<repo-root>/<owner>/<name>.git`. Code that breaks this either repairs or refuses at the transport; never recreates history.
3. `private=0` + `backend='agent'` is legal (public repo, truth on your laptop) — the web reader and anonymous clone flow must route through the tunnel in phase 2. Plan for that routing now: never write "if private then check else allow" without asking which backend.
4. Owner never appears in `repo_collabs`. Owner role = admin by computation.
5. Delete flows: user delete cascades ssh_keys, sessions, tokens; repo rows cascade collabs + agent_repos + remotes; the on-disk bare repo is deleted by code *after* row deletion — row-first ordering is documented in `repository-lifecycle.md`.

## How other forges model this (steal the good parts)

### Gitea / Forgejo (most directly comparable)

- Tables `user`, `repository`, `access`, `ssh_key`, `email_address`, `_mirror`... in `models/` (see `models/user/user.go`, `models/repo/repo.go`, `models/perm/access/access_mode.go`).
- Notable differences from ours:
  - Gitea stores access as ints (`1=read,2=write,3=admin`) in an `access` table — same shape as ours, different rendering.
  - Gitea `user` table has `passwd`, `salt`, `passwd_hash_algo` — they've migrated hash algorithms over the years. The `passwd_hash_algo` column exists precisely because they got burned; if we ever migrate from bcrypt, add the algo column *now* (cheap: `ALTER TABLE users ADD COLUMN ...` in a later migration) instead of brainstorming.
  - Gitea usernames are case-insensitive too (they renamed a whole migration family to fix historical collisions).
- Their migrations live under `models/migrations/` — one file per version bump, forward-only. Match that discipline.

### GitLab

- Postgres: `users`, `namespaces`/`projects`, `members` (access levels `10 guest, 20 reporter, 30 developer, 40 maintainer, 50 owner`), `keys`.
- The numeric ladder with named names is a nice knob when you add orgs; our `read/write/admin` triad is the minimal version. When phase 3 needs orgs, prefer GitLab's numeric ladder over accumulating booleans.
- GitLab keeps `protected_branches` + `push_access_levels` as separate rows — branch protection is data, not code. Model ours the same way when it arrives.

### Gogs

- Nearly the same shape as early Gitea (same lineage). Grep-friendly, good for reading *simple* versions of the same idea.

### SourceHut

- Split into multiple services (meta.sr.ht owns users/keys; git.sr.ht references them by ID). Note the audacity: SSH auth prompts in `gitssh.sr.ht` call back to meta only for identity, authz stays with the repo service. We get the same outcome from one db; the pattern matters when gite grows federation teeth.

## sqlite production practices (read before the first schema change)

- WAL mode explainer: <https://www.sqlite.org/wal.html> (also read `https://www.sqlite.org/pragma.html` entries for `journal_mode`, `busy_timeout`, `foreign_keys`)
- Fly.io's sqlite articles are the best practical summaries of "sqlite as a real app database": <https://fly.io/blog/> (search "SQLite")
- Litestream docs explain single-writer/Multi-reader replication trade-offs (relevant for phase 2 mirrors): <https://litestream.io/topics/>
- `modernc.org/sqlite` quirks: it's a translation of C sqlite via libcc; performance is fine for our load; `database/sql` ergonomics are standard: <http://go-database-sql.org/>
- Migration doctrine: forward-only, never edit an applied migration, additive-first (add column, backfill, then change writer code), test that applying twice is a no-op. Gitea's `models/migrations/` is the reference layout.
- Don't use `AUTOINCREMENT` (bloats `sqlite_sequence`); `INTEGER PRIMARY KEY` reuses freed rowids — irrelevant here because we cascade-delete whole subtree on repo delete (we accept id reuse; we never persist foreign ids across restarts anyway).

## Pitfall checklist

- [ ] No raw string interpolation anywhere near SQL (`?` placeholders only).
- [ ] Every FK has `ON DELETE CASCADE` on child rows (agents too) — deleting a user must not require a choreography of partial deletes.
- [ ] Unique indexes do double duty as fastpaths (fingerprint, cookie_hash, token_hash, (owner_id, name)) — no duplicate secondary index without a distinct query in need of it.
- [ ] `private` default `1` (fail closed). Public must be a considered action.
- [ ] Bcrypt cost documented (`bcrypt.DefaultCost` ~ fine); no sha1/sha256-as-password-hash ever. Reference: OWASP password storage cheat sheet <https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html>
- [ ] No storing raw PAT/cookie values; store hashes and show raw only once at creation time.
