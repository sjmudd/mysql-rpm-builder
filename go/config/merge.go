// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// MergeStatus reports the outcome of MergeBuild.
type MergeStatus int

const (
	// Merged means the build entry was inserted into config.yaml.
	Merged MergeStatus = iota
	// SkippedIdentical means (osName, label) already existed with identical settings.
	SkippedIdentical
	// SkippedDiffers means (osName, label) already existed with different settings.
	SkippedDiffers
)

// backupTimeFormat matches the UTC timestamp format already used for
// log/build filenames in go/host/host.go.
const backupTimeFormat = "20060102.150405"

// entryIndent is the fixed indentation (in spaces) of a build label key
// under oses.<os>.builds in config.yaml (oses: 0 -> OS name: 2 -> builds: 4
// -> label: 6 -> fields: 8).
const entryIndent = 6

var (
	buildsHeaderRe = regexp.MustCompile(`^    builds:\s*$`)
	// outerBoundaryRe matches the start of the next real (uncommented)
	// top-level OS key, e.g. "  ol10:". It deliberately excludes lines
	// starting with '#' at that column, since config.yaml uses that column
	// both for section-divider comments ("  # Rocky Linux") and, in places,
	// for commented-out build entries -- neither of which end the current
	// OS's builds block.
	outerBoundaryRe = regexp.MustCompile(`^  [^#\s]`)
	// realEntryRe matches a real (uncommented), 6-space-indented build label
	// key, e.g. "      9.7.1:". Commented-out entries (whether flush-left
	// "#      9.7.1:" or indented "  #    9.7.1:") never match.
	realEntryRe = regexp.MustCompile(`^      [^#\s].*:\s*$`)
)

// MergeBuild folds a validated build entry for (osName, label) into
// config.yaml, preserving everything else in the file byte-for-byte. It is
// used by `build-one -c <alt> -add-if-successful` once a full build of an
// alternate config's entry has succeeded.
//
// If (osName, label) already exists in config.yaml, MergeBuild never
// overwrites it: it returns SkippedIdentical if the existing entry matches
// build exactly, or SkippedDiffers if it doesn't. Before installing a merged
// file, the pre-merge config.yaml is preserved as config.yaml.<UTC
// timestamp> (now, formatted) so every auto-merge leaves a recoverable
// snapshot behind.
func MergeBuild(dir, osName, label string, build Build, now time.Time) (MergeStatus, error) {
	mainCfg, err := Load(dir, "")
	if err != nil {
		return 0, fmt.Errorf("loading %s: %w", DefaultConfigFile, err)
	}

	if existing, ok := mainCfg.Build(osName, label); ok {
		if reflect.DeepEqual(existing, build) {
			return SkippedIdentical, nil
		}
		return SkippedDiffers, nil
	}
	if _, ok := mainCfg.config.OSes[osName]; !ok {
		return 0, fmt.Errorf("OS %q has no section in %s; add it manually first", osName, DefaultConfigFile)
	}

	configPath := filepath.Join(dir, DefaultConfigFile)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", configPath, err)
	}

	merged, err := insertBuild(string(data), osName, label, build)
	if err != nil {
		return 0, err
	}

	tmpPath := configPath + ".merge-tmp"
	if err := os.WriteFile(tmpPath, []byte(merged), 0o644); err != nil {
		return 0, fmt.Errorf("writing %s: %w", tmpPath, err)
	}
	defer func() { _ = os.Remove(tmpPath) }()

	if err := validateMerge(dir, tmpPath, osName, label, build); err != nil {
		return 0, err
	}

	backupPath := configPath + "." + now.Format(backupTimeFormat)
	if _, err := os.Stat(backupPath); err == nil {
		return 0, fmt.Errorf("backup path %s already exists, refusing to overwrite it", backupPath)
	}
	if err := os.Rename(configPath, backupPath); err != nil {
		return 0, fmt.Errorf("backing up %s to %s: %w", configPath, backupPath, err)
	}
	if err := os.Rename(tmpPath, configPath); err != nil {
		return 0, fmt.Errorf("installing merged %s (previous version backed up at %s): %w", configPath, backupPath, err)
	}
	return Merged, nil
}

// validateMerge reloads the candidate merged config file and confirms
// (osName, label) resolves to exactly the build that was meant to be merged,
// so a bug in insertBuild's line-scan can never corrupt config.yaml.
func validateMerge(dir, candidatePath, osName, label string, want Build) error {
	c, err := Load(dir, filepath.Base(candidatePath))
	if err != nil {
		return fmt.Errorf("merge validation: candidate config failed to parse: %w", err)
	}
	got, ok := c.Build(osName, label)
	if !ok {
		return fmt.Errorf("merge validation: %s/%s not found in candidate config", osName, label)
	}
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("merge validation: %s/%s in candidate config does not match the intended build entry", osName, label)
	}
	if _, err := c.Resolve(osName, label); err != nil {
		return fmt.Errorf("merge validation: %s/%s does not resolve: %w", osName, label, err)
	}
	return nil
}

