// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

// Package steps implements the individual stages of a build as small, composable
// functions. Each stage is independently runnable (via its own subcommand) so a
// failed step can be re-run in a debug container without repeating the expensive
// rpmbuild. The stages are direct ports of the two-stage logic in the original
// bash `build` script.
package steps

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/sjmudd/mysql-rpm-builder/go/config"
	"github.com/sjmudd/mysql-rpm-builder/go/logx"
	"github.com/sjmudd/mysql-rpm-builder/go/osrelease"
)

// BuildUser is the non-root user that runs the rpmbuild stage.
const BuildUser = "rpmbuild"

// DataDir is where the repository is mounted inside the container.
const DataDir = "/data"

// Runner carries the resolved configuration for one (os, label) build and
// provides one method per stage.
type Runner struct {
	Cfg     config.Resolved
	OS      osrelease.Info
	DataDir string
}

// NewRunner detects the current OS, loads the configuration from dataDir and
// resolves the build for the given MySQL label.
func NewRunner(dataDir, label string) (*Runner, error) {
	info, err := osrelease.Detect()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		return nil, err
	}
	resolved, err := cfg.Resolve(info.OSLabel(), label)
	if err != nil {
		return nil, err
	}
	return &Runner{Cfg: resolved, OS: info, DataDir: dataDir}, nil
}

// osLabel is the "<id><major>" key, e.g. "ol10".
func (r *Runner) osLabel() string { return r.OS.OSLabel() }

// elDefine is the rpm macro name, e.g. "el10".
func (r *Runner) elDefine() string { return fmt.Sprintf("el%d", r.OS.Major) }

// rpmDefine is the `--define` argument passed to rpmbuild (and yum-builddep so
// it evaluates the same conditional BuildRequires), e.g. "el10 1".
func (r *Runner) rpmDefine() string { return r.elDefine() + " 1" }

// srpmsDir / logDir / builtDir are the persisted data directories.
func (r *Runner) srpmsDir() string { return filepath.Join(r.DataDir, "SRPMS") }
func (r *Runner) logDir() string   { return filepath.Join(r.DataDir, "log") }
func (r *Runner) builtDir() string { return filepath.Join(r.DataDir, "built") }

// ---- root stages -----------------------------------------------------------

// Refresh updates system packages and ensures dnf config-manager is available.
func (r *Runner) Refresh() error {
	logx.Log("### refresh: ensuring system packages are up to date")
	if err := run("yum", "update", "-y"); err != nil {
		return err
	}
	return run("yum", "install", "-y", "dnf-command(config-manager)")
}

// SetupRepos installs the EPEL packages and enables the configured repos.
//
// EPEL packages are installed first because some repos we enable (e.g. the
// Oracle *_developer_EPEL repos) are only defined once the corresponding EPEL
// release package is present. Repos in Enable that already exist in the base
// image (e.g. codeready_builder) can be enabled either way.
func (r *Runner) SetupRepos() error {
	repos := r.Cfg.Repos
	logx.Logf("### setup-repos: epel=%v enable=%v", repos.EPELPackages, repos.Enable)
	for _, pkg := range repos.EPELPackages {
		if pkg == "" {
			continue
		}
		if err := run("dnf", "install", "-y", pkg); err != nil {
			return err
		}
	}
	for _, repo := range repos.Enable {
		if repo == "" {
			continue
		}
		if err := run("yum", "config-manager", "--set-enabled", repo); err != nil {
			return err
		}
	}
	return nil
}

