// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package steps

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/sjmudd/mysql-rpm-builder/go/logx"
)

// run executes a command, teeing its output to the current log destination.
func run(name string, args ...string) error { return runIn("", name, args...) }

// runIn executes a command in dir (empty = current dir), teeing output to logs.
func runIn(dir, name string, args ...string) error {
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

// runShell executes a shell snippet via `sh -c`, teeing output to logs.
func runShell(script string) error {
	logx.Logf("+ sh -c %q", script)
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdout = logx.Writer()
	cmd.Stderr = logx.Writer()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("shell command failed: %w", err)
	}
	return nil
}

// lookupUser reports whether a system user exists.
func lookupUser(name string) (*user.User, error) { return user.Lookup(name) }

// copyDirFiles copies the regular files directly under srcDir into dstDir.
// A missing or empty srcDir is not an error.
func copyDirFiles(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		src := filepath.Join(srcDir, e.Name())
		dst := filepath.Join(dstDir, e.Name())
		logx.Logf("- copying %s -> %s", src, dst)
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies a single file, preserving mode.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// moveFile moves src to dst, falling back to copy+remove when src and dst are
// on different filesystems (os.Rename fails with EXDEV). This happens inside
// the container: rpmbuild's HOME and the mounted /data are separate devices.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	if err := copyFile(src, dst); err != nil {
		return err
	}
	return os.Remove(src)
}

// applyPatch applies a patch file inside dir using `patch -p0`.
func applyPatch(dir, patchFile string) error {
	f, err := os.Open(patchFile)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	cmd := exec.Command("patch", "-p0")
	cmd.Dir = dir
	cmd.Stdin = f
	cmd.Stdout = logx.Writer()
	cmd.Stderr = logx.Writer()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("patch %s failed: %w", patchFile, err)
	}
	return nil
}

// specFileIn returns the single *.spec filename in specsDir, as its base name
// so it can be passed to rpmbuild/yum-builddep with specsDir as the working
// directory. Installing a src.rpm lays down exactly one spec; finding none or
// several is an error, since we cannot know which to build.
func specFileIn(specsDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(specsDir, "*.spec"))
	if err != nil {
		return "", err
	}
	sort.Strings(matches)
	switch len(matches) {
	case 1:
		return filepath.Base(matches[0]), nil
	case 0:
		return "", fmt.Errorf("no .spec file found in %s (was the src.rpm installed?)", specsDir)
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = filepath.Base(m)
		}
		return "", fmt.Errorf("expected exactly one .spec file in %s, found %d: %v", specsDir, len(matches), names)
	}
}

// captureRPMQA writes a sorted `rpm -qa` listing to path.
func captureRPMQA(path string) error {
	out, err := exec.Command("rpm", "-qa").Output()
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	sort.Strings(lines)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
