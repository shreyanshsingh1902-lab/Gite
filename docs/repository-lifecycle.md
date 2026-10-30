# repository lifecycle

What happens on `create`, `delete`, `rename`, visibility flip, clone of an
empty repo, hooks, and maintenance. `internal/repository/*` owns all of it,
with `git` the only tool that touches repo internals.

## Bare repo anatomy (what you're creating)

```
<repo-root>/owner/name.git/
├── HEAD                     # symbolic ref: "ref: refs/heads/main"
├── config                   # bare = true (written by `git init --bare`)
├── description              # cosmetic; gitweb-era; ours lives in sqlite, ignore
├── objects/
│   ├── xx/yyyy...           # loose objects
│   ├── pack/pack-*.pack ~/.idx   # packfiles
│   └── info/                # alternates, commit-graph live here too
├── refs/
│   ├── heads/<branch>       # 40-char sha files (the branches)
│   └── tags/<tag>
└── hooks/                   # samples; we install real ones later (phase 3)
```

Authoritative reference: `gitrepository-layout(5)`
<https://git-scm.com/docs/gitrepository-layout>. The whole transport and web
reader are built on these files; knowing them cold makes debugging 10x faster
(`cat HEAD`, `ls refs/heads`, `git -C path cat-file -e <sha>^{commit}`).

## Create

Ordering rule (differed deliberately from Gitea — see comparison below):

1. Validate name/branch (`repository-lifecycle` regexes; `^[a-z0-9][a-z0-9_.-]*$`,
   no `.git` suffix, branch `main` default).
2. **Insert catalog row first.** The `UNIQUE(owner_id, name)` index is the
   correctness primitive: two racing creates lose by constraint, not by
   check-then-create TOCTOU.
3. `mkdir -p` parent, then `git init --bare <dir>`, then
   `git symbolic-ref HEAD refs/heads/<branch>` (works on any git ≥ 2.x, unlike
   `init -b` which needs ≥ 2.28).