// InstallPackages installs the build dependencies for this (os, version).
//
// The steps run in order:
//  1. extra_packages — packages missing from the src.rpm's BuildRequires,
//     installed first so they are present however the rest are resolved.
//  2. auto_install_dependencies — if set, install yum-utils (which provides
//     yum-builddep) and let yum-builddep resolve the src.rpm's BuildRequires.
//  3. packages — the explicitly listed packages, installed afterwards.
func (r *Runner) InstallPackages() error {
	b := r.Cfg.Build
	if !b.AutoInstallDependencies && len(b.Packages) == 0 && len(b.ExtraPackages) == 0 {
		return fmt.Errorf("nothing to install for %s / %s: set auto_install_dependencies or list packages", r.osLabel(), r.Cfg.Label)
	}

	if len(b.ExtraPackages) > 0 {
		logx.Logf("### install-packages: installing %d extra package(s) missing from BuildRequires", len(b.ExtraPackages))
		if err := run("yum", append([]string{"install", "-y"}, b.ExtraPackages...)...); err != nil {
			return err
		}
	}

	if b.AutoInstallDependencies {
		logx.Log("### install-packages: resolving build dependencies with yum-builddep")
		if err := run("yum", "install", "-y", "yum-utils"); err != nil { // provides yum-builddep
			return err
		}
		// yum-builddep reads BuildRequires from the src.rpm header, which is
		// frozen at src.rpm build time. Deps gated behind the custom el<N> macro
		// this project passes to rpmbuild (e.g. libquadmath-devel on el10) are
		// not in that header, so they must be listed in extra_packages.
		if err := run("yum-builddep", "-y", r.srpmRef()); err != nil {
			return err
		}
	}

	if len(b.Packages) > 0 {
		logx.Logf("### install-packages: installing %d package(s)", len(b.Packages))
		if err := run("yum", append([]string{"install", "-y"}, b.Packages...)...); err != nil {
			return err
		}
	}
	return nil
}

// srpmRef returns a reference to the source RPM for yum-builddep: the cached
// local copy under SRPMS/ if it has already been downloaded, otherwise the
// remote URL (yum-builddep can fetch it directly). InstallPackages runs before
// the rpmbuild user's install-srpm stage, so on a first build the cache is
// usually empty and the URL is used.
func (r *Runner) srpmRef() string {
	url := r.Cfg.Build.SRPM
	cached := filepath.Join(r.srpmsDir(), filepath.Base(url))
	if _, err := os.Stat(cached); err == nil {
		return cached
	}
	return url
}

// FixAnnobin works around the gcc-toolset annobin plugin naming mismatch.
//
// gcc is invoked with the short plugin name "annobin" (via redhat-annobin-cc1),
// so it looks for annobin.so / gcc-annobin.so in each gcc-toolset plugin dir.
// Depending on the OS the plugin ships under a different real name and some of
// these aliases are missing, which makes cmake's "is the C compiler working"
// check fail with:
//
//	cc1: fatal error: inaccessible plugin file .../plugin/annobin.so
//	expanded from short plugin name annobin: No such file or directory
//
// This has been seen on both el8 (CentOS 8, gcc-toolset-10/12; real object
// annobin.so, gcc-annobin.so missing) and el9 (CentOS 9 / Oracle Linux 9,
// gcc-toolset-12; real object gts-annobin.so.0.0.0, all aliases missing).
// Toolsets that already ship the aliases (e.g. gcc-toolset-14) and OSes with
// plain gcc and no toolset dirs (e.g. el10) are left untouched. Ported from the
// legacy ossetup scripts. See https://bugs.mysql.com/bug.php?id=108049.
func (r *Runner) FixAnnobin() error {
	// Aliases gcc may resolve the "annobin" short name to.
	aliases := []string{"annobin.so", "annobin.so.0.0.0", "gcc-annobin.so", "gcc-annobin.so.0.0.0"}
	// Candidate real plugin objects, newest naming first: gts-annobin.so.0.0.0
	// on el9, plain annobin.so* on el8.
	realNames := []string{"gts-annobin.so.0.0.0", "annobin.so.0.0.0", "annobin.so"}

	// e.g. /opt/rh/gcc-toolset-12/root/usr/lib/gcc/x86_64-redhat-linux/12/plugin.
	// Glob keeps this arch- and toolset-version-agnostic.
	dirs, err := filepath.Glob("/opt/rh/gcc-toolset-*/root/usr/lib/gcc/*/*/plugin")
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		logx.Log("### fix-annobin: no gcc-toolset plugin dirs (nothing to do)")
		return nil
	}
	for _, dir := range dirs {
		// Locate the real (regular-file) plugin object in this toolset.
		var realObj string
		for _, n := range realNames {
			if fi, err := os.Lstat(filepath.Join(dir, n)); err == nil && fi.Mode().IsRegular() {
				realObj = n
				break
			}
		}
		if realObj == "" {
			continue // toolset without the annobin plugin
		}
		for _, a := range aliases {
			if a == realObj {
				continue
			}
			link := filepath.Join(dir, a)
			if _, err := os.Lstat(link); err == nil {
				continue // alias already present
			}
			logx.Logf("### fix-annobin: symlinking %s -> %s", link, realObj)
			if err := os.Symlink(realObj, link); err != nil {
				return fmt.Errorf("symlink %s: %w", link, err)
			}
		}
	}
	return nil
}

