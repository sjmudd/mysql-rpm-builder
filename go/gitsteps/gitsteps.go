// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

// Package gitsteps builds a MySQL src.rpm directly from a mysql-server git
// tag, without ever downloading Oracle's official src.rpm: clone the tag,
// run `cmake configure` (which is what produces the real
// packaging/rpm-oel/mysql.spec via CMakeLists.txt's own
// CONFIGURE_FILE(mysql.spec.in ...) -- nothing here hand-substitutes that
// template), generate Docs/INFO_SRC and (optionally) the pre-generated bison
// output, package the source tarball via CPack, and run `rpmbuild -bs`.
//
// This intentionally never resolves a config.yaml build entry: there is no
// src.rpm URL for a git-tag build to have, so it does not go through
// go/config.Resolve/go/steps.NewRunner at all. Root OS-prep is done via
// go/osprep directly, using git-build-deps.yaml's bootstrap package list
// (see docs/srpm-tarball-differs-from-git-tag.md for why a plain git
// checkout is an equally valid starting point as the official tarball).
package gitsteps

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sjmudd/mysql-rpm-builder/go/config"
	"github.com/sjmudd/mysql-rpm-builder/go/logx"
	"github.com/sjmudd/mysql-rpm-builder/go/osprep"
	"github.com/sjmudd/mysql-rpm-builder/go/osrelease"
)

// BuildUser is the non-root user that clones, configures and assembles the
// src.rpm -- the same user go/steps uses for its rpmbuild-user stages.
const BuildUser = "rpmbuild"

// DepsFile is the bootstrap-package config, relative to DataDir. Separate
// schema from config.yaml (flat oses.<os>.packages, no srpm/label nesting):
// see git-build-deps.yaml's own header comment for why.
const DepsFile = "git-build-deps.yaml"

// DefaultOutputDir is where produced RPMs land (relative to DataDir) when
// -o is not given.
const DefaultOutputDir = "built-from-git"

// DefaultRepo is cloned when -repo doesn't override it: Oracle's own public
// mysql-server repo, the same one this package has always cloned.
const DefaultRepo = "https://github.com/mysql/mysql-server.git"

// Runner carries the state for one git-tag build.
type Runner struct {
	OS  osrelease.Info
	Tag string // version label: used for naming (clone/build/output dirs) and
	// to predict the CPack-produced tarball filename (mysql-<Tag>.tar.gz).
	// Must match the real MYSQL_VERSION of whatever's actually checked out --
	// true by construction for a real release tag (mysql-9.7.1 checks out
	// exactly version 9.7.1), and true by convention when Repo/Ref override
	// what's cloned (e.g. testing a patched trunk commit still labelled by
	// its real 26.7.0 version, just from a different repo/branch).

	Repo string // git remote to clone; defaults to DefaultRepo
	Ref  string // branch or tag to check out (NOT a commit SHA -- see Clone's
	// doc comment); defaults to Tag

	DataDir   string
	OutputDir string // base directory (relative to DataDir) for produced RPMs
	SkipBison bool
}

// NewRunner detects the current OS and prepares a Runner for the given tag.
// repo/ref independently override what gets cloned: an empty repo defaults
// to DefaultRepo, an empty ref defaults to tag itself -- so overriding only
// the repo (e.g. to build against a Percona fork at whatever ref matches
// tag) or only the ref (e.g. a differently-named branch on the default
// repo) both work, not just the combined case. tag continues to serve
// purely as the version label regardless of what repo/ref resolve to.
// Passing both empty is exactly today's tag-only behaviour: DefaultRepo at
// ref==tag.
func NewRunner(dataDir, tag, outputDir string, skipBison bool, repo, ref string) (*Runner, error) {
	info, err := osrelease.Detect()
	if err != nil {
		return nil, err
	}
	if outputDir == "" {
		outputDir = DefaultOutputDir
	}
	repo, ref = resolveRepoRef(repo, ref, tag)
	return &Runner{OS: info, Tag: tag, Repo: repo, Ref: ref, DataDir: dataDir, OutputDir: outputDir, SkipBison: skipBison}, nil
}

