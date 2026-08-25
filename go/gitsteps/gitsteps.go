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
// This intentionally never resolves a rpm-build-config.yaml build entry: there is no
// src.rpm URL for a git-tag build to have, so it does not go through
// go/config.Resolve/go/steps.NewRunner at all. Root OS-prep is done via
// go/osprep directly, using git-build-config.yaml's bootstrap package list
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
	"os/user"
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

// DepsFile is the git-based build's own config, relative to DataDir.
// Separate schema from rpm-build-config.yaml (nested oses.<os>.<tier>,
// no srpm/label nesting): see git-build-config.yaml's own header comment
// for why.
const DepsFile = "git-build-config.yaml"

// DefaultOutputDir is the base directory (relative to DataDir) where
// produced RPMs land when -o is not given -- config.BuiltDir, shared with
// go/steps' download-based build-one path (its own builtDir() joins the
// same base with a "build-one" segment), so all three build types
// partition one base by build type rather than each owning a separate root.
const DefaultOutputDir = config.BuiltDir

// rpmbuild's own fixed subdirectory names inside ~/rpmbuild (SPECS,
// SOURCES, SRPMS, RPMS) -- rpmbuild's contract, not this package's to
// rename, but named once here rather than repeated as bare strings.
// Distinct from config.SRPMSCacheDir, which is this package's own
// top-level download cache (see fetchExternalSources) -- same word,
// unrelated directories.
const (
	rpmbuildSpecsDir   = "SPECS"
	rpmbuildSourcesDir = "SOURCES"
	rpmbuildSRPMSDir   = "SRPMS"
	rpmbuildRPMSDir    = "RPMS"
)

// Subcommand names for the whole git-based build family, defined once here
// so go/cmd's dispatch and this package's own suBuild re-exec calls can't
// drift apart silently -- both sides are untyped strings otherwise, so the
// compiler can't catch a typo across that boundary the way it would for a
// misspelled identifier. CmdBuildSrcRPM/CmdBuildRPMs double as the
// DefaultOutputDir partition names (see outputSubdir) as well as the host
// subcommand names, so a result directory is self-explanatory about which
// command produced it.
const (
	CmdBuildSrcRPM = "git-build-src-rpm" // host
	CmdBuildRPMs   = "git-build-rpms"    // host

	CmdSrcRPMBuild  = "git-src-rpm-build"  // in-container orchestrator
	CmdAllRPMsBuild = "git-all-rpms-build" // in-container orchestrator

	CmdClone          = "git-clone"
	CmdApplyPatches   = "git-apply-patches"
	CmdConfigure      = "git-configure"
	CmdAssembleSrcRPM = "git-assemble-src-rpm"
	CmdStage          = "git-stage"
	CmdBuildDeps      = "git-builddeps"
	CmdRPMBuild       = "git-rpmbuild"

	// CmdGenerateConfig turns a git-build-src-rpm run's output into a
	// build-one config entry -- see GenerateBuildOneConfig. Host-only, no
	// container involved.
	CmdGenerateConfig = "generate-build-one-config"
)

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

