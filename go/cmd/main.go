// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

// Command mysql-rpm-builder builds MySQL binary RPMs from source RPMs in a
// controlled Docker environment.
//
// It is a single static binary that plays several roles, dispatched by
// subcommand:
//
//   - Host:          build-one [-n] <os> <label>   launch a container build
//   - Orchestration: run|setup|build <label>       run inside the container
//   - Individual:    refresh|setup-repos|install-packages|fix-annobin|os-tweaks
//     |create-user|install-srpm|apply-patches|rpmbuild|collect <label>
//
// The individual step commands let a failed stage be re-run in a debug
// container without repeating the expensive rpmbuild.
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"

	"github.com/sjmudd/mysql-rpm-builder/go/host"
	"github.com/sjmudd/mysql-rpm-builder/go/logx"
	"github.com/sjmudd/mysql-rpm-builder/go/steps"
	"github.com/sjmudd/mysql-rpm-builder/go/version"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "build-one":
		runBuildOne(rest)
	case "run", "setup", "build",
		"refresh", "setup-repos", "install-packages", "fix-annobin", "os-tweaks", "create-user",
		"install-srpm", "apply-patches", "rpmbuild", "collect":
		runContainer(cmd, rest)
	case "version", "-v", "--version":
		fmt.Printf("mysql-rpm-builder %s\n", version.Version)
	case "-h", "--help", "help":
		usage()
	default:
		logx.Fatalf(1, "unknown command %q (try --help)", cmd)
	}
}

// runBuildOne handles the host-side
// `build-one [-n] [-test] [-until <re>] [-timeout <dur>] <os> <label>`.
func runBuildOne(args []string) {
	fs := flag.NewFlagSet("build-one", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: mysql-rpm-builder build-one [flags] <os> <label>

  -n              dry run: print the docker command without running it
  -test           stop once the build starts compiling (i.e. past cmake); a
                  quick way to verify a new (os, version) combination's OS prep,
                  build deps and cmake configure all work without a full build
  -until <regexp> stop the container when a line of build output matches <regexp>
  -timeout <dur>  stop the container after <dur> (e.g. 30m, 2h)

A build stopped early by -test/-until/-timeout is reported as success (rc 0).
`)
	}
	noop := fs.Bool("n", false, "dry run")
	test := fs.Bool("test", false, "stop once compilation starts (past cmake)")
	until := fs.String("until", "", "stop when build output matches this regexp")
	timeout := fs.Duration("timeout", 0, "stop the container after this duration")
	_ = fs.Parse(args)

	pos := fs.Args()
	if len(pos) < 2 {
		fs.Usage()
		os.Exit(1)
	}

	opts := host.Options{Noop: *noop, Timeout: *timeout}
	switch {
	case *until != "":
		re, err := regexp.Compile(*until)
		if err != nil {
			logx.Fatalf(1, "invalid -until regexp: %v", err)
		}
		opts.Until = re
	case *test:
		opts.Until = regexp.MustCompile(host.CompileMarker)
	}
	os.Exit(host.BuildOne(pos[0], pos[1], opts))
}

// stageNeeds records the required privilege for each in-container command.
// true = must run as root; false = must run as the (non-root) build user.
var stageNeedsRoot = map[string]bool{
	"setup":            true,
	"run":              true,
	"refresh":          true,
	"setup-repos":      true,
	"install-packages": true,
	"fix-annobin":      true,
	"os-tweaks":        true,
	"create-user":      true,
	"build":            false,
	"install-srpm":     false,
	"apply-patches":    false,
	"rpmbuild":         false,
	"collect":          false,
}

// runContainer handles all in-container commands.
func runContainer(cmd string, args []string) {
	if len(args) < 1 {
		logx.Fatalf(1, "usage: mysql-rpm-builder %s <label>", cmd)
	}
	label := args[0]

	checkPrivilege(cmd)

	r, err := steps.NewRunner(steps.DataDir, label)
	if err != nil {
		logx.Fatalf(1, "%v", err)
	}

	// Orchestration commands tee to a per-run logfile like the original stages.
	switch cmd {
	case "setup", "run":
		teeTo(r.LogFileFor("ossetup"))
	case "build":
		teeTo(r.LogFileFor("build"))
	}

	logx.Logf("mysql-rpm-builder %s: %s %s / %s", version.Version, cmd, r.OS.OSLabel(), label)

	var stageErr error
	switch cmd {
	case "run", "setup":
		stageErr = r.Setup()
	case "build":
		stageErr = r.Build()
	case "refresh":
		stageErr = r.Refresh()
	case "setup-repos":
		stageErr = r.SetupRepos()
	case "install-packages":
		stageErr = r.InstallPackages()
	case "fix-annobin":
		stageErr = r.FixAnnobin()
	case "os-tweaks":
		stageErr = r.OSTweaks()
	case "create-user":
		stageErr = r.CreateUser()
	case "install-srpm":
		stageErr = r.InstallSRPM()
	case "apply-patches":
		stageErr = r.ApplyPatches()
	case "rpmbuild":
		stageErr = r.RPMBuild()
	case "collect":
		stageErr = r.Collect()
	}

	if stageErr != nil {
		logx.Fatalf(1, "%s failed: %v", cmd, stageErr)
	}
	logx.Logf("### %s completed for %s / %s", cmd, r.OS.OSLabel(), label)
}

// checkPrivilege enforces that a command runs as the expected user.
func checkPrivilege(cmd string) {
	needsRoot, known := stageNeedsRoot[cmd]
	if !known {
		return
	}
	isRoot := os.Geteuid() == 0
	if needsRoot && !isRoot {
		logx.Fatalf(1, "%s must run as root (OS preparation stage)", cmd)
	}
	if !needsRoot && isRoot {
		logx.Fatalf(1, "%s must run as the %s user, not root", cmd, steps.BuildUser)
	}
}

// teeTo redirects logging (and subprocess output) to both stdout and a file.
func teeTo(path string) {
	if _, err := logx.SetTee(path); err != nil {
		logx.Fatalf(1, "cannot open logfile %s: %v", path, err)
	}
	logx.Logf("- logging to %s", path)
}

func usage() {
	fmt.Fprint(os.Stderr, `mysql-rpm-builder - build MySQL RPMs from source RPMs

Host:
  build-one [-n] <os> <label>   launch a Docker container to build <label> on <os>

In-container orchestration:
  run <label>                   full build (setup + rpmbuild), invoked by build-one
  setup <label>                 root OS-prep stages, then hands off to build
  build <label>                 rpmbuild-user stages

In-container individual steps (root):
  refresh <label> | setup-repos <label> | install-packages <label>
  fix-annobin <label> | os-tweaks <label> | create-user <label>

In-container individual steps (rpmbuild user):
  install-srpm <label> | apply-patches <label> | rpmbuild <label> | collect <label>

Other:
  version                       print the binary version and exit
`)
}
