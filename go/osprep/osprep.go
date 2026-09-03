// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

// Package osprep implements the OS-preparation actions shared by every build
// path this repo drives: refreshing packages, enabling repos, installing
// packages, working around the gcc-toolset annobin plugin-naming mismatch,
// running arbitrary shell tweaks, and creating the build user plus persisted
// directories.
//
// Each function takes plain arguments instead of a config.Resolved, so
// callers that have no resolvable config.yaml build entry at all -- like a
// git-tag build, which has no src.rpm URL to resolve -- can call these
// directly, without going through config.Resolve/steps.NewRunner.
// go/steps.Runner's OS-prep methods are thin wrappers over this package, so
// the original srpm-based build path's behavior and log output are
// unchanged.
package osprep

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/sjmudd/mysql-rpm-builder/go/config"
	"github.com/sjmudd/mysql-rpm-builder/go/logx"
)

// Run executes a command, teeing its output to the current log destination.
func Run(name string, args ...string) error { return RunIn("", name, args...) }

// RunIn executes a command in dir (empty = current dir), teeing output to logs.
func RunIn(dir, name string, args ...string) error {
	logx.Logf("+ %s %s", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = logx.Writer()
	cmd.Stderr = logx.Writer()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

// RunShell executes a shell snippet via `sh -c`, teeing output to logs.
func RunShell(script string) error {
	logx.Logf("+ sh -c %q", script)
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdout = logx.Writer()
	cmd.Stderr = logx.Writer()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("shell command failed: %w", err)
	}
	return nil
}

// DefaultBasePackages is the fallback when images.yaml's base_packages is
// empty/absent for an OS. util-linux provides 'su'.
var DefaultBasePackages = []string{"rpm-build", "util-linux"}

// DefaultConfigManagerPackage is the fallback used when images.yaml's
// config_manager_package is empty/absent for an OS.
const DefaultConfigManagerPackage = "dnf-command(config-manager)"

// DefaultBuilddepPackage is the fallback used when images.yaml's
// builddep_package is empty/absent for an OS. Provides yum-builddep.
const DefaultBuilddepPackage = "yum-utils"

// Refresh updates system packages and installs the base build tooling every
// build needs (images.yaml's base_packages, or DefaultBasePackages when
// unset). It runs right after the initial yum update, so these installs
// always succeed.
func Refresh(basePackages []string) error {
	if len(basePackages) == 0 {
		basePackages = DefaultBasePackages
	}
	logx.Log("### refresh: ensuring system packages are up to date")
	if err := Run("yum", "update", "-y"); err != nil {
		return err
	}
	logx.Logf("### refresh: installing base build tooling %v", basePackages)
	return Run("yum", append([]string{"install", "-y"}, basePackages...)...)
}

// SetupRepos installs the EPEL packages and enables the configured repos.
//
// EPEL packages are installed first because some repos we enable (e.g. the
// Oracle *_developer_EPEL repos) are only defined once the corresponding EPEL
// release package is present. Repos in Enable that already exist in the base
// image (e.g. codeready_builder) can be enabled either way.
//
// Installs the config-manager package itself first, rather than relying on
// Refresh to have done it: SetupRepos is the caller that actually needs
// `yum config-manager`, and callers (e.g. go/gitsteps.Runner.Run) may want
// repos ready before anything else runs at all, i.e. before Refresh.
func SetupRepos(repos config.Repos, configManagerPackage string) error {
	if configManagerPackage == "" {
		configManagerPackage = DefaultConfigManagerPackage
	}
	logx.Logf("### setup-repos: epel=%v enable=%v", repos.EPELPackages, repos.Enable)
	if err := Run("yum", "install", "-y", configManagerPackage); err != nil {
		return err
	}
	for _, pkg := range repos.EPELPackages {
		if pkg == "" {
			continue
		}
		if err := Run("dnf", "install", "-y", pkg); err != nil {
			return err
		}
	}
	for _, repo := range repos.Enable {
		if repo == "" {
			continue
		}
		if err := Run("yum", "config-manager", "--set-enabled", repo); err != nil {
			return err
		}
	}
	return nil
}

// InstallPackages installs the given packages as root. A nil/empty list is a
// no-op (callers with their own "must specify something" invariant, like the
// srpm build path's auto_install_dependencies/Packages check, validate that
// before calling this).
func InstallPackages(packages []string) error {
	if len(packages) == 0 {
		return nil
	}
	logx.Logf("### install-packages: installing %d package(s)", len(packages))
	return Run("yum", append([]string{"install", "-y"}, packages...)...)
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
func FixAnnobin() error {
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

// OSTweaks runs any optional shell workarounds (escape hatch), in order.
func OSTweaks(tweaks []string) error {
	if len(tweaks) == 0 {
		logx.Log("### os-tweaks: none configured")
		return nil
	}
	for i, t := range tweaks {
		logx.Logf("### os-tweaks: [%d/%d] %s", i+1, len(tweaks), t)
		if err := RunShell(t); err != nil {
			return err
		}
	}
	return nil
}

// CreateUser creates the given build user (if absent) and the given
// directories (owned by that user).
func CreateUser(username string, dirs []string) error {
	logx.Logf("### create-user: ensuring build user %q exists", username)
	if _, err := user.Lookup(username); err != nil {
		logx.Logf("- adding user %s", username)
		if err := Run("useradd", "-m", username); err != nil {
			return err
		}
	} else {
		logx.Logf("- user %s already present", username)
	}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err == nil {
			continue
		}
		logx.Logf("- creating %s owned by %s", dir, username)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := Run("chown", username+":"+username, dir); err != nil {
			return err
		}
	}
	return nil
}
