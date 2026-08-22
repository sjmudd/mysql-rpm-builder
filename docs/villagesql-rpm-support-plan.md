# Plan: build RPMs from git for MySQL and VillageSQL

## Status

Merged to `main` at v2.8.1. Target for the villagesql-server work itself: **v3.0.0**.

**Done:**
- Command rename + new full-build pipeline (`git-build-rpms`) — see Commands
- Output directory unified across all three build types — see Output directory
- Dependency tier split (4 tiers) implemented — see Dependency config
- `log/` partitioned the same way as `built/`, plus shared `config.LogDir`/`BuiltDir`/`SRPMSCacheDir` constants
- Git-based patching (`git-apply-patches`, `patches:`, `config/git-patches/<tag>/`) implemented — see Patching
- Repo-wide comment/doc sweep (stale filenames, "original bash script" references, chatty comments)
- `Clone()` no longer passes `--no-tags` — checkout keeps `refs/tags/<ref>`
  when ref is a tag, so `git describe --tags` shows what was cloned
- First full `git-build-rpms` completion: ol10/mysql-26.7.0, exit 0, 20 RPMs
  in `built/git-build-rpms/ol10__mysql-26.7.0/`: confirms we can build all
  RPMs directly from git, not just a src.rpm.
- **Step 0 fully closed**: `build-one -test` against that tag's
  git-produced src.rpm also succeeded (clean container, zero extra
  `packages:`), confirming the spec's `BuildRequires:` is genuinely
  self-sufficient. Tooling to make this repeatable without manual YAML
  editing built and unit-tested: `.config.yaml` sidecar, `go/config`
  `Annotations`, `generate-build-one-config`. See "Git-build verification
  workflow".

