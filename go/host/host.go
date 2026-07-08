// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

// Package host implements the host-side `build-one` command: it resolves the
// container image for an OS and runs the builder inside Docker, mirroring the
// original build-one bash script.
package host

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/sjmudd/mysql-rpm-builder/go/config"
	"github.com/sjmudd/mysql-rpm-builder/go/logx"
	"github.com/sjmudd/mysql-rpm-builder/go/version"
)

// ContainerBinary is where the mounted repo exposes this binary in the container.
const ContainerBinary = "/data/mysql-rpm-builder"

// CompileMarker matches the first real compilation line emitted after cmake has
// finished configuring the tree. Reaching it means the OS prep, dependency
// resolution and cmake configure all succeeded — which is what most "does this
// (os, version) combination work?" tests actually want to confirm, without
// waiting hours for the full rpmbuild.
const CompileMarker = `Building C(?:XX)? object`

// Options controls how BuildOne runs the container.
type Options struct {
	// Noop prints the docker command instead of running it.
	Noop bool
	// Timeout, if non-zero, stops the container after this duration.
	Timeout time.Duration
	// Until, if non-nil, stops the container as soon as a line of build output
	// matches this regexp (see CompileMarker for the common "past cmake" case).
	Until *regexp.Regexp
	// ConfigFile, if non-empty, is an alternate config.yaml path (relative to the repo root).
	ConfigFile string
	// AddIfSuccessful merges ConfigFile's build entry into config.yaml once
	// a full build (not an early -test/-until/-timeout stop) succeeds.
	// Requires ConfigFile to be set.
	AddIfSuccessful bool
}

// BuildOne launches a Docker container to build the given MySQL label on the
// given OS. It returns the process exit code. When a build is stopped early on
// purpose (Timeout or Until), that is reported as success (rc 0).
func BuildOne(osName, label string, opts Options) int {
	start := time.Now()

	// One random code and one timestamp per run, shared by the container name
	// and every log filename (host build-one log plus the in-container
	// ossetup/build/rpm-qa files, via the RUN_CODE/RUN_DATETIME environment variables
	// below) so a run's files all carry the same code and date.
	code := randomSuffix(5)
	date := start.UTC().Format("20060102.150405")

	dir, err := os.Getwd()
	if err != nil {
		logx.Fatalf(1, "cannot determine working directory: %v", err)
	}

	cfg, err := config.Load(dir, opts.ConfigFile)
	if err != nil {
		logx.Fatalf(1, "%v", err)
	}
	image, ok := cfg.Image(osName)
	if !ok {
		logx.Fatalf(3, "no image found for OS %q (known: %v)", osName, cfg.OSes())
	}
	resolved, err := cfg.Resolve(osName, label)
	if err != nil {
		logx.Fatalf(2, "%v", err)
	}
	if opts.AddIfSuccessful {
		if n := cfg.BuildCount(); n != 1 {
			logx.Logf("- warning: %s defines %d build entries; -add-if-successful expects exactly 1", opts.ConfigFile, n)
		}
	}

	logFile := filepath.Join(dir, "log", fmt.Sprintf("build-one.%s__%s__%s__%s.log", osName, label, code, date))
	closer, err := logx.SetTee(logFile)
	if err != nil {
		logx.Fatalf(1, "cannot open logfile %s: %v", logFile, err)
	}
	defer func() { _ = closer.Close() }()

	logx.Logf("mysql-rpm-builder %s", version.Version)

	noopText := ""
	if opts.Noop {
		noopText = "NOT "
	}
	logx.Logf("%sattempting to build MySQL %s on %s (image %s)", noopText, label, osName, image)
	if opts.ConfigFile != "" {
		logx.Logf("- using alternate config file: %s", opts.ConfigFile)
	}
	if opts.Until != nil {
		logx.Logf("- will stop the container when build output matches /%s/", opts.Until)
	}
	if opts.Timeout > 0 {
		logx.Logf("- will stop the container after %s", opts.Timeout)
	}

	name := fmt.Sprintf("mysql-rpm-builder-%s-%s", label, code)
	dockerArgs := []string{
		"run",
		"--name=" + name,
		"--rm",
		"--network=host",
		"--hostname=buildhost",
		"-v", dir + ":/data",
		"-w", "/data",
		"-e", "RUN_CODE=" + code,
		"-e", "RUN_DATETIME=" + date,
		image,
		ContainerBinary, "run",
	}
	if opts.ConfigFile != "" {
		dockerArgs = append(dockerArgs, "-c", opts.ConfigFile)
	}
	dockerArgs = append(dockerArgs, label)

	rc := 0
	var stopper earlyStopper
	if opts.Noop {
		logx.Logf("NOOP: docker %v", dockerArgs)
	} else {
		stopper.name = name
		cmd := exec.Command("docker", dockerArgs...)
		cmd.Dir = dir

		w := logx.Writer()
		if opts.Until != nil {
			w = &lineWatcher{dst: w, re: opts.Until, onMatch: func(_ string) {
				stopper.stop(fmt.Sprintf("output matched /%s/", opts.Until))
			}}
		}
		cmd.Stdout = w
		cmd.Stderr = w

		if err := cmd.Start(); err != nil {
			logx.Fatalf(1, "cannot start docker: %v", err)
		}
		if opts.Timeout > 0 {
			t := time.AfterFunc(opts.Timeout, func() {
				stopper.stop(fmt.Sprintf("timeout of %s reached", opts.Timeout))
			})
			defer t.Stop()
		}
		if err := cmd.Wait(); err != nil {
			rc = exitCode(err)
		}
	}

	status := "OK"
	switch {
	case opts.Noop:
		status = "NOOP"
	case stopper.stopped():
		// Intentional early stop: the container's non-zero exit is expected.
		status = "STOPPED"
		rc = 0
		logx.Logf("stopped container early: %s", stopper.reason())
	case rc != 0:
		status = "FAILED"
	}
	elapsed := int(time.Since(start).Seconds())
	appendBuildStatus(dir, osName, label, image, status, rc, elapsed)

	if opts.AddIfSuccessful && status == "OK" {
		mergeConfig(dir, osName, label, resolved.Build)
	}

	logx.Logf("exit status %d (%s) for %sbuild of %s on %s", rc, status, noopText, label, osName)
	return rc
}