// outputSubdir is where this (os, tag) build's result lands:
// <OutputDir>/<buildType>/<os><major>__<tag>/, mirroring
// go/steps.Runner's built/build-one/<os><major>__<label>/ convention.
// buildType partitions this package's two producers (AssembleSrcRPM's
// "git-build-src-rpm" and RPMBuild's "git-build-rpms") so a src.rpm-only
// run and a full-rpm run for the same (os, tag) never land in, or mix
// files into, the same directory.
func (r *Runner) outputSubdir(buildType string) string {
	return filepath.Join(r.DataDir, r.OutputDir, buildType, r.osLabel()+"__"+r.Tag)
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

// SrcRPMBuild performs the src.rpm-only git-tag build: root OS-prep (via
// go/osprep, using git-build-config.yaml's bootstrap package list), then
// re-execs as BuildUser to Clone, Configure and AssembleSrcRPM in turn. Must
// run as root.
func (r *Runner) SrcRPMBuild() error {
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
	// all). It's only actually been observed to matter on el8/el9
	// gcc-toolset images; on ol10 (no gcc-toolset dir at all) this is
	// always a no-op. AllRPMsBuild calls it too, for the same reason plus
	// the real `rpmbuild -ba` compile that follows.
	if err := osprep.FixAnnobin(); err != nil {
		return err
	}
	// Pre-create and chown the output directory as root so the build-user
	// stages below can write the finished src.rpm into it directly, the same
	// reason go/steps.Runner.CreateUser pre-creates built/ etc.
	if err := osprep.CreateUser(BuildUser, []string{filepath.Join(r.DataDir, r.OutputDir)}); err != nil {
		return err
	}

	for _, stage := range []string{CmdClone, CmdApplyPatches, CmdConfigure, CmdAssembleSrcRPM} {
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
// git-build-config.yaml's ol9 entry, which needs a gcc-toolset package for
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

// depsFile mirrors the top level of git-build-config.yaml: an OS-stable
// minimal_git_packages base, plus per-tag src_rpm_build_packages (added on
// top for that exact tag only), all_rpms_extra_packages, and patches (see
// loadPatches/ApplyPatches -- unrelated to the three dependency tiers) --
// see the file's own header comment for what each tier means and why
// they're kept separate. Deliberately not config.yaml-shaped otherwise --
// there is no srpm URL for this, and minimal_git_packages/
// src_rpm_build_packages are additive rather than each build entry
// standing alone.
type depsFile struct {
	OSes map[string]struct {
		MinimalGitPackages []string `yaml:"minimal_git_packages"`
		Builds             map[string]struct {
			SrcRPMBuildPackages  []string `yaml:"src_rpm_build_packages"`
			AllRPMsExtraPackages []string `yaml:"all_rpms_extra_packages"`
			Patches              []string `yaml:"patches,omitempty"`
		} `yaml:"builds"`
	} `yaml:"oses"`
}

func loadDeps(dataDir, osLabel string) (depsFile, error) {
	depsPath := filepath.Join(dataDir, DepsFile)
	data, err := os.ReadFile(depsPath)
	if err != nil {
		return depsFile{}, fmt.Errorf("cannot read %s: %w", depsPath, err)
	}
	var df depsFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&df); err != nil {
		return depsFile{}, fmt.Errorf("cannot parse %s: %w", depsPath, err)
	}
	if entry, ok := df.OSes[osLabel]; !ok || len(entry.MinimalGitPackages) == 0 {
		return depsFile{}, fmt.Errorf("no minimal_git_packages configured for OS %q in %s", osLabel, depsPath)
	}
	return df, nil
}

// loadDepsPackages returns the bootstrap package list needed just to get
// `cmake configure` running (tiers 2+3: minimal_git_packages plus this
// tag's own src_rpm_build_packages) -- used by both git-build-src-rpm and
// git-build-rpms, since both run `cmake configure` before either produces
// anything.
func loadDepsPackages(dataDir, osLabel, tag string) ([]string, error) {
	df, err := loadDeps(dataDir, osLabel)
	if err != nil {
		return nil, err
	}
	entry := df.OSes[osLabel]
	packages := append([]string{}, entry.MinimalGitPackages...)
	if build, ok := entry.Builds[tag]; ok {
		packages = append(packages, build.SrcRPMBuildPackages...)
	}
	return packages, nil
}

// loadAllRPMsExtraPackages returns tier 4: packages that patch a gap in
// this tag's spec's own declared BuildRequires. Used only by BuildDeps
// (git-build-rpms); git-build-src-rpm never needs this since `-bs` never
// evaluates BuildRequires. Empty (not an error) when nothing is configured
// for this (os, tag) -- most tags need nothing here.
func loadAllRPMsExtraPackages(dataDir, osLabel, tag string) ([]string, error) {
	df, err := loadDeps(dataDir, osLabel)
	if err != nil {
		return nil, err
	}
	build, ok := df.OSes[osLabel].Builds[tag]
	if !ok {
		return nil, nil
	}
	return build.AllRPMsExtraPackages, nil
}

// loadPatches returns this (os, tag)'s configured patch filenames from
// config/git-patches/<tag>/ (see ApplyPatches). Every entry must be a bare
// filename, no path component -- a "/" in one is a config error, not
// silently joined into some other path.
func loadPatches(dataDir, osLabel, tag string) ([]string, error) {
	df, err := loadDeps(dataDir, osLabel)
	if err != nil {
		return nil, err
	}
	build, ok := df.OSes[osLabel].Builds[tag]
	if !ok {
		return nil, nil
	}
	for _, p := range build.Patches {
		if filepath.Base(p) != p {
			return nil, fmt.Errorf("git-build-config.yaml: patch %q for %s/%s must be a bare filename (no path component) -- put it directly under config/git-patches/%s/", p, osLabel, tag, tag)
		}
	}
	return build.Patches, nil
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
	// implies --single-branch, which alone keeps this to one ref -- it does
	// not fetch mysql-server's other several thousand tags. When r.Ref names
	// a tag, that single tag's ref is still created locally as part of
	// resolving --branch, so `git tag`/`git describe --tags` in the checked-
	// out tree shows what was cloned; deliberately not passing --no-tags,
	// which would suppress even that one ref and leave no trace of which tag
	// produced the checkout.
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
	return osprep.Run("git", "clone", "--depth", "1", "--branch", r.Ref,
		r.Repo, src)
}

// ApplyPatches applies this (os, tag)'s configured patches (see loadPatches)
// against the freshly cloned tree via `git apply`, in list order, before
// Configure runs -- so a patch to packaging/rpm-oel/mysql.spec.in takes
// effect in the spec cmake renders. No-op if none are configured. Must run
// as BuildUser, after Clone. Shared by git-build-src-rpm and git-build-rpms.
func (r *Runner) ApplyPatches() error {
	patches, err := loadPatches(r.DataDir, r.osLabel(), r.Tag)
	if err != nil {
		return err
	}
	if len(patches) == 0 {
		logx.Log("### apply-patches: no patches configured, skipping")
		return nil
	}
	src, err := cloneDir(r.version())
	if err != nil {
		return err
	}
	patchDir := filepath.Join(r.DataDir, "config", "git-patches", r.Tag)
	for _, name := range patches {
		p := filepath.Join(patchDir, name)
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("apply-patches: declared patch %q not found at %s", name, p)
		}
		logx.Logf("### apply-patches: applying %s", name)
		if err := osprep.RunIn(src, "git", "apply", p); err != nil {
			return fmt.Errorf("apply-patches: %s: %w", name, err)
		}
	}
	return nil
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

// AssembleSrcRPM stages the build (see Stage) and runs `rpmbuild -bs`,
// producing just a src.rpm into <output_dir>/<os><major>__<tag>/. Must run
// as BuildUser, after Configure.
func (r *Runner) AssembleSrcRPM() error {
	if err := r.Stage(); err != nil {
		return err
	}
	dir, err := rpmbuildDir()
	if err != nil {
		return err
	}
	logx.Log("### assemble_src_rpm: rpmbuild -bs")
	if err := osprep.RunIn(filepath.Join(dir, rpmbuildSpecsDir), "rpmbuild", "--define", r.rpmDefine(), "-bs", "mysql.spec"); err != nil {
		return err
	}
	if err := r.collect(dir); err != nil {
		return err
	}
	return r.writeConfigSidecar(r.outputSubdir(CmdBuildSrcRPM))
}

// rpmbuildDir returns ~/rpmbuild for the current user -- shared by both the
// src.rpm-only path (AssembleSrcRPM) and the full binary-rpm path
// (RPMBuild), both of which read/write the same staged
// SPECS/SOURCES/SRPMS/RPMS tree that Stage prepares.
func rpmbuildDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "rpmbuild"), nil
}

