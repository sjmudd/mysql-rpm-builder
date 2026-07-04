// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

// Package version holds the embedded build version of mysql-rpm-builder.
//
// v2 marks the switch from the original shell-based builder to this Go
// implementation. Bump Version here when cutting a release.
package version

// Version is the semantic version of this binary, compiled in from source.
const Version = "v2.1.1"
