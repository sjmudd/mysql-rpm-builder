# Reference

Full command reference, config schema, and internals. Start with
[`README.md`](README.md) if you haven't already — this is the deep-dive for
once you're actually using the tool.

## A single Go binary

The tooling is a single statically-linked Go binary, `mysql-rpm-builder`.
The same binary runs on the host (to launch a Docker container) and inside
the container (to prepare the OS and build the rpms) — the repository
directory is mounted at `/data` and the binary re-executes itself in the
right role. It has to be genuinely static (not just CGO-free) because one
build of it needs to run unmodified inside every target container,
including older ones (el8: glibc 2.28) that may be older than the build
host's own glibc — a binary linked against the host's glibc would fail
with unresolved `GLIBC_2.xx` symbols on those.

```
make                              # fmt, vet, lint, then build
```

`make build` (via the `Makefile`, not a plain `go build`) is what actually
produces a portable binary: `CGO_ENABLED=1 CC=musl-gcc go build -ldflags
"-linkmode external -extldflags '-static'"`, linking against musl instead
of glibc. A plain `CGO_ENABLED=0 go build` was tried first and isn't
sufficient — see the comments above the `$(BINARY):` rule in `Makefile`
for what was tried and why it didn't work. This needs `musl-gcc` installed
on the build host first (Ubuntu/Debian: `sudo apt install musl-tools`).

### From a src.rpm (`build-one`)

| Command | Where | Purpose |
|---|---|---|
| `build-one [-n] [-c <config>] <os> <label>` | host | launch a Docker container and build `<label>` on `<os>` |
| `run [-c <config>] <label>` / `setup [-c <config>] <label>` | container (root) | full build: run all OS-prep stages, then drive the build across privilege boundaries; invoked by `build-one` |
| `build-rpm [-c <config>] <label>` | container (rpmbuild) | patch → rpmbuild → collect (run after `install-srpm`/`install-builddeps`) |
| `refresh [-c <config>] <label>` / `setup-repos` / `install-packages` / `fix-annobin` / `os-tweaks` / `create-user` / `install-builddeps` | container (root) | individual OS-prep / build-dep steps |
| `install-srpm [-c <config>] <label>` / `apply-patches` / `rpmbuild` / `collect` | container (rpmbuild) | individual build steps |

All subcommands optionally accept `-c <config>` to use an alternate config
file (relative to the repo root) instead of the default
`rpm-build-config.yaml`.