// Stage prepares everything a build needs after Configure: generates
// Docs/INFO_SRC, optionally the pre-generated bison output (skipped when
// SkipBison is set -- mysql.spec requires bison unconditionally, so a real
// rpmbuild -ba regenerates these itself regardless of what the tarball
// ships), packages the source tarball via CPack, and copies the
// cmake-rendered spec and tarball into ~/rpmbuild/{SPECS,SOURCES}. Shared by
// both AssembleSrcRPM (src.rpm only, via rpmbuild -bs) and BuildDeps +
// RPMBuild (full binary rpms, via rpmbuild -ba) -- neither depends on how
// far the other path goes. Must run as BuildUser, after Configure.
func (r *Runner) Stage() error {
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
	tarball, err := findSourceTarball(build, r.version())
	if err != nil {
		return err
	}

	spec := r.generatedSpec(build)
	dir, err := rpmbuildDir()
	if err != nil {
		return err
	}
	for _, sub := range []string{rpmbuildSourcesDir, rpmbuildSpecsDir, rpmbuildSRPMSDir, rpmbuildRPMSDir} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return err
		}
	}
	if err := osprep.Run("cp", tarball, filepath.Join(dir, rpmbuildSourcesDir)+string(filepath.Separator)); err != nil {
		return err
	}
	specCopy := filepath.Join(dir, rpmbuildSpecsDir, "mysql.spec")
	if err := osprep.Run("cp", spec, specCopy); err != nil {
		return err
	}

	// e.g. el8/el9's compat-library boost source (see fetchExternalSources'
	// doc comment) -- rpmbuild -bs errors on any declared Source that isn't
	// present, even ones whose %if branch is irrelevant to what we actually
	// want out of this build.
	if err := fetchExternalSources(specCopy, r.elDefine(), filepath.Join(dir, rpmbuildSourcesDir), filepath.Join(r.DataDir, config.SRPMSCacheDir)); err != nil {
		return err
	}

	// mysql-8.0.x's spec (only -- 8.4.x/9.x's spec.in doesn't declare these
	// at all, confirmed by checking both) declares Source90/91 as bare local
	// filenames, not URLs, so fetchExternalSources' http(s)-only scan never
	// sees them. They don't exist anywhere in the public git tree either
	// (packaging/rpm-oel/'s own CMakeLists.txt only ever generates
	// mysql.spec/mysql.init there) -- part of Oracle's private packaging
	// pipeline, same theme as docs/srpm-tarball-differs-from-git-tag.md.
	return provideLegacyFilterScripts(specCopy, filepath.Join(dir, rpmbuildSourcesDir))
}