// OSTweaks runs any optional per-build shell workarounds (escape hatch).
func (r *Runner) OSTweaks() error {
	tweaks := r.Cfg.Build.Tweaks
	if len(tweaks) == 0 {
		logx.Log("### os-tweaks: none configured")
		return nil
	}
	for i, t := range tweaks {
		logx.Logf("### os-tweaks: [%d/%d] %s", i+1, len(tweaks), t)
		if err := runShell(t); err != nil {
			return err
		}
	}
	return nil
}

// CreateUser creates the rpmbuild user and the persisted data directories,
// porting config/ossetup/create_rpmbuild_user.
func (r *Runner) CreateUser() error {
	logx.Logf("### create-user: ensuring build user %q exists", BuildUser)
	if _, err := lookupUser(BuildUser); err != nil {
		logx.Logf("- adding user %s", BuildUser)
		if err := run("useradd", "-m", BuildUser); err != nil {
			return err
		}
	} else {
		logx.Logf("- user %s already present", BuildUser)
	}
	for _, dir := range []string{r.srpmsDir(), r.logDir(), r.builtDir()} {
		if _, err := os.Stat(dir); err == nil {
			continue
		}
		logx.Logf("- creating %s owned by %s", dir, BuildUser)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := run("chown", BuildUser+":"+BuildUser, dir); err != nil {
			return err
		}
	}
	return nil
}

// ---- rpmbuild-user stages --------------------------------------------------

// rpmbuildHome returns ~/rpmbuild for the current (build) user.
func rpmbuildHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "rpmbuild"), nil
}

// InstallSRPM downloads (with caching) and installs the source RPM.
func (r *Runner) InstallSRPM() error {
	url := r.Cfg.Build.SRPM
	name := filepath.Base(url)
	cached := filepath.Join(r.srpmsDir(), name)

	if _, err := os.Stat(cached); err == nil {
		logx.Logf("### install-srpm: using cached %s", cached)
	} else {
		logx.Logf("### install-srpm: downloading %s", url)
		if err := os.MkdirAll(r.srpmsDir(), 0o755); err != nil {
			return err
		}
		if err := runIn(r.srpmsDir(), "wget", "-nv", "-O", cached, url); err != nil {
			return err
		}
	}
	logx.Logf("- installing %s", cached)
	return run("rpm", "-ivh", cached)
}

// ApplyPatches copies any custom SPECS/SOURCES for this label into ~/rpmbuild
// and applies spec patches, porting install_custom_patches.
func (r *Runner) ApplyPatches() error {
	home, err := rpmbuildHome()
	if err != nil {
		return err
	}
	base := filepath.Join(r.DataDir, "config", r.Cfg.Label)
	if _, err := os.Stat(base); err != nil {
		logx.Logf("### apply-patches: no custom config for %s", r.Cfg.Label)
		return nil
	}
	logx.Logf("### apply-patches: applying custom config from %s", base)
	for _, sub := range []string{"SPECS", "SOURCES"} {
		if err := copyDirFiles(filepath.Join(base, sub), filepath.Join(home, sub)); err != nil {
			return err
		}
	}
	// Apply any patch files copied into ~/rpmbuild/SPECS, in sorted order.
	specs := filepath.Join(home, "SPECS")
	entries, _ := filepath.Glob(filepath.Join(specs, "*patch*"))
	sort.Strings(entries)
	for _, p := range entries {
		logx.Logf("- applying patch %s", p)
		if err := applyPatch(specs, p); err != nil {
			return err
		}
	}
	return nil
}

