// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

// Package config loads the declarative build configuration (images.yaml +
// rpm-build-config.yaml) and resolves a concrete build for a given
// (os, label) pair.
//
// The configuration is layered OS -> MySQL version. images.yaml holds the
// per-OS, flavour-stable definition (container image + repo setup);
// rpm-build-config.yaml holds a chronological sequence of fully-explicit
// build entries per OS (source RPM URL + package list + optional shell
// tweaks). There is deliberately no inheritance or per-OS override magic:
// each build entry stands alone.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Top-level directory names, relative to the working directory (which is
// /data inside the build container). Defined here (not in go/steps or
// go/gitsteps) specifically so both can reference the same constants
// without go/gitsteps importing go/steps, which it deliberately never does.
const (
	// LogDir and BuiltDir are shared by all three build types, each
	// partitioning its own subdirectory under them (e.g. log/build-one/,
	// built/git-build-rpms/) so their logs/output never collide.
	LogDir   = "log"
	BuiltDir = "built"
	// SRPMSCacheDir is go/steps' own top-level cache of downloaded src.rpm
	// files -- unrelated to rpmbuild's fixed "SRPMS" subdirectory inside
	// ~/rpmbuild (that name is rpmbuild's own contract, not ours to name;
	// see gitsteps.go's own SRPMS-subdirectory handling, which is kept
	// separate from this constant on purpose).
	SRPMSCacheDir = "SRPMS"
)

// Default file locations, relative to the working directory (which is /data
// inside the build container).
const (
	DefaultImagesFile = "images.yaml"
	DefaultConfigFile = "rpm-build-config.yaml"
)

// Repos describes the repository setup for an OS. Names in Enable are enabled
// via `yum config-manager --set-enabled`; EPELPackages are installed via
// `dnf install`.
type Repos struct {
	Enable       []string `yaml:"enable"`
	EPELPackages []string `yaml:"epel_packages"`
}

// OSDef is the per-OS definition from images.yaml.
type OSDef struct {
	Image string `yaml:"image"`
	Repos Repos  `yaml:"repos"`
	// BasePackages are installed unconditionally in refresh, before anything
	// else, for every build on this OS (e.g. rpm-build, util-linux).
	BasePackages []string `yaml:"base_packages"`
	// ConfigManagerPackage is installed before repos.enable/epel_packages,
	// providing `yum config-manager` (e.g. dnf-command(config-manager)).
	ConfigManagerPackage string `yaml:"config_manager_package"`
	// BuilddepPackage is installed before build-dependency resolution,
	// providing `yum builddep`/`yum-builddep` (e.g. yum-utils).
	BuilddepPackage string `yaml:"builddep_package"`
	// EnableRPMBuildDefineEL passes --define "el<major> 1" to rpmbuild/rpmspec.
	// Only valid for EL-family OSes; see RPMDefine.
	EnableRPMBuildDefineEL bool `yaml:"enable_rpmbuild_define_el"`
}

// KnownELFamily lists OS IDs (osrelease.Info.ID) using the elN dist-macro
// convention. Excludes "fedora" (%fedora/%fc<N> instead, auto-defined).
var KnownELFamily = map[string]bool{
	"almalinux": true,
	"centos":    true,
	"ol":        true,
	"rhel":      true,
	"rocky":     true,
}

// RPMDefine returns "el<major> 1" when enableEL is true, "" when false.
// Errors if enableEL is true for an osID not in KnownELFamily.
func RPMDefine(osID string, osMajor int, enableEL bool) (string, error) {
	if !enableEL {
		return "", nil
	}
	if !KnownELFamily[osID] {
		return "", fmt.Errorf("enable_rpmbuild_define_el is true but OS %q is not a known EL-family distro", osID)
	}
	return fmt.Sprintf("el%d 1", osMajor), nil
}

// Build is a single, fully-explicit build entry from config.yaml.
//
// Packages are installed as root (steps.Runner.InstallPackages) before the
// build. When AutoInstallDependencies is set, yum-builddep additionally
// resolves the spec's BuildRequires in the separate install-builddeps step
// (see steps.Runner.InstallBuildDeps); Packages then only needs whatever the
// build actually requires but the spec's BuildRequires doesn't declare for
// this OS (e.g. el8's mysql.spec needs `cpp`, for rpcgen, without saying so).
type Build struct {
	// SRPM is normally an https:// download URL (e.g. dev.mysql.com). It may
	// also be a file:// URL, for a locally built src.rpm that was never
	// published anywhere -- e.g. one produced by ./build-src-rpm-from-git
	// under built/git-build-src-rpm/<os><major>__<label>/.
	// steps.Runner.InstallSRPM installs directly from a file:// path (no
	// download/caching), and since install-srpm always runs inside the
	// container, the path must be container-visible:
	// /data/built/git-build-src-rpm/<os><major>__<label>/<name>.src.rpm,
	// not a host-side relative path. See ol10-9.7.1-own-built-src-rpm.yaml
	// for a worked example.
	SRPM string `yaml:"srpm"`
	// AutoInstallDependencies lets yum-builddep determine and install the
	// spec's BuildRequires instead of (or in addition to) listing them all
	// in Packages. Must be true/false.
	AutoInstallDependencies *bool    `yaml:"auto_install_dependencies,omitempty"`
	Packages                []string `yaml:"packages"`
	Tweaks                  []string `yaml:"tweaks"`
	// Patches declares the patch files this build expects under
	// config/<label>/ (paths relative to that directory, e.g.
	// "SPECS/mysql.spec.patch"). When set, steps.Runner.ApplyPatches
	// verifies every listed file exists and errors if config/<label>/ or
	// any listed file is missing, instead of silently producing an
	// unpatched build. Optional and backward compatible: omit it and
	// apply-patches behaves exactly as before (applies whatever it finds
	// under config/<label>/, or no-ops if that directory is absent).
	Patches []string `yaml:"patches,omitempty"`
	// Annotations records a git-produced src.rpm's configuration: informational
	// only, never read by Resolve. Populated by generate-build-one-config
	// from git-build-src-rpm's .config.yaml sidecar; see go/gitsteps.
	Annotations *Annotations `yaml:"annotations,omitempty"`
}

