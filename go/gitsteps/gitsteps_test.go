// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package gitsteps

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sjmudd/mysql-rpm-builder/go/osrelease"
)

// These are deliberately limited to the pure logic (path/label derivation,
// file parsing): Clone/Configure/AssembleSRPM/Run all shell out to
// git/cmake/rpmbuild or need a real container, the same reason go/steps and
// go/osprep have no unit tests for their command-running paths either (see
// CLAUDE.md's Testing section -- that behavior is exercised by an actual
// ./build-rpm-from-git run instead).

func testRunner(tag string) *Runner {
	return &Runner{
		OS:        osrelease.Info{ID: "ol", Major: 10},
		Tag:       tag,
		DataDir:   "/data",
		OutputDir: DefaultOutputDir,
	}
}

func TestVersionStripsMysqlPrefix(t *testing.T) {
	if got, want := testRunner("mysql-9.7.1").version(), "9.7.1"; got != want {
		t.Errorf("version() = %q, want %q", got, want)
	}
	// A bare tag/branch with no "mysql-" prefix is passed through unchanged.
	if got, want := testRunner("9.7.1").version(), "9.7.1"; got != want {
		t.Errorf("version() = %q, want %q", got, want)
	}
}

func TestOSAndRPMDefines(t *testing.T) {
	r := testRunner("mysql-9.7.1")
	if got, want := r.osLabel(), "ol10"; got != want {
		t.Errorf("osLabel() = %q, want %q", got, want)
	}
	if got, want := r.elDefine(), "el10"; got != want {
		t.Errorf("elDefine() = %q, want %q", got, want)
	}
	if got, want := r.rpmDefine(), "el10 1"; got != want {
		t.Errorf("rpmDefine() = %q, want %q", got, want)
	}
}

func TestOutputSubdir(t *testing.T) {
	r := testRunner("mysql-9.7.1")
	got := r.outputSubdir()
	want := filepath.Join("/data", DefaultOutputDir, "ol10__mysql-9.7.1")
	if got != want {
		t.Errorf("outputSubdir() = %q, want %q", got, want)
	}
}

func TestFindBoostDir(t *testing.T) {
	src := t.TempDir()
	boost := filepath.Join(src, "extra", "boost", "boost_1_87_0")
	if err := os.MkdirAll(boost, 0o755); err != nil {
		t.Fatalf("setting up fixture: %v", err)
	}

	got, err := findBoostDir(src)
	if err != nil {
		t.Fatalf("findBoostDir: %v", err)
	}
	if got != boost {
		t.Errorf("findBoostDir() = %q, want %q", got, boost)
	}
}

func TestFindBoostDirMissing(t *testing.T) {
	// 8.0.x/8.4.x pull boost as a separate Source instead of bundling it
	// under extra/boost/ -- this should error clearly, not panic or silently
	// pick something wrong.
	src := t.TempDir()
	if _, err := findBoostDir(src); err == nil {
		t.Error("findBoostDir on a tree with no extra/boost/boost_* = nil error, want one")
	}
}

// fakeBoostCMake writes a minimal cmake/boost.cmake fixture under srcDir,
// mirroring the real file's shape closely enough for the regexes to match --
// including that BOOST_DOWNLOAD_URL is itself a "${BOOST_TARBALL}"
// cmake-variable reference, not a literal filename, the same as the real
// file (this is the exact thing an earlier version of this parser got
// wrong: it assumed the URL ended in a literal ".tar.bz2").
func fakeBoostCMake(t *testing.T, srcDir, pkg, urlPrefix string) {
	t.Helper()
	dir := filepath.Join(srcDir, "cmake")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("setting up fixture: %v", err)
	}
	content := "SET(BOOST_PACKAGE_NAME \"" + pkg + "\")\n" +
		"SET(BOOST_TARBALL \"${BOOST_PACKAGE_NAME}.tar.bz2\")\n" +
		"SET(BOOST_DOWNLOAD_URL\n  \"" + urlPrefix + "${BOOST_TARBALL}\"\n  )\n"
	if err := os.WriteFile(filepath.Join(dir, "boost.cmake"), []byte(content), 0o644); err != nil {
		t.Fatalf("setting up fixture: %v", err)
	}
}