// findSourceTarball locates the CPack-produced source tarball for this
// version: "mysql-<version>" plus whatever suffix this source tree's own
// cmake/*_version.cmake appends (empty for vanilla mysql-server, but a
// fork can add one: villagesql-server's cmake/vsql_version.cmake appends
// "-villagesql-<vsql-version>[-<pre>][-<githash>]"). Globs rather than
// assuming an exact name, so this works for any fork's suffix, not just
// one hardcoded pattern.
func findSourceTarball(build, version string) (string, error) {
	pattern := filepath.Join(build, "mysql-"+version+"*.tar.gz")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one source tarball matching %s, found %d", pattern, len(matches))
	}
	return matches[0], nil
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
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("rpmspec -P %s: %w:\n%s", specPath, err, exitErr.Stderr)
		}
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
			logx.Logf("### assemble_src_rpm: %s already present, leaving it alone", dst)
			continue
		}
		cached := filepath.Join(cacheDir, name)
		if _, err := os.Stat(cached); err == nil {
			logx.Logf("### assemble_src_rpm: using cached external source %s", cached)
		} else {
			logx.Logf("### assemble_src_rpm: downloading external source declared by the spec: %s", url)
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
			logx.Logf("### assemble_src_rpm: %s already present, leaving it alone", dst)
			continue
		}
		logx.Logf("### assemble_src_rpm: providing %s (declared by the spec, absent from the git tree)", name)
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
	matches, err := filepath.Glob(filepath.Join(rpmbuildDir, rpmbuildSRPMSDir, "*.rpm"))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return fmt.Errorf("no src.rpm found under %s", filepath.Join(rpmbuildDir, rpmbuildSRPMSDir))
	}
	dest := r.outputSubdir(CmdBuildSrcRPM)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	logx.Logf("### assemble_src_rpm: copying %d src.rpm(s) to %s", len(matches), dest)
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

const sidecarFile = ".config.yaml"

