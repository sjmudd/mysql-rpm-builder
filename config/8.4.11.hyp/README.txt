Patch the build to compile the hypergraph optimizer into the normal
binaries (WITH_HYPERGRAPH_OPTIMIZER_DEFAULT forced ON, instead of only
on debug builds).

Ported forward from config/8.2.0.hyp/ to mysql-8.4.11: same semantic
change to CMakeLists.txt, re-diffed against the current file (only the
line numbers moved, the surrounding %{?debug} gate is unchanged since
8.2.0). Release suffix bumped to 1%{?commercial:.1}%{?dist}.hypergraph
so a patched build is never mistakable for an official one.

Verifies config's new explicit `patches:` field (see config.yaml's
oses.ol10.builds.8.4.11.hyp entry): apply-patches now checks both
SPECS/mysql.spec.patch and SOURCES/000.hypergraph_optimizer_enable.diff
exist before applying anything, instead of silently no-opping had the
directory or a file been missing/misnamed.

Real bug found and fixed while porting: the CMakeLists.txt patch's `-p1`
path depth is one level shallower than 8.2.0's. mysql-8.4.11's Source0
tarball has an internal top-level `mysql-8.4.11/` directory, and
`%setup -q -T -a 0 -c -n %{src_dir}` (`src_dir` = `mysql-8.4.11`) creates
and cds into a directory of that same name *before* extracting Source0
into it -- so the real file lives at `mysql-8.4.11/CMakeLists.txt`
relative to the %prep working directory, not at `CMakeLists.txt` directly.
The patch header therefore needs two leading path components
(`a/mysql-8.4.11/CMakeLists.txt`) for `-p1` to strip down to the right
relative path; a naive one-component regeneration (`a/CMakeLists.txt`,
matching the original 8.2.0.hyp header) fails with "can't find file to
patch" (confirmed by reproducing the %setup extraction by hand). Whether
the original 8.2.0.hyp patch has ever actually been build-tested is
unknown -- it may have the same latent bug.

Verified end-to-end via `./build-one -test -c ol10-8.4.11.hyp.yaml ol10
8.4.11.hyp` (2026-08-13): apply-patches validated + applied both files,
rpmbuild's `%patch0 -p1` applied cleanly during %prep, cmake configure
ran, and the generated config header shows `#define
WITH_HYPERGRAPH_OPTIMIZER` (no `#undef`) -- confirming the forced-ON
default actually took effect, not just that the patch applied
syntactically. Build was `-test`-stopped at first compilation (by
design, to avoid an hours-long full build); not run to completion, and
not yet merged into the real config.yaml (see ol10-8.4.11.hyp.yaml at
the repo root for the alt-config used).