func TestFindOrFetchBoostPrefersBundled(t *testing.T) {
	// When extra/boost/boost_* exists (9.x layout), it must win even if a
	// cmake/boost.cmake fixture is also present -- no download attempted.
	src := t.TempDir()
	boost := filepath.Join(src, "extra", "boost", "boost_1_87_0")
	if err := os.MkdirAll(boost, 0o755); err != nil {
		t.Fatalf("setting up fixture: %v", err)
	}
	fakeBoostCMake(t, src, "boost_1_77_0", "http://example.invalid/release/1.77.0/source/")

	r := testRunner("mysql-9.7.1")
	r.DataDir = t.TempDir()
	got, err := r.findOrFetchBoost(src)
	if err != nil {
		t.Fatalf("findOrFetchBoost: %v", err)
	}
	if got != boost {
		t.Errorf("findOrFetchBoost() = %q, want %q", got, boost)
	}
}

func TestFetchBoostDownloadsAndCaches(t *testing.T) {
	const tarballBody = "not a real boost tarball, just test content"
	var requests int
	var lastPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests++
		lastPath = req.URL.Path
		_, _ = w.Write([]byte(tarballBody))
	}))
	defer srv.Close()

	src := t.TempDir()
	fakeBoostCMake(t, src, "boost_1_77_0", srv.URL+"/release/1.77.0/source/")

	r := testRunner("mysql-8.0.46")
	r.DataDir = t.TempDir()

	got, err := r.findOrFetchBoost(src)
	if err != nil {
		t.Fatalf("findOrFetchBoost: %v", err)
	}
	wantDir := filepath.Join(r.DataDir, "boost-cache")
	if got != wantDir {
		t.Errorf("findOrFetchBoost() = %q, want %q", got, wantDir)
	}
	tarball := filepath.Join(wantDir, "boost_1_77_0.tar.bz2")
	body, err := os.ReadFile(tarball)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", tarball, err)
	}
	if string(body) != tarballBody {
		t.Errorf("downloaded tarball content = %q, want %q", body, tarballBody)
	}
	if requests != 1 {
		t.Fatalf("expected 1 request to the fake boost server, got %d", requests)
	}
	// Proves the "${BOOST_TARBALL}" cmake-variable reference in
	// BOOST_DOWNLOAD_URL was actually substituted, not requested literally.
	if want := "/release/1.77.0/source/boost_1_77_0.tar.bz2"; lastPath != want {
		t.Errorf("request path = %q, want %q", lastPath, want)
	}

	// A second call with the same cache dir must reuse the cached tarball,
	// not re-download it (these containers are --rm, so this is the only
	// thing making repeated git-builds affordable).
	if _, err := r.findOrFetchBoost(src); err != nil {
		t.Fatalf("findOrFetchBoost (second call): %v", err)
	}
	if requests != 1 {
		t.Errorf("expected findOrFetchBoost to reuse the cached tarball, but the server saw %d requests", requests)
	}
}

func TestFetchBoostMissingCMakeFile(t *testing.T) {
	src := t.TempDir() // no cmake/boost.cmake at all
	r := testRunner("mysql-8.0.46")
	r.DataDir = t.TempDir()
	if _, err := r.findOrFetchBoost(src); err == nil {
		t.Error("findOrFetchBoost with no cmake/boost.cmake = nil error, want one")
	}
}

func TestProvideLegacyFilterScriptsWritesWhenDeclaredAndMissing(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "mysql.spec")
	content := "Name: mysql\nSource90:       filter-provides.sh\nSource91:       filter-requires.sh\n"
	if err := os.WriteFile(spec, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	sourcesDir := t.TempDir()

	if err := provideLegacyFilterScripts(spec, sourcesDir); err != nil {
		t.Fatalf("provideLegacyFilterScripts: %v", err)
	}
	for name, want := range legacyFilterScripts {
		got, err := os.ReadFile(filepath.Join(sourcesDir, name))
		if err != nil {
			t.Fatalf("expected %s to be written: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", name, got, want)
		}
	}
}

