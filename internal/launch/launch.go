// Package launch starts a Claude Code session in a console of its own.
//
// It calls CreateProcessW directly rather than going through os/exec, because
// os/exec always passes STARTF_USESTDHANDLES with handles of its own choosing
// (os.DevNull when Stdin/Stdout/Stderr are nil). Claude Code would then see a
// non-TTY stdout and refuse to start its interactive UI. Leaving the std
// handles unset is what lets the brand-new console supply them.
package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Options describe the session to start.
type Options struct {
	Exe    string // path to claude.exe
	Args   []string
	Dir    string   // working directory; must already exist
	Env    []string // child environment as "K=V"; nil inherits this process's
	Hidden bool     // create the console window hidden
}

// hostSessionVars are the environment variables a running Claude Code session
// stamps on the processes it spawns. They reach the new session whenever ccrun
// is run from a shell that a session owns -- a skill launching a session, or
// testing ccrun from one -- and a session that inherits them is not a session of
// its own: CLAUDE_CODE_CHILD_SESSION alone turns off its transcript, so it
// cannot be resumed, and the rest hand it the launching session's identity and
// IPC endpoints. Everything else in the environment -- PATH, credentials, the
// user's own CLAUDE_CODE_* settings -- is passed through untouched.
var hostSessionVars = []string{
	"CLAUDECODE",
	"CLAUDE_PID",
	"CLAUDE_EFFORT",
	"CLAUDE_CODE_ENTRYPOINT",
	"CLAUDE_CODE_SESSION_ID",
	"CLAUDE_CODE_BRIDGE_SESSION_ID",
	"CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_MESSAGING_SOCKET",
	"CLAUDE_CODE_MESSAGING_TOKEN",
	"CLAUDE_CODE_EXECPATH",
}

// SessionEnv returns this process's environment with the host session's markers
// removed, so the launched session starts as an independent one.
func SessionEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if ok && slices.ContainsFunc(hostSessionVars, func(v string) bool {
			return strings.EqualFold(v, name)
		}) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// envBlock encodes env as the doubly-NUL-terminated UTF-16 block CreateProcess
// expects for lpEnvironment.
func envBlock(env []string) (*uint16, error) {
	var block []uint16
	for _, kv := range env {
		if strings.ContainsRune(kv, 0) {
			return nil, fmt.Errorf("environment entry contains NUL: %q", kv)
		}
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			return nil, err
		}
		block = append(block, u...) // UTF16FromString already appends the NUL
	}
	block = append(block, 0)
	return &block[0], nil
}

// Start creates the process and returns its PID. The child keeps running after
// this process exits: it owns a separate console and is never waited on.
func Start(o Options) (uint32, error) {
	exe, err := filepath.Abs(o.Exe)
	if err != nil {
		return 0, err
	}
	exePtr, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	// ComposeCommandLine quotes each argument the way CommandLineToArgvW parses
	// it back, so prompts containing spaces, quotes or newlines survive intact.
	cmdLine, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{exe}, o.Args...)))
	if err != nil {
		return 0, err
	}
	dirPtr, err := windows.UTF16PtrFromString(o.Dir)
	if err != nil {
		return 0, err
	}

	var env *uint16
	if o.Env != nil {
		if env, err = envBlock(o.Env); err != nil {
			return 0, err
		}
	}

	var si windows.StartupInfo
	si.Cb = uint32(unsafe.Sizeof(si))
	// Sessions start minimized. A launcher that opens them in the background
	// would otherwise bury the user's screen under console windows, and
	// SW_SHOWMINNOACTIVE additionally keeps the new window from stealing focus
	// from whatever they were doing. Nothing else is affected: keystrokes go
	// into the console's input buffer rather than through its window, so a
	// minimized session takes prompts exactly like a visible one -- and it stays
	// on the taskbar, so a person can still open it and take over.
	si.Flags = windows.STARTF_USESHOWWINDOW
	si.ShowWindow = windows.SW_SHOWMINNOACTIVE
	var pi windows.ProcessInformation

	// CREATE_NEW_CONSOLE is the whole point: the session needs a console of its
	// own to be attachable later. CREATE_BREAKAWAY_FROM_JOB additionally frees
	// it from any job object the caller sits in -- ccrun is meant to be run from
	// inside another agent's tool call, and a job with JOB_OBJECT_LIMIT_KILL_ON_
	// JOB_CLOSE would otherwise take the session down when that call ends. Jobs
	// may forbid breakaway, so fall back to plain CREATE_NEW_CONSOLE.
	const base = windows.CREATE_NEW_CONSOLE | windows.CREATE_UNICODE_ENVIRONMENT
	err = windows.CreateProcess(exePtr, cmdLine, nil, nil, false, base|windows.CREATE_BREAKAWAY_FROM_JOB, env, dirPtr, &si, &pi)
	if err == windows.ERROR_ACCESS_DENIED {
		err = windows.CreateProcess(exePtr, cmdLine, nil, nil, false, base, env, dirPtr, &si, &pi)
	}
	if err != nil {
		return 0, fmt.Errorf("CreateProcess %s: %w", exe, err)
	}
	windows.CloseHandle(pi.Thread)
	windows.CloseHandle(pi.Process)
	return pi.ProcessId, nil
}

// EnsureDir creates dir (and any missing parents) and returns its absolute path.
func EnsureDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("create working directory: %w", err)
	}
	return abs, nil
}
