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
//   - Orchestration: run|setup|build-rpm <label>   run inside the container
//   - Individual:    record-init|refresh|setup-repos|install-packages|fix-annobin
//     |os-tweaks|create-user|install-srpm|install-builddeps|apply-patches|rpmbuild|collect <label>
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
	case "run", "setup", "build-rpm",
		"record-init", "refresh", "setup-repos", "install-packages", "fix-annobin", "os-tweaks", "create-user",
		"install-srpm", "install-builddeps", "apply-patches", "rpmbuild", "collect":
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
// `build-one [-n] [-test] [-until <re>] [-timeout <dur>] [-c <config>] <os> <label>`.
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
  -c <path>       use an alternate config file instead of config.yaml

A build stopped early by -test/-until/-timeout is reported as success (rc 0).
`)
	}
	noop := fs.Bool("n", false, "dry run")
	test := fs.Bool("test", false, "stop once compilation starts (past cmake)")
	until := fs.String("until", "", "stop when build output matches this regexp")
	timeout := fs.Duration("timeout", 0, "stop the container after this duration")
	configFile := fs.String("c", "", "alternate config.yaml path, relative to the repo root")
	_ = fs.Parse(args)

	pos := fs.Args()
	if len(pos) < 2 {
		fs.Usage()
		os.Exit(1)
	}

	opts := host.Options{Noop: *noop, Timeout: *timeout, ConfigFile: *configFile}
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
	"setup":             true,
	"run":               true,
	"record-init":       true,
	"refresh":           true,
	"setup-repos":       true,
	"install-packages":  true,
	"fix-annobin":       true,
	"os-tweaks":         true,
	"create-user":       true,
	"install-builddeps": true,
	"build-rpm":         false,
	"install-srpm":      false,
	"apply-patches":     false,
	"rpmbuild":          false,
	"collect":           false,
}

// runContainer handles all in-container commands.
func runContainer(cmd string, args []string) {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: mysql-rpm-builder %s [flags] <label>\n", cmd)
		fmt.Fprintf(os.Stderr, "\nflags:\n")
		fs.PrintDefaults()
	}
	configFile := fs.String("c", "", "alternate config.yaml path, relative to the repo root")
	_ = fs.Parse(args)

	pos := fs.Args()
	if len(pos) < 1 {
		fs.Usage()
		os.Exit(1)
	}
	label := pos[0]

	checkPrivilege(cmd)

	r, err := steps.NewRunner(steps.DataDir, label, *configFile)
	if err != nil {
		logx.Fatalf(1, "%v", err)
	}

	// Orchestration commands tee to a per-run logfile like the original stages.
	switch cmd {
	case "setup", "run":
		teeTo(r.LogFileFor("ossetup"))
	case "build-rpm":
		teeTo(r.LogFileFor("build"))
	}

	logx.Logf("mysql-rpm-builder %s: %s %s / %s", version.Version, cmd, r.OS.OSLabel(), label)

	var stageErr error
	switch cmd {
	case "run", "setup":
		stageErr = r.Setup()
	case "build-rpm":
		stageErr = r.BuildRPM()
	case "record-init":
		stageErr = r.RecordInitialPackages()
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
	case "install-builddeps":
		stageErr = r.InstallBuildDeps()
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
  build-one [flags] <os> <label>   launch a Docker container to build <label> on <os>

In-container orchestration:
  run [flags] <label>              full build (setup + rpmbuild), invoked by build-one
  setup [flags] <label>            root OS-prep, then drives install-srpm,
                                   install-builddeps (root) and build-rpm
  build-rpm [flags] <label>        rpmbuild-user stages after install-srpm/builddep

In-container individual steps (root):
  record-init [flags] <label> | refresh [flags] <label> | setup-repos [flags] <label>
  install-packages [flags] <label> | fix-annobin [flags] <label> | os-tweaks [flags] <label>
  create-user [flags] <label> | install-builddeps [flags] <label>

In-container individual steps (rpmbuild user):
  install-srpm [flags] <label> | apply-patches [flags] <label> | rpmbuild [flags] <label> | collect [flags] <label>

Flags:
  -c path                       use an alternate config file (relative to repo root) instead of config.yaml

Other:
  version                       print the binary version and exit
`)
}