func TestProvideLegacyFilterScriptsSkipsWhenNotDeclared(t *testing.T) {
	// 8.4.x/9.x's spec doesn't declare Source90/91 at all -- must be a no-op,
	// not write anything.
	dir := t.TempDir()
	spec := filepath.Join(dir, "mysql.spec")
	if err := os.WriteFile(spec, []byte("Name: mysql\n"), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	sourcesDir := t.TempDir()

	if err := provideLegacyFilterScripts(spec, sourcesDir); err != nil {
		t.Fatalf("provideLegacyFilterScripts: %v", err)
	}
	entries, err := os.ReadDir(sourcesDir)
	if err != nil {
		t.Fatalf("reading sourcesDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no files written when the spec doesn't declare Source90/91, got %v", entries)
	}
}

func TestProvideLegacyFilterScriptsNeverOverwritesExisting(t *testing.T) {
	// A file already present in sourcesDir for any reason -- a real one
	// shipped by some future git tree, or left over from a prior run -- must
	// win over this hardcoded fallback, never get clobbered by it.
	dir := t.TempDir()
	spec := filepath.Join(dir, "mysql.spec")
	content := "Name: mysql\nSource90:       filter-provides.sh\nSource91:       filter-requires.sh\n"
	if err := os.WriteFile(spec, []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	sourcesDir := t.TempDir()
	const existing = "#!/bin/bash\n# a real, different filter-provides.sh\n"
	if err := os.WriteFile(filepath.Join(sourcesDir, "filter-provides.sh"), []byte(existing), 0o755); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if err := provideLegacyFilterScripts(spec, sourcesDir); err != nil {
		t.Fatalf("provideLegacyFilterScripts: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(sourcesDir, "filter-provides.sh"))
	if err != nil {
		t.Fatalf("reading filter-provides.sh: %v", err)
	}
	if string(got) != existing {
		t.Errorf("filter-provides.sh was overwritten: got %q, want unchanged %q", got, existing)
	}
	// filter-requires.sh wasn't pre-existing, so it should still get written.
	if _, err := os.Stat(filepath.Join(sourcesDir, "filter-requires.sh")); err != nil {
		t.Errorf("expected filter-requires.sh to still be written: %v", err)
	}
}

func TestLoadDepsPackages(t *testing.T) {
	dir := t.TempDir()
	content := "oses:\n  ol10:\n    packages:\n      - git\n      - cmake\n      - bison\n"
	if err := os.WriteFile(filepath.Join(dir, DepsFile), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	got, err := loadDepsPackages(dir, "ol10", "mysql-9.7.1")
	if err != nil {
		t.Fatalf("loadDepsPackages: %v", err)
	}
	want := []string{"git", "cmake", "bison"}
	if len(got) != len(want) {
		t.Fatalf("loadDepsPackages() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("loadDepsPackages()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLoadDepsPackagesPerTagOverride(t *testing.T) {
	dir := t.TempDir()
	content := "oses:\n" +
		"  ol9:\n" +
		"    packages:\n" +
		"      - git\n" +
		"      - cmake\n" +
		"    builds:\n" +
		"      mysql-9.7.1:\n" +
		"        packages:\n" +
		"          - gcc-toolset-14-gcc\n" +
		"      mysql-8.0.45:\n" +
		"        packages: []\n"
	if err := os.WriteFile(filepath.Join(dir, DepsFile), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	// A tag with an override gets the OS base plus that tag's packages appended.
	got, err := loadDepsPackages(dir, "ol9", "mysql-9.7.1")
	if err != nil {
		t.Fatalf("loadDepsPackages: %v", err)
	}
	want := []string{"git", "cmake", "gcc-toolset-14-gcc"}
	if len(got) != len(want) {
		t.Fatalf("loadDepsPackages(mysql-9.7.1) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("loadDepsPackages(mysql-9.7.1)[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	// A tag with an empty override list still just gets the OS base, no error.
	got, err = loadDepsPackages(dir, "ol9", "mysql-8.0.45")
	if err != nil {
		t.Fatalf("loadDepsPackages: %v", err)
	}
	want = []string{"git", "cmake"}
	if len(got) != len(want) {
		t.Fatalf("loadDepsPackages(mysql-8.0.45) = %v, want %v", got, want)
	}

	// A tag with no builds: entry at all also just gets the OS base.
	got, err = loadDepsPackages(dir, "ol9", "mysql-8.4.10")
	if err != nil {
		t.Fatalf("loadDepsPackages: %v", err)
	}
	want = []string{"git", "cmake"}
	if len(got) != len(want) {
		t.Fatalf("loadDepsPackages(mysql-8.4.10) = %v, want %v", got, want)
	}
}

func TestLoadDepsPackagesMissingOS(t *testing.T) {
	dir := t.TempDir()
	content := "oses:\n  ol10:\n    packages:\n      - git\n"
	if err := os.WriteFile(filepath.Join(dir, DepsFile), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := loadDepsPackages(dir, "rocky9", "mysql-9.7.1"); err == nil {
		t.Error("loadDepsPackages for an OS with no entry = nil error, want one")
	}
}

func TestLoadDepsPackagesUnknownField(t *testing.T) {
	// KnownFields(true), same as go/config -- a typo'd/unexpected key should
	// fail loudly rather than be silently ignored.
	dir := t.TempDir()
	content := "oses:\n  ol10:\n    package: [git]\n" // typo: "package" not "packages"
	if err := os.WriteFile(filepath.Join(dir, DepsFile), []byte(content), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if _, err := loadDepsPackages(dir, "ol10", "mysql-9.7.1"); err == nil {
		t.Error("loadDepsPackages with an unknown field = nil error, want one")
	}
}