// RPMBuild runs rpmbuild and records the resulting package list, porting
// rpmbuild_rpms. A build failure is returned as an error after the failed
// package list is captured.
func (r *Runner) RPMBuild() error {
	home, err := rpmbuildHome()
	if err != nil {
		return err
	}
	specs := filepath.Join(home, "SPECS")
	logx.Logf("### rpmbuild: started at %s", time.Now().UTC().Format(time.RFC3339))
	buildErr := runIn(specs, "rpmbuild", "--define", r.rpmDefine(), "-ba", "mysql.spec")
	logx.Logf("### rpmbuild: finished, error=%v", buildErr)

	qa := filepath.Join(r.logDir(), "rpm-qa."+r.runLabel())
	if buildErr != nil {
		qa += ".failed"
	}
	if err := captureRPMQA(qa); err != nil {
		logx.Logf("- warning: could not capture rpm -qa: %v", err)
	}
	return buildErr
}

// Collect moves the built RPMs to the persisted built/ directory, porting the
// tail of build_rpm_stage_logged.
func (r *Runner) Collect() error {
	home, err := rpmbuildHome()
	if err != nil {
		return err
	}
	dest := filepath.Join(r.builtDir(), r.osLabel()+"__"+r.Cfg.Label)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	logx.Logf("### collect: moving RPMs to %s", dest)

	patterns := []string{
		filepath.Join(home, "SRPMS", "*.rpm"),
		filepath.Join(home, "RPMS", "*", "*.rpm"),
	}
	var moved int
	for _, pat := range patterns {
		files, _ := filepath.Glob(pat)
		for _, f := range files {
			target := filepath.Join(dest, filepath.Base(f))
			logx.Logf("- %s -> %s", f, target)
			if err := moveFile(f, target); err != nil {
				return err
			}
			moved++
		}
	}
	if moved == 0 {
		return fmt.Errorf("no RPMs found to collect under %s", home)
	}
	// Record the OS the RPMs were built on.
	if err := copyFile("/etc/os-release", filepath.Join(dest, "etc_os-release")); err != nil {
		logx.Logf("- warning: could not copy /etc/os-release: %v", err)
	}
	logx.Logf("### collect: moved %d RPMs", moved)
	return nil
}

// ---- orchestration ---------------------------------------------------------

// Setup runs all root stages and then re-execs the build stage as the rpmbuild
// user. It mirrors ossetup_stage + the `su - rpmbuild` handoff.
func (r *Runner) Setup() error {
	for _, stage := range []struct {
		name string
		fn   func() error
	}{
		{"refresh", r.Refresh},
		{"setup-repos", r.SetupRepos},
		{"install-packages", r.InstallPackages},
		{"fix-annobin", r.FixAnnobin},
		{"os-tweaks", r.OSTweaks},
		{"create-user", r.CreateUser},
	} {
		if err := stage.fn(); err != nil {
			return fmt.Errorf("%s: %w", stage.name, err)
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logx.Logf("### switching to user %s to run the build stage", BuildUser)
	cmd := exec.Command("su", "-", BuildUser, "-c", fmt.Sprintf("%s build %s", exe, r.Cfg.Label))
	cmd.Stdout = logx.Writer()
	cmd.Stderr = logx.Writer()
	return cmd.Run()
}

// Build runs all rpmbuild-user stages, mirroring build_rpm_stage.
func (r *Runner) Build() error {
	for _, stage := range []struct {
		name string
		fn   func() error
	}{
		{"install-srpm", r.InstallSRPM},
		{"apply-patches", r.ApplyPatches},
		{"rpmbuild", r.RPMBuild},
		{"collect", r.Collect},
	} {
		if err := stage.fn(); err != nil {
			return fmt.Errorf("%s: %w", stage.name, err)
		}
	}
	return nil
}

// runLabel builds the timestamped label used for per-run log/rpm-qa filenames.
func (r *Runner) runLabel() string {
	return fmt.Sprintf("%s__%s__%s", r.Cfg.Label, r.osLabel(), time.Now().UTC().Format("20060102.150405"))
}

// LogFileFor returns the tee logfile path for an orchestration stage.
func (r *Runner) LogFileFor(kind string) string {
	return filepath.Join(r.logDir(), fmt.Sprintf("%s__%s.log", kind, r.runLabel()))
}
