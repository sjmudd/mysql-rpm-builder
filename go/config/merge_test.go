// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

// mergeFixtureDir copies the merge testdata fixture (images.yaml +
// config.yaml) into a fresh temp dir, since MergeBuild mutates config.yaml
// in place (plus writes a timestamped backup) and tests must not touch the
// checked-in fixture.
func mergeFixtureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"images.yaml", "config.yaml"} {
		data, err := os.ReadFile(filepath.Join("testdata", "merge", name))
		if err != nil {
			t.Fatalf("reading fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatalf("writing %s into temp dir: %v", name, err)
		}
	}
	return dir
}

var mergeNow = time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)

func backupPath(dir string) string {
	return filepath.Join(dir, DefaultConfigFile+"."+mergeNow.Format(backupTimeFormat))
}

func TestMergeBuildInsertsAfterLastRealEntry(t *testing.T) {
	dir := mergeFixtureDir(t)
	original, err := os.ReadFile(filepath.Join(dir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("reading original config.yaml: %v", err)
	}

	build := Build{SRPM: "https://example.invalid/9.8.0.src.rpm", AutoInstallDependencies: boolPtr(true)}
	status, err := MergeBuild(dir, "trailing1", "9.8.0", build, mergeNow)
	if err != nil {
		t.Fatalf("MergeBuild: %v", err)
	}
	if status != Merged {
		t.Fatalf("status = %v, want Merged", status)
	}

	merged, err := os.ReadFile(filepath.Join(dir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("reading merged config.yaml: %v", err)
	}
	text := string(merged)

	// Inserted after the real 9.7.1 entry (and its fields), before trailing2.
	i971 := strings.Index(text, "9.7.1:\n        srpm: https://example.invalid/9.7.1.src.rpm")
	iNew := strings.Index(text, "9.8.0:")
	iT2 := strings.Index(text, "trailing2:")
	if i971 < 0 || iNew < 0 || iT2 < 0 {
		t.Fatalf("expected markers not found in merged file:\n%s", text)
	}
	if i971 >= iNew || iNew >= iT2 {
		t.Errorf("expected 9.7.1 < 9.8.0 < trailing2 offsets, got %d, %d, %d", i971, iNew, iT2)
	}

	// Untouched content (comments in other styles) survives verbatim.
	for _, want := range []string{
		"#      tree:",
		"  #    8.4.10:",
		"#      9.4.0:",
		"  # Next section",
		"#      9.5.0:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("merged file lost expected untouched content %q", want)
		}
	}

	// Round-trips and resolves.
	c, err := Load(dir, "")
	if err != nil {
		t.Fatalf("reloading merged config: %v", err)
	}
	got, ok := c.Build("trailing1", "9.8.0")
	if !ok {
		t.Fatalf("merged build not found")
	}
	if !reflect.DeepEqual(got, build) {
		t.Errorf("Build() = %+v, want %+v", got, build)
	}

	// Backup preserves the exact pre-merge content.
	backup, err := os.ReadFile(backupPath(dir))
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if !reflect.DeepEqual(backup, original) {
		t.Errorf("backup content does not match pre-merge config.yaml")
	}
}

func TestMergeBuildInsertsBeforeTrailingDividerComment(t *testing.T) {
	dir := mergeFixtureDir(t)

	build := Build{SRPM: "https://example.invalid/9.8.0-t2.src.rpm", AutoInstallDependencies: boolPtr(true)}
	status, err := MergeBuild(dir, "trailing2", "9.8.0", build, mergeNow)
	if err != nil {
		t.Fatalf("MergeBuild: %v", err)
	}
	if status != Merged {
		t.Fatalf("status = %v, want Merged", status)
	}

	merged, err := os.ReadFile(filepath.Join(dir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("reading merged config.yaml: %v", err)
	}
	text := string(merged)

	i971 := strings.Index(text, "9.7.1:\n        srpm: https://example.invalid/9.7.1-t2.src.rpm")
	iNew := strings.Index(text, "9.8.0:")
	iDivider := strings.Index(text, "# Next section")
	iAllCommented := strings.Index(text, "allcommented:")
	if i971 < 0 || iNew < 0 || iDivider < 0 || iAllCommented < 0 {
		t.Fatalf("expected markers not found in merged file:\n%s", text)
	}
	if i971 >= iNew || iNew >= iDivider || iDivider >= iAllCommented {
		t.Errorf("expected 9.7.1 < 9.8.0 < divider < allcommented offsets, got %d, %d, %d, %d",
			i971, iNew, iDivider, iAllCommented)
	}
}

func TestMergeBuildAllCommentedEntries(t *testing.T) {
	dir := mergeFixtureDir(t)

	build := Build{SRPM: "https://example.invalid/9.8.0-ac.src.rpm", AutoInstallDependencies: boolPtr(false), Packages: []string{"cmake", "gcc"}}
	status, err := MergeBuild(dir, "allcommented", "9.8.0", build, mergeNow)
	if err != nil {
		t.Fatalf("MergeBuild: %v", err)
	}
	if status != Merged {
		t.Fatalf("status = %v, want Merged", status)
	}

	merged, err := os.ReadFile(filepath.Join(dir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("reading merged config.yaml: %v", err)
	}
	text := string(merged)

	iBuilds := strings.Index(text, "allcommented:\n    builds:")
	iNew := strings.Index(text, "9.8.0:")
	i95 := strings.Index(text, "#      9.5.0:")
	if iBuilds < 0 || iNew < 0 || i95 < 0 {
		t.Fatalf("expected markers not found in merged file:\n%s", text)
	}
	if iBuilds >= iNew || iNew >= i95 {
		t.Errorf("expected builds: < 9.8.0 < commented 9.5.0 offsets, got %d, %d, %d", iBuilds, iNew, i95)
	}
	if !strings.Contains(text, "packages: [cmake, gcc]") {
		t.Errorf("expected flow-style packages list in merged entry, got:\n%s", text)
	}

	c, err := Load(dir, "")
	if err != nil {
		t.Fatalf("reloading merged config: %v", err)
	}
	if _, err := c.Resolve("allcommented", "9.8.0"); err != nil {
		t.Errorf("Resolve(allcommented, 9.8.0) failed: %v", err)
	}
}

func TestMergeBuildSkipIdentical(t *testing.T) {
	dir := mergeFixtureDir(t)
	original, err := os.ReadFile(filepath.Join(dir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("reading original config.yaml: %v", err)
	}

	build := Build{SRPM: "https://example.invalid/9.9.9.src.rpm", AutoInstallDependencies: boolPtr(true)}
	status, err := MergeBuild(dir, "existing", "9.9.9", build, mergeNow)
	if err != nil {
		t.Fatalf("MergeBuild: %v", err)
	}
	if status != SkippedIdentical {
		t.Fatalf("status = %v, want SkippedIdentical", status)
	}

	after, err := os.ReadFile(filepath.Join(dir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("reading config.yaml after skip: %v", err)
	}
	if !reflect.DeepEqual(after, original) {
		t.Errorf("config.yaml was modified on a SkippedIdentical merge")
	}
	if _, err := os.Stat(backupPath(dir)); err == nil {
		t.Errorf("backup file was created on a SkippedIdentical merge")
	}
}

func TestMergeBuildSkipDiffers(t *testing.T) {
	dir := mergeFixtureDir(t)
	original, err := os.ReadFile(filepath.Join(dir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("reading original config.yaml: %v", err)
	}

	build := Build{SRPM: "https://example.invalid/9.9.9-different.src.rpm", AutoInstallDependencies: boolPtr(true)}
	status, err := MergeBuild(dir, "existing", "9.9.9", build, mergeNow)
	if err != nil {
		t.Fatalf("MergeBuild: %v", err)
	}
	if status != SkippedDiffers {
		t.Fatalf("status = %v, want SkippedDiffers", status)
	}

	after, err := os.ReadFile(filepath.Join(dir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("reading config.yaml after skip: %v", err)
	}
	if !reflect.DeepEqual(after, original) {
		t.Errorf("config.yaml was modified on a SkippedDiffers merge")
	}
	if _, err := os.Stat(backupPath(dir)); err == nil {
		t.Errorf("backup file was created on a SkippedDiffers merge")
	}
}

func TestMergeBuildErrorsWhenOSSectionMissing(t *testing.T) {
	dir := mergeFixtureDir(t)
	original, err := os.ReadFile(filepath.Join(dir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("reading original config.yaml: %v", err)
	}

	build := Build{SRPM: "https://example.invalid/ghost.src.rpm", AutoInstallDependencies: boolPtr(true)}
	if _, err := MergeBuild(dir, "ghost", "1.0.0", build, mergeNow); err == nil {
		t.Fatalf("expected an error for an OS with no section in config.yaml, got nil")
	}

	after, err := os.ReadFile(filepath.Join(dir, DefaultConfigFile))
	if err != nil {
		t.Fatalf("reading config.yaml after failed merge: %v", err)
	}
	if !reflect.DeepEqual(after, original) {
		t.Errorf("config.yaml was modified despite the merge failing")
	}
	if _, err := os.Stat(backupPath(dir)); err == nil {
		t.Errorf("backup file was created despite the merge failing")
	}
}

func TestBuildCount(t *testing.T) {
	c := loadTestdata(t)
	if got, want := c.BuildCount(), 3; got != want {
		t.Errorf("BuildCount() = %d, want %d", got, want)
	}

	alt, err := Load("testdata", "alt-config.yaml")
	if err != nil {
		t.Fatalf("loading alt-config.yaml: %v", err)
	}
	if got, want := alt.BuildCount(), 1; got != want {
		t.Errorf("BuildCount() (alt-config) = %d, want %d", got, want)
	}
}