// resolveRepoRef applies NewRunner's repo/ref defaulting as a pure function,
// split out so it can be unit-tested without going through osrelease.Detect.
func resolveRepoRef(repo, ref, tag string) (string, string) {
	if repo == "" {
		repo = DefaultRepo
	}
	if ref == "" {
		ref = tag
	}
	return repo, ref
}

func (r *Runner) version() string   { return strings.TrimPrefix(r.Tag, "mysql-") }
func (r *Runner) osLabel() string   { return r.OS.OSLabel() }
func (r *Runner) elDefine() string  { return fmt.Sprintf("el%d", r.OS.Major) }
func (r *Runner) rpmDefine() string { return r.elDefine() + " 1" }

// outputSubdir is where this (os, tag) build's src.rpm lands, mirroring
// go/steps.Runner's built/<os><major>__<label>/ convention.
func (r *Runner) outputSubdir() string {
	return filepath.Join(r.DataDir, r.OutputDir, r.osLabel()+"__"+r.Tag)
}

// gitWorkSubdir is where the clone/build trees live under the build user's
// home (~/git/), separate from srpm-based builds' ~/rpmbuild/.
const gitWorkSubdir = "git"

// cloneDir / buildDir are deterministic (not mktemp) paths under the current
// user's home: the root orchestrator and the re-exec'd build-user steps both
// need to agree on where the checkout/build tree live across the `su` hop.
func cloneDir(version string) (string, error) { return userPath(gitWorkSubdir, "mysql-"+version) }
func buildDir(version string) (string, error) {
	return userPath(gitWorkSubdir, "build-mysql-"+version)
}
func userPath(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, parts...)...), nil
}

func (r *Runner) generatedSpec(build string) string {
	return filepath.Join(build, "packaging", "rpm-oel", "mysql.spec")
}

// ---- root stage -------------------------------------------------------

// Run performs the full git-tag build: root OS-prep (via go/osprep, using
// git-build-deps.yaml's bootstrap package list), then re-execs as BuildUser
// to Clone, Configure and AssembleSRPM in turn. Must run as root.
func (r *Runner) Run() error {
	repos, packages, err := r.loadBootstrap()
	if err != nil {
		return err
	}
	// Repos first, then refresh/install: the full repo set (CRB/EPEL/etc.)
	// should be available before anything gets updated or installed, not
	// enabled partway through. (go/steps.Runner.Setup, by contrast, runs
	// refresh before setup-repos -- that ordering is left as-is here since
	// it's an established, tested sequence for the srpm-based path; only
	// this git-tag path's own orchestration is reordered.)
	if err := osprep.SetupRepos(repos); err != nil {
		return err
	}
	if err := osprep.Refresh(); err != nil {
		return err
	}
	if err := osprep.InstallPackages(packages); err != nil {
		return err
	}
	// FixAnnobin's known trigger is CMake's own compiler-detection check
	// during `cmake configure` (project()/enable_language()), not just a
	// full `rpmbuild -ba` compile -- see osprep.FixAnnobin's doc comment
	// ("makes cmake's 'is the C compiler working' check fail"). So it's kept
	// here even though this path only runs `rpmbuild -bs` (no compilation at
	// all). That said, it's only actually been observed to matter on el8/el9
	// gcc-toolset images, and git-build-deps.yaml only has an ol10 entry
	// today (which has no gcc-toolset dir at all, so this is always a no-op
	// in current practice) -- re-verify this is still needed/sufficient once
	// git-build-deps.yaml gains el8/el9 entries or -ba binary-build support
	// is added (see build-rpm-from-git's git history / the plan for that).
	if err := osprep.FixAnnobin(); err != nil {
		return err
	}
	// Pre-create and chown the output directory as root so the build-user
	// stages below can write the finished src.rpm into it directly, the same
	// reason go/steps.Runner.CreateUser pre-creates built/ etc.
	if err := osprep.CreateUser(BuildUser, []string{filepath.Join(r.DataDir, r.OutputDir)}); err != nil {
		return err
	}

	for _, stage := range []string{"git-clone", "git-configure", "git-assemble-srpm"} {
		if err := r.suBuild(stage); err != nil {
			return fmt.Errorf("%s: %w", stage, err)
		}
	}
	return nil
}

