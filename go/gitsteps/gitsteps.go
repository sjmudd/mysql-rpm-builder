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
const DefaultOutputDir = "rpms_built_from_git"

// Runner carries the state for one git-tag build.
type Runner struct {
	OS  osrelease.Info
	Tag string // git ref/tag/branch to build; also used as the output-directory label

	DataDir   string
	OutputDir string // base directory (relative to DataDir) for produced RPMs
	SkipBison bool
}

// NewRunner detects the current OS and prepares a Runner for the given tag.
func NewRunner(dataDir, tag, outputDir string, skipBison bool) (*Runner, error) {
	info, err := osrelease.Detect()
	if err != nil {
		return nil, err
	}
	if outputDir == "" {
		outputDir = DefaultOutputDir
	}
	return &Runner{OS: info, Tag: tag, DataDir: dataDir, OutputDir: outputDir, SkipBison: skipBison}, nil
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

// cloneDir / buildDir are deterministic (not mktemp) paths under the current
// user's home: the root orchestrator and the re-exec'd build-user steps both
// need to agree on where the checkout/build tree live across the `su` hop.
func cloneDir(version string) (string, error) { return userPath("git-build", "mysql-"+version) }
func buildDir(version string) (string, error) {
	return userPath("git-build", "build-mysql-"+version)
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

// suBuild re-execs this binary as BuildUser to run a single build-user stage,
// porting the same -o/-no-bison flags through.
func (r *Runner) suBuild(stage string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logx.Logf("### switching to user %s to run %s", BuildUser, stage)
	cmdStr := fmt.Sprintf("%s %s -o %s", exe, stage, r.OutputDir)
	if r.SkipBison {
		cmdStr += " -no-bison"
	}
	cmdStr += " " + r.Tag
	cmd := exec.Command("su", "-", BuildUser, "-c", cmdStr)
	cmd.Stdout = logx.Writer()
	cmd.Stderr = logx.Writer()
	return cmd.Run()
}

// ---- build-user stages --------------------------------------------------

// Clone shallow-clones r.Tag into a deterministic path under the current
// user's home. Must run as BuildUser.
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
	logx.Logf("### clone: %s into %s (shallow, single tag only)", r.Tag, src)
	if err := os.MkdirAll(filepath.Dir(src), 0o755); err != nil {
		return err
	}
	return osprep.Run("git", "clone", "--depth", "1", "--no-tags", "--branch", r.Tag,
		"https://github.com/mysql/mysql-server.git", src)
}

// Configure runs cmake configure against the already-cloned tree. Must run
// as BuildUser, after Clone.
//
// WITH_BOOST assumes the 9.x-style layout where boost is bundled under
// extra/boost/ in the source tree itself. 8.0.x/8.4.x pull boost as a
// separate Source instead -- this does not handle that layout yet.
func (r *Runner) Configure() error {
	src, err := cloneDir(r.version())
	if err != nil {
		return err
	}
	build, err := buildDir(r.version())
	if err != nil {
		return err
	}
	boost, err := findBoostDir(src)
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
	return "", fmt.Errorf("no bundled extra/boost/boost_* found under %s -- see the WITH_BOOST layout caveat: 8.0.x/8.4.x pull boost as a separate Source, not handled yet", srcDir)
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
		if err := osprep.Run("cp", cached, filepath.Join(sourcesDir, name)); err != nil {
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
	args := append(append([]string{}, matches...), dest+string(filepath.Separator))
	return osprep.Run("cp", args...)
}
