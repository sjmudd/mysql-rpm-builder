# MySQL rpm (re)builder

## Why this exists

Building MySQL RPMs from a clean, minimal environment shouldn't be
guesswork. In practice, the official `BuildRequires:` in `mysql.spec.in`
has repeatedly been incomplete (missing packages needed on specific RHEL
versions), and there's no public, repeatable process documented for
reproducing an official build, or for building a patched version of your
own. This repo makes both fully explicit and repeatable: given a known
starting point (a bare OS Docker image), it declares exactly what gets
installed and exactly how the rpm gets built, driven entirely by YAML
config and a single self-contained Go binary.

Feedback welcome — `sjmudd` at `pobox.com`, or file an
[issue](https://github.com/sjmudd/mysql-rpm-builder/issues/new).

## What it does

- **Build binary rpms from an official src.rpm**, in a container, with the
  build environment (packages, repos) fully declared rather than assumed.
- **Build a patched version** of that src.rpm (a spec patch and/or a source
  patch), same repeatable process.
- **Build a src.rpm directly from a mysql-server git tag, branch, or fork**
  (`build-rpm-from-git`) — useful for testing a candidate fix before it's
  an official release, since there's no official src.rpm to point at yet.

## Quick start

Two YAML files drive everything, layered **OS → MySQL version**:

- **`images.yaml`** — one entry per OS: which container image, and its repo
  setup (what to enable, what EPEL-equivalent package to install first).
- **`config.yaml`** — the build matrix: one entry per `(os, label)`, each
  fully explicit — its src.rpm URL, and how build deps get installed:

  ```yaml
  oses:
    ol10:
      builds:
        9.7.1:
          srpm: https://dev.mysql.com/get/Downloads/MySQL-9.0/mysql-community-9.7.1-1.el10.src.rpm
          auto_install_dependencies: true   # yum-builddep resolves BuildRequires
  ```

With that in place:

```
make                            # fmt, vet, lint, build -> ./mysql-rpm-builder
./build-one ol10 9.7.1          # build MySQL 9.7.1 for Oracle Linux 10
./build-one -n ol10 9.7.1       # dry run: print the docker command, don't run it
./build-one -test ol10 9.7.1    # stop as soon as compiling starts (fast sanity check)
```

Output lands in `built/<os><major>__<label>/`; logs (including a package
list snapshot before and after the build) in `log/`.

See [`REFERENCE.md`](REFERENCE.md) for the full command reference, config
schema, individual debugging steps, and the git-tag build path's internals.

## When a build fails

The most common cause is an incomplete `BuildRequires:` in the spec —
`yum-builddep` installs what's *declared*, and if a package that's
actually needed isn't declared, the build fails, sometimes only after a
long compile (see "Fixing the bug" below for how to tell these apart).

1. Read the error. A repo/package not found (`No match for argument: ...`)
   usually means a repo isn't enabled — check `images.yaml`'s `repos:` for
   that OS. A missing package deeper into the build usually means the spec
   itself needs it and doesn't ask for it.
2. Add the missing package(s) to that build's `packages:` list in
   `config.yaml`, alongside `auto_install_dependencies: true`, and rebuild.
   This gets you a working build quickly, but it's a local workaround, not
   a fix — anyone building from a clean environment elsewhere hits the same
   failure.
3. Use `./build-one -test <os> <label>` (stop once compiling starts) or
   `-until '<pattern>'` / `-timeout <dur>` to iterate quickly instead of
   waiting hours per attempt while you find the right package list.

## Fixing the actual bug and filing a report

If the build only fails because `BuildRequires:` is genuinely incomplete,
the real fix belongs in `mysql-server`'s
`packaging/rpm-oel/mysql.spec.in`, not just in this repo's `config.yaml`.
Workflow, based on bugs.mysql.com/120895:

1. Patch `mysql.spec.in` on a branch in your own `mysql-server` fork.
2. Build a src.rpm from that branch without needing an official release:
   `./build-rpm-from-git -repo <your fork URL> -ref <branch> <os> <version>`
   (`<version>` still needs to match the real `MYSQL_VERSION` at that
   commit — see [`REFERENCE.md`](REFERENCE.md)).
3. Point a `config.yaml` entry at the result (`srpm: file:///data/built-from-git/...`)
   with **no** `packages:` override — just `auto_install_dependencies: true`
   — and run a full `./build-one`. If it passes with nothing manually
   added, that's your proof the spec fix is complete, not just a working
   local hack.
4. Also confirm the *un*patched build actually fails the same way, so the
   report shows both sides (before/after) rather than just an assertion.
5. File at [bugs.mysql.com](https://bugs.mysql.com/) with the reproduction
   (OS/version, the missing package, the exact error) and the spec diff.

`docs/` has worked examples of this from real bugs found this way,
including the full before/after evidence trail for 120895.

## Related

- [`REFERENCE.md`](REFERENCE.md) — full command reference and internals
- [`docs/`](docs/) — case studies (git-tag build gotchas, src.rpm vs. git
  tarball differences, etc.)
- Some bugs found via this process:
  [#118796](https://bugs.mysql.com/118796),
  [#115484](https://bugs.mysql.com/115484),
  [#111159](https://bugs.mysql.com/111159),
  [#111088](https://bugs.mysql.com/111088),
  [#120895](https://bugs.mysql.com/120895)
- Not MySQL-specific — the same approach applies to rebuilding any rpm.
  [bacula-rpm-builder](https://github.com/sjmudd/bacula-rpm-builder/) was
  the original inspiration, building from a git tree instead of a src.rpm.