// loadBootstrap resolves the repo/EPEL setup for this OS from images.yaml
// (reused as-is -- the same OS/image/repo definitions apply to git-tag
// builds) and the bootstrap package list from DepsFile.
//
// images.yaml's repos.enable/epel_packages were originally set up and
// validated for the srpm-based build path (go/steps) specifically -- e.g.
// ol9's ol9_codeready_builder/ol9_developer_EPEL are what already makes
// gcc-toolset-* packages installable there today. This path is opportunistic
// reuse of that same config, not something independently verified for the
// git-tag flow's own needs; it happens to be sufficient in practice (see
// git-build-deps.yaml's ol9 entry, which needs a gcc-toolset package for
// exactly this reason) but isn't guaranteed to stay so for every OS/package.
func (r *Runner) loadBootstrap() (config.Repos, []string, error) {
	cfg, err := config.Load(r.DataDir, "")
	if err != nil {
		return config.Repos{}, nil, err
	}
	osDef, ok := cfg.OSDef(r.osLabel())
	if !ok {
		return config.Repos{}, nil, fmt.Errorf("no OS %q defined in %s", r.osLabel(), config.DefaultImagesFile)
	}
	packages, err := loadDepsPackages(r.DataDir, r.osLabel(), r.Tag)
	if err != nil {
		return config.Repos{}, nil, err
	}
	return osDef.Repos, packages, nil
}

// depsFile mirrors the top level of git-build-deps.yaml: an OS-stable base
// oses.<os>.packages list, plus an optional oses.<os>.builds.<tag>.packages
// added on top for that exact tag only (deliberately not config.yaml-shaped
// otherwise -- there is no srpm URL for this, and unlike config.yaml this IS
// additive rather than each build entry standing alone -- see this file's
// header comment for why).
type depsFile struct {
	OSes map[string]struct {
		Packages []string `yaml:"packages"`
		Builds   map[string]struct {
			Packages []string `yaml:"packages"`
		} `yaml:"builds"`
	} `yaml:"oses"`
}

func loadDepsPackages(dataDir, osLabel, tag string) ([]string, error) {
	depsPath := filepath.Join(dataDir, DepsFile)
	data, err := os.ReadFile(depsPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", depsPath, err)
	}
	var df depsFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&df); err != nil {
		return nil, fmt.Errorf("cannot parse %s: %w", depsPath, err)
	}
	entry, ok := df.OSes[osLabel]
	if !ok || len(entry.Packages) == 0 {
		return nil, fmt.Errorf("no packages configured for OS %q in %s", osLabel, depsPath)
	}
	packages := append([]string{}, entry.Packages...)
	if build, ok := entry.Builds[tag]; ok {
		packages = append(packages, build.Packages...)
	}
	return packages, nil
}

// stageCmd builds the shell command line re-exec'd as BuildUser for stage,
// porting every flag the top-level Runner was given through to the child
// process -- split out from suBuild as a pure function specifically so the
// full flag set can be asserted in a test without shelling out to `su`
// (see the FIXME history: -repo/-ref were once missing here entirely,
// silently discarding a fork/branch override on every re-exec'd stage).
func (r *Runner) stageCmd(exe, stage string) string {
	cmdStr := fmt.Sprintf("%s %s -o %s -repo %s -ref %s", exe, stage, r.OutputDir, r.Repo, r.Ref)
	if r.SkipBison {
		cmdStr += " -no-bison"
	}
	return cmdStr + " " + r.Tag
}

