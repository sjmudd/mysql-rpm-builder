// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

// Package config loads the declarative build configuration (images.yaml +
// config.yaml) and resolves a concrete build for a given (os, label) pair.
//
// The configuration is layered OS -> MySQL version. images.yaml holds the
// per-OS, flavour-stable definition (container image + repo setup); config.yaml
// holds a chronological sequence of fully-explicit build entries per OS (source
// RPM URL + package list + optional shell tweaks). There is deliberately no
// inheritance or per-OS override magic: each build entry stands alone.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Default file locations, relative to the working directory (which is /data
// inside the build container).
const (
	DefaultImagesFile = "images.yaml"
	DefaultConfigFile = "config.yaml"
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
}

// Build is a single, fully-explicit build entry from config.yaml.
//
// Build dependencies are installed in this order (see steps.Runner.InstallPackages):
//  1. ExtraPackages — packages missing from the src.rpm's BuildRequires,
//     installed first so they are present however the rest are resolved.
//  2. if AutoInstallDependencies is set, yum-builddep resolves the src.rpm's
//     BuildRequires (yum-utils is installed first, as it provides yum-builddep).
//  3. Packages — the explicitly listed packages, installed afterwards.
type Build struct {
	SRPM string `yaml:"srpm"`
	// AutoInstallDependencies lets yum-builddep determine and install the
	// src.rpm's BuildRequires instead of (or in addition to) listing them all
	// in Packages. Must be true/false.
	AutoInstallDependencies bool `yaml:"auto_install_dependencies"`
	// ExtraPackages are packages missing from the src.rpm's BuildRequires that
	// yum-builddep would not install; they are installed first.
	ExtraPackages []string `yaml:"extra_packages"`
	Packages      []string `yaml:"packages"`
	Tweaks        []string `yaml:"tweaks"`
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
	images imagesFile
	config configFile
}

// Resolved is everything needed to build one (os, label) combination.
type Resolved struct {
	OS    string
	Label string
	Image string
	Repos Repos
	Build Build
}

// Load reads and parses images.yaml and config.yaml from dir.
func Load(dir string) (*Config, error) {
	c := &Config{}
	if err := readYAML(filepath.Join(dir, DefaultImagesFile), &c.images); err != nil {
		return nil, err
	}
	if err := readYAML(filepath.Join(dir, DefaultConfigFile), &c.config); err != nil {
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
		return Resolved{}, fmt.Errorf("no builds configured for OS %q in %s", osName, DefaultConfigFile)
	}
	build, ok := entry.Builds[label]
	if !ok {
		return Resolved{}, fmt.Errorf("no build %q for OS %q in %s (known: %v)", label, osName, DefaultConfigFile, c.Labels(osName))
	}
	if build.SRPM == "" {
		return Resolved{}, fmt.Errorf("build %q on OS %q has no srpm URL", label, osName)
	}
	return Resolved{
		OS:    osName,
		Label: label,
		Image: osDef.Image,
		Repos: osDef.Repos,
		Build: build,
	}, nil
}
