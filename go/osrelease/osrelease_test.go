// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

package osrelease

import (
	"os"
	"path/filepath"
	"testing"
)

// writeOSRelease writes content to a temp os-release file and returns its path.
func writeOSRelease(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing test os-release: %v", err)
	}
	return path
}

func TestParseFile(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		wantID    string
		wantMajor int
		wantLabel string
		wantErr   bool
	}{
		{
			name: "oracle linux 10",
			content: `NAME="Oracle Linux Server"
ID="ol"
VERSION_ID="10"`,
			wantID:    "ol",
			wantMajor: 10,
			wantLabel: "ol10",
		},
		{
			name: "rocky with minor version is truncated to major",
			content: `NAME="Rocky Linux"
ID="rocky"
VERSION_ID="9.4"`,
			wantID:    "rocky",
			wantMajor: 9,
			wantLabel: "rocky9",
		},
		{
			name: "comments blank lines and unquoted values",
			content: `# this is a comment

ID=almalinux
VERSION_ID=9`,
			wantID:    "almalinux",
			wantMajor: 9,
			wantLabel: "almalinux9",
		},
		{
			name: "unsupported id is an error",
			content: `ID="ubuntu"
VERSION_ID="24.04"`,
			wantErr: true,
		},
		{
			name: "non-numeric version id is an error",
			content: `ID="rocky"
VERSION_ID="rawhide"`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := parseFile(writeOSRelease(t, tc.content))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got none (info=%+v)", info)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.ID != tc.wantID {
				t.Errorf("ID = %q, want %q", info.ID, tc.wantID)
			}
			if info.Major != tc.wantMajor {
				t.Errorf("Major = %d, want %d", info.Major, tc.wantMajor)
			}
			if got := info.OSLabel(); got != tc.wantLabel {
				t.Errorf("OSLabel() = %q, want %q", got, tc.wantLabel)
			}
		})
	}
}

func TestParseFileMissing(t *testing.T) {
	if _, err := parseFile(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected an error for a missing file, got none")
	}
}