// suBuild re-execs this binary as BuildUser to run a single build-user stage,
// porting the same flags through (see stageCmd).
func (r *Runner) suBuild(stage string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logx.Logf("### switching to user %s to run %s", BuildUser, stage)
	cmd := exec.Command("su", "-", BuildUser, "-c", r.stageCmd(exe, stage))
	cmd.Stdout = logx.Writer()
	cmd.Stderr = logx.Writer()
	return cmd.Run()
}

// ---- build-user stages --------------------------------------------------

// Clone shallow-clones r.Ref from r.Repo into a deterministic path under the
// current user's home, labelled by r.Tag (the version, not necessarily the
// same as r.Ref -- see Runner.Tag). Must run as BuildUser.
func (r *Runner) Clone() error {
	src, err := cloneDir(r.version())
	if err != nil {
		return err
	}
	// --depth 1 fetches only the tree at that one commit, not the repo's 20+
	// years of history. --branch works for either a branch or a tag name and
	// implies --single-branch. --no-tags additionally skips fetching every
	// other tag's ref advertisement (mysql-server has thousands), which
	// --branch alone does not suppress.
	//
	// FIXME: --branch cannot resolve a bare commit SHA (only refs/heads and
	// refs/tags), so r.Ref is currently limited to branch/tag names. Pinning
	// an exact commit would need a different sequence instead (`git init` +
	// `git remote add` + `git fetch --depth 1 <repo> <sha>` + `git checkout
	// FETCH_HEAD`), not implemented here yet.
	logx.Logf("### clone: %s (ref %s) into %s (shallow, single ref only)", r.Repo, r.Ref, src)
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		return err
	}
	return osprep.Run("git", "clone", "--depth", "1", "--no-tags", "--branch", r.Ref,
		r.Repo, src)
}

// Configure runs cmake configure against the already-cloned tree. Must run
// as BuildUser, after Clone.
func (r *Runner) Configure() error {
	src, err := cloneDir(r.version())
	if err != nil {
		return err
	}
	build, err := buildDir(r.version())
	if err != nil {
		return err
	}
	boost, err := r.findOrFetchBoost(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(build, 0o755); err != nil {
		return err
	}
	logx.Log("### configure: cmake configure (produces packaging/rpm-oel/mysql.spec)")
	if err := osprep.RunIn(build, "cmake", src,
		"-DBUILD_CONFIG=mysql_release",
		"-DINSTALL_LAYOUT=RPM",
		"-DCMAKE_BUILD_TYPE=RelWithDebInfo",
		"-DWITH_BOOST="+boost,
		"-DWITH_SSL=system",
		"-DWITH_CURL=system",
		"-DWITH_SYSTEMD=1",
		"-DDOWNLOAD_BOOST=0",
	); err != nil {
		return err
	}
	spec := r.generatedSpec(build)
	if _, err := os.Stat(spec); err != nil {
		return fmt.Errorf("mysql.spec was not generated by cmake at %s -- not a LINUX_RHEL host?", spec)
	}
	return nil
}

// findOrFetchBoost determines the -DWITH_BOOST=<dir> value cmake configure
// needs. 9.x and 8.4.x both bundle a matching extra/boost/boost_* directory
// right in the source tree (findBoostDir; confirmed for 8.4.10 -- don't
// assume this from the 8.0.x behavior below, they differ). 8.0.x doesn't, so
// for that one we fall back to fetchBoost.
func (r *Runner) findOrFetchBoost(srcDir string) (string, error) {
	if dir, err := findBoostDir(srcDir); err == nil {
		return dir, nil
	}
	return r.fetchBoost(srcDir)
}

func findBoostDir(srcDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(srcDir, "extra", "boost", "boost_*"))
	if err != nil {
		return "", err
	}
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() {
			return m, nil
		}
	}
	return "", fmt.Errorf("no bundled extra/boost/boost_* found under %s", srcDir)
}

