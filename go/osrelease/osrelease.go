// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

// Package osrelease parses /etc/os-release to determine the distribution id and
// major version, mirroring get_os_and_version() from the original build script.
package osrelease

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Info holds the fields we care about from /etc/os-release.
type Info struct {
	ID        string // e.g. "ol", "rocky", "almalinux", "centos", "rhel"
	Name      string // pretty name
	VersionID string // e.g. "10", "9.4"
	Major     int    // single-digit major version derived from VersionID
}

// supported lists the distribution IDs we know how to build on.
var supported = map[string]bool{
	"almalinux": true,
	"ol":        true,
	"rocky":     true,
	"centos":    true,
	"rhel":      true,
}

// OSLabel returns the "<id><major>" label used to key configuration, e.g. "ol10".
func (i Info) OSLabel() string { return fmt.Sprintf("%s%d", i.ID, i.Major) }

// Detect reads /etc/os-release and returns the parsed Info.
func Detect() (Info, error) { return parseFile("/etc/os-release") }

func parseFile(path string) (Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return Info{}, fmt.Errorf("cannot read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	fields := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	if err := sc.Err(); err != nil {
		return Info{}, err
	}

	info := Info{
		ID:        fields["ID"],
		Name:      fields["NAME"],
		VersionID: fields["VERSION_ID"],
	}
	if !supported[info.ID] {
		return info, fmt.Errorf("unrecognised OS: %s (ID=%s VERSION_ID=%s)", info.Name, info.ID, info.VersionID)
	}
	// Convert VERSION_ID to a single-digit major (strip any ".minor").
	majorStr, _, _ := strings.Cut(info.VersionID, ".")
	major, err := strconv.Atoi(majorStr)
	if err != nil {
		return info, fmt.Errorf("cannot parse major version from VERSION_ID %q: %w", info.VersionID, err)
	}
	info.Major = major
	return info, nil
}