// writeConfigSidecar writes dest/.config.yaml, recording this build's
// configuration for generate-build-one-config to read later.
func (r *Runner) writeConfigSidecar(dest string) error {
	commit, err := r.commitSHA()
	if err != nil {
		return err
	}
	patches, err := loadPatches(r.DataDir, r.osLabel(), r.Tag)
	if err != nil {
		return err
	}
	df, err := loadDeps(r.DataDir, r.osLabel())
	if err != nil {
		return err
	}
	entry := df.OSes[r.osLabel()]
	ann := config.Annotations{
		Repo:                r.Repo,
		Ref:                 r.Ref,
		Commit:              commit,
		GitPatches:          patches,
		MinimalGitPackages:  entry.MinimalGitPackages,
		SrcRPMBuildPackages: entry.Builds[r.Tag].SrcRPMBuildPackages,
		BisonGenerated:      !r.SkipBison,
	}
	data, err := yaml.Marshal(ann)
	if err != nil {
		return fmt.Errorf("marshalling %s: %w", sidecarFile, err)
	}
	return os.WriteFile(filepath.Join(dest, sidecarFile), data, 0o644)
}

// commitSHA resolves fresh from the on-disk checkout rather than caching
// from Clone: each git-* stage re-execs as its own process (see suBuild),
// so nothing set during Clone's process would survive to here.
func (r *Runner) commitSHA() (string, error) {
	src, err := cloneDir(r.version())
	if err != nil {
		return "", err
	}
	out, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD in %s: %w", src, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// GenerateBuildOneConfig globs the src.rpm a prior git-build-src-rpm run
// produced for (osLabel, tag) under outputDir/git-build-src-rpm/<osLabel>__<tag>/,
// reads its .config.yaml sidecar if present, and writes a scratch
// <osLabel>-<tag>-from-git.yaml build-one can consume via -c. Runs
// entirely on the host, no container: osLabel here is the target OS a
// prior run was for, not this host's own OS, so this doesn't use
// Runner/osrelease detection at all. Returns the written file's path.
func GenerateBuildOneConfig(dataDir, outputDir, osLabel, tag string) (string, error) {
	if outputDir == "" {
		outputDir = DefaultOutputDir
	}
	srcDir := filepath.Join(dataDir, outputDir, CmdBuildSrcRPM, osLabel+"__"+tag)
	matches, err := filepath.Glob(filepath.Join(srcDir, "*.src.rpm"))
	if err != nil {
		return "", err
	}
	if len(matches) != 1 {
		return "", fmt.Errorf("expected exactly one src.rpm in %s, found %d", srcDir, len(matches))
	}

	var ann *config.Annotations
	if data, err := os.ReadFile(filepath.Join(srcDir, sidecarFile)); err == nil {
		var a config.Annotations
		if err := yaml.Unmarshal(data, &a); err != nil {
			return "", fmt.Errorf("parsing %s: %w", filepath.Join(srcDir, sidecarFile), err)
		}
		ann = &a
	}

	label := tag + "-from-git"
	autoInstall := true
	build := config.Build{
		SRPM:                    "file:///data/" + filepath.Join(outputDir, CmdBuildSrcRPM, osLabel+"__"+tag, filepath.Base(matches[0])),
		AutoInstallDependencies: &autoInstall,
		Annotations:             ann,
	}

	entryLines, err := config.FormatBuildEntry(label, build)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	buf.WriteString("oses:\n  " + osLabel + ":\n    builds:\n")
	for _, l := range entryLines {
		buf.WriteString(l + "\n")
	}

	outPath := fmt.Sprintf("%s-%s-from-git.yaml", osLabel, tag)
	if err := os.WriteFile(outPath, []byte(buf.String()), 0o644); err != nil {
		return "", err
	}
	return outPath, nil
}

// ---- full binary-rpm build (git-build-rpms) --------------------------

// BuildDeps resolves the staged spec's BuildRequires as root with
// yum-builddep, then installs any all_rpms_extra_packages configured for
// this (os, tag) in git-build-config.yaml -- tier 4, patching a gap in the
// spec's own declared BuildRequires (empty for most tags; see the file's
// header comment). Independent of go/steps.Runner.BuildDeps (same
// reasoning as downloadFile above: go/gitsteps deliberately does not
// import go/steps). Must run as root, after Stage.
func (r *Runner) BuildDeps() error {
	logx.Log("### install-builddeps: resolving build dependencies with yum-builddep")
	if err := osprep.Run("yum", "install", "-y", "yum-utils"); err != nil { // provides yum-builddep
		return err
	}
	u, err := user.Lookup(BuildUser)
	if err != nil {
		return err
	}
	specs := filepath.Join(u.HomeDir, "rpmbuild", rpmbuildSpecsDir)
	if err := osprep.RunIn(specs, "yum-builddep", "-y", filepath.Join(specs, "mysql.spec")); err != nil {
		return err
	}
	extra, err := loadAllRPMsExtraPackages(r.DataDir, r.osLabel(), r.Tag)
	if err != nil {
		return err
	}
	if len(extra) == 0 {
		return nil
	}
	logx.Logf("### install-builddeps: installing all_rpms_extra_packages: %v", extra)
	return osprep.InstallPackages(extra)
}

// RPMBuild runs `rpmbuild -ba` against the staged spec -- producing both
// binary RPMs and a src.rpm in one pass, unlike AssembleSrcRPM's `-bs` -- then
// collects everything into <output_dir>/<os><major>__<tag>/. Must run as
// BuildUser, after BuildDeps.
func (r *Runner) RPMBuild() error {
	dir, err := rpmbuildDir()
	if err != nil {
		return err
	}
	specs := filepath.Join(dir, rpmbuildSpecsDir)
	logx.Log("### rpmbuild: rpmbuild -ba")
	if err := osprep.RunIn(specs, "rpmbuild", "--define", r.rpmDefine(), "-ba", "mysql.spec"); err != nil {
		return err
	}
	return r.collectAll(dir)
}

// collectAll copies both the src.rpm and binary RPMs produced by
// RPMBuild into <output_dir>/<os><major>__<tag>/ -- like collect, but
// also picks up RPMS/*/*.rpm (mirroring go/steps.Runner.Collect, which
// globs the same two patterns for the download-based path).
func (r *Runner) collectAll(rpmbuildDir string) error {
	var matches []string
	for _, pat := range []string{
		filepath.Join(rpmbuildDir, rpmbuildSRPMSDir, "*.rpm"),
		filepath.Join(rpmbuildDir, rpmbuildRPMSDir, "*", "*.rpm"),
	} {
		m, err := filepath.Glob(pat)
		if err != nil {
			return err
		}
		matches = append(matches, m...)
	}
	if len(matches) == 0 {
		return fmt.Errorf("no RPMs found under %s", rpmbuildDir)
	}
	dest := r.outputSubdir(CmdBuildRPMs)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	logx.Logf("### rpmbuild: copying %d RPM(s) to %s", len(matches), dest)
	args := append(append([]string{}, matches...), dest+string(filepath.Separator))
	return osprep.Run("cp", args...)
}

// AllRPMsBuild performs the full git-tag binary-RPM build: the same root
// OS-prep as SrcRPMBuild, then BuildUser for Clone/Configure/Stage, back to
// root for BuildDeps, then BuildUser again for RPMBuild -- the same
// privilege hand-off go/steps.Runner.Setup uses for the download-based path
// (builddep must run as root but needs the spec the build user just
// staged). Must run as root.
func (r *Runner) AllRPMsBuild() error {
	repos, packages, err := r.loadBootstrap()
	if err != nil {
		return err
	}
	if err := osprep.SetupRepos(repos); err != nil {
		return err
	}
	if err := osprep.Refresh(); err != nil {
		return err
	}
	if err := osprep.InstallPackages(packages); err != nil {
		return err
	}
	if err := osprep.FixAnnobin(); err != nil {
		return err
	}
	if err := osprep.CreateUser(BuildUser, []string{filepath.Join(r.DataDir, r.OutputDir)}); err != nil {
		return err
	}

	for _, stage := range []string{CmdClone, CmdApplyPatches, CmdConfigure, CmdStage} {
		if err := r.suBuild(stage); err != nil {
			return fmt.Errorf("%s: %w", stage, err)
		}
	}
	if err := r.BuildDeps(); err != nil {
		return fmt.Errorf("%s: %w", CmdBuildDeps, err)
	}
	return r.suBuild(CmdRPMBuild)
}
