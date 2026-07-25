// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package osprep

import "testing"

// These are deliberately limited to the pure/no-op paths: most of this
// package's job is running real yum/dnf/useradd commands, which aren't
// meaningfully unit-testable without root and a real package manager (the
// same reason go/steps, which this package's logic was extracted from, has
// no unit tests either -- that behavior is exercised by an actual
// ./build-one/./build-rpm-from-git run instead, see CLAUDE.md's Testing
// section).

func TestInstallPackagesEmptyIsNoop(t *testing.T) {
	if err := InstallPackages(nil); err != nil {
		t.Errorf("InstallPackages(nil) = %v, want nil", err)
	}
	if err := InstallPackages([]string{}); err != nil {
		t.Errorf("InstallPackages([]string{}) = %v, want nil", err)
	}
}

func TestOSTweaksEmptyIsNoop(t *testing.T) {
	if err := OSTweaks(nil); err != nil {
		t.Errorf("OSTweaks(nil) = %v, want nil", err)
	}
	if err := OSTweaks([]string{}); err != nil {
		t.Errorf("OSTweaks([]string{}) = %v, want nil", err)
	}
}

func TestFixAnnobinNoToolsetDirsIsNoop(t *testing.T) {
	// On a normal dev/CI machine (not a gcc-toolset-equipped RHEL-family
	// container), /opt/rh/gcc-toolset-* doesn't exist, so this should always
	// no-op cleanly rather than erroring.
	if err := FixAnnobin(); err != nil {
		t.Errorf("FixAnnobin() = %v, want nil (no /opt/rh/gcc-toolset-* on the test host)", err)
	}
}

func TestBaseBuildPackagesNonEmpty(t *testing.T) {
	if len(BaseBuildPackages) == 0 {
		t.Error("BaseBuildPackages is empty; every build needs at least rpm-build")
	}
}
