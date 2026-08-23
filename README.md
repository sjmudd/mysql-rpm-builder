# MySQL RPM (re)builder

Reproducible MySQL RPM builds, from a bare OS image or directly from git,
with the build environment (packages, repos, patches) fully declared in
YAML instead of assumed. Built because MySQL's own `BuildRequires:` has
repeatedly been incomplete on specific RHEL versions, with no public,
repeatable process to reproduce an official build or test a patched one.

## Workflows

| I want to... | Use |
|---|---|
| Build official MySQL RPMs from a src.rpm | `build-one` |
| Test a candidate fix before it's an official release | `verify-git-build`, which chains `build-src-rpm-from-git` → `build-one` for you |
| Build a full custom RPM set straight from a git ref/fork | `build-rpms-from-git` |

All three run in a `--rm` Docker container, as a non-root build user, from a
declared package list — nothing implicit from the host or a cached image
state.

## Configuration

Three YAML files, layered:

**`images.yaml`** — one entry per OS: container image, repos to enable.
```yaml
oses:
  ol10:
    image: oraclelinux:10
    repos:
      enable: [ol10_codeready_builder, ol10_u1_developer_EPEL]
      epel_packages: [oracle-epel-release-el10]
```

**`git-build-config.yaml`** — only for the two git-based commands; not used by
`build-one`. Three tiers: universal bootstrap tooling, this-tag's extra
`cmake configure` needs, and (full-build only) a patch for a gap in the
spec's own declared `BuildRequires:`.
```yaml
oses:
  ol10:
    minimal_git_packages: [bison, cmake, gcc, gcc-c++, git, make, rpm-build, yum-utils]
    builds:
      mysql-9.7.1:
        src_rpm_build_packages: [gcc-toolset-14-gcc, gcc-toolset-14-gcc-c++]
        all_rpms_extra_packages: []   # only if yum-builddep against the real spec still misses something
```

**`rpm-build-config.yaml`** — used by `build-one`: one entry per `(os, label)`, its
src.rpm and how deps get installed.
```yaml
oses:
  ol10:
    builds:
      9.7.1:
        srpm: https://dev.mysql.com/get/Downloads/MySQL-9.0/mysql-community-9.7.1-1.el10.src.rpm
        auto_install_dependencies: true   # yum-builddep resolves BuildRequires
```

To build a patched src.rpm instead (spec patch, source patch, or both), add
`patches:` and drop the files under `config/<label>/`:
```yaml
oses:
  ol10:
    builds:
      9.7.1-hyp:
        srpm: https://dev.mysql.com/get/Downloads/MySQL-9.0/mysql-community-9.7.1-1.el10.src.rpm
        auto_install_dependencies: true
        patches: [SPECS/mysql.spec.patch, SOURCES/000.hypergraph_optimizer_enable.diff]
```
```
config/9.7.1-hyp/
  SPECS/mysql.spec.patch       # applied to the extracted spec with patch -p0
  SOURCES/000.hypergraph_optimizer_enable.diff   # copied in; the spec patch adds the Patch0:/%patch0 directive
```

The git-based commands can be patched too, via `git-build-config.yaml`'s own
`patches:` list and `config/git-patches/<tag>/` — same idea, different
target file and diff format (it patches the raw `mysql.spec.in` template in
the freshly cloned tree via `git apply`, before `cmake configure` runs, not
the already-rendered spec `build-one` patches). See
[`REFERENCE.md`](REFERENCE.md#patching-a-git-based-build) for the exact
differences.

Full schema and every field: [`REFERENCE.md`](REFERENCE.md).

## Example invocations

```
make                              # fmt, vet, lint, build -> ./mysql-rpm-builder

# from a src.rpm
./build-one ol10 9.7.1            # build MySQL 9.7.1 for Oracle Linux 10
./build-one -n ol10 9.7.1         # dry run: print the docker command, don't run it
./build-one -test ol10 9.7.1      # stop as soon as compiling starts (fast sanity check)

# src.rpm only, straight from git
./build-src-rpm-from-git ol10 mysql-9.7.1
./build-src-rpm-from-git -repo <fork url> -ref <branch> ol9 26.7.0

# full RPM set, straight from git, no src.rpm round trip
./build-rpms-from-git ol10 mysql-9.7.1
```

Output: `built/build-one/<os>__<label>/`, `built/git-build-src-rpm/<os>__<tag>/`,
`built/git-build-rpms/<os>__<tag>/`. Logs (including a package list snapshot
before/after) in the matching `log/<same-name>/`.

## When a build fails

Usually an incomplete `BuildRequires:` — `yum-builddep` installs what's
*declared*, and a package the build actually needs but the spec doesn't ask
for fails, sometimes only after a long compile.

1. Read the error. A repo/package not found means a repo isn't enabled —
   check `images.yaml`. A missing package deeper into the build means the
   spec needs it and doesn't declare it.
2. Add it to that build's `packages:` in `rpm-build-config.yaml` (with
   `auto_install_dependencies: true`) and rebuild — a working local
   workaround, not a fix; anyone building elsewhere hits the same failure.
3. Use `-test`, `-until '<pattern>'`, or `-timeout <dur>` to iterate quickly
   instead of waiting hours per attempt.

## Reporting an upstream bug

If `BuildRequires:` is genuinely incomplete, the real fix belongs in
`mysql-server`'s `packaging/rpm-oel/mysql.spec.in`. Workflow (based on
bugs.mysql.com/120895):

1. Patch `mysql.spec.in` on a branch in your own fork.
2. `./build-src-rpm-from-git -repo <fork url> -ref <branch> <os> <version>`
   (`<version>` must match the real `MYSQL_VERSION` at that commit).
3. `./generate-build-one-config <os> <version>` writes a
   `rpm-build-config.yaml`-shaped entry pointing at the result, with just
   `auto_install_dependencies: true`, no manual `packages:`. Run `build-one
   -c <that file>` against it: passing with nothing added proves the spec
   fix is complete, not just a working local hack. See REFERENCE.md's
   "Verifying a git-produced src.rpm's `BuildRequires:` are sufficient" for
   why this two-step check (not just `build-rpms-from-git` succeeding) is
   what actually proves it.
4. Confirm the *un*patched build fails the same way, so the report shows
   both sides.
5. File at [bugs.mysql.com](https://bugs.mysql.com/) with the reproduction
   and the spec diff.

## Reference, feedback, prior art

- [`REFERENCE.md`](REFERENCE.md) — full command reference, config schema,
  individual debugging steps, git-tag build internals.
- Feedback welcome — `sjmudd` at `pobox.com`, or file an
  [issue](https://github.com/sjmudd/mysql-rpm-builder/issues/new).
- Bugs found via this process:
  [#118796](https://bugs.mysql.com/118796),
  [#115484](https://bugs.mysql.com/115484),
  [#111159](https://bugs.mysql.com/111159),
  [#111088](https://bugs.mysql.com/111088),
  [#120895](https://bugs.mysql.com/120895).
- Not MySQL-specific — the same approach applies to rebuilding any rpm.
  [bacula-rpm-builder](https://github.com/sjmudd/bacula-rpm-builder/) was
  the original inspiration, building from a git tree instead of a src.rpm.