// Annotations records a git-produced src.rpm's configuration: what was
// cloned, what patches were applied, and what packages were used to build it.
type Annotations struct {
	Repo                string   `yaml:"repo,omitempty"`
	Ref                 string   `yaml:"ref,omitempty"`
	Commit              string   `yaml:"commit,omitempty"`
	GitPatches          []string `yaml:"git_patches,omitempty"`
	MinimalGitPackages  []string `yaml:"minimal_git_packages,omitempty"`
	SrcRPMBuildPackages []string `yaml:"src_rpm_build_packages,omitempty"`
	BisonGenerated      bool     `yaml:"bison_generated,omitempty"`
}

// ShouldInstallDependencies returns true if we explicitly set AutoInstallDependencies to true
func (b Build) ShouldInstallDependencies() bool {
	return b.AutoInstallDependencies != nil && *b.AutoInstallDependencies
}

// imagesFile mirrors the top level of images.yaml.
type imagesFile struct {
	OSes map[string]OSDef `yaml:"oses"`
}

// configFile mirrors the top level of config.yaml.
type configFile struct {
	OSes map[string]struct {
		Builds map[string]Build `yaml:"builds"`
	} `yaml:"oses"`
}

// Config is the merged, in-memory configuration.
type Config struct {
	images         imagesFile
	config         configFile
	configFileName string
}

// Resolved is everything needed to build one (os, label) combination.
type Resolved struct {
	OS                     string
	Label                  string
	Image                  string
	Repos                  Repos
	BasePackages           []string
	ConfigManagerPackage   string
	BuilddepPackage        string
	EnableRPMBuildDefineEL bool
	Build                  Build
}

// Load reads and parses images.yaml and a config file from dir.
// If configFile is empty, DefaultConfigFile ("rpm-build-config.yaml") is used.
func Load(dir, configFile string) (*Config, error) {
	if configFile == "" {
		configFile = DefaultConfigFile
	}
	c := &Config{configFileName: configFile}
	if err := readYAML(filepath.Join(dir, DefaultImagesFile), &c.images); err != nil {
		return nil, err
	}
	if err := readYAML(filepath.Join(dir, configFile), &c.config); err != nil {
		return nil, err
	}
	return c, nil
}

func readYAML(path string, dst any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("cannot parse %s: %w", path, err)
	}
	return nil
}

// OSDef returns the image/repo definition for an OS.
func (c *Config) OSDef(osName string) (OSDef, bool) {
	def, ok := c.images.OSes[osName]
	return def, ok
}

// Image returns the container image for an OS (used by the host command before
// a container exists).
func (c *Config) Image(osName string) (string, bool) {
	def, ok := c.images.OSes[osName]
	if !ok {
		return "", false
	}
	return def.Image, true
}

// OSes returns the sorted list of OSes that have image definitions.
func (c *Config) OSes() []string {
	names := make([]string, 0, len(c.images.OSes))
	for k := range c.images.OSes {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Build returns the raw build entry for (osName, label) as currently
// configured, without the SRPM/image validation Resolve performs. Used to
// check whether a candidate build entry already exists.
func (c *Config) Build(osName, label string) (Build, bool) {
	entry, ok := c.config.OSes[osName]
	if !ok {
		return Build{}, false
	}
	build, ok := entry.Builds[label]
	return build, ok
}

// BuildCount returns the total number of build entries configured across all
// OSes in this config file. Used to sanity-check that an alternate config
// file passed via -c defines exactly one build entry.
func (c *Config) BuildCount() int {
	n := 0
	for _, entry := range c.config.OSes {
		n += len(entry.Builds)
	}
	return n
}

// Labels returns the sorted MySQL labels configured for an OS.
func (c *Config) Labels(osName string) []string {
	entry, ok := c.config.OSes[osName]
	if !ok {
		return nil
	}
	labels := make([]string, 0, len(entry.Builds))
	for k := range entry.Builds {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	return labels
}

// Resolve returns the concrete build for (os, label), erroring with a helpful
// message if either the OS or the label is not configured.
func (c *Config) Resolve(osName, label string) (Resolved, error) {
	osDef, ok := c.images.OSes[osName]
	if !ok {
		return Resolved{}, fmt.Errorf("no OS %q defined in %s (known: %v)", osName, DefaultImagesFile, c.OSes())
	}
	entry, ok := c.config.OSes[osName]
	if !ok {
		return Resolved{}, fmt.Errorf("no builds configured for OS %q in %s", osName, c.configFileName)
	}
	build, ok := entry.Builds[label]
	if !ok {
		return Resolved{}, fmt.Errorf("no build %q for OS %q in %s (known: %v)", label, osName, c.configFileName, c.Labels(osName))
	}
	if build.SRPM == "" {
		return Resolved{}, fmt.Errorf("build %q on OS %q has no srpm URL", label, osName)
	}
	return Resolved{
		OS:                     osName,
		Label:                  label,
		Image:                  osDef.Image,
		Repos:                  osDef.Repos,
		BasePackages:           osDef.BasePackages,
		ConfigManagerPackage:   osDef.ConfigManagerPackage,
		BuilddepPackage:        osDef.BuilddepPackage,
		EnableRPMBuildDefineEL: osDef.EnableRPMBuildDefineEL,
		Build:                  build,
	}, nil
}
