# gite auth & permissions

Objective: the github-style experience at the smallest defensible size. Read
`schema.md` first (tables referenced here live there), then this, then write
`internal/auth/*`.

## The model

Users only in v1; orgs and teams wait for phase 3 (when the data model needs
to impress, revisit GitLab's numeric access ladder).

Roles per repository: `none < read < write < admin`.

- **Owner**: the user who created the repo (`repos.owner_id`). Owner is
  *implicitly admin* — never a row in `repo_collabs`, always computed. This
  means role semantics never need a data migration on owner changes.
- **Site admin** (`users.is_admin`): implicitly admin on every repo. Needed
  for a self-hosted operator to fix things; document that power loudly in the
  UI ("site administrators can view this repository").
- **Collaborators**: explicit rows in `repo_collabs`.
- **Anonymous**: `none` on private repos; `read` on public repos.
  Authenticated non-collaborators: same rule.

## Operation × role matrix (print this, test against it)

| Operation | none | read | write | admin | owner | site admin |
| --------- | ---- | ---- | ----- | ----- | ----- | ---------- |
| clone/fetch (upload-pack) | public only | yes | yes | yes | yes | yes |
| push (receive-pack) | no | no | yes | yes | yes | yes |
| view web pages (repo) | public only | yes | yes | yes | yes | yes |
| manage collaborators | no | no | no | yes | yes | yes |
| delete/rename repo, flip visibility | no | no | no | yes | yes | yes |
| create/delete users (CLI/API admin) | site admin only |
| create the repo | any user; becomes owner |

The single choke point:

```go
// Decide-and-explain, not decide-and-shrug.
func Evaluate(ctx context.Context, d *db.DB, u *User, ri RepoInfo) Role
func Require(ctx context.Context, d *db.DB, u *User, ri RepoInfo, want Role) error
```

`RepoInfo` is an interface implemented by `repository.Repo`:

```go
type RepoInfo interface {
    RepoID()       int64
    OwnerUserID()  int64
    IsPrivate()    bool
}
```

Every door calls `Require` with the role each operation demands:
clone → `RoleRead`, push → `RoleWrite`, repo settings → `RoleAdmin`,
admin commands → caller must be `User.IsAdmin`.

## The 404 rule (steal this from GitHub)

**A private repo you may not read does not exist.** Return not-found, never
forbidden. Rationale: "403" is a confirmation oracle — it tells an attacker
"this owner has a repo with exactly this name". 404 is a quieter world and it
needs zero additional code beyond one mapping. Gitea does this too (`routers/web/repo` → notFound page).

Boundary wording standard:

- `read` shortfall on private → error text `repository not found`, HTTP 404.
- `write` shortfall on a public repo (or readable private) → error text
  `write access denied on owner/name`, HTTP 403 in web, transport failure over git.
- admin shortfall → 404 as well (existence of the page isn't for you).

## The three auth doors, one identity

| Door | Credential | Resolution | Transport specifics |
| ---- | ---------- | ---------- | ------------------- |
| git-over-ssh | ssh public key | fingerprint match into `ssh_keys` → user | key IS identity; username in URL is display-only; server `-p 2222` built-in |
| git-over-http (phase 1) | personal access token via Basic auth | sha256(token) → `pat_tokens` → user + scopes | never accept web cookies for git transport |
| web viewer (phase 1) | session cookie (login form) | sha256(cookie) → `sessions` → user + expiry | HttpOnly, Secure, SameSite=Lax; CSRF token on all POST forms; never accept PATs in forms for session login |

Door-sharing rules (write them in code comments, test for them):

1. A PAT used over Basic for git must be a separate capability from a session:
   revoking a token must not log a user out; changing password must kill all
   sessions (and not tokens; they're explicit).
2. ssh login failure is cheap to observe but must be rate-limitable: log warn
   per rejected attempt with fingerprint + remote (internal/gateway does this).
3. Token creation returns the raw token exactly once; store only the hash
   (prefix raw ones `gite_pat_` so secret tools can detect them in logs).

## Method contract for `internal/auth`

```go
// users.go
CreateUser(ctx, d, username, email, password, admin) (*User, error)
    // lowercases username; bcrypt(password); Username/Email uniques → ErrConflict
    // errors: ErrInvalid (len/email/password 8+), ErrConflict
GetUserByUsername(ctx, d, username) (*User, error)      // ErrNotFound
ListUsers(ctx, d) ([]*User, error)
// phase 1: VerifyPassword(ctx, d, username, password) (*User, error)   // constant-time bcrypt compare
// phase 1: UpdatePassword, DeleteUser (site-admin actions)

// keys.go
ParsePublicKey(data []byte) (ssh.PublicKey, comment string, err error)
AddKey(ctx, d, u, title, rawKeyData) (*SSHKey, error)
    // fingerprint = ssh.FingerprintSHA256 (SHA256:19 chars, collision-proof for our scale)
    // unique fingerprint → ErrConflict ("already registered to another account")
UserKeyByFingerprint(ctx, d, fingerprint) (*SSHKey, *User, error)   // the ssh-door hotpath
ListKeys(ctx, d, u) ([]*SSHKey, error)
RemoveKey(ctx, d, fingerprint) error                                  // ErrNotFound if absent

// perms.go
type Role int                // RoleNone < RoleRead < RoleWrite < RoleAdmin (comparable!)
ParseRole(s string) (Role, error)
Evaluate(ctx, d, u, ri RepoInfo) Role      // owner/siteadmin => admin; collab row; public => raise to read
Require(ctx, d, u, ri, want Role) error    // messages per the 404 rule
Grant(ctx, d, repoID, userID, role) error  // upsert; validating caller-side that userID != ownerID
Revoke(ctx, d, repoID, userID) error       // ErrNotFound when no row
ListCollabs(ctx, d, repoID) ([]*Collab, error)

// tokens.go  (phase 1)
CreateToken(ctx, d, u, name, scopes, ttl) (rawToken string, err error) // returns ONCE
VerifyToken(ctx, d, rawToken) (*TokenGrant, error)  // hash-compare via sha256; Equality must be constant-time (hmac energy wasted — sha256+subtle.ConstantTimeCompare)
ListTokens / RevokeToken

// sessions.go (phase 1)
CreateSession(ctx, d, u, ttl) (cookieValue string, err error)
UserSession(ctx, d, cookieValue) (*User, error)  // expiry checked, sliding window option
DeleteSession(ctx, d, cookieValue) error
DeleteAllSessions(ctx, d, u) error               // on password change
```

Error taxonomy (all packages converge on these):

- `ErrNotFound` — the thing may or may not exist; caller decides veil.
- `ErrConflict` — unique violation, human-differing text.
- `ErrInvalid` — input rejected pre-storage.
- `ErrAccessDenied` — authz is the cause; renders 403 (or transport message).
- `ErrStorageMissing` — catalog row ok, bare dir missing (see repository-lifecycle).

## How other forges do it

### Gitea/Forgejo

- `models/perm/access/`: int access modes rung read/write/admin; `access` table is exactly our `repo_collabs`. They also have "units" (issues/wiki perms) — that's where ours *would* go if units arrive (phase 4+), and it would be additive: `unit_perms(repo_id, user_id, unit, role)`.
- SSH key model: `models/asymkey/`, revocation is cascade from user deletion; and their auth middleware computes permission per-request from db (no caching at login-time) — same as our `Evaluate` per transport call. Rule learned: **never bake authorization into a session**.
- Study file: `routers/private/internal.go` — the internal API shape for serv-authorize.

### GitLab

- Numeric ladder (10/20/30/40/50) in `members.access_level` + code mapping to abilities. Advantage: you can insert new rungs without re-rating booleans. When orgs land, copy the ladder.
- Permission enforcement happens REST-side for web/API and shell-side per-git-op; both consult the same rules module. Their dev docs on "permissions.md" and "member roles" are worth reading once before building the org story.
- <https://docs.gitlab.com/ee/user/permissions.html>

### GitHub (behavioral reference, not source)

- Role names: read/triage/write/maintain/admin for repositories in orgs; personal repos have "collaborator" (write) + owner. The public docs spell out each capability per level — our matrix above mirrors that shape: <https://docs.github.com/en/account-and-profile/roles-in-an-organization> and the repo-permission docs under docs.github.com → repositories.
- Key learning: write and merge permissions separated by *actions*, not vibes; when phase 3 adds protected branches, functionality maps level→allowed-verb in one table.

### Gitolite

- Rule table with user+repo+glob and `R`/`RW` flags, enforced via forced commands. Pure and elegant, but regex-glob authorization doesn't compose with databases and collaborative UI. Steal its philosophy — "authz is data + one evaluator", not its syntax: <https://gitolite.com/gitolite/>

## Security checklist (pin in tests where marked `test:`)

- [x] Key fingerprint = SHA256 universal (no md5). `test:` two users, same key → second AddKey fails; removing first user doesn't orphan the fingerprint row (cascade) and unique is free again.
- [x] `test:` ssh unknown fingerprint → failure (no user enumeration via timing — compare cost identical).
- [x] `test:` unauthorized clone of private → message identical to nonexistent repo (byte-for-byte stderr).
- [x] `test:` public repo push by non-collaborator → access denied, and the repo still clonable by them.
- [x] `test:` role revoke during an open connection: the *current* push may finish (transport checked at start) — document this consciously in the runbook, enforce tighter via pre-receive in phase 3.
- [x] PAT: raw shown at creation only; `test:` two tokens with same name OK, same *value* impossible.
- [x] Sessions: cookie not in referer/logs: HttpOnly; Secure only when serving https; `test:` logout kills server-side row, not just client cookie.
- [x] `test:` password change invalidates all sessions (token staleness is separate).
- [x] Username normalization: `test:` Alice/alice same user; on-disk dir exactly one (owner dir name = canonical lowercase username).

## Password & key hygiene references

- OWASP Authentication cheat sheet: <https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html>
- OWASP Password storage: <https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html>
- bcrypt pkg: <https://pkg.go.dev/golang.org/x/crypto/bcrypt>
- ssh key parsing: <https://pkg.go.dev/golang.org/x/crypto/ssh> (ParseAuthorizedKey, FingerprintSHA256, PublicKeys auth)
- GitHub's token prefix discipline (`ghp_`, `gho_`, `ghs_`...): copy the idea — a `gite_pat_` prefix makes leaked token detection by scanners/S GitHub-style tooling trivial.