var (
	boostPackageNameRe = regexp.MustCompile(`SET\(BOOST_PACKAGE_NAME\s+"([^"]+)"\)`)
	boostTarballRe     = regexp.MustCompile(`SET\(BOOST_TARBALL\s+"([^"]+)"\)`)
	boostDownloadURLRe = regexp.MustCompile(`SET\(BOOST_DOWNLOAD_URL\s*"([^"]+)"`)
)

// fetchBoost handles the 8.0.x layout, where boost isn't bundled in the
// source tree and mysql.spec doesn't exist yet at this point in the pipeline
// (it's generated BY this same cmake configure run, so it can't be the
// source of truth here). cmake/boost.cmake is: it's plain text present right
// after Clone, and names both the exact package cmake requires
// (BOOST_PACKAGE_NAME) and where to get it (BOOST_DOWNLOAD_URL) -- never
// hardcode either, since the pinned boost version has changed across MySQL
// releases before and will again.
//
// BOOST_DOWNLOAD_URL and BOOST_TARBALL are themselves cmake ${VAR}
// references (e.g. ".../source/${BOOST_TARBALL}", where BOOST_TARBALL is
// itself "${BOOST_PACKAGE_NAME}.tar.bz2"), not literal strings -- so this
// resolves those two substitutions itself the same way cmake would, rather
// than assuming any particular literal file extension.
//
// Downloads (through a local cache under DataDir/boost-cache, since these
// containers are --rm and the tarball is large -- and per cmake/boost.cmake's
// own comment recommending downloading it only once) the tarball natively via
// Go rather than reaching for DOWNLOAD_BOOST=1, keeping fetches centralized
// through the one native downloader this project already uses elsewhere
// (steps.downloadFile / this package's own downloadFile) instead of letting
// cmake configure make in-container network calls. Handing cmake the cache
// dir as WITH_BOOST is sufficient: boost.cmake's own FIND_FILE/EXECUTE_PROCESS
// logic extracts a "<pkg>.tar.bz2" it finds sitting there itself, so no
// manual tar extraction is needed on our side either.
func (r *Runner) fetchBoost(srcDir string) (string, error) {
	cmakeFile := filepath.Join(srcDir, "cmake", "boost.cmake")
	data, err := os.ReadFile(cmakeFile)
	if err != nil {
		return "", fmt.Errorf("reading %s to determine required boost version: %w", cmakeFile, err)
	}
	pkgMatch := boostPackageNameRe.FindSubmatch(data)
	if pkgMatch == nil {
		return "", fmt.Errorf("could not find BOOST_PACKAGE_NAME in %s", cmakeFile)
	}
	pkg := string(pkgMatch[1])

	tarballMatch := boostTarballRe.FindSubmatch(data)
	if tarballMatch == nil {
		return "", fmt.Errorf("could not find BOOST_TARBALL in %s", cmakeFile)
	}
	tarballName := strings.ReplaceAll(string(tarballMatch[1]), "${BOOST_PACKAGE_NAME}", pkg)

	urlMatch := boostDownloadURLRe.FindSubmatch(data)
	if urlMatch == nil {
		return "", fmt.Errorf("could not find BOOST_DOWNLOAD_URL in %s", cmakeFile)
	}
	url := strings.ReplaceAll(string(urlMatch[1]), "${BOOST_TARBALL}", tarballName)

	cacheDir := filepath.Join(r.DataDir, "boost-cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	tarball := filepath.Join(cacheDir, tarballName)
	if _, err := os.Stat(tarball); err == nil {
		logx.Logf("### configure: using cached %s (required boost: %s)", tarball, pkg)
	} else {
		logx.Logf("### configure: boost not bundled in this source tree; downloading %s: %s", pkg, url)
		if err := downloadFile(tarball, url); err != nil {
			return "", err
		}
	}
	return cacheDir, nil
}

// AssembleSRPM finishes the build after Configure: generates Docs/INFO_SRC,
// optionally the pre-generated bison output (skipped when SkipBison is set --
// mysql.spec requires bison unconditionally, so a real rpmbuild -ba
// regenerates these itself regardless of what the tarball ships), packages
// the source tarball via CPack, runs `rpmbuild -bs`, and copies the
// resulting src.rpm into <output_dir>/<os><major>__<tag>/. Must run as
// BuildUser, after Configure.
func (r *Runner) AssembleSRPM() error {
	src, err := cloneDir(r.version())
	if err != nil {
		return err
	}
	build, err := buildDir(r.version())
	if err != nil {
		return err
	}

	logx.Log("### info_src: generating Docs/INFO_SRC")
	if err := osprep.RunIn(build, "cmake", "--build", ".", "--target", "INFO_SRC"); err != nil {
		return err
	}
	if err := osprep.Run("cp", filepath.Join(build, "Docs", "INFO_SRC"), filepath.Join(src, "Docs", "INFO_SRC")); err != nil {
		return err
	}

	if r.SkipBison {
		logx.Log("### bison: -no-bison set, skipping the pre-generated bison output (sql_yacc.cc/.h, sql_hints.yy.cc/.h)")
	} else {
		logx.Log("### bison: generating pre-generated bison output")
		if err := osprep.RunIn(build, "cmake", "--build", ".", "--target", "GenBison_mysql", "GenBison_hints"); err != nil {
			return err
		}
		sqlDest := filepath.Join(src, "sql") + string(filepath.Separator)
		if err := osprep.Run("cp", filepath.Join(build, "sql", "sql_yacc.cc"), filepath.Join(build, "sql", "sql_yacc.h"), sqlDest); err != nil {
			return err
		}
		if err := osprep.Run("cp", filepath.Join(build, "sql", "sql_hints.yy.cc"), filepath.Join(build, "sql", "sql_hints.yy.h"), sqlDest); err != nil {
			return err
		}
	}

	// Also what excludes the vendored *Makefile*s the official tarball
	// excludes, via CPACK_SOURCE_IGNORE_FILES.
	logx.Log("### package_source: packaging source tarball via CPack")
	if err := osprep.RunIn(build, "cmake", "--build", ".", "--target", "package_source"); err != nil {
		return err
	}
	tarball := filepath.Join(build, "mysql-"+r.version()+".tar.gz")
	if _, err := os.Stat(tarball); err != nil {
		return fmt.Errorf("expected %s not found", tarball)
	}

	spec := r.generatedSpec(build)
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	rpmbuildDir := filepath.Join(home, "rpmbuild")
	for _, sub := range []string{"SOURCES", "SPECS", "SRPMS"} {
		if err := os.MkdirAll(filepath.Join(rpmbuildDir, sub), 0o755); err != nil {
			return err
		}
	}
	if err := osprep.Run("cp", tarball, filepath.Join(rpmbuildDir, "SOURCES")+string(filepath.Separator)); err != nil {
		return err
	}
	specCopy := filepath.Join(rpmbuildDir, "SPECS", "mysql.spec")
	if err := osprep.Run("cp", spec, specCopy); err != nil {
		return err
	}

	// e.g. el8/el9's compat-library boost source (see fetchExternalSources'
	// doc comment) -- rpmbuild -bs errors on any declared Source that isn't
	// present, even ones whose %if branch is irrelevant to what we actually
	// want out of this build.
	if err := fetchExternalSources(specCopy, r.elDefine(), filepath.Join(rpmbuildDir, "SOURCES"), filepath.Join(r.DataDir, "SRPMS")); err != nil {
		return err
	}

	// mysql-8.0.x's spec (only -- 8.4.x/9.x's spec.in doesn't declare these
	// at all, confirmed by checking both) declares Source90/91 as bare local
	// filenames, not URLs, so fetchExternalSources' http(s)-only scan never
	// sees them. They don't exist anywhere in the public git tree either
	// (packaging/rpm-oel/'s own CMakeLists.txt only ever generates
	// mysql.spec/mysql.init there) -- part of Oracle's private packaging
	// pipeline, same theme as docs/srpm-tarball-differs-from-git-tag.md.
	if err := provideLegacyFilterScripts(specCopy, filepath.Join(rpmbuildDir, "SOURCES")); err != nil {
		return err
	}

	logx.Log("### assemble_srpm: rpmbuild -bs")
	if err := osprep.RunIn(filepath.Join(rpmbuildDir, "SPECS"), "rpmbuild", "--define", r.rpmDefine(), "-bs", "mysql.spec"); err != nil {
		return err
	}

	return r.collect(rpmbuildDir)
}

// fetchExternalSources downloads (with caching under cacheDir, the same
// SRPMS/ directory the srpm-based path uses for its own downloads -- these
// containers are --rm, so without this every run would re-fetch a
// potentially large tarball from scratch) any Source: the *real,
// macro-expanded and %if-evaluated* spec declares as an http(s) URL and
// doesn't already have.
//
// The concrete case this exists for: el8/el9 mysql.spec always builds an
// auxiliary compat library (a backward-compatible libmysqlclient.so) pinned
// to "the latest previous major version available" at whatever point that
// specific mysql-server release was cut -- e.g. mysql-9.7.1's spec pins it
// to 8.0.37 today, via `%global compatsrc
// https://cdn.mysql.com/Downloads/MySQL-8.0/mysql-boost-%{compatver}.tar.gz`
// under `%if 0%{?rhel} == 8 || 0%{?rhel} == 9`. That pin changes over
// releases, so it must be read from the real spec every time, never
// hardcoded. Grepping the raw spec.in template wouldn't work either: it
// would still show the literal, unexpanded `%{compatsrc}` macro reference,
// not the resolved URL -- only `rpmspec -P` (parse: expands macros AND
// evaluates %if, so only the branches active for this specific el<N> build
// survive) gives us the real, active Source: lines to act on.
func fetchExternalSources(specPath, elDefine, sourcesDir, cacheDir string) error {
	out, err := exec.Command("rpmspec", "--define", elDefine+" 1", "-P", specPath).Output()
	if err != nil {
		return fmt.Errorf("rpmspec -P %s: %w", specPath, err)
	}
	re := regexp.MustCompile(`(?m)^Source[0-9]*:\s*(https?://\S+)\s*$`)
	for _, m := range re.FindAllSubmatch(out, -1) {
		url := string(m[1])
		name := path.Base(url)
		dst := filepath.Join(sourcesDir, name)
		if _, err := os.Stat(dst); err == nil {
			// The main tarball's own Source0 is also an http(s) URL (the
			// official release download link), and it's already sitting
			// here from the CPack package_source step a few lines above --
			// re-fetching it from the CDN would be redundant even when the
			// CDN still has it, and fails outright once an older release is
			// superseded and pulled (confirmed: mysql-8.4.7.tar.gz 404s on
			// cdn.mysql.com while mysql-8.4.10.tar.gz still 200s).
			logx.Logf("### assemble_srpm: %s already present, leaving it alone", dst)
			continue
		}
		cached := filepath.Join(cacheDir, name)
		if _, err := os.Stat(cached); err == nil {
			logx.Logf("### assemble_srpm: using cached external source %s", cached)
		} else {
			logx.Logf("### assemble_srpm: downloading external source declared by the spec: %s", url)
			if err := os.MkdirAll(cacheDir, 0o755); err != nil {
				return err
			}
			if err := downloadFile(cached, url); err != nil {
				return err
			}
		}
		if err := osprep.Run("cp", cached, dst); err != nil {
			return err
		}
	}
	return nil
}

//go:embed assets/filter-provides.sh
var filterProvidesSH string

//go:embed assets/filter-requires.sh
var filterRequiresSH string

// legacyFilterScripts are extracted verbatim (see the commit introducing
// this for the sha256sums used to verify the extraction) from Oracle's
// official mysql-community-8.0.46-1.el9.src.rpm, the only place they exist
// at all -- mysql-8.0.x's spec.in declares Source90/91 by these exact bare
// filenames, but they're absent from the public git tree (checked
// packaging/rpm-oel/ directly) and from 8.4.10/9.7.1's official src.rpms
// too (checked: neither's spec.in declares Source90/91 at all, so this is
// an 8.0.x-only gap, not something every version needs). They're generic,
// static rpm %__perl_provides/%__perl_requires wrapper scripts with no
// MySQL-version-specific content, so the same two files are expected to
// work unchanged for any 8.0.x tag -- re-verify against a real official
// src.rpm if a future 8.0.x build ever fails the same way with different
// content expected.
var legacyFilterScripts = map[string]string{
	"filter-provides.sh": filterProvidesSH,
	"filter-requires.sh": filterRequiresSH,
}

// provideLegacyFilterScripts writes legacyFilterScripts into sourcesDir, but
// only for a script that's both (a) actually declared by the spec as a
// bare-filename Source and (b) not already present in sourcesDir -- never
// unconditionally, so this stays a no-op for every OS/tag that doesn't need
// it (i.e. everything except mysql-8.0.x today), and never overwrites a file
// that's already there for any reason (e.g. a real one the git tree starts
// shipping in some future version -- that must win over this hardcoded
// fallback, not get clobbered by it).
func provideLegacyFilterScripts(specPath, sourcesDir string) error {
	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", specPath, err)
	}
	for name, content := range legacyFilterScripts {
		re := regexp.MustCompile(`(?m)^Source[0-9]*:\s*` + regexp.QuoteMeta(name) + `\s*$`)
		if !re.Match(data) {
			continue
		}
		dst := filepath.Join(sourcesDir, name)
		if _, err := os.Stat(dst); err == nil {
			logx.Logf("### assemble_srpm: %s already present, leaving it alone", dst)
			continue
		}
		logx.Logf("### assemble_srpm: providing %s (declared by the spec, absent from the git tree)", name)
		if err := os.WriteFile(dst, []byte(content), 0o755); err != nil {
			return err
		}
	}
	return nil
}