**Pending:**
- Run `verify-git-build` itself end to end as one command (its three
  underlying steps were each run and proven individually for real, but the
  wrapper script hasn't been invoked as a single chain yet)
- villagesql-server spec.in patch and everything after (unstarted)

**Open questions:**
- `minimal_git_packages` not yet audited/minimized — see Dependency config
- Router `Conflicts:`/`Provides:` treatment — see spec.in item 2
- SDK/bundled-extension packaging — see Steps item 8
- Patch strip-level (`-p0`/`-p1`) auto-detection, both mechanisms — documented as a known follow-up in REFERENCE.md, not implemented
- `rpmbuild` container user's uid/gid not pinned to the host user's — see REFERENCE.md's "rpm build user", not implemented
- Library packages (`libmysqlclient` etc.) — `Provides: mysql-server` (spec.in item 3) covers package-name resolution, but check the libraries are actually ABI/behaviourally compatible with what external software linked against upstream mysql expects, not just name-compatible. Deferred, not blocking now.

## Commands

| Piece | Name |
|---|---|
| Host wrapper (src.rpm only) | `build-src-rpm-from-git` |
| Host wrapper (full) | `build-rpms-from-git` |
| Host subcommand (src.rpm only) | `git-build-src-rpm` |
| Host subcommand (full) | `git-build-rpms` |
| Container orchestrator (src.rpm only) | `git-src-rpm-build` |
| Container orchestrator (full) | `git-all-rpms-build` |
| Steps (unchanged) | `git-clone`, `git-configure`, `git-assemble-src-rpm` |
| Steps (new) | `git-stage`, `git-builddeps`, `git-rpmbuild` |

Pattern: host subcommands are `git-build-<X>`; container orchestrators are
`git-<X>-build`. All nine names are exported Go constants in
`go/gitsteps/gitsteps.go` (`CmdBuildSrcRPM`, `CmdSrcRPMBuild`, `CmdClone`,
...), not bare string literals — a stale string in one `suBuild` call
(`"git-rpmbuild-rpms"`, pre-rename) wasn't caught by the compiler and only
surfaced on a real run, at the very last privilege hand-off after a full
OS-prep + clone + configure + stage + builddeps had already run.

`git-build-rpms` pipeline: `Clone()` → `Configure()` → `Stage()` →
`BuildDeps()` → `rpmbuild -ba` (`RPMBuild()`) → `collect()`. No src.rpm
round trip — `rpmbuild -ba` still produces one as a byproduct in `SRPMS/`,
nothing reinstalls it. `Stage()` is the old `AssembleSRPM` (now
`AssembleSrcRPM`) minus its `rpmbuild -bs` tail — shared staging, not
src.rpm-specific. `BuildDeps()`/`collect()`/`collectAll()` are new and
independent of `go/steps` (deliberately never imported, same as the
existing `downloadFile` duplication).

## Output directory

All three build types partition one shared base by build type:

- `built/build-one/<os>__<label>/` (download-based, `go/steps`)
- `built/git-build-src-rpm/<os>__<tag>/`
- `built/git-build-rpms/<os>__<tag>/`

`go/steps.Runner.builtDir()` and `go/gitsteps.Runner.outputSubdir()` both
changed. Existing files under the old flat `built/`/`built-from-git/`
layout can be relocated once this is confirmed working — not automatic.

## Dependency config: four tiers

1. Base container image (`images.yaml`) — unchanged.
2. **`oses.<os>.minimal_git_packages`** — universal tooling for *any*
   MySQL git tag's `cmake configure` to run on this OS.
3. **`oses.<os>.builds.<tag>.src_rpm_build_packages`** — *this tag's*
   `cmake configure` needs, on top of tier 2. Used by both
   `git-build-src-rpm` and `git-build-rpms`.
4. **`oses.<os>.builds.<tag>.all_rpms_extra_packages`** — patches a gap in
   this tag's spec's own declared `BuildRequires:`. Used only by
   `BuildDeps()` (`git-build-rpms`) — `-bs` never evaluates `BuildRequires:`
   at all, so `git-build-src-rpm` never needs this. Empty for every (os,
   tag) so far; don't pre-populate speculatively.

`rpm-build-config.yaml` is not touched by the git-based path.

Implemented in `go/gitsteps/gitsteps.go` (`depsFile`, `loadDepsPackages` for
tiers 2+3, `loadAllRPMsExtraPackages` for tier 4, wired into `BuildDeps()`)
and `git-build-config.yaml` (`packages:`→`minimal_git_packages`,
`builds.<tag>.packages:`→`src_rpm_build_packages`, new
`all_rpms_extra_packages`).

**Open:** `minimal_git_packages` (ol8/ol9/ol10) was renamed in place, not
audited — it was one flat list before this split, so some entries (e.g.
`libfido2-devel`, `libudev-devel`, `libquadmath-devel`, `libtirpc-devel`)
may only be needed by specific tags and belong in tier 3. Minimize tier 2
empirically, per package, the same way the gcc-toolset entries were
determined — not by guessing from the current list.

## Git-build verification workflow

First-ever completed run of the full binary-RPM pipeline (`git-build-rpms`,
distinct from `git-build-src-rpm`'s `-bs`-only path). ol10/mysql-26.7.0,
attempts:

1. Predates this session, irrelevant.
2. Failed at `cmake configure`: `bison` had been dropped from
   `minimal_git_packages` (mid-session `-no-bison`→`-gen-bison` experiment).
   `sql/CMakeLists.txt` requires bison OR a pre-existing `sql/sql_yacc.h`
   fallback a raw git checkout never has. Reverted; `bison` stays
   unconditional in `minimal_git_packages`.
3. Passed configure, but built from a mid-revert binary — stopped.
4. Clean code. **Succeeded** — exit 0, 20 RPMs (src.rpm +
   debuginfo/debugsource/binary), ~3h50m. See
   `log/git-build-rpms/ol10__mysql-26.7.0__isigh.log`.

Confirms the pipeline itself works, but wasn't the whole story on its own: `BuildDeps()`'s `yum-builddep` runs
against a container already seeded with `minimal_git_packages`/
`src_rpm_build_packages`, so a clean `git-build-rpms` pass alone can mask a
real `BuildRequires:` gap (suspected cause of bug #120895 shipping across
multiple Oracle GA releases). Only `build-one` against the git-produced
src.rpm (`auto_install_dependencies: true`, nothing else) is a genuine
clean-room proof.

**Second leg, now done for real (ol10/mysql-26.7.0):**
- `.config.yaml` sidecar, written by `git-build-src-rpm`: `repo`, `ref`,
  `commit`, `git_patches`, `minimal_git_packages`,
  `src_rpm_build_packages`, `bison_generated`. Confirmed real content
  after an actual run (not a synthetic fixture).
- `generate-build-one-config <os> <tag>`: globs the produced src.rpm,
  reads the sidecar if present, writes `<os>-<tag>-from-git.yaml`. Ran
  against the real sidecar above and produced a correct config.
- `go/config.Build.Annotations` field, rendered by `FormatBuildEntry`.
  Unit-tested (substring + full decode round-trip).
- `build-one -c <generated>.yaml -test ol10 mysql-26.7.0-from-git`:
  `exit status 0 (STOPPED)`, reached compilation cleanly in a genuinely
  clean container. Confirms the spec's `BuildRequires:` alone is
  sufficient for this tag.
- `verify-git-build <os> <tag>`: thin wrapper chaining the three steps
  above. Written, but not yet run as a single command; each step was run
  individually instead. Deliberately excludes `build-rpms-from-git`,
  that's the routine post-verification build path, not part of one-time
  verification.
- Deferred: sibling `comment:` field on `Build`, for freeform notes.

Step 0 is done. Remaining: run `verify-git-build ol10 mysql-26.7.0` itself
as a single command at least once, to confirm the wrapper's own plumbing
(not just its three underlying steps) works.

## Patching

Two options now: `-repo`/`-ref` (commit to a branch in a fork), or
`git-build-config.yaml`'s `patches:` list + `config/git-patches/<tag>/`
(`git apply` against the freshly cloned tree, before `cmake configure`
runs — see REFERENCE.md's "Patching a git-based build"). A
local-checkout-mount mode for fast iteration was considered and rejected
separately — patching the git tree is expected to be rare (find a build
bug, fix it on a branch, report it), not a routine fast-iteration workflow.

## `packaging/rpm-oel/mysql.spec.in` in villagesql-server

Unmodified from stock MySQL today — same `Name:`, paths, service, user as real MySQL. Patch, don't rewrite:

1. Rename `Name:` → e.g. `villagesql-server%{?product_suffix}`. `product_suffix` already defaults to `community`; every `%if 0%{?commercial}` block is already dead — no change needed there.
2. Rename the two `%package -n` router stanzas (lines 667, 692) — `mysql-router-%{product_suffix}[-devel]` doesn't follow `%{name}`. Router's own code is untouched; whether router also gets `Conflicts:`/`Provides:` is TBD later.
3. Add `Provides: mysql-server = %{version}` (+ per sub-package) so `Requires: mysql-server` still resolves.
4. Add `Conflicts: mysql-server, mysql-community-server, mariadb-server` (+ equivalents), deliberately **no** `Obsoletes:` — `Conflicts:` blocks co-install; `Obsoletes:` would make `dnf upgrade` auto-swap the packages. Omitting it keeps replacement a conscious `dnf swap`/remove-then-install.
5. `Version: 8.4.10` (from `MYSQL_NO_DASH_VERSION`), VillageSQL's part in `Release:`, e.g. `Release: 1.vsql0.0.6~dev%{?dist}` (`~` sorts correctly for a pre-release tag). RPM tags can't contain `-`; note `SELECT @@version` today reports `8.4.10-villagesql-0.0.6-dev-<githash>` (`cmake/vsql_version.cmake`), same constraint would apply if that string is ever reused.
6. Leave paths, service name, user, `%files`, `BuildRequires`, scriptlets as-is (deliberate drop-in design). VEF SDK/bundled-extension packaging is deferred, not blocking.
7. Strip `%changelog` (lines 1991–3038, 1047 lines of Oracle history) down to one fresh fork-point entry — do this last, after the build passes.

## Steps

OS order: **ol10 first**, then ol9, then ol8 — sequential, not parallel.

0. `./build-rpms-from-git ol10 mysql-26.7.0` — **done**, exit 0. Remaining
   before step 0 fully closes: `build-one` against the git-produced
   src.rpm (see "Git-build verification workflow"), audit
   `minimal_git_packages` (see "Open" above), bump `go/version/version.go`
   to **v3.0.0**, commit, merge.
1. Smoke-test unpatched against villagesql-server on ol10 (`-repo`/`-ref`) — expect it to build, just still identity-colliding with stock MySQL.
2. Patch the spec per above.
3. Rebuild; confirm `Name:`/`Provides:`/`Conflicts:` show up and the full build succeeds.
4. Functional test: install the RPM in a clean container, start `villagesqld`, connect.
5. Verify the `Conflicts:`/no-`Obsoletes:` behavior directly: install `mysql-server`, confirm `dnf install villagesql-server` is blocked; confirm `dnf upgrade` doesn't auto-swap it.
6. GPG-sign the built RPMs.
7. Add permanent `git-build-config.yaml` entries for VillageSQL builds (all three tiers as needed).
8. Decide on SDK/bundled-extension packaging (not blocking).
9. Repeat steps 1–8 for ol9, then ol8. (Step 0 is one-time, not repeated per OS.)
