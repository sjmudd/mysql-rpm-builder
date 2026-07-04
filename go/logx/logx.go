// Copyright (c) 2026 Simon J Mudd <sjmudd@pobox.com>
// Use of this source code is governed by a BSD-2-Clause
// license that can be found in the LICENSE file.

// Package logx provides UTC-timestamped logging that mirrors the log() helpers
// from the original bash scripts and can optionally tee output to a logfile.
package logx

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

var (
	prog     = filepath.Base(os.Args[0])
	hostname = shortHostname()
	pid      = os.Getpid()
)

func shortHostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "UNKNOWNHOST"
	}
	return h
}

// timestamp returns the UTC time in the format used throughout the project.
func timestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05")
}

// prefix builds the common "<ts> <host> <prog>[<pid>]:" log prefix.
func prefix() string {
	return fmt.Sprintf("%s %s %s[%d]:", timestamp(), hostname, prog, pid)
}

// out is the destination for Log/Logf. It defaults to stdout but SetTee can
// redirect it to a stdout+file writer (mirroring `| tee -a logfile`).
var out io.Writer = os.Stdout

// SetTee directs all subsequent log output to both stdout and the named file
// (appending). It returns a closer for the file, or an error if it cannot be
// opened. Passing an empty path resets output to stdout only.
func SetTee(path string) (io.Closer, error) {
	if path == "" {
		out = os.Stdout
		return io.NopCloser(nil), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	out = io.MultiWriter(os.Stdout, f)
	return f, nil
}

// Writer returns the current log destination, so subprocess stdout/stderr can
// be teed to the same place as log lines.
func Writer() io.Writer { return out }

// Log writes a single timestamped line.
func Log(args ...any) {
	_, _ = fmt.Fprintf(out, "%s %s\n", prefix(), fmt.Sprint(args...))
}

// Logf writes a single timestamped, formatted line.
func Logf(format string, args ...any) {
	_, _ = fmt.Fprintf(out, "%s %s\n", prefix(), fmt.Sprintf(format, args...))
}

// Fatalf writes an ERROR line to stderr and exits with the given code.
func Fatalf(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s ERROR: %s\n", prefix(), fmt.Sprintf(format, args...))
	os.Exit(code)
}
