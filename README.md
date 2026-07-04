# MySQL rpm (re)builder

note: As of commit 30776d5bffc7daa7d6a206ebde630d2f3771975b the build
code was converted to golang from the previous shell script configuration.

## Overview

### Trigger repeatable MySQL rpm builds from a src rpm.

This repo has two main purposes:
- build binary rpms from the src.rpm in a repeatable manner ensuring the
  exact build requirements are defined.
  While rpm does have a `BuildRequires:` section intended to provide the
  list of build dependencies often this is far from precise and it is
  often incomplete.  This can mean that if your build environment is
  setup differently to mine you can build binary packages but I can not,
  something which is problematic.  This repo is intended to make the OS
  environment build process explicit (e.g when building from a docker
  image) and also ensures the subsequent rpm-build run is explicit too.
- build patched versions of the original src rpm with minimal changes.
  Along the same lines as the previous step we can now build a patched
  version of the upstream src.rpms by providing the required changes,
  usually a patch, and building using the same process as before.

**Intention**: to build from an empty OS container, ensure the build
environment is setup explicitly and then to build as a non-root user.

I have found that the docs on performing a rebuild, at least as done
by upstream vendors, may be far from complete and this has made building
for a new OS or wanting to build the existing software with a specific
patch much harder than expected.

Given a known initial state (the bare OS from docker), the build is
driven entirely by declarative configuration and a single self-contained
binary, so the whole process is completely defined and should be
repeatable.