4. On step 3 failure: **compensate** — delete the row, return the error.
   Never leave a row without a dir (that's the `ErrStorageMissing` ghost).

```sh
# the exact commands create() runs, no more
git init --bare --quiet /root/you/widget.git
git -C /root/you/widget.git symbolic-ref HEAD refs/heads/main
```

Visibility defaults to **private** (`schema.md` fail-closed rule), the CLI/UI
must require an explicit `--public`/checkbox, because a public misfire is a
data leak and a private misfire is mildly annoying.

**Naming**: Git forbids some things our regex already forbids (dots-only,
`..` path tricks, trailing `.git`), plus our collate-NOCASE rules mean
`Widget` and `widget` are one repo. `git check-ref-format` is the oracular
test for exotic names; our regex is deliberately narrower.

### How Gitea handles create (and why we differ)

Gitea's `services/repository/CreateRepository`: creates the bare repo, *then*
inserts the row, and on db failure deletes the created directory. Two problems
with that order for us: (a) a crash between dir-create and row-insert leaks an
orphan dir (their cleanup is in-process only), (b) the row is the truth we
route *by* (backends!), routing must not transiently exist while storage may
not. Row-first + compensation gives us exactly one direction of drift
("row without dir") which our transports already handle with `ErrStorageMissing`.
Neither ordering removes the race; both handle it; ours makes the failure mode
uniform. Read both, argue, keep ours.

## Clone of an empty repo

Fully legal git-wise: upload-pack advertises no refs, client warns
`warning: You appear to have cloned an empty repository.` Then the first
`git push -u upstream main` sets `refs/heads/main` on the server (the symbolic
`HEAD` we set at create-time is what makes the client's *first* push target
understood and what decides the bare repo's default branch display later).

Because of that, `HEAD` must point at a branch name matching the client push
(or push fails with `src refspec`... if names disagree, the client creates the
branch it has — fine). Our `default_branch` catalog value and the bare `HEAD`
move in lockstep in create.

## Delete

```
gite repos delete <owner>/<name> --yes
```

1. Require admin-of-repo (implicit-owner/site-admin).
2. `DELETE FROM repos WHERE id = ?` — collabs/agent_repos/remotes cascade
   (foreign_keys pragma must be on, see schema doc).
3. `os.RemoveAll(path)` — admin-only, flag-guarded, log + print exact path
   deleted. If RemoveAll fails: the row is already gone; report loudly
   (repair note). Never keep a half-row "as backup" — backups are fs-level,
   corruption of that principle is how forges end up with undeletable zombies.

No ` recycle bin` in v1. Backup strategy (documented runbook): periodic
archive of `<home>` (sqlite file with WAL checkpoint + zipped repo root) —
later litestream. Gitea does the same philosophy.

## Rename + visibility flips (phase 1)

- Rename: catalog update + `os.Rename(dir)` in one step; both must succeed or
  both roll back (dir rename first, row update second, full revert on failure —
  dir rename is the atomic cheap op; if the row update fails, rename back and
  report). Case-preserving renames within NOCASE universe are a rename to self
  (verify hitless).
- In-flight clones of the old path 404 after the change; github behaves the
  same without redirects (scan the webhook UX debate in gitlab before adding
  redirects-for-clone — we don't).
- Visibility flip: single UPDATE; every transport verifies current value at
  request time, so the flip takes effect on the next connection with no cache
  to invalidate (deliberate: correctness first, denormalized caching later and
  only if needed).

## Hooks (the receive-time policy engine, phase 3 arrival, design now)

Server-side hooks live in the bare repo's `hooks/` dir and get their inputs
on stdin, per-update in env (for `update`), and decide accept/reject via exit
status. We don't need them in phases 0-2 because branch protection isn't on the
menu yet — but our design supports *one hook script pattern* so it's cheap:

```
hooks/pre-receive         # one exec of `gite hook pre-receive`
hooks/post-receive        # mirror fan-out for agent repos (if ever needed)
```

`gite hook pre-receive` behavior (known from gitolite/gitea lineage):
1. Read stdin lines: `<old-sha> <new-sha> <refname>`.
2. Spawn env gives repo path (cwd is the bare repo) + user via `GITE_USER` env
   (set by the transport at exec time — ssh server adds it, http adds it).
3. Query the catalog: is that ref protected, does the user's role allow, is the
   commit signed (future), then exit 0/1 with a one-line stderr explain.

Full docs: <https://git-scm.com/docs/githooks>, plus
<https://git-scm.com/book/en/v2/Customizing-Git-Git-Hooks>.

How the others run theirs:
- **Gitea**: hook scripts call back to `gitea hook` binaries → internal API per
  update. The internal call includes "a protected branch list" and
  "is the pusher the same as the repo owner" style checks.
- **GitLab**: `pre-receive` in gitlab-shell → POSTs to Rails' `internal/allowed`
  API per batch, Rails owns `protected_branches` data. Same data-not-code shape
  we documented in schema.md.
- **GitHub**: "branch protection" is their productized version of exactly this.
  When we get there, read their docs on required status checks *before*
  building — every detail (bypass lists, include/exclude globs, push rules)
  is a scoping lesson: <https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches>

Environment injection for receive hooks (git-side):
- `GIT_QUARANTINE_PATH` — pushed-but-not-yet-migrated objects live here until
  pre-receive says yes; the receiving git moves them on accept. Our hook can
  cat-file those objects: requires `GIT_OBJECT_DIRECTORY` + `GIT_ALTERNATE_OBJECT_DIRECTORIES`.

## Maintenance: what actually runs when nobody is looking

- **Loose object accumulation**: every push writes loose objects; packs come
  from gc. Forgets left alone, `git count-objects` balloons; clone speed
  degrades measurably at loose-object counts in the tens of thousands.
- **The modern answer** is `git maintenance start` / `git gc --cruft` on a
  schedule, packaged cron on the server: <https://git-scm.com/docs/git-maintenance>
  Read Pro Git's "Maintenance and Data Recovery" chapter:
  <https://git-scm.com/book/en/v2/Git-Internals-Maintenance-and-Data-Recovery>
- **GitLab's answer**: "housekeeping" — gitaly runs gc/cruft/repack per project
  on rotation with health metrics. The *product-level knob* they added is worth
  copying when we have N repos under a UI: per-repo last-housekeeping column,
  nightly worker picks N stalest.
- **Gitea's answer**: `gitea doctor` CLI + a `repo-health` internal check; also
  install-time `git gc` for imported repos. Their "check + repair" CLI pattern
  (`gitea doctor check --fix`) maps to our future `gite admin repair`.
- Do NOT hook `gc --auto` into the push path: it runs per-push (`receive.autogc`
  default) but a self-hosted instance with few users is better swept nightly
  (fewer surprise CPU spikes; same total work).

## Failure handling that the transports rely on

- `os.Stat(<path>/HEAD)` missing → transport speaks `ErrStorageMissing`
  (worded `repository storage missing`), the webview renders a sad-repo page
  (phase 1) with a one-click `gite admin repair` hint (phase 3).
- Concurrent push to the same ref: git ref updates are compare-and-swap under
  a lockfile; last successful push wins; no partial state, nothing to do on our
  side. (This is why the *test matrix* checks concurrent pushes once, ever:
  it's guaranteed by git, not by us.)
- Concurrent create + transport resolve: row-first ordering means the transport
  may briefly see the row before the dir exists; transports must tolerate
  `ErrStorageMissing` for the sub-second race and NOT immediately retry-create.
  A short back-and-forth is expected behavior, log at Info.

## Pitfall checklist

- [ ] Every create/delete path compensates its own partner (row→dir, dir→row).
- [ ] Default branch catalog value and bare `HEAD` always moved together; add
      one function `SetDefaultBranch(row, branch)` updating both, never two paths.
- [ ] Delete is `--yes` flag-gated; output names the destroyed path.
- [ ] `os.Rename`/path building always from catalog fields (owner canonical
      lowercase, name canonical) — never re-derived from raw user input.
- [ ] Maintenance runs are logged like everything else (slog), and skippable
      per-repo via catalog flag for the inevitable lone 4GB monorepo.
