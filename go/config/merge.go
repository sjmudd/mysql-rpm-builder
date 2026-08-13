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

// PreflightMergeStatus reports, without writing anything, what a later
// MergeBuild call for (osName, label) would do: SkippedIdentical if
// config.yaml already has an identical entry, SkippedDiffers if it already
// has a different one, or Merged if there's no entry yet (i.e. a real merge
// would happen on a successful build). Intended for build-one
// -add-if-successful to warn upfront when the merge is already known to be a
// no-op, rather than only reporting that after a full (possibly hours-long)
// build.
func PreflightMergeStatus(dir, osName, label string, build Build) (MergeStatus, error) {
	mainCfg, err := Load(dir, "")
	if err != nil {
		return 0, fmt.Errorf("loading %s: %w", DefaultConfigFile, err)
	}
	status, _ := checkMergeStatus(mainCfg, osName, label, build)
	return status, nil
}

// checkMergeStatus reports whether (osName, label) already exists in
// mainCfg, and if so whether it matches build exactly. mergeable is true only
// when no entry exists yet, i.e. when MergeBuild should proceed to insert
// one; status is only meaningful when mergeable is false.
func checkMergeStatus(mainCfg *Config, osName, label string, build Build) (status MergeStatus, mergeable bool) {
	existing, ok := mainCfg.Build(osName, label)
	if !ok {
		return Merged, true
	}
	if reflect.DeepEqual(existing, build) {
		return SkippedIdentical, false
	}
	return SkippedDiffers, false
}

// MergeBuild folds a validated build entry for (osName, label) into
// config.yaml, preserving everything else in the file byte-for-byte. It is
// used by `build-one -c <alt> -add-if-successful` once a full build of an
// alternate config's entry has succeeded.
//
// If sourceConfigFile is non-empty, the entry is copied from that file's raw
// text (via entryLinesFor) rather than reconstructed from build alone, so
// any comments on it survive the merge. This matters more here than in a
// typical project: this repo's whole reason for existing is that rebuilding
// MySQL RPMs is full of undocumented, easy-to-forget gotchas (missing
// BuildRequires, annobin plugin naming mismatches, yum-builddep quirks --
// see docs/), and a build entry's comment is usually the *only* place that
// knowledge is recorded (e.g. config.yaml's ol8/8.4.7 entry, or
// ol8-8.4.10.yaml's draft of the same). Reconstructing the entry from the
// parsed Build struct via formatBuildEntry -- the fallback used when
// sourceConfigFile is empty, and the only thing possible once a file has
// been through config.Load, which never keeps comments -- would silently
// discard exactly that "why", right when a successful test build is the
// strongest signal yet that the workaround it documents is real.
//
// If (osName, label) already exists in config.yaml, MergeBuild never
// overwrites it: it returns SkippedIdentical if the existing entry matches
// build exactly, or SkippedDiffers if it doesn't. Before installing a merged
// file, the pre-merge config.yaml is preserved as config.yaml.<UTC
// timestamp> (now, formatted) so every auto-merge leaves a recoverable
// snapshot behind.
func MergeBuild(dir, osName, label string, build Build, sourceConfigFile string, now time.Time) (MergeStatus, error) {
	mainCfg, err := Load(dir, "")
	if err != nil {
		return 0, fmt.Errorf("loading %s: %w", DefaultConfigFile, err)
	}

	if status, mergeable := checkMergeStatus(mainCfg, osName, label, build); !mergeable {
		return status, nil
	}
	if _, ok := mainCfg.config.OSes[osName]; !ok {
		return 0, fmt.Errorf("OS %q has no section in %s; add it manually first", osName, DefaultConfigFile)
	}

	configPath := filepath.Join(dir, DefaultConfigFile)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", configPath, err)
	}

	var sourceLines []string
	if sourceConfigFile != "" {
		srcPath := filepath.Join(dir, sourceConfigFile)
		srcData, err := os.ReadFile(srcPath)
		if err != nil {
			return 0, fmt.Errorf("reading %s to preserve its comments: %w", srcPath, err)
		}
		sourceLines, err = entryLinesFor(string(srcData), osName, label)
		if err != nil {
			return 0, fmt.Errorf("extracting %s/%s from %s to preserve its comments: %w", osName, label, srcPath, err)
		}
	}

	merged, err := insertBuild(string(data), osName, label, build, sourceLines)
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
//
// If sourceLines is non-nil, it is used verbatim as the entry's text
// (comments and all -- see entryLinesFor/MergeBuild); otherwise the entry is
// rendered from build via formatBuildEntry. Either way, sourceLines/build
// are expected to agree: validateMerge (in MergeBuild) re-parses the result
// and confirms it actually resolves to build, so a source file using a
// different indent convention than config.yaml's (which would misplace the
// raw lines) is caught as a merge error rather than silently corrupting
// config.yaml.
func insertBuild(content, osName, label string, build Build, sourceLines []string) (string, error) {
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

	var entryLines []string
	if sourceLines != nil {
		entryLines = sourceLines
	} else {
		var err error
		entryLines, err = formatBuildEntry(label, build)
		if err != nil {
			return "", err
		}
	}

	result := make([]string, 0, len(lines)+len(entryLines))
	result = append(result, lines[:insertAt]...)
	result = append(result, entryLines...)
	result = append(result, lines[insertAt:]...)
	return strings.Join(result, "\n"), nil
}

