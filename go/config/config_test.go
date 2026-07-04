// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadTestdata loads the synthetic images.yaml + config.yaml under testdata/.
func loadTestdata(t *testing.T) *Config {
	t.Helper()
	c, err := Load("testdata")
	if err != nil {
		t.Fatalf("loading testdata config: %v", err)
	}
	return c
}

func TestResolveSuccess(t *testing.T) {
	c := loadTestdata(t)
	got, err := c.Resolve("ol10", "9.7.1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Image != "oraclelinux:10" {
		t.Errorf("Image = %q, want %q", got.Image, "oraclelinux:10")
	}
	if got.Build.SRPM != "https://example.invalid/mysql-9.7.1.src.rpm" {
		t.Errorf("SRPM = %q, unexpected", got.Build.SRPM)
	}
	if !reflect.DeepEqual(got.Build.Packages, []string{"cmake", "gcc", "gcc-c++"}) {
		t.Errorf("Packages = %v, unexpected", got.Build.Packages)
	}
	if !got.Build.ShouldInstallDependencies() {
		t.Errorf("ShouldInstallDependencies() = false, want true")
	}
	if !reflect.DeepEqual(got.Repos.Enable, []string{"ol10_codeready_builder"}) {
		t.Errorf("Repos.Enable = %v, unexpected", got.Repos.Enable)
	}
}

func TestResolveDefaultsWhenFieldsOmitted(t *testing.T) {
	c := loadTestdata(t)
	// ol10/9.7.0 sets auto_install_dependencies: false.
	got, err := c.Resolve("ol10", "9.7.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Build.ShouldInstallDependencies() {
		t.Errorf("ShouldInstallDependencies() = true, want false")
	}
}

func TestResolveErrors(t *testing.T) {
	c := loadTestdata(t)
	cases := []struct {
		name       string
		os, label  string
		wantSubstr string
	}{
		{"unknown os", "nosuch", "9.7.1", `no OS "nosuch"`},
		{"os defined but no builds", "almalinux10", "9.7.1", `no builds configured for OS "almalinux10"`},
		{"unknown label", "ol10", "1.2.3", `no build "1.2.3" for OS "ol10"`},
		{"missing srpm url", "rocky9", "broken", `has no srpm URL`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Resolve(tc.os, tc.label)
			if err == nil {
				t.Fatalf("expected an error, got none")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestOSesAndLabelsSorted(t *testing.T) {
	c := loadTestdata(t)
	if got, want := c.OSes(), []string{"almalinux10", "ol10", "rocky9"}; !reflect.DeepEqual(got, want) {
		t.Errorf("OSes() = %v, want %v", got, want)
	}
	// Labels are sorted; ol10 has 9.7.0 and 9.7.1 configured.
	if got, want := c.Labels("ol10"), []string{"9.7.0", "9.7.1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Labels(ol10) = %v, want %v", got, want)
	}
	if got := c.Labels("nosuch"); got != nil {
		t.Errorf("Labels(nosuch) = %v, want nil", got)
	}
}

func TestImage(t *testing.T) {
	c := loadTestdata(t)
	if img, ok := c.Image("rocky9"); !ok || img != "rockylinux:9" {
		t.Errorf("Image(rocky9) = %q, %v; want %q, true", img, ok, "rockylinux:9")
	}
	if _, ok := c.Image("nosuch"); ok {
		t.Errorf("Image(nosuch) ok = true, want false")
	}
}

// TestRealConfigResolves loads the repository's actual images.yaml and
// config.yaml and asserts that every configured (os, label) resolves cleanly.
// This guards against typos or unknown keys in the files most often edited
// (readYAML uses KnownFields(true), so stray keys fail the load).
func TestRealConfigResolves(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	c, err := Load(repoRoot)
	if err != nil {
		t.Fatalf("loading real config from %s: %v", repoRoot, err)
	}
	oses := c.OSes()
	if len(oses) == 0 {
		t.Fatal("real images.yaml defines no OSes")
	}
	total := 0
	for _, os := range oses {
		for _, label := range c.Labels(os) {
			total++
			if _, err := c.Resolve(os, label); err != nil {
				t.Errorf("Resolve(%q, %q) failed: %v", os, label, err)
			}
		}
	}
	if total == 0 {
		t.Fatal("real config.yaml defines no builds")
	}
	t.Logf("resolved %d build(s) across %d OS(es)", total, len(oses))
}