Feel free to provide feedback at `sjmudd` at `pobox.com` or file an
[issue](https://github.com/sjmudd/mysql-rpm-builder/issues/new) on
github directly.

## A single Go binary

The tooling is a single statically-linked Go binary, `mysql-rpm-builder`.
The same binary runs on the host (to launch a Docker container) and inside
the container (to prepare the OS and build the rpms) — the repository
directory is mounted at `/data` and the binary re-executes itself in the
right role.

Build it with `make` (which also formats, vets and lints), or `go build`
directly:

```
make                              # fmt, vet, lint, then build
go build -o mysql-rpm-builder ./go/cmd   # or just build
```

The binary is invoked by subcommand:

| Command | Where | Purpose |
|---|---|---|
| `build-one [-n] <os> <label>` | host | launch a Docker container and build `<label>` on `<os>` |
| `run <label>` | container | full build (OS prep + rpmbuild); invoked by `build-one` |
| `setup <label>` | container (root) | run all OS-prep stages, then hand off to the build stage |
| `build <label>` | container (rpmbuild) | run all rpmbuild-user stages |
| `refresh` / `setup-repos` / `install-packages` / `os-tweaks` / `create-user` `<label>` | container (root) | individual OS-prep steps |
| `install-srpm` / `apply-patches` / `rpmbuild` / `collect` `<label>` | container (rpmbuild) | individual build steps |

Every step is individually runnable, which makes debugging a failed build
much easier (see [Building in individual steps](#building-in-individual-steps)).

A thin `build_one` shell wrapper is provided so the historical invocation
still works: `./build_one ol10 9.7.1`.

## Which versions do I rebuild?

The `config.yaml` build matrix currently covers the modern el9/el10
combinations of MySQL 8.4.x and 9.x across Oracle Linux, Rocky Linux,
AlmaLinux and CentOS Stream. Older el7/el8 combinations can be added the
same way (see [Configuration](#configuration)).

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
  URL, package list, and optional shell `tweaks`. There is deliberately no
  inheritance — to add a new release, copy the newest block for that OS and
  bump the version key + srpm URL.

  ```yaml
  oses:
    ol10:
      builds:
        9.7.0:
          srpm: https://dev.mysql.com/get/Downloads/MySQL-9.0/mysql-community-9.7.0-1.el10.src.rpm
          packages: [cmake, gcc, gcc-c++, ...]
        9.7.1:                 # copy of 9.7.0, version + srpm bumped
          srpm: https://dev.mysql.com/get/Downloads/MySQL-9.0/mysql-community-9.7.1-1.el10.src.rpm
          packages: [cmake, gcc, gcc-c++, ...]
  ```

The package list is recorded per `(os, version)` because MySQL build
dependencies (compilers/toolsets) change between MySQL releases and differ
slightly per OS flavour.

### Adding a build

1. Ensure the OS exists in `images.yaml` (image + repos).
2. Add a `<version>:` block under `oses.<os>.builds` in `config.yaml` with
   the `srpm:` URL and `packages:` list — usually by copying the previous
   version's block.
3. Build it: `./build_one <os> <version>`.

## Build Process

### Simple way

Typical usage:

- `./build_one <os> <version>`
- e.g. `./build_one ol10 9.7.1` or `./build_one rocky9 9.6.0`

If you omit a parameter, the valid choices are listed.

### What's under the hood?

`build-one` resolves the container image from `images.yaml` and runs the
binary inside Docker, roughly equivalent to:

```
docker run --rm --network=host --hostname=buildhost \
    -v $PWD:/data -w /data \
    oraclelinux:10 \
    /data/mysql-rpm-builder run 9.7.1
```

Inside the container `run` executes as root: it prepares the OS
(`refresh` → `setup-repos` → `install-packages` → `os-tweaks` →
`create-user`), then re-execs itself as the non-root `rpmbuild` user to run
the build (`install-srpm` → `apply-patches` → `rpmbuild` → `collect`).

Use `./build_one -n <os> <version>` for a dry run that prints the docker
command without executing it.

#### Quickly testing a new (os, version) combination

A full `rpmbuild` takes hours, but most per-flavour problems (missing repos
or build deps, a failing cmake configure) show up long before that. These
flags stop the container early so a new combination can be validated fast:

```
./build_one -test ol10 9.7.1              # stop as soon as compiling starts (past cmake)
./build_one -timeout 30m ol10 9.7.1       # stop after 30m regardless
./build_one -until 'Building CXX object' ol10 9.7.1  # stop on a custom output marker
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
$ ./start-docker-container.sh oraclelinux:10
```

or

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

and, as the `rpmbuild` user (`su - rpmbuild`), the build steps:

```
$ /data/mysql-rpm-builder install-srpm 9.7.1
$ /data/mysql-rpm-builder apply-patches 9.7.1
$ /data/mysql-rpm-builder rpmbuild 9.7.1
$ /data/mysql-rpm-builder collect 9.7.1
```

Any step that fails can be re-run in place without repeating the expensive
`rpmbuild` step.

### Output and Logging

On success the binary rpms are moved to `built/<os><major>__<version>/`
(e.g. `built/ol10__9.7.1/`), together with the container's `/etc/os-release`.

Logs are written under `log/` (UTC timestamps). Because `log/`, `SRPMS/`
and `built/` live in the mounted `$PWD` they persist even when the
container is removed with `--rm`:

- `log/build_one-<os>-<label>.log` — host-side launcher log
- `log/build_one.build_status` — one line per build (status, rc, elapsed)
- `log/ossetup__<label>.log`, `log/build__<label>.log` — in-container stages
- `log/rpm-qa.<label>` (or `.failed`) — the installed package list at build
  time, useful for reproducing or reporting a build

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
base src.rpm, and build with `./build_one <os> <label>`. See
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

## Related thoughts

None of what is done here is specific to MySQL, so this approach could be
used for building other packages following the same philosophy.

Others may ask why I build from the src.rpm files and not directly from the
git repo. That might be an interesting addition to the tooling — the same
lack of explicit documentation applies to building from the git tree.
[Here](https://github.com/sjmudd/bacula-rpm-builder/) is an example of
building from a git tree.

## Some reported RPM rebuild failures and related bugs

- [Bug#118796: RPM spec files are missing some buildrequires](https://bugs.mysql.com/118796)
- [Bug#115484: Missing BuildRequires for gcc-toolset-12 in mysql.spec.in for 9.0.0+](https://bugs.mysql.com/115484)
- [Bug#111159: Incomplete documentation on MySQL rpm rebuilds makes rebuilding packages hard](https://bugs.mysql.com/111159)
- [Bug#111088: src tarball made from github repo and provided in src.rpm files is not the same](https://bugs.mysql.com/111088)
