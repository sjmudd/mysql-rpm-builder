// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package main

import (
	"testing"

	"github.com/sjmudd/mysql-rpm-builder/go/host"
)

// Deliberately limited to the pure docker-argv construction: runGitBuildOne
// itself shells out to docker and needs a real container to exercise.

func TestGitBuildOneDockerArgs(t *testing.T) {
	got := gitBuildOneDockerArgs(
		"/data", "oraclelinux:9", "26.7.0", "abcde", "built-from-git",
		false, "https://github.com/sjmudd/mysql-server.git", "bug/120895",
	)
	want := []string{
		"run",
		"--name=mysql-rpm-builder-26.7.0-abcde",
		"--rm",
		"--network=host",
		"--hostname=buildhost",
		"-v", "/data:/data",
		"-w", "/data",
		"oraclelinux:9",
		host.ContainerBinary, "git-run", "-o", "built-from-git",
		"-repo", "https://github.com/sjmudd/mysql-server.git",
		"-ref", "bug/120895",
		"26.7.0",
	}
	assertArgsEqual(t, got, want)
}

func TestGitBuildOneDockerArgsSkipBison(t *testing.T) {
	// Regression-shaped test: -no-bison must land between -o and -repo/-ref,
	// not get dropped when combined with the repo/ref flags.
	got := gitBuildOneDockerArgs(
		"/data", "oraclelinux:10", "mysql-9.7.1", "abcde", "built-from-git",
		true, "https://github.com/mysql/mysql-server.git", "mysql-9.7.1",
	)
	want := []string{
		"run",
		"--name=mysql-rpm-builder-mysql-9.7.1-abcde",
		"--rm",
		"--network=host",
		"--hostname=buildhost",
		"-v", "/data:/data",
		"-w", "/data",
		"oraclelinux:10",
		host.ContainerBinary, "git-run", "-o", "built-from-git",
		"-no-bison",
		"-repo", "https://github.com/mysql/mysql-server.git",
		"-ref", "mysql-9.7.1",
		"mysql-9.7.1",
	}
	assertArgsEqual(t, got, want)
}

func assertArgsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}