// downloadFile fetches url and writes it to dst, downloading through
// dst+".download" first and renaming into place only once the transfer
// completes, so a failed/interrupted download never leaves a corrupt file
// at dst for a later run's cache check to mistake for a good one.
// Deliberately a local duplicate of go/steps/helpers.go's downloadFile
// (same small pattern) rather than a shared import, to keep go/gitsteps
// independent of go/steps.
func downloadFile(dst, url string) error {
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: unexpected status %s", url, resp.Status)
	}

	tmp := dst + ".download"
	out2, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	if _, err := io.Copy(out2, resp.Body); err != nil {
		_ = out2.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	if err := out2.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	return os.Rename(tmp, dst)
}

// collect copies the produced src.rpm(s) into <output_dir>/<os><major>__<tag>/,
// mirroring go/steps.Runner.Collect()'s built/<os><major>__<label>/
// convention. The parent output directory must already exist and be
// writable by BuildUser -- Run creates and chowns it during the root OS-prep
// phase, before re-execing as BuildUser.
func (r *Runner) collect(rpmbuildDir string) error {
	matches, err := filepath.Glob(filepath.Join(rpmbuildDir, "SRPMS", "*.rpm"))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("no src.rpm found under %s", filepath.Join(rpmbuildDir, "SRPMS"))
	}
	dest := r.outputSubdir()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	logx.Logf("### assemble_srpm: copying %d src.rpm(s) to %s", len(matches), dest)
	// FIXME: this is a plain "cp", leaving the original also sitting in
	// rpmbuildDir/SRPMS for the rest of the container's lifetime -- doubling
	// disk usage there until --rm tears the container down. Nothing reads
	// that copy again after this point, so switching to "mv" (an actual
	// move, not "mv" as an alias for cp) would cut peak disk usage during
	// the run, which matters on disk/NAS-constrained hosts even though the
	// duplicate is eventually reclaimed anyway. Not changed here -- revisit
	// later.
	args := append(append([]string{}, matches...), dest+string(filepath.Separator))
	return osprep.Run("cp", args...)
}
