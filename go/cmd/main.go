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
//
// It also builds a src.rpm directly from a mysql-server git tag, bypassing
// Oracle's official src.rpm download entirely (see go/gitsteps):
//
//   - Host:          git-build-one [flags] <os> <tag>
//   - Orchestration: git-run [flags] <tag>          run inside the container
//   - Individual:    git-clone|git-configure|git-assemble-srpm [flags] <tag>
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/sjmudd/mysql-rpm-builder/go/config"
	"github.com/sjmudd/mysql-rpm-builder/go/gitsteps"
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
	case "git-build-one":
		runGitBuildOne(rest)
	case "git-run", "git-clone", "git-configure", "git-assemble-srpm":
		runGitContainer(cmd, rest)
	case "version", "-v", "--version":
		fmt.Printf("mysql-rpm-builder %s\n", version.Version)
	case "-h", "--help", "help":
		usage()
	default:
		logx.Fatalf(1, "unknown command %q (try --help)", cmd)
	}
}

// runBuildOne handles the host-side
// `build-one [-n] [-test] [-until <re>] [-timeout <dur>] [-c <config>] [-add-if-successful] <os> <label>`.
func runBuildOne(args []string) {
	fs := flag.NewFlagSet("build-one", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: mysql-rpm-builder build-one [flags] <os> <label>

  -n                  dry run: print the docker command without running it
  -test               stop once the build starts compiling (i.e. past cmake); a
                      quick way to verify a new (os, version) combination's OS prep,
                      build deps and cmake configure all work without a full build
  -until <regexp>     stop the container when a line of build output matches <regexp>
  -timeout <dur>      stop the container after <dur> (e.g. 30m, 2h)
  -c <path>           use an alternate config file instead of config.yaml
  -add-if-successful  after a full successful build (not -test/-until/-timeout),
                      merge the -c config's build entry into config.yaml.
                      Requires -c. An existing (os, label) entry is never
                      overwritten: identical entries are skipped silently,
                      differing ones are skipped with a warning. The
                      pre-merge config.yaml is preserved as
                      config.yaml.<UTC timestamp>.

A build stopped early by -test/-until/-timeout is reported as success (rc 0).
`)
	}
	noop := fs.Bool("n", false, "dry run")
	test := fs.Bool("test", false, "stop once compilation starts (past cmake)")
	until := fs.String("until", "", "stop when build output matches this regexp")
	timeout := fs.Duration("timeout", 0, "stop the container after this duration")
	configFile := fs.String("c", "", "alternate config.yaml path, relative to the repo root")
	addIfSuccessful := fs.Bool("add-if-successful", false,
		"after a full successful build, merge the -c config's build entry into config.yaml")
	_ = fs.Parse(args)

	if *addIfSuccessful && *configFile == "" {
		fmt.Fprintln(os.Stderr, "error: -add-if-successful requires -c <config>")
		fs.Usage()
		os.Exit(1)
	}

	pos := fs.Args()
	if len(pos) < 2 {
		fs.Usage()
		os.Exit(1)
	}

	opts := host.Options{Noop: *noop, Timeout: *timeout, ConfigFile: *configFile, AddIfSuccessful: *addIfSuccessful}
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

// gitBuildOneDockerArgs builds the `docker run` argv for one git-build-one
// run, as a pure function so the full flag set can be asserted in a test
// without actually invoking docker -- -repo/-ref were once missing here
// entirely (added alongside -o/-no-bison later), which is exactly the kind
// of gap this is meant to catch early.
func gitBuildOneDockerArgs(dir, image, tag, code, outputDir string, noBison bool, repo, ref string) []string {
	// Named the same way build-one names its containers
	// (mysql-rpm-builder-<label>-<code>) so `docker ps` shows what's
	// actually running instead of a random docker-assigned name.
	args := []string{
		"run",
		"--name=" + fmt.Sprintf("mysql-rpm-builder-%s-%s", tag, code),
		"--rm",
		"--network=host",
		"--hostname=buildhost",
		"-v", dir + ":/data",
		"-w", "/data",
		image,
		host.ContainerBinary, "git-run", "-o", outputDir,
	}
	if noBison {
		args = append(args, "-no-bison")
	}
	args = append(args, "-repo", repo, "-ref", ref)
	return append(args, tag)
}

// runGitBuildOne handles the host-side
// `git-build-one [-o <dir>] [-no-bison] [-n] <os> <tag>`.
//
// Deliberately simpler than runBuildOne/host.BuildOne: no -test/-until/-timeout
// early-stop flags, no -add-if-successful config merge, no per-run
// code/build_status tracking -- this port is scaffolding, not yet at parity
// with the srpm-based path's maturity.
func runGitBuildOne(args []string) {
	fs := flag.NewFlagSet("git-build-one", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `usage: mysql-rpm-builder git-build-one [flags] <os> <tag>

  -o <dir>            output directory for the produced src.rpm, relative to
                       the repo root (default %q)
  -no-bison            skip the pre-generated bison output (sql_yacc.cc/.h,
                       sql_hints.yy.cc/.h) -- mysql.spec requires bison
                       unconditionally, so a real rpmbuild -ba regenerates
                       these itself regardless of what the tarball ships
  -repo <url>          git remote to clone instead of the default upstream
                       repo (default %q)
  -ref <name>          branch or tag to check out instead of a tag matching
                       <tag> (default: <tag> itself). Not yet a commit SHA
                       -- see go/gitsteps.Clone's FIXME. <tag> always still
                       names the version (it must match the real
                       MYSQL_VERSION at whatever gets checked out).
                       Examples:
                       -repo https://github.com/sjmudd/mysql-server.git -ref bug/120895
                       -repo https://github.com/percona/percona-server.git
  -n                   dry run: print the docker command without running it
`, gitsteps.DefaultOutputDir, gitsteps.DefaultRepo)
	}
	outputDir := fs.String("o", gitsteps.DefaultOutputDir, "output directory for the produced src.rpm")
	noBison := fs.Bool("no-bison", false, "skip the pre-generated bison output")
	repo := fs.String("repo", gitsteps.DefaultRepo, "git remote to clone instead of the default upstream repo")
	ref := fs.String("ref", "", "branch or tag to check out instead of a tag matching <tag> (defaults to <tag> itself)")
	noop := fs.Bool("n", false, "dry run")
	_ = fs.Parse(args)

	pos := fs.Args()
	if len(pos) < 2 {
		fs.Usage()
		os.Exit(1)
	}
	osName, tag := pos[0], pos[1]

	dir, err := os.Getwd()
	if err != nil {
		logx.Fatalf(1, "cannot determine working directory: %v", err)
	}
	cfg, err := config.Load(dir, "")
	if err != nil {
		logx.Fatalf(1, "%v", err)
	}
	image, ok := cfg.Image(osName)
	if !ok {
		logx.Fatalf(3, "no image found for OS %q (known: %v)", osName, cfg.OSes())
	}

	code := host.RandomSuffix(5)
	logFile := filepath.Join(dir, "log", fmt.Sprintf("git-build-one.%s__%s__%s.log", osName, tag, code))
	if _, err := logx.SetTee(logFile); err != nil {
		logx.Fatalf(1, "cannot open logfile %s: %v", logFile, err)
	}
	logx.Logf("mysql-rpm-builder %s: git-build-one %s %s (image %s)", version.Version, osName, tag, image)

	dockerArgs := gitBuildOneDockerArgs(dir, image, tag, code, *outputDir, *noBison, *repo, *ref)

	if *noop {
		logx.Logf("NOOP: docker %v", dockerArgs)
		return
	}

	cmd := exec.Command("docker", dockerArgs...)
	cmd.Dir = dir
	cmd.Stdout = logx.Writer()
	cmd.Stderr = logx.Writer()
	rc := 0
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			rc = exitErr.ExitCode()
		} else {
			rc = 1
		}
	}
	logx.Logf("exit status %d for git-build-one of %s on %s", rc, tag, osName)
	os.Exit(rc)
}

// runGitContainer handles the in-container git-* commands: the root
// orchestrator (git-run) and the individually re-runnable build-user steps
// (git-clone, git-configure, git-assemble-srpm).
func runGitContainer(cmd string, args []string) {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: mysql-rpm-builder %s [flags] <tag>\n\nflags:\n", cmd)
		fs.PrintDefaults()
	}
	outputDir := fs.String("o", gitsteps.DefaultOutputDir, "output directory for the produced src.rpm")
	noBison := fs.Bool("no-bison", false, "skip the pre-generated bison output")
	repo := fs.String("repo", gitsteps.DefaultRepo, "git remote to clone instead of the default upstream repo")
	ref := fs.String("ref", "", "branch or tag to check out instead of a tag matching <tag> (defaults to <tag> itself)")
	_ = fs.Parse(args)

	pos := fs.Args()
	if len(pos) < 1 {
		fs.Usage()
		os.Exit(1)
	}
	tag := pos[0]

	checkPrivilege(cmd)

	r, err := gitsteps.NewRunner(steps.DataDir, tag, *outputDir, *noBison, *repo, *ref)
	if err != nil {
		logx.Fatalf(1, "%v", err)
	}

	if cmd == "git-run" {
		teeTo(filepath.Join(steps.DataDir, "log", fmt.Sprintf("git-run.%s__%s.log", r.OS.OSLabel(), tag)))
	}

	logx.Logf("mysql-rpm-builder %s: %s %s / %s", version.Version, cmd, r.OS.OSLabel(), tag)

	var stageErr error
	switch cmd {
	case "git-run":
		stageErr = r.Run()
	case "git-clone":
		stageErr = r.Clone()
	case "git-configure":
		stageErr = r.Configure()
	case "git-assemble-srpm":
		stageErr = r.AssembleSRPM()
	}
	if stageErr != nil {
		logx.Fatalf(1, "%s failed: %v", cmd, stageErr)
	}
	logx.Logf("### %s completed for %s / %s", cmd, r.OS.OSLabel(), tag)
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
	"git-run":           true,
	"git-clone":         false,
	"git-configure":     false,
	"git-assemble-srpm": false,
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

Build a src.rpm from a mysql-server git tag instead of downloading Oracle's
official src.rpm (see go/gitsteps; scaffolding, src.rpm only for now):

Host:
  git-build-one [flags] <os> <tag>   launch a Docker container to build <tag> on <os>

In-container orchestration:
  git-run [flags] <tag>              root OS-prep, then drives the steps below

In-container individual steps (rpmbuild user):
  git-clone [flags] <tag> | git-configure [flags] <tag> | git-assemble-srpm [flags] <tag>

Flags:
  -o dir                         output directory for the produced src.rpm (default built-from-git)
  -no-bison                      skip the pre-generated bison output (see docs/srpm-tarball-differs-from-git-tag.md)

Other:
  version                       print the binary version and exit
`)
}