// mergeConfig folds a just-validated build entry into config.yaml (see
// config.MergeBuild) and logs the outcome. It never affects the build's exit
// code: the build already succeeded, and this is a best-effort convenience
// on top of it.
func mergeConfig(dir, osName, label string, build config.Build) {
	status, err := config.MergeBuild(dir, osName, label, build, time.Now().UTC())
	switch {
	case err != nil:
		logx.Logf("- warning: could not merge %s/%s into %s: %v (merge manually)",
			osName, label, config.DefaultConfigFile, err)
	case status == config.Merged:
		logx.Logf("- merged %s/%s into %s", osName, label, config.DefaultConfigFile)
	case status == config.SkippedIdentical:
		logx.Logf("- %s/%s already present in %s and identical; nothing to merge",
			osName, label, config.DefaultConfigFile)
	case status == config.SkippedDiffers:
		logx.Logf("- warning: %s/%s already present in %s with different settings; not overwriting",
			osName, label, config.DefaultConfigFile)
	}
}

// earlyStopper stops a named container once, recording why. It is safe for the
// timeout goroutine and the output-watcher goroutine to race on it.
type earlyStopper struct {
	name string
	once sync.Once
	mu   sync.Mutex
	why  string
	did  bool
}

func (s *earlyStopper) stop(reason string) {
	s.once.Do(func() {
		s.mu.Lock()
		s.why = reason
		s.did = true
		s.mu.Unlock()
		logx.Logf("- stopping container %s: %s", s.name, reason)
		if err := exec.Command("docker", "stop", "-t", "10", s.name).Run(); err != nil {
			logx.Logf("- warning: docker stop %s failed (already gone?): %v", s.name, err)
		}
	})
}

func (s *earlyStopper) stopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.did
}

func (s *earlyStopper) reason() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.why
}

// lineWatcher tees writes to dst while scanning complete lines for a regexp,
// invoking onMatch on the first match. os/exec may write to it concurrently
// from the stdout and stderr copiers, so Write is serialised.
type lineWatcher struct {
	dst     io.Writer
	re      *regexp.Regexp
	onMatch func(line string)
	mu      sync.Mutex
	buf     []byte
	fired   bool
}

func (w *lineWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.dst.Write(p)
	if w.fired {
		return n, err
	}
	w.buf = append(w.buf, p[:n]...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if w.re.MatchString(line) {
			w.fired = true
			w.buf = nil
			go w.onMatch(line)
			break
		}
	}
	return n, err
}

// appendBuildStatus appends a one-line summary to log/build-one.build_status,
// matching the format used by the original script.
func appendBuildStatus(dir, osName, label, image, status string, rc, elapsed int) {
	path := filepath.Join(dir, "log", "build-one.build_status")
	host, _ := os.Hostname()
	line := fmt.Sprintf("%s %s build-one[%d] osname=%s, label=%s, image=%s, status=%s, rc=%d, elapsed=%d\n",
		time.Now().UTC().Format("2006-01-02T15:04:05"), host, os.Getpid(), osName, label, image, status, rc, elapsed)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logx.Logf("- warning: cannot write build_status: %v", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(line); err != nil {
		logx.Logf("- warning: cannot write build_status: %v", err)
	}
}

// randomSuffix returns n lowercase letters.
func randomSuffix(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "xxxxx"[:n]
	}
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b)
}

// exitCode extracts the process exit code from an *exec.ExitError.
func exitCode(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
	}
	return 1
}