// entryLinesFor extracts the raw lines of an existing build entry for
// (osName, label) out of content -- from its "label:" line (plus any
// contiguous, same-indent comment lines immediately preceding it) through
// the last line still indented >= entryIndent (comments included, wherever
// they fall within that span; the two real examples in this repo disagree
// on where -- config.yaml's ol8/8.4.7 entry has its comment between the
// label and srpm:, while ol8-8.4.10.yaml's draft of the same entry has it
// between srpm: and packages:). That's why this copies the whole span
// verbatim rather than picking one fixed slot to look for a comment in.
//
// Uses the same "first line with indent < entryIndent" boundary rule as
// insertBuild's anchor scan above, so a low-indent section-divider comment
// between entries (e.g. "  # Next section") correctly ends the entry rather
// than being swept into it.
func entryLinesFor(content, osName, label string) ([]string, error) {
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
		return nil, fmt.Errorf("could not find %q section header", osName)
	}
	if osIdx+1 >= len(lines) || !buildsHeaderRe.MatchString(lines[osIdx+1]) {
		return nil, fmt.Errorf("expected \"builds:\" immediately after %q", osName)
	}
	buildsIdx := osIdx + 1

	outerEnd := len(lines)
	for i := buildsIdx + 1; i < len(lines); i++ {
		if outerBoundaryRe.MatchString(lines[i]) {
			outerEnd = i
			break
		}
	}

	labelLineRe := regexp.MustCompile(`^      ` + regexp.QuoteMeta(label) + `:\s*$`)
	labelIdx := -1
	for i := buildsIdx + 1; i < outerEnd; i++ {
		if labelLineRe.MatchString(lines[i]) {
			labelIdx = i
			break
		}
	}
	if labelIdx < 0 {
		return nil, fmt.Errorf("could not find build entry %q under %q", label, osName)
	}

	// Walk backward over any comment lines immediately preceding the label,
	// at the label's own indent (6 spaces) -- a comment explaining this
	// specific entry, as opposed to e.g. a section divider at a shallower
	// indent, or another entry's trailing comment separated by a blank line.
	labelCommentRe := regexp.MustCompile(`^      #`)
	start := labelIdx
	for start > buildsIdx+1 && labelCommentRe.MatchString(lines[start-1]) {
		start--
	}

	end := outerEnd
	for i := labelIdx + 1; i < outerEnd; i++ {
		indent, blank := indentOf(lines[i])
		if !blank && indent < entryIndent {
			end = i
			break
		}
	}
	// The scan above only stops at a non-blank low-indent line, so a blank
	// line right before that (or before EOF/outerEnd) is still inside
	// [start:end); trim it so the extracted span is exactly the entry, with
	// no trailing blank line once spliced into the destination file.
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[start:end], nil
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
	if len(b.Patches) > 0 {
		fields.Content = append(fields.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "patches"},
			stringFlowSeq(b.Patches))
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
