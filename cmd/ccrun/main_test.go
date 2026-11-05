package main

import (
	"flag"
	"io"
	"slices"
	"testing"
)

func TestParseInterleaved(t *testing.T) {
	tests := []struct {
		args     []string
		wantArgs []string
		wantQuit bool
		wantPID  uint
	}{
		{[]string{"-p", "7", "hello", "-q"}, []string{"hello"}, true, 7},
		{[]string{"-q", "-p", "7", "hello"}, []string{"hello"}, true, 7},
		{[]string{"-p", "7", "hello world", "-q"}, []string{"hello world"}, true, 7},
		{[]string{"-p", "7", "say", "-q", "now"}, []string{"say", "now"}, true, 7},
		{[]string{"-p", "7", "use -q here"}, []string{"use -q here"}, false, 7},
		{[]string{"-p", "7", "--", "a", "-q"}, []string{"a", "-q"}, false, 7},
		{[]string{"-p", "7", "a", "--", "-q"}, []string{"a", "-q"}, false, 7},
		{[]string{"-p", "7", "-"}, []string{"-"}, false, 7},
		{[]string{"-p", "7", "-q"}, nil, true, 7},
	}
	for _, tt := range tests {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		quit := fs.Bool("q", false, "")
		pid := fs.Uint("p", 0, "")
		got, err := parseInterleaved(fs, tt.args)
		if err != nil {
			t.Errorf("%q: %v", tt.args, err)
			continue
		}
		if !slices.Equal(got, tt.wantArgs) || *quit != tt.wantQuit || *pid != tt.wantPID {
			t.Errorf("%q: got args %q quit %v pid %d, want %q %v %d", tt.args, got, *quit, *pid, tt.wantArgs, tt.wantQuit, tt.wantPID)
		}
	}
}

func TestParseInterleavedUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Bool("q", false, "")
	if _, err := parseInterleaved(fs, []string{"hello", "-x"}); err == nil {
		t.Error("an unknown flag after a prompt was accepted")
	}
}
