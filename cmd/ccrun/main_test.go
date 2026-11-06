package main

import (
	"flag"
	"io"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// testFlags is a flag set shaped like ccrun's: a bool, a number and a string
// that takes a value.
func testFlags() (fs *flag.FlagSet, quit *bool, pid *uint, name *string) {
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	quit = fs.Bool("q", false, "")
	pid = fs.Uint("p", 0, "")
	name = fs.String("n", "", "")
	return fs, quit, pid, name
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		args         []string
		wantArgs     []string
		wantClaude   []string
		wantDashdash bool
		wantQuit     bool
		wantPID      uint
		wantName     string
	}{
		// Flags and the prompt, interleaved, with no "--".
		{args: []string{"-p", "7", "hello", "-q"}, wantArgs: []string{"hello"}, wantQuit: true, wantPID: 7},
		{args: []string{"-q", "-p", "7", "hello"}, wantArgs: []string{"hello"}, wantQuit: true, wantPID: 7},
		{args: []string{"-p", "7", "hello world", "-q"}, wantArgs: []string{"hello world"}, wantQuit: true, wantPID: 7},
		{args: []string{"-p", "7", "say", "-q", "now"}, wantArgs: []string{"say", "now"}, wantQuit: true, wantPID: 7},
		{args: []string{"-p", "7", "use -q here"}, wantArgs: []string{"use -q here"}, wantPID: 7},
		{args: []string{"-p", "7", "-"}, wantArgs: []string{"-"}, wantPID: 7},
		{args: []string{"-p", "7", "-q"}, wantQuit: true, wantPID: 7},

		// The split: before "--" is ccrun's, after it is claude's.
		{
			args:     []string{"-n", "bot", "it is 8:30", "--", "--dangerously-load-development-channels", "server:webhook"},
			wantArgs: []string{"it is 8:30"}, wantName: "bot",
			wantClaude: []string{"--dangerously-load-development-channels", "server:webhook"}, wantDashdash: true,
		},
		{
			args:       []string{"-n", "bot", "--", "--model", "opus", "--continue"},
			wantName:   "bot",
			wantClaude: []string{"--model", "opus", "--continue"}, wantDashdash: true,
		},
		// Flags and prompt words interleaved before "--" are all ccrun's.
		{
			args:     []string{"say", "-q", "now", "-n", "bot", "--", "-x"},
			wantArgs: []string{"say", "now"}, wantQuit: true, wantName: "bot",
			wantClaude: []string{"-x"}, wantDashdash: true,
		},
		// After "--", ccrun's own flags are not parsed and words are not prompt.
		{
			args:       []string{"-p", "7", "--", "-q", "-p", "8", "-n", "x", "hello"},
			wantPID:    7,
			wantClaude: []string{"-q", "-p", "8", "-n", "x", "hello"}, wantDashdash: true,
		},
		// Only the first "--" splits; a later one is claude's.
		{
			args:       []string{"--", "a", "--", "b"},
			wantClaude: []string{"a", "--", "b"}, wantDashdash: true,
		},
		// An empty "--", at the end or alone.
		{
			args:     []string{"-p", "7", "hello", "--"},
			wantArgs: []string{"hello"}, wantPID: 7,
			wantClaude: []string{}, wantDashdash: true,
		},
		{args: []string{"--"}, wantClaude: []string{}, wantDashdash: true},
		// Arguments with spaces, quotes, backslashes or nothing in them stay
		// one argument each, unchanged.
		{
			args:         []string{"hi there", "--", "--append-system-prompt", `say "hi", then stop`, "", `C:\dir with space\`, `a\"b`},
			wantArgs:     []string{"hi there"},
			wantClaude:   []string{"--append-system-prompt", `say "hi", then stop`, "", `C:\dir with space\`, `a\"b`},
			wantDashdash: true,
		},
	}
	for _, tt := range tests {
		fs, quit, pid, name := testFlags()
		args, claude, dashdash, err := parseArgs(fs, tt.args)
		if err != nil {
			t.Errorf("%q: %v", tt.args, err)
			continue
		}
		if !slices.Equal(args, tt.wantArgs) || *quit != tt.wantQuit || *pid != tt.wantPID || *name != tt.wantName {
			t.Errorf("%q: got args %q quit %v pid %d name %q, want %q %v %d %q", tt.args, args, *quit, *pid, *name, tt.wantArgs, tt.wantQuit, tt.wantPID, tt.wantName)
		}
		if !slices.Equal(claude, tt.wantClaude) || dashdash != tt.wantDashdash {
			t.Errorf("%q: got claude %q dashdash %v, want %q %v", tt.args, claude, dashdash, tt.wantClaude, tt.wantDashdash)
		}
		if tt.wantDashdash && claude == nil {
			t.Errorf("%q: claude is nil though there was a --", tt.args)
		}
	}
}

func TestParseArgsErrors(t *testing.T) {
	for _, args := range [][]string{
		{"hello", "-x"},       // an unknown flag after a prompt
		{"-x", "--", "-q"},    // an unknown flag before "--"
		{"-n", "--", "hello"}, // "--" is never a flag's value
	} {
		fs, _, _, _ := testFlags()
		if _, _, _, err := parseArgs(fs, args); err == nil {
			t.Errorf("%q was accepted", args)
		}
	}
	// What is unknown to ccrun after "--" is claude's business.
	fs, _, _, _ := testFlags()
	if _, _, _, err := parseArgs(fs, []string{"hello", "--", "-x"}); err != nil {
		t.Errorf("an unknown flag after -- was refused: %v", err)
	}
}

// TestLaunchArgs checks that what follows "--" reaches claude in order, after
// ccrun's own arguments, and that each argument survives the trip through the
// command line that launch.Start builds.
func TestLaunchArgs(t *testing.T) {
	claude := []string{"--dangerously-load-development-channels", "server:webhook", "--append-system-prompt", `say "hi", then stop`, "", `C:\dir with space\`, `a\"b`}
	tests := []struct {
		prompt string
		want   []string
	}{
		{"it is 8:30", append([]string{"--remote-control", "bot", "--name", "bot", "it is 8:30"}, claude...)},
		{"", append([]string{"--remote-control", "bot", "--name", "bot"}, claude...)},
	}
	for _, tt := range tests {
		got := launchArgs("bot", tt.prompt, claude)
		if !slices.Equal(got, tt.want) {
			t.Errorf("prompt %q: got %q, want %q", tt.prompt, got, tt.want)
			continue
		}
		exe := `C:\Program Files\claude\claude.exe`
		back, err := windows.DecomposeCommandLine(windows.ComposeCommandLine(append([]string{exe}, got...)))
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(back, append([]string{exe}, got...)) {
			t.Errorf("prompt %q: the command line read back as %q", tt.prompt, back)
		}
	}
	if got := launchArgs("bot", "", nil); !slices.Equal(got, []string{"--remote-control", "bot", "--name", "bot"}) {
		t.Errorf("without --: got %q", got)
	}
}

func TestCcrunsOwn(t *testing.T) {
	tests := []struct {
		claude []string
		want   string
	}{
		{nil, ""},
		{[]string{"--model", "opus", "--continue"}, ""},
		{[]string{"--remote-control-session-name-prefix", "box"}, ""},
		{[]string{"--append-system-prompt", "use --name here"}, ""},
		{[]string{"--model", "opus", "--name", "other"}, "--name"},
		{[]string{"--name=other"}, "--name=other"},
		{[]string{"-n", "other"}, "-n"},
		{[]string{"-nother"}, "-nother"},
		{[]string{"--remote-control"}, "--remote-control"},
		{[]string{"--remote-control=other"}, "--remote-control=other"},
	}
	for _, tt := range tests {
		if got := ccrunsOwn(tt.claude); got != tt.want {
			t.Errorf("%q: got %q, want %q", tt.claude, got, tt.want)
		}
	}
}

// TestRunRefusesDashdash covers the forms that start no session: a "--" there
// is refused, even an empty one, before anything is looked up or typed.
func TestRunRefusesDashdash(t *testing.T) {
	tests := []options{
		{pid: 7, args: []string{"hello"}, claude: []string{"--model", "opus"}, dashdash: true},
		{name: "bot", args: []string{"hello"}, claude: []string{}, dashdash: true},
		{name: "bot", claude: []string{"hello"}, dashdash: true},
		{name: "bot", screen: true, claude: []string{}, dashdash: true},
		{name: "bot", quit: true, claude: []string{"--model", "opus"}, dashdash: true},
		{list: true, args: []string{"bot"}, claude: []string{}, dashdash: true},
		{move: true, args: []string{"a", "b"}, claude: []string{}, dashdash: true},
		{claude: []string{"--model", "opus"}, dashdash: true},
	}
	for _, o := range tests {
		err := run(o)
		if err == nil || !strings.Contains(err.Error(), "only -d") {
			t.Errorf("%+v: got %v, want the -- refusal", o, err)
		}
	}
}

// TestRunRefusesCcrunsOwn checks that -d refuses an argument for claude that
// ccrun gives itself, before it looks for a session or starts one.
func TestRunRefusesCcrunsOwn(t *testing.T) {
	o := options{dir: "nonexistent", args: []string{"hello"}, claude: []string{"--model", "opus", "--name", "other"}, dashdash: true}
	err := run(o)
	if err == nil || !strings.Contains(err.Error(), "--name after --") {
		t.Errorf("got %v, want the --name refusal", err)
	}
}
