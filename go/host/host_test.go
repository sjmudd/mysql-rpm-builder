// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package host

import "testing"

// These are deliberately limited to the pure logic (docker argv
// construction): BuildOne itself shells out to docker and needs a real
// container, the same reason go/steps and go/gitsteps have no unit tests
// for their command-running paths either -- that behavior is exercised by
// an actual ./build-one run instead.

func TestContainerName(t *testing.T) {
	if got, want := containerName("9.7.1", "abcde"), "mysql-rpm-builder-9.7.1-abcde"; got != want {
		t.Errorf("containerName() = %q, want %q", got, want)
	}
}

func TestBuildOneDockerArgs(t *testing.T) {
	args := buildOneDockerArgs("/data", "oraclelinux:10", "9.7.1", "abcde", "20260101.000000", Options{})
	want := []string{
		"run",
		"--name=mysql-rpm-builder-9.7.1-abcde",
		"--rm",
		"--network=host",
		"--hostname=buildhost",
		"-v", "/data:/data",
		"-w", "/data",
		"-e", "RUN_CODE=abcde",
		"-e", "RUN_DATETIME=20260101.000000",
		"oraclelinux:10",
		ContainerBinary, "run",
		"9.7.1",
	}
	assertArgsEqual(t, args, want)
}

func TestBuildOneDockerArgsWithConfigFile(t *testing.T) {
	// Regression-shaped test: any new Options field (like ConfigFile here)
	// must actually show up in the constructed argv, not just get parsed
	// and silently dropped before reaching docker.
	args := buildOneDockerArgs("/data", "oraclelinux:10", "9.7.1", "abcde", "20260101.000000", Options{ConfigFile: "test-config.yaml"})
	want := []string{
		"run",
		"--name=mysql-rpm-builder-9.7.1-abcde",
		"--rm",
		"--network=host",
		"--hostname=buildhost",
		"-v", "/data:/data",
		"-w", "/data",
		"-e", "RUN_CODE=abcde",
		"-e", "RUN_DATETIME=20260101.000000",
		"oraclelinux:10",
		ContainerBinary, "run",
		"-c", "test-config.yaml",
		"9.7.1",
	}
	assertArgsEqual(t, args, want)
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