// insertBuild returns config.yaml's content with a new build entry for
// (osName, label) spliced in under oses.<osName>.builds, immediately after
// the last real (uncommented) build entry there -- or immediately after the
// "builds:" line if every entry under that OS is currently commented out.
// Everything else in the file is left completely untouched.
func insertBuild(content, osName, label string, build Build) (string, error) {
	lines := strings.Split(content, "\n")

	osLineRe := regexp.MustCompile(`^  ` + regexp.QuoteMeta(osName) + `:\s*$`)
	osIdx := -1
	for i, l := range lines {
		if osLineRe.MatchString(l) {
			osIdx = i
			break
		}
	}
	if osIdx < 0 {
		return "", fmt.Errorf("could not find %q section header in %s", osName, DefaultConfigFile)
	}
	if osIdx+1 >= len(lines) || !buildsHeaderRe.MatchString(lines[osIdx+1]) {
		return "", fmt.Errorf("expected \"builds:\" immediately after %q in %s", osName, DefaultConfigFile)
	}
	buildsIdx := osIdx + 1

	// outerEnd bounds the search for real entries: the next real (i.e. not
	// commented-out) top-level OS key, or EOF.
	outerEnd := len(lines)
	for i := buildsIdx + 1; i < len(lines); i++ {
		if outerBoundaryRe.MatchString(lines[i]) {
			outerEnd = i
			break
		}
	}

	// anchor is the last real, uncommented build entry key under this OS.
	anchor := -1
	for i := buildsIdx + 1; i < outerEnd; i++ {
		if realEntryRe.MatchString(lines[i]) {
			anchor = i
		}
	}

	insertAt := buildsIdx + 1
	if anchor >= 0 {
		// Insert right after the anchor entry's own fields: the first line
		// after it whose indentation drops below entryIndent (skipping
		// blank lines and any trailing comments, whatever their indent
		// style, that still belong to this OS's block).
		insertAt = outerEnd
		for i := anchor + 1; i < outerEnd; i++ {
			indent, blank := indentOf(lines[i])
			if !blank && indent < entryIndent {
				insertAt = i
				break
			}
		}
	}

	entryLines, err := formatBuildEntry(label, build)
	if err != nil {
		return "", err
	}

	result := make([]string, 0, len(lines)+len(entryLines))
	result = append(result, lines[:insertAt]...)
	result = append(result, entryLines...)
	result = append(result, lines[insertAt:]...)
	return strings.Join(result, "\n"), nil
}

// indentOf returns the number of leading spaces in line. blank is true for
// an empty or whitespace-only line, in which case indent is meaningless.
func indentOf(line string) (indent int, blank bool) {
	trimmed := strings.TrimLeft(line, " ")
	if trimmed == "" {
		return 0, true
	}
	return len(line) - len(trimmed), false
}

// formatBuildEntry renders a build entry as it should appear in config.yaml,
// indented to entryIndent (the "label:" column). Packages/tweaks are
// rendered in flow style ("[a, b, c]") to match the file's existing
// convention; yaml.Node encoding (rather than yaml.Marshal on the Build
// struct) is used so scalars still get the encoder's normal quoting rules.
func formatBuildEntry(label string, b Build) ([]string, error) {
	fields := &yaml.Node{Kind: yaml.MappingNode}
	fields.Content = append(fields.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: "srpm"},
		&yaml.Node{Kind: yaml.ScalarNode, Value: b.SRPM})
	if b.AutoInstallDependencies != nil {
		v := "false"
		if *b.AutoInstallDependencies {
			v = "true"
		}
		fields.Content = append(fields.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "auto_install_dependencies"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v})
	}
	if len(b.Packages) > 0 {
		fields.Content = append(fields.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "packages"},
			stringFlowSeq(b.Packages))
	}
	if len(b.Tweaks) > 0 {
		fields.Content = append(fields.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "tweaks"},
			stringFlowSeq(b.Tweaks))
	}

	root := &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: label},
			fields,
		},
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, fmt.Errorf("formatting new build entry: %w", err)
	}
	_ = enc.Close()

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	prefix := strings.Repeat(" ", entryIndent)
	for i, l := range lines {
		if l == "" {
			continue
		}
		lines[i] = prefix + l
	}
	return lines, nil
}

func stringFlowSeq(items []string) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
	for _, it := range items {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: it})
	}
	return seq
}