Every step is individually runnable, which makes debugging a failed build
much easier (see [Building in individual steps](#building-in-individual-steps)).

A thin `build-one` shell wrapper is provided so the historical invocation
still works: `./build-one ol10 9.7.1`.

### From git, no src.rpm (`build-src-rpm-from-git` / `build-rpms-from-git`)

Entirely separate command family — see [Building directly from git](#building-directly-from-git-instead-of-a-srcrpm).

## Which versions do I rebuild?

The `rpm-build-config.yaml` build matrix currently covers the modern
el8/el9/el10 combinations of MySQL 8.4.x, 9.x and 26.x across Oracle Linux,
Rocky Linux, AlmaLinux and CentOS Stream. Older el7 combinations can be
added the same way (see [Configuration](#configuration)).

## Configuration

Configuration is declarative YAML, layered **OS → MySQL version**:

- **`images.yaml`** — one entry per OS (flavour + major version): the
  container image, repository setup, base packages, and the OS-varying
  behavior every build path used to hardcode (which package manager
  command installs the config-manager plugin, which package provides
  `yum-builddep`, and whether rpmbuild needs `--define "el<major> 1"`).
  Repo setup is stable per OS major version so it lives here once, not per
  MySQL version. Shared by every build path (`build-one`,
  `build-src-rpm-from-git`, `build-rpms-from-git`).

  ```yaml
  oses:
    ol10:
      image: oraclelinux:10
      repos:
        enable: [ol10_codeready_builder, ol10_u1_developer_EPEL]  # yum config-manager --set-enabled
        epel_packages: [oracle-epel-release-el10]                 # dnf install -y
      base_packages: [rpm-build, util-linux]           # installed unconditionally, every build
      config_manager_package: dnf-command(config-manager)   # installed before repos.enable/epel_packages
      builddep_package: yum-utils                       # installed before build-dependency resolution
      enable_rpmbuild_define_el: true                   # pass --define "el10 1" to rpmbuild/rpmspec
  ```

  `base_packages`, `config_manager_package`, and `builddep_package` are
  optional: each falls back to a Go-side default (`[rpm-build, util-linux]`,
  `dnf-command(config-manager)`, `yum-utils` respectively) when omitted, so
  only non-RHEL-family OSes typically need to override them.

  `enable_rpmbuild_define_el` has no fallback: omitting it defaults to
  `false` (Go's zero value), meaning no `--define` is passed. Setting it
  `true` for an OS not in the known EL family (`almalinux`, `centos`, `ol`,
  `rhel`, `rocky`) is a configuration error, not silently ignored: Fedora
  (`%fedora`/`%fc<N>` auto-resolve via its own rpm macros) sets it `false`.

- **`rpm-build-config.yaml`** — used only by `build-one`: the build matrix,
  a chronological sequence of builds per OS. Each `(os, version)` entry is
  fully explicit: its own source RPM URL, how build dependencies are
  installed, and optional shell `tweaks`. There is deliberately no
  inheritance — to add a new release, copy the newest block for that OS
  and bump the version key + srpm URL.

  ```yaml
  oses:
    ol10:
      builds:
        9.7.0:
          srpm: https://dev.mysql.com/get/Downloads/MySQL-9.0/mysql-community-9.7.0-1.el10.src.rpm
          auto_install_dependencies: false
          packages: [cmake, gcc, gcc-c++, ...]
        9.7.1:                 # copy of 9.7.0, version + srpm bumped
          srpm: https://dev.mysql.com/get/Downloads/MySQL-9.0/mysql-community-9.7.1-1.el10.src.rpm
          auto_install_dependencies: true          # let yum-builddep resolve BuildRequires
                                                   # (no packages list needed)
  ```

  The base tooling every build needs — `rpm-build` (provides `rpmbuild`) —
  is installed unconditionally by the program (right after the initial
  `yum update`), so it never needs listing.

  There are two ways to supply the remaining build dependencies:

  1. **`packages`** — an explicit list, installed as root before the build.
  2. **`auto_install_dependencies: true`** — the `install-builddeps` step
     installs `yum-utils` (which provides `yum-builddep`) and runs
     `yum-builddep` against the *extracted spec* (auto-detected as the single
     `*.spec` in `SPECS/`) to resolve its `BuildRequires`. Optional. If present
     must be `true`/`false`.

  At least one of `auto_install_dependencies` or `packages` must be set.
  Listing everything in `packages` (with `auto_install_dependencies: false`)
  records the deps per `(os, version)` explicitly; `auto_install_dependencies`
  instead delegates to the spec's own `BuildRequires`, so `packages` can usually
  be omitted entirely.

  Note `yum-builddep` is run against the `.spec` file, not the `.src.rpm`:
  it ignores macro-conditional `BuildRequires` (and `--define`) for a
  `.src.rpm` target. See: https://bugzilla.redhat.com/show_bug.cgi?id=2497059

  `srpm` is normally an `https://` download URL, but it can also be a
  `file://` URL for a locally built src.rpm with no real download URL — e.g.
  one produced by `./build-src-rpm-from-git` (see below). `install-srpm` then
  installs directly from that path (no download/caching involved). Since
  `install-srpm` always runs *inside* the container, the path must be
  container-visible: `file:///data/built/git-build-src-rpm/<os><major>__<tag>/<name>.src.rpm`,
  not a host-relative one.

- **`git-build-config.yaml`** — used only by the two git-based commands
  (`build-src-rpm-from-git`, `build-rpms-from-git`); not read by
  `build-one`. See [Building directly from git](#building-directly-from-git-instead-of-a-srcrpm)
  for its schema.

### Adding a build

1. Ensure the OS exists in `images.yaml` (image + repos).
2. Create a test config file (e.g., `test-config.yaml`) with your new build
   entry, or add it to `rpm-build-config.yaml` directly.
3. Build and test it: `./build-one -c test-config.yaml <os> <version>` (or
   `./build-one <os> <version>` if added to `rpm-build-config.yaml`).
4. For a quick validation without a full build, use
   `./build-one -test -c test-config.yaml <os> <version>` to stop as soon
   as compilation starts (past cmake).
5. Once validated, add the entry to `rpm-build-config.yaml` permanently
   (copying the previous version's block is usually sufficient, but watch
   for compiler/other changes over time).

The `-c <config>` flag is useful for testing new build entries without
modifying `rpm-build-config.yaml`: you can prepare a separate config file,
validate it works, and then merge it into the main config once ready.

## Build Process

### What's under the hood?

`build-one` resolves the container image from `images.yaml` and runs the
binary inside Docker, roughly equivalent to:

```
docker run --rm --network=host --hostname=buildhost \
    -v $PWD:/data -w /data \
    oraclelinux:10 \
    /data/mysql-rpm-builder run 9.7.1
```

Inside the container `run` executes as root and prepares the OS
(`record-init` → `refresh` → `setup-repos` → `install-packages` →
`fix-annobin` → `os-tweaks` → `create-user`), then drives the build across
privilege boundaries:

1. as the non-root `rpmbuild` user, fetch and extract the source RPM
   (`install-srpm`) — this lays down the package's spec file;
2. back as root, resolve the spec's build dependencies
   (`install-builddeps`, when `auto_install_dependencies` is set);
3. as `rpmbuild` again, patch, build and collect
   (`apply-patches` → `rpmbuild` → `collect`).

`install-builddeps` must run as root but needs the spec that `install-srpm`
lays down as the build user, which is why the build-user work is split
around it.

Use `./build-one -n <os> <version>` for a dry run that prints the docker
command without executing it.

#### Quickly testing a new (os, version) combination

A full `rpmbuild` takes hours, but most per-flavour problems (missing repos
or build deps, a failing cmake configure) show up long before that. These
flags stop the container early so a new combination can be validated fast:

```
./build-one -test ol10 9.7.1              # stop as soon as compiling starts (past cmake)
./build-one -timeout 30m ol10 9.7.1       # stop after 30m regardless
./build-one -until 'Building CXX object' ol10 9.7.1  # stop on a custom output marker
```

`-test` is the common case: reaching the first compile line means OS prep,
build-dependency resolution and cmake all succeeded. A build stopped this
way is reported as `STOPPED` with `rc 0` (not `FAILED`). Flags must come
before the `<os> <version>` positional arguments.

Build failures are typically due to a repo not being enabled so that some
build rpms cannot be found. Repo naming and setup differ per OS flavour;
this is what `images.yaml` `repos:` captures. If you see something like:

```
No match for argument: libfido2-devel
Error: Unable to find a match: libfido2-devel libtirpc-devel
```

then the required repo is probably not enabled — adjust the `repos:` block
for that OS in `images.yaml`.

### Building in individual steps

Because a full rebuild can take hours, it is often easier to debug by
running one step at a time in a throwaway container. Start a shell:

```
$ docker run --rm -it --network=host -v $PWD:/data -w /data oraclelinux:10 bash
```

Then, as root, run the OS-prep steps:

```
# /data/mysql-rpm-builder refresh 9.7.1
# /data/mysql-rpm-builder setup-repos 9.7.1
# /data/mysql-rpm-builder install-packages 9.7.1
# /data/mysql-rpm-builder create-user 9.7.1
```

Then, as the `rpmbuild` user (`su - rpmbuild`), fetch and extract the source
RPM:

```
$ /data/mysql-rpm-builder install-srpm 9.7.1
```

Back as root, resolve the build dependencies from the extracted spec (only
needed when `auto_install_dependencies` is set for this build):

```
# /data/mysql-rpm-builder install-builddeps 9.7.1
```

And, as the `rpmbuild` user again, the remaining build steps:

```
$ /data/mysql-rpm-builder apply-patches 9.7.1
$ /data/mysql-rpm-builder rpmbuild 9.7.1
$ /data/mysql-rpm-builder collect 9.7.1
```

Any step that fails can be re-run in place without repeating the expensive
`rpmbuild` step. Pass `-c <config>` to each step if testing with an
alternate config file.

### Output and Logging

On success the binary rpms are moved to `built/build-one/<os><major>__<version>/`
(e.g. `built/build-one/ol10__9.7.1/`), together with the container's
`/etc/os-release`. The two git-based commands use their own sibling
directories under `built/` — see [Building directly from
git](#building-directly-from-git-instead-of-a-srcrpm).

Logs are written under `log/` (UTC timestamps), partitioned by build type
the same way `built/` is: `log/build-one/`, `log/git-build-src-rpm/`,
`log/git-build-rpms/`. Because `log/`, `SRPMS/` and `built/` live in the
mounted `$PWD` they persist even when the container is removed with `--rm`.

`build-one`'s logs, under `log/build-one/`. Per-run files share one
identifier, `<os>__<label>__<code>__<datetime>`, where `<code>` is a random
per-run code (also used for the container name) and `<datetime>` is a
single timestamp generated once per run:

- `<os>__<label>__<code>__<datetime>.log` — host-side launcher log
- `build_status` — one line per build (status, rc, elapsed)
- `ossetup.<os>__<label>__<code>__<datetime>.log`,
  `build.<os>__<label>__<code>__<datetime>.log` — in-container stages
- `rpm-qa.init.<os>__<label>__<code>__<datetime>` — sorted package list of
  the untouched base image, before any packages are changed
- `rpm-qa.post.<os>__<label>__<code>__<datetime>` — sorted end-state
  package list, captured just before `rpmbuild -ba` (so it is written even on an
  early `-test`/`-timeout` stop or a build failure), useful for reproducing or
  reporting a build and for comparing base images across OS versions

The two git-based commands' logs, under `log/git-build-src-rpm/` and
`log/git-build-rpms/` respectively:

- `<os>__<tag>__<code>.log` — host-side launcher log
- `git-src-rpm-build.<os>__<tag>.log` / `git-all-rpms-build.<os>__<tag>.log`
  — in-container orchestrator log (not per-run-code-suffixed, unlike the
  host launcher log — a second run for the same (os, tag) overwrites this one)

## A note on OS labels

The labels are NOT random. They come from `/etc/os-release`, using the
`ID` and the major part of `VERSION_ID`:

```
ID="rocky"
VERSION_ID="9.3"
```

resolves to `rocky9`. These labels are the keys used in `images.yaml` and
`rpm-build-config.yaml`.

## Patching

To build a patched version, create a directory `config/<label>/` (where
`<label>` matches the build key in `rpm-build-config.yaml`) containing
`SPECS/` and/or `SOURCES/`. After the src.rpm is installed, the
`apply-patches` step copies these into `~/rpmbuild/SPECS` and
`~/rpmbuild/SOURCES`, then applies any file matching `*patch*` in `SPECS/`
to the spec file with `patch -p0` (in sorted order).

Two kinds of change are supported:

- **Patch the spec file** — put a `SPECS/mysql.spec.patch`. It is applied
  directly to `~/rpmbuild/SPECS/mysql.spec`, e.g. to change the release
  string or add a `Patch0:` / `%patch0` directive:

  ```
  --- mysql.spec.orig     2023-11-02 21:20:49 +0100
  +++ mysql.spec          2023-11-02 21:29:35 +0100
  @@ -150,7 +150,7 @@
   Version:        8.2.0
  -Release:        1%{?commercial:.1}%{?dist}
  +Release:        1%{?commercial:.1}%{?dist}.hypergraph
  @@ -162,6 +162,7 @@
   Source91:       filter-requires.sh
  +Patch0:         000.hypergraph_optimizer_enable.diff
  @@ -792,6 +793,8 @@
   %endif # 0%{?compatlib}
  +# 000 Enable hypergraph optimizer
  +%patch0 -p1
  ```

- **Patch the source tree** — put the patch under `SOURCES/` (e.g.
  `SOURCES/000.hypergraph_optimizer_enable.diff`). It is copied into
  `~/rpmbuild/SOURCES` and applied by rpmbuild during `%prep` via the
  `Patch0:`/`%patch0` directive your spec patch added.

Then add a `rpm-build-config.yaml` build entry keyed by `<label>` pointing
at the base src.rpm, and build with `./build-one <os> <label>`. See
`config/8.2.0.hyp/` for a complete example.

Optionally, list the expected patch files on the build entry itself with
`patches: [SPECS/mysql.spec.patch, SOURCES/000.foo.diff]` (paths relative
to `config/<label>/`). When set, `apply-patches` verifies every listed
file exists before applying anything and fails loudly if `config/<label>/`
or any listed file is missing — catching a typo'd label or a misplaced
patch file instead of silently producing an unpatched build. Omitting
`patches` keeps the original behaviour: whatever is found under
`config/<label>/` is applied, and a missing `config/<label>/` is a no-op.

This exact patch-file mechanism (`config/<label>/SPECS`/`SOURCES`, `patch
-p0`) is `build-one`-only. The git-based commands have their own, similar
but not identical mechanism — see [Patching a git-based
build](#patching-a-git-based-build) — plus `-repo`/`-ref`, for committing a
fix to a branch in your own fork instead of a local patch file.

## Warning on differences between equivalent OS versions

The RHEL-compatible distributions (Oracle Linux, Rocky Linux, AlmaLinux,
CentOS Stream) intend to be equivalent, but in practice there are
differences — most notably in the names and setup of the additional repos
that provide the newer compiler toolsets MySQL needs. What works on one may
need a small change on another. This is why each OS is its own entry in
`images.yaml` and its own test target: a build that works on `ol10` should
also be verified on `rocky10`, `almalinux10`, etc.

## Build times

Rebuilding the rpms takes surprisingly long, because the rpm build produces
both the normal and the debug rpms (the latter containing debug symbols).
On a home system (Beelink SER 4700u) this is about 2h45m using a NAS vs
1h20m using local nvme storage — the C/C++ build reads and writes a lot, so
storage latency matters. Applies to both `build-one` and `build-rpms-from-git`
(the two commands that actually run `rpmbuild -ba`); `build-src-rpm-from-git`
only assembles a src.rpm (`rpmbuild -bs`) and is much faster.

## rpm build user

The `rpmbuild` user created inside the container gets the first free
uid/gid, which on RH systems is 1000. There is an assumption that the
volume mounted via docker uses the same uid/gid; if it does not, things may
fail.

**Known follow-up, not implemented:** pin this to the invoking host user's
actual uid/gid (`os.Getuid()`/`os.Getgid()`) instead of relying on the
container's default allocation, so the assumption above always holds
regardless of the host user's uid. Would mean passing the host uid/gid into
the container (e.g. as env vars) and using them explicitly in
`go/osprep/osprep.go`'s `useradd`/`groupadd` calls, with a fallback for when
they're unset and a guard against a uid/gid collision with an existing
account in the base image.

## Building directly from git instead of a src.rpm

There's no public trigger for `rpmbuild` anywhere in the `mysql/mysql-server`
repo: `.github/workflows/` only tests and validates PRs, and
`packaging/rpm-oel/` has the `mysql.spec.in` template plus the cmake
plumbing that generates the real spec, but nothing that chains
`cmake configure` → `cpack` → `rpmbuild` into an actual build. Oracle's own
build-and-sign pipeline is private tooling — the reason this repo exists.
Two commands reconstruct the equivalent of that pipeline from public inputs
only: a git ref, plus the spec's own declared `BuildRequires`, instead of
depending on Oracle's official src.rpm download at all.

| Command | Produces |
|---|---|
| `build-src-rpm-from-git` | a src.rpm only (`rpmbuild -bs`) |
| `build-rpms-from-git` | the full binary RPM set (`rpmbuild -ba`), no src.rpm round trip |

```
./build-src-rpm-from-git [-no-bison] [-o <dir>] [-repo <url>] [-ref <name>] <os> <tag>
./build-rpms-from-git    [-no-bison] [-o <dir>] [-repo <url>] [-ref <name>] <os> <tag>
e.g. ./build-src-rpm-from-git ol10 mysql-9.7.1
e.g. ./build-rpms-from-git -repo https://github.com/<you>/mysql-server.git -ref bug/120895 ol9 26.7.0
```

`-repo`/`-ref` override what actually gets cloned (a fork and/or a branch,
instead of the default upstream repo at a tag matching `<tag>`), while
`<tag>` always still names the version being built — it must match the
real `MYSQL_VERSION` at whatever commit gets checked out, since it's used
to predict the CPack-produced tarball filename. This is what makes it
possible to build and test a patched tree (see
[Reporting an upstream bug](README.md#reporting-an-upstream-bug) in the
README) before it's an official release with a real src.rpm to point at.

Both are thin wrappers around the same `mysql-rpm-builder` binary's own
subcommands. Host subcommands are named `git-build-<X>`; in-container
orchestrators are `git-<X>-build`:

| Command | Where | Purpose |
|---|---|---|
| `git-build-src-rpm [-o <dir>] [-no-bison] [-repo <url>] [-ref <name>] [-n] <os> <tag>` | host | launch a container, produce a src.rpm only |
| `git-src-rpm-build [-o <dir>] [-no-bison] [-repo <url>] [-ref <name>] <tag>` | container (root) | OS-prep, then `git-clone` → `git-apply-patches` → `git-configure` → `git-assemble-src-rpm` |
| `git-build-rpms [-o <dir>] [-no-bison] [-repo <url>] [-ref <name>] [-n] <os> <tag>` | host | launch a container, produce the full binary RPM set |
| `git-all-rpms-build [-o <dir>] [-no-bison] [-repo <url>] [-ref <name>] <tag>` | container (root) | OS-prep, then the full pipeline below |
| `git-clone` / `git-apply-patches` / `git-configure` / `git-stage` [`-o <dir>` `-no-bison` `-repo <url>` `-ref <name>`] `<tag>` | container (rpmbuild) | individually re-runnable build-user steps, shared by both commands (`git-apply-patches` is a no-op if this (os, tag) has no `patches:` configured) |
| `git-assemble-src-rpm <tag>` | container (rpmbuild) | `git-src-rpm-build` only: `rpmbuild -bs` after `git-stage` |
| `git-builddeps <tag>` | container (root) | `git-all-rpms-build` only: `yum-builddep` against the rendered spec |
| `git-rpmbuild <tag>` | container (rpmbuild) | `git-all-rpms-build` only: `rpmbuild -ba` + collect |

Both shallow-clone the ref, apply any configured patches (`git-apply-patches`
— see [Patching a git-based build](#patching-a-git-based-build); a no-op if
none are configured), then run `cmake configure` (this is what actually
produces the real `packaging/rpm-oel/mysql.spec` — nothing here
hand-substitutes that template), optionally skipping the pre-generated
bison output (`-no-bison`: `mysql.spec` requires bison unconditionally, so
a real `rpmbuild -ba` regenerates `sql_yacc.cc`/etc. itself regardless of
what the tarball ships), then package the source tarball via CPack and stage it with the rendered
spec into `~/rpmbuild/{SPECS,SOURCES}` (`git-stage`, shared by both). From
there:

- `git-build-src-rpm` runs `rpmbuild -bs` and stops — output lands in
  `built/git-build-src-rpm/<os><major>__<tag>/`.
- `git-build-rpms` instead resolves the *real* `BuildRequires:` from the
  now-rendered spec (`git-builddeps`, root — needs root to install
  packages but reads the spec the build user just staged, same reason
  `build-one`'s `install-builddeps` runs where it does) and runs
  `rpmbuild -ba` (`git-rpmbuild`) — producing both binary RPMs and a
  src.rpm in one pass. No src.rpm round trip: that src.rpm is a normal
  byproduct in `SRPMS/`, never reinstalled. Output lands in
  `built/git-build-rpms/<os><major>__<tag>/`.

Two gaps between a plain git checkout and what `cmake configure` actually
needs are handled automatically, both without requiring any network access
from inside the container beyond a native Go download:

- **Boost.** MySQL 9.x and 8.4.x both bundle a matching `boost_<ver>`
  directory right in the source tree (`extra/boost/`, confirmed for
  `8.4.10`: `boost_1_84_0`); 8.0.x doesn't — it expects boost fetched
  separately, and the required version+URL is only knowable from
  `cmake/boost.cmake` in the freshly cloned tree (not from `mysql.spec`,
  which doesn't exist yet at this point — it's generated *by* this same
  `cmake configure` run). When the bundled directory isn't found, it's
  fetched from the URL `cmake/boost.cmake` names and cached under
  `boost-cache/` (persists across runs, since successive 8.0.x point
  releases are likely to keep pinning the same boost version) — `cmake
  configure` itself then extracts the cached tarball in place, same as it
  would for a manually pre-downloaded one.
- **`filter-provides.sh`/`filter-requires.sh`.** `mysql-8.0.x`'s spec (only
  — 8.4.x/9.x's spec.in doesn't declare these at all) references these two
  as bare-filename `Source:` entries, but they don't exist anywhere in the
  public git tree — part of Oracle's private packaging pipeline. Verbatim copies (extracted
  from Oracle's official src.rpm, sha256-verified) are checked into
  `go/gitsteps/assets/` and written into `SOURCES/` only when the spec
  actually declares them and only if not already present — never
  overwriting a real file.

### Verifying a git-produced src.rpm's `BuildRequires:` are sufficient

`git-build-rpms` succeeding on its own isn't a complete check: `git-builddeps`
runs `yum-builddep` against a container already seeded with
`minimal_git_packages`/`src_rpm_build_packages` (see `git-build-config.yaml`
below), so a real gap in the spec's own declared `BuildRequires:` can be
masked by packages that were only there to get `cmake configure` running in
the first place. The only genuine proof is `build-one` against the
git-produced src.rpm in a container with none of those tiers applied: just
`auto_install_dependencies: true`.

```
./build-src-rpm-from-git <os> <tag>
./generate-build-one-config <os> <tag>
./build-one -c <os>-<tag>-from-git.yaml -add-if-successful <os> <tag>-from-git
```

Or, chained as one command:

```
./verify-git-build <os> <tag>
```

`generate-build-one-config` globs the src.rpm `build-src-rpm-from-git`
produced under `built/git-build-src-rpm/<os><major>__<tag>/`, reads the
`.config.yaml` sidecar `build-src-rpm-from-git` writes there (`repo`, `ref`,
`commit`, `git_patches`, `minimal_git_packages`, `src_rpm_build_packages`,
`bison_generated`), and writes `<os>-<tag>-from-git.yaml`: a single
`rpm-build-config.yaml`-shaped entry with `srpm: file:///data/...`,
`auto_install_dependencies: true`, and an `annotations:` block carrying the
sidecar's contents. `annotations:` is informational only, never read by
`config.Resolve`: it exists so a `build-one`-merged entry sourced from a
git tag carries its own origin instead of relying on a label suffix or a
hand-written comment.

Each step stays independently re-runnable: a failure in `build-one` doesn't
require repeating the (expensive) src.rpm build, just fix and rerun that
last command against the already-generated `<os>-<tag>-from-git.yaml`.
`verify-git-build` deliberately never runs `build-rpms-from-git`: once a
tag's spec is proven clean this way, `build-rpms-from-git` becomes the fast,
routine way to build it, not part of the one-time verification itself.

### `git-build-config.yaml`

Separate from `rpm-build-config.yaml` — not read by `build-one`, and shaped
differently (nested `oses.<os>.<tier>`, no srpm/label per entry, since
there's no srpm URL for a git-tag build to have). Four tiers, only the
first outside this file:

1. Base container image and repos — `images.yaml`, shared with `build-one`.
2. **`oses.<os>.minimal_git_packages`** — universal tooling for *any*
   MySQL git tag's `cmake configure` to run on this OS (same for every tag).
3. **`oses.<os>.builds.<tag>.src_rpm_build_packages`** — *this tag's*
   `cmake configure` needs, on top of tier 2. Read by both
   `git-build-src-rpm` and `git-build-rpms` (both run `cmake configure`).
4. **`oses.<os>.builds.<tag>.all_rpms_extra_packages`** — patches a gap in
   *this tag's spec's own declared* `BuildRequires:`. Read only by
   `git-builddeps` (`git-build-rpms`) — `git-build-src-rpm` never needs
   this, since `rpmbuild -bs` only assembles a source tarball + spec and
   never evaluates `BuildRequires:` at all.

```yaml
oses:
  ol10:
    minimal_git_packages: [bison, cmake, gcc, gcc-c++, git, krb5-devel,
      libaio-devel, make, rpm-build, yum-utils, ...]
    builds:
      mysql-9.7.1:
        src_rpm_build_packages: [gcc-toolset-14-gcc, gcc-toolset-14-gcc-c++]
        all_rpms_extra_packages: []   # only if yum-builddep against the real spec still misses something
```

Tiers 2/3 are determined empirically per (os, tag), same discipline as the
compiler-toolset entries: an empty-list run's `cmake configure` error names
exactly what's missing (e.g. `Could not find devtoolset compiler/linker in
/opt/rh/gcc-toolset-<N>`), not guessed in advance. Tier 4 the same way, via
whatever `yum-builddep`/the compile reports missing.

**Current limitation**: tier 2 lists (ol8/ol9/ol10) were carried forward
from this file's earlier, single flat `packages:` list and have not been
individually re-verified against tier 3 — some entries there may only be
needed by specific tags and belong in tier 3 instead. Minimize tier 2 per
package empirically before trusting it as the true OS-universal minimum.

### Patching a git-based build

Optional, and unrelated to the four dependency tiers above. `git-clone`
shallow-clones the tag; `git-apply-patches` (shared by both
`git-build-src-rpm` and `git-build-rpms`) then applies any patches
configured for this (os, tag) via `git-build-config.yaml`'s `patches:`
list, before `cmake configure` runs:

```yaml
oses:
  ol10:
    builds:
      mysql-9.7.1:
        patches: [000.fix.patch]
```

Each entry must be a bare filename — no path component — and the file
itself lives in `config/git-patches/<tag>/`, applied via `git apply` in
list order:

```
config/git-patches/mysql-9.7.1/000.fix.patch
```

No patches configured is the normal case and a clean no-op.

**How this differs from `build-one`'s `SPECS`/`SOURCES` patching** (see
[Patching](#patching) above) — genuinely different, not just a naming
variation, because each targets whatever actually exists at that point in
its own pipeline:

| | `build-one` | git-based (`git-apply-patches`) |
|---|---|---|
| Target file | the already cmake-rendered `mysql.spec` in `~/rpmbuild/SPECS/` — the raw `mysql.spec.in` template doesn't exist in a src.rpm at all | the raw `packaging/rpm-oel/mysql.spec.in` (or any file) in the freshly cloned tree, before any substitution |
| Tool | `patch -p0` | `git apply` |
| Diff format | bare paths, e.g. `--- mysql.spec` | standard `git diff` output, `a/`/`b/`-prefixed paths, e.g. `--- a/packaging/rpm-oel/mysql.spec.in` |
| Config location | `config/<label>/SPECS/`, `config/<label>/SOURCES/` | `config/git-patches/<tag>/` |

A patch written for one will not apply against the other, even for a
logically-similar change — the base text is genuinely different content,
not just a differently-formatted diff of the same file. If you're patching
the spec for both a `build-one` build and a git-based one, expect to
maintain two separate patch files.

**Known follow-up, not implemented**: neither mechanism auto-detects `-p0`
vs `-p1` (`patch`) or `-p1` vs `-p0` (`git apply`) today — each is
hardcoded to one strip level, so a patch generated in the "wrong" format
for its target fails outright rather than being auto-corrected. Dry-running
both levels and using whichever applies cleanly (falling back to a clear,
actionable error naming both expected formats if neither does) is planned
but not yet built, for either mechanism — see `applyPatch` in
`go/steps/helpers.go` and `ApplyPatches` in `go/gitsteps/gitsteps.go`.

### Confirming a git-built src.rpm is genuinely reproducible

`git-build-rpms` succeeding proves the build works *given that exact git
checkout and staging* — it doesn't prove the resulting `.src.rpm`, unpacked
somewhere else with none of that ambient context, is actually
self-sufficient. `git-stage` copies the CPack tarball and rendered spec
into `~/rpmbuild` inside the *same* container/checkout that just ran
`cmake configure`; if `package_source` or the compat-source-fetching logic
ever leaned on something present in that specific build tree but not
captured in the distributable src.rpm, a full `git-build-rpms` run would
never catch it (this is exactly the class of bug official Oracle src.rpms
have shipped with before).

To confirm the src.rpm is genuinely standalone, point a
`rpm-build-config.yaml`-shaped `-c` file's `srpm:` at it with a `file://`
URL and run it through `build-one` — a real `rpm -ivh` into a *fresh*
`~/rpmbuild`, no residual git-checkout state:

```yaml
oses:
  ol10:
    builds:
      9.7.1-from-git:
        srpm: file:///data/built/git-build-src-rpm/ol10__mysql-9.7.1/mysql-community-9.7.1-1.el10.src.rpm
        auto_install_dependencies: true
```

`-test`/`-add-if-successful` work exactly as they do for an official
src.rpm — including that a comment on the merged entry survives the merge
into `rpm-build-config.yaml` (see `go/config/merge.go`) rather than being
silently discarded, since a build entry's comment is often the only record
of *why* a workaround exists.
