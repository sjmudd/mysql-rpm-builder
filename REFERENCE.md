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

| Command | Where | Purpose |
|---|---|---|
| `build-one [-n] [-c <config>] <os> <label>` | host | launch a Docker container and build `<label>` on `<os>` |
| `run [-c <config>] <label>` / `setup [-c <config>] <label>` | container (root) | full build: run all OS-prep stages, then drive the build across privilege boundaries; invoked by `build-one` |
| `build-rpm [-c <config>] <label>` | container (rpmbuild) | patch → rpmbuild → collect (run after `install-srpm`/`install-builddeps`) |
| `refresh [-c <config>] <label>` / `setup-repos` / `install-packages` / `fix-annobin` / `os-tweaks` / `create-user` / `install-builddeps` | container (root) | individual OS-prep / build-dep steps |
| `install-srpm [-c <config>] <label>` / `apply-patches` / `rpmbuild` / `collect` | container (rpmbuild) | individual build steps |

All subcommands optionally accept `-c <config>` to use an alternate config file (relative to the repo root) instead of the default `config.yaml`.

Every step is individually runnable, which makes debugging a failed build
much easier (see [Building in individual steps](#building-in-individual-steps)).

A thin `build-one` shell wrapper is provided so the historical invocation
still works: `./build-one ol10 9.7.1`.

There's also a git-tag/branch-based build path, entirely separate from the
src.rpm one described above — see [Building from a git tag, branch, or
fork](#building-from-a-git-tag-branch-or-fork-instead-of-a-src-rpm).

## Which versions do I rebuild?

The `config.yaml` build matrix currently covers the modern el8/el9/el10
combinations of MySQL 8.4.x, 9.x and 26.x across Oracle Linux, Rocky Linux,
AlmaLinux and CentOS Stream. Older el7 combinations can be added the same
way (see [Configuration](#configuration)).

## Configuration

Configuration is declarative YAML, layered **OS → MySQL version**:

- **`images.yaml`** — one entry per OS (flavour + major version): the
  container image and the repository setup. Repo setup is stable per OS
  major version so it lives here once, not per MySQL version.

  ```yaml
  oses:
    ol10:
      image: oraclelinux:10
      repos:
        enable: [ol10_codeready_builder, ol10_u1_developer_EPEL]  # yum config-manager --set-enabled
        epel_packages: [oracle-epel-release-el10]                 # dnf install -y
  ```

- **`config.yaml`** — the build matrix, a chronological sequence of builds
  per OS. Each `(os, version)` entry is fully explicit: its own source RPM
  URL, how build dependencies are installed, and optional shell `tweaks`.
  There is deliberately no inheritance — to add a new release, copy the
  newest block for that OS and bump the version key + srpm URL.

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
  one produced by `./build-rpm-from-git` (see below). `install-srpm` then
  installs directly from that path (no download/caching involved). Since
  `install-srpm` always runs *inside* the container, the path must be
  container-visible: `file:///data/built-from-git/<os><major>__<tag>/<name>.src.rpm`,
  not a host-relative one.

### Adding a build

1. Ensure the OS exists in `images.yaml` (image + repos).
2. Create a test config file (e.g., `test-config.yaml`) with your new build
   entry, or add it to `config.yaml` directly.
3. Build and test it: `./build-one -c test-config.yaml <os> <version>` (or
   `./build-one <os> <version>` if added to `config.yaml`).
4. For a quick validation without a full build, use
   `./build-one -test -c test-config.yaml <os> <version>` to stop as soon
   as compilation starts (past cmake).
5. Once validated, add the entry to `config.yaml` permanently (copying the
   previous version's block is usually sufficient, but watch for compiler/
   other changes over time).

The `-c <config>` flag is useful for testing new build entries without modifying
`config.yaml`: you can prepare a separate config file, validate it works, and
then merge it into the main config once ready.

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

On success the binary rpms are moved to `built/<os><major>__<version>/`
(e.g. `built/ol10__9.7.1/`), together with the container's `/etc/os-release`.

Logs are written under `log/` (UTC timestamps). Because `log/`, `SRPMS/`
and `built/` live in the mounted `$PWD` they persist even when the
container is removed with `--rm`:

Per-run files share one identifier, `<os>__<label>__<code>__<datetime>`, where
`<code>` is a random per-run code (also used for the container name) and
`<datetime>` is a single timestamp generated once per run:

- `log/build-one.<os>__<label>__<code>__<datetime>.log` — host-side launcher log
- `log/build-one.build_status` — one line per build (status, rc, elapsed)
- `log/ossetup.<os>__<label>__<code>__<datetime>.log`,
  `log/build.<os>__<label>__<code>__<datetime>.log` — in-container stages
- `log/rpm-qa.init.<os>__<label>__<code>__<datetime>` — sorted package list of
  the untouched base image, before any packages are changed
- `log/rpm-qa.post.<os>__<label>__<code>__<datetime>` — sorted end-state
  package list, captured just before `rpmbuild -ba` (so it is written even on an
  early `-test`/`-timeout` stop or a build failure), useful for reproducing or
  reporting a build and for comparing base images across OS versions

## A note on OS labels

The labels are NOT random. They come from `/etc/os-release`, using the
`ID` and the major part of `VERSION_ID`:

```
ID="rocky"
VERSION_ID="9.3"
```

resolves to `rocky9`. These labels are the keys used in `images.yaml` and
`config.yaml`.

## Patching

To build a patched version, create a directory `config/<label>/` (where
`<label>` matches the build key in `config.yaml`) containing `SPECS/`
and/or `SOURCES/`. After the src.rpm is installed, the `apply-patches`
step copies these into `~/rpmbuild/SPECS` and `~/rpmbuild/SOURCES`, then
applies any file matching `*patch*` in `SPECS/` to the spec file with
`patch -p0` (in sorted order).

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

Then add a `config.yaml` build entry keyed by `<label>` pointing at the
base src.rpm, and build with `./build-one <os> <label>`. See
`config/8.2.0.hyp/` for a complete example.

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
storage latency matters.

## rpm build user

The `rpmbuild` user created inside the container gets the first free
uid/gid, which on RH systems is 1000. There is an assumption that the
volume mounted via docker uses the same uid/gid; if it does not, things may
fail.

## Building from a git tag, branch, or fork instead of a src.rpm

Checking the `mysql/mysql-server` repo itself confirms there's no public
trigger for `rpmbuild` anywhere in it: `.github/workflows/` only tests and
validates PRs, and `packaging/rpm-oel/` has the `mysql.spec.in` template plus
the cmake plumbing that generates the real spec, but nothing that chains
`cmake configure` → `cpack` → `rpmbuild` into an actual build. That confirms
Oracle's own build-and-sign pipeline is private tooling, not something this
repo can lean on — which is the whole reason it exists in the first place.
`build-rpm-from-git` reconstructs the equivalent of that pipeline from public
inputs only: a git ref, plus the spec's own declared `BuildRequires`,
instead of depending on Oracle's official src.rpm download at all.

```
./build-rpm-from-git [-no-bison] [-o <dir>] [-repo <url>] [-ref <name>] <os> <tag>
e.g. ./build-rpm-from-git ol10 mysql-9.7.1
e.g. ./build-rpm-from-git -repo https://github.com/<you>/mysql-server.git -ref bug/120895 ol9 26.7.0
```

`-repo`/`-ref` override what actually gets cloned (a fork and/or a branch,
instead of the default upstream repo at a tag matching `<tag>`), while
`<tag>` always still names the version being built — it must match the
real `MYSQL_VERSION` at whatever commit gets checked out, since it's used
to predict the CPack-produced tarball filename. This is what makes it
possible to build and test a patched tree (see
[Fixing the actual bug and filing a report](README.md#fixing-the-actual-bug-and-filing-a-report)
in the README) before it's an official release with a real src.rpm to
point at.

Like `build-one`, it's a thin wrapper around the same `mysql-rpm-builder`
binary's own subcommands:

| Command | Where | Purpose |
|---|---|---|
| `git-build-one [-o <dir>] [-no-bison] [-repo <url>] [-ref <name>] [-n] <os> <tag>` | host | launch a Docker container and build `<tag>` on `<os>` |
| `git-run [-o <dir>] [-no-bison] [-repo <url>] [-ref <name>] <tag>` | container (root) | OS-prep (via `git-build-deps.yaml`'s bootstrap package list), then drives the steps below |
| `git-clone` / `git-configure` / `git-assemble-srpm [-o <dir>] [-no-bison] [-repo <url>] [-ref <name>] <tag>` | container (rpmbuild) | individually re-runnable build-user steps, same debuggability as the src.rpm path's steps |

It shallow-clones the ref, runs `cmake configure` (this is what actually
produces the real `packaging/rpm-oel/mysql.spec` — nothing here
hand-substitutes that template), optionally skips the pre-generated bison
output (`-no-bison`: `mysql.spec` requires bison unconditionally, so a real
`rpmbuild -ba` regenerates `sql_yacc.cc`/etc. itself regardless of what the
tarball ships — see `docs/srpm-tarball-differs-from-git-tag.md`), packages
the source tarball via CPack, and runs `rpmbuild -bs`. The resulting
src.rpm lands in `built-from-git/<os><major>__<tag>/`.

Two gaps between a plain git checkout and what `cmake configure`/`rpmbuild
-bs` actually need are handled automatically, both without requiring any
network access from inside the container beyond a native Go download:

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
  public git tree — part of Oracle's private packaging pipeline, same theme
  as `docs/srpm-tarball-differs-from-git-tag.md`. Verbatim copies (extracted
  from Oracle's official src.rpm, sha256-verified) are checked into
  `go/gitsteps/assets/` and written into `SOURCES/` only when the spec
  actually declares them and only if not already present — never
  overwriting a real file.

**Current scope: src.rpm only** — it doesn't (yet) run `rpmbuild -ba` to
produce binary RPMs directly; you build the resulting src.rpm the normal
way afterward (see below). `git-build-deps.yaml` (the bootstrap package
list needed just to get `cmake configure` running at all, before any real
spec exists to run `yum-builddep` against) has `ol8`, `ol9` and `ol10`
entries. The compiler toolset (`gcc-toolset-<N>`, when the base OS's own
compiler is too old) is a per-`(os, tag)` override in that same file,
determined empirically per tag rather than assumed — see the file's own
header comment for the discovery method.

To confirm a git-built src.rpm is actually usable by the normal
`rpmbuild -ba` pipeline, point a `config.yaml`-shaped `-c` file's `srpm:` at
it with a `file://` URL instead of a download one (see
[Configuration](#configuration)):

```yaml
oses:
  ol10:
    builds:
      9.7.1-from-git:
        srpm: file:///data/built-from-git/ol10__mysql-9.7.1/mysql-community-9.7.1-1.el10.src.rpm
        auto_install_dependencies: true
```

`-test`/`-add-if-successful` work exactly as they do for an official
src.rpm — including that a comment on the merged entry survives the merge
into `config.yaml` rather than being silently discarded (see
`go/config/merge.go`), which matters here specifically because a build
entry's comment is often the only record of *why* a workaround exists.
