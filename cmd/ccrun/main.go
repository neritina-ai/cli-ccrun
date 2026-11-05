// Command ccrun starts a Claude Code session and drives it by PID: inject a
// prompt, see what it is showing, or end it. It exists so that a third-party
// tool -- an OpenClaw automation, a skill -- can open a session in a chosen
// directory with Remote Control enabled, hand the operator its PID, and later
// feed it prompts or shut it down, without anyone sitting at that terminal. A
// session whose PID was not kept, or that ccrun did not start, is found by
// keyword; and a session can be driven by the name it was started with instead
// of its PID, which is what survives a claude --resume that brings it back
// under a new PID.
//
// Forms:
//
//	ccrun -d <working-dir> [-n <name>] [prompt] [-q]     start a session, or reuse it
//	ccrun -n <name> <prompt> [-q]                        inject a prompt
//	ccrun -n <name> - [-q]                               inject a prompt from stdin
//	ccrun -n <name> -s                                   print what the session shows
//	ccrun -n <name> -q                                   end the session after its turn
//	ccrun -p <pid> <prompt> | - | -s | -q                the same, by PID
//	ccrun -l [keyword ...]                               find sessions and their PIDs
//	ccrun -m <old-dir> <new-dir>                         carry a moved directory's data over
//
// Two internal forms, spawned by the public ones:
//
//	ccrun __inject <pid> <ready|prompt|quit|screen>
//	ccrun __quit <pid> <since-unix-ms>                   end the session after its turn
//
// The session is launched with CREATE_NEW_CONSOLE so it owns a console, and
// every later command reaches it the way neritina-ai/self-compact reaches its
// own session: AttachConsole to that console and WriteConsoleInputW straight
// into its input buffer. Nothing depends on window focus, or on whether the
// window is minimized. What does stop a session is a selection left in its
// console window: conhost holds the session's output while there is one, and
// the session freezes at its next write. Injecting ends such a selection first
// (coninject.Thaw), and -s warns about one.
//
// Console work runs in a short-lived child process (__inject) because
// AttachConsole requires first calling FreeConsole, which would tear down this
// process's own stdout/stderr and swallow everything it still has to report.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/neritina-ai/cli-ccrun/internal/coninject"
	"github.com/neritina-ai/cli-ccrun/internal/launch"
	"github.com/neritina-ai/cli-ccrun/internal/projects"
	"github.com/neritina-ai/cli-ccrun/internal/sessions"
)

const (
	// injectCmd is the internal subcommand that does the console work.
	injectCmd = "__inject"

	// quitCmd is the internal subcommand that waits for a session's turn to
	// end and then ends the session.
	quitCmd = "__quit"

	// idleSettle is how long a session has to stay idle before a waiting -q
	// takes that for the end of its turn rather than a gap between two.
	idleSettle = 3 * time.Second

	// exitError is the exit code for any failure; success is always 0.
	exitError = 1

	// enterDelay is the pause between typing text and pressing Enter, long
	// enough for Claude Code's input box to have registered the text first.
	enterDelay = 150 * time.Millisecond

	// newlineDelay surrounds the backslash-continuation that inserts a newline
	// inside one prompt, so the editor applies it before the next line arrives.
	newlineDelay = 60 * time.Millisecond

	// settleDelay is how long a freshly launched session is given before its
	// liveness is checked. Long enough to catch an immediate crash (bad flags,
	// unreadable working directory), short enough not to stall the caller.
	settleDelay = 1500 * time.Millisecond

	// readyGrace is how long a new session is given to reach its input box,
	// including answering the workspace-trust prompt on the way. It covers a
	// cold start: plugin sync, MCP servers and CLAUDE.md discovery all happen
	// before the box appears.
	readyGrace = 30 * time.Second

	// nameGrace is how long a ready session is given to be listed under the
	// name it was launched with, which takes a second or two.
	nameGrace = 10 * time.Second

	// quitGrace is how long "-q" waits for /exit to take effect. /exit acts the
	// moment it is typed, even mid-turn, which is why -q only sends it once the
	// turn is over; this covers a session slow to shut down.
	quitGrace = 10 * time.Second

	// trustMarker identifies the workspace-trust prompt in the session's visible
	// console text, lowercased.
	trustMarker = "trust this folder"

	// maxPromptRunes bounds a prompt in either direction: on launch it travels
	// on claude.exe's command line (Windows caps that at 32767 characters), and
	// on injection it is typed key by key into a console input buffer. Long
	// content belongs in a file that the prompt points at.
	maxPromptRunes = 8000
)

const usage = `ccrun - start and drive Claude Code sessions by PID

usage:
  ccrun -d <working-dir> [-n <name>] [prompt] [-q]     start a session, or prompt the one
                                                       already running; prints its PID
  ccrun -n <name> <prompt> [-q]                        inject a prompt into that session
  ccrun -n <name> - [-q]                               inject a prompt read from stdin
  ccrun -n <name> -s                                   print what that session is showing
  ccrun -n <name> -q                                   end that session once its turn is done
  ccrun -p <pid> <prompt> | - | -s | -q                the same, picking the session by PID
  ccrun -l [keyword ...]                               list running sessions and their PIDs
  ccrun -m <old-dir> <new-dir>                         after a working directory was renamed or
                                                       moved, move what Claude Code keeps for it

options:
  -d <dir>     working directory for the new session; created if missing
  -n <name>    the session's name. With -d it names the new session (default:
               the working directory's name); if a session with this name is
               already running in that directory, the prompt goes to it and no
               new one starts, and if one is running in another directory,
               nothing starts. Without -d it picks the running session with
               this name: the whole name, ignoring case. The name stays with the
               session through claude --resume; the PID does not.
  -p <pid>     act on the running session with this PID
  -s           with -p or -n: print the session's visible console text on stdout
  -q           end the session at the end of its turn, never in the middle of
               one: an idle session ends now; a busy one -- background commands
               included -- ends once it is done, while ccrun returns at once.
               With a prompt, the session ends once that prompt's turn is
               done; with -d, a prompt is required
  --           everything after it is prompt text; flags may otherwise come
               before or after the prompt
  -l           list running sessions, one per line with the PID first; with
               keywords, only those whose name, directory or session ID
               contains every keyword (case-insensitive)
  -m           move the transcripts and auto memory Claude Code keeps for
               <old-dir> to where it looks for them in <new-dir>. Move the
               directory first: <new-dir> must exist and <old-dir> must not.
               Nothing is overwritten, and no session may be working in either.

-d prints the session's PID on stdout, whether it started the session or found
it running. Everything exits 0 on success and 1 on failure; -l fails when no
session matches.`

func main() {
	// The internal subcommand is dispatched before flag parsing so its
	// arguments are never mistaken for ccrun's own.
	if len(os.Args) > 1 && (os.Args[1] == injectCmd || os.Args[1] == quitCmd) {
		sub := runInject
		if os.Args[1] == quitCmd {
			sub = runQuitWatch
		}
		if err := sub(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitError)
		}
		return
	}

	fs := flag.NewFlagSet("ccrun", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, usage) }
	var (
		dir    = fs.String("d", "", "working directory for the new session")
		pid    = fs.Uint("p", 0, "act on the session with this PID")
		name   = fs.String("n", "", "the session's name: names a new one with -d, picks a running one otherwise")
		screen = fs.Bool("s", false, "with -p or -n: print the session's visible console text")
		quit   = fs.Bool("q", false, "with -p or -n: end the session")
		list   = fs.Bool("l", false, "list running sessions matching every keyword")
		move   = fs.Bool("m", false, "move what Claude Code keeps for a working directory that moved")
	)
	args, err := parseInterleaved(fs, os.Args[1:])
	if err != nil {
		os.Exit(exitError)
	}

	err = run(options{
		dir:    *dir,
		pid:    uint32(*pid),
		name:   *name,
		screen: *screen,
		quit:   *quit,
		list:   *list,
		move:   *move,
		args:   args,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ccrun:", err)
		os.Exit(exitError)
	}
}

// parseInterleaved parses flags wherever they stand among the arguments, so
// that "ccrun -p <pid> <prompt> -q" means what it reads as; the flag package
// alone stops at the first argument that is not a flag. A prompt is still one
// argument however many words it has, so a dash inside it is never mistaken
// for a flag; only an argument that is a flag on its own is. Everything after
// "--" is taken as it is.
func parseInterleaved(fs *flag.FlagSet, all []string) ([]string, error) {
	var rest []string
	args := all
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return rest, nil
		}
		// The flag package consumes a "--" and reports everything after it as
		// arguments. args is always a tail of all, so whether it did is told by
		// the argument just before that tail.
		if i := len(all) - len(args); i > 0 && all[i-1] == "--" {
			return append(rest, args...), nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}

type options struct {
	dir    string
	pid    uint32
	name   string
	screen bool
	quit   bool
	list   bool
	move   bool
	args   []string
}

// run dispatches to one of the public forms.
func run(o options) error {
	switch {
	case o.move:
		if o.pid != 0 || o.name != "" || o.dir != "" || o.screen || o.quit || o.list || len(o.args) != 2 {
			return fmt.Errorf("-m takes the directory's old path and its new one, nothing else\n%s", usage)
		}
		return moveData(o.args[0], o.args[1])

	case o.list:
		if o.pid != 0 || o.name != "" || o.dir != "" || o.screen || o.quit {
			return fmt.Errorf("-l takes only keywords\n%s", usage)
		}
		return listSessions(o.args)

	case o.dir != "":
		if o.pid != 0 || o.screen {
			return fmt.Errorf("-d starts a new session; -p and -s act on a running one\n%s", usage)
		}
		prompt, err := readPrompt(o.args)
		if err != nil {
			return err
		}
		if o.quit && prompt == "" {
			return fmt.Errorf("-d with -q needs a prompt: the session ends once that prompt's turn is done\n%s", usage)
		}
		return startSession(o, prompt)

	case o.pid != 0 && o.name != "":
		return fmt.Errorf("-p and -n both pick a running session; use one\n%s", usage)

	case o.pid != 0 || o.name != "":
		if o.quit && o.screen {
			return fmt.Errorf("-s and -q are different modes; use one\n%s", usage)
		}
		var prompt string
		switch {
		case o.screen:
			if len(o.args) > 0 {
				return fmt.Errorf("-s takes no prompt\n%s", usage)
			}
		case o.quit:
			var err error
			if prompt, err = readPrompt(o.args); err != nil {
				return err
			}
		default:
			var err error
			if prompt, err = readPrompt(o.args); err != nil {
				return err
			}
			if prompt == "" {
				return fmt.Errorf("nothing to inject: give a prompt, \"-\" to read one from stdin, -s to see the screen, or -q to end the session\n%s", usage)
			}
		}

		pid := o.pid
		if o.name != "" {
			var err error
			if pid, err = pidByName(o.name); err != nil {
				return err
			}
		}
		switch {
		case o.screen:
			return showScreen(pid)
		case o.quit && prompt == "":
			return quitAtEndOfTurn(pid)
		case o.quit:
			return promptThenQuit(pid, prompt)
		}
		return injectPrompt(pid, prompt)

	case o.quit || o.screen:
		return fmt.Errorf("-s and -q need -p <pid> or -n <name>\n%s", usage)

	default:
		return errors.New(usage)
	}
}

// readPrompt turns the trailing arguments into the prompt text: a lone "-"
// means read it from stdin, several words are joined with spaces (so a caller
// that forgot to quote still gets what it meant).
func readPrompt(args []string) (string, error) {
	var text string
	if len(args) == 1 && args[0] == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read prompt from stdin: %w", err)
		}
		text = string(data)
	} else {
		text = strings.Join(args, " ")
	}
	text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"))
	if n := len([]rune(text)); n > maxPromptRunes {
		return "", fmt.Errorf("prompt is %d characters, over the %d limit; put it in a file and point the prompt at it", n, maxPromptRunes)
	}
	return text, nil
}

// startSession creates the working directory, launches Claude Code there with
// Remote Control enabled, and prints the PID that every later command takes --
// unless the session it would launch is already running.
func startSession(o options, prompt string) error {
	abs, err := filepath.Abs(o.dir)
	if err != nil {
		return err
	}
	name := o.name
	if name == "" {
		name = defaultName(abs)
	}
	// The name is how -n finds this session later, which only works while no
	// other running session has it; Claude Code itself lets names repeat. A
	// session that already has the name and works in this directory is the one
	// this would start, so it gets the prompt instead: a schedule can run the
	// same launch every time and keep one session going.
	taken, err := sessionsNamed(name)
	if err != nil {
		return err
	}
	switch {
	case len(taken) == 1 && sameDir(taken[0].Dir, abs):
		return reuseSession(taken[0], prompt, o.quit)
	case len(taken) == 1:
		return fmt.Errorf("session %d is already named %q and works in %s, not %s; end that one (ccrun -n %q -q) or give this one another name with -n", taken[0].PID, name, taken[0].Dir, abs, name)
	case len(taken) > 1:
		return fmt.Errorf("%d running sessions are named %q (pids %s), so -n cannot pick out one; end all but one or give this one another name with -n", len(taken), name, pidList(taken))
	}

	dir, err := launch.EnsureDir(abs)
	if err != nil {
		return err
	}
	exe, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found on PATH: %w", err)
	}

	// --name is the session's own name: its prompt box, terminal title and
	// /resume picker show it, its session record carries it for -n to find, and
	// claude --resume keeps it. --remote-control turns Remote Control on, and
	// gets the same name so that the Remote Control list shows the same thing;
	// it can only be read back on claude.ai, so it could never be what -n goes
	// by.
	args := []string{"--remote-control", name, "--name", name}
	if prompt != "" {
		args = append(args, prompt)
	}

	pid, err := launch.Start(launch.Options{
		Exe:  exe,
		Args: args,
		Dir:  dir,
		Env:  launch.SessionEnv(),
	})
	if err != nil {
		return err
	}

	// A session that dies during startup would otherwise hand the caller a PID
	// that is already meaningless.
	time.Sleep(settleDelay)
	if !coninject.Alive(pid) {
		return fmt.Errorf("claude exited immediately after starting in %s (pid %d)", dir, pid)
	}
	_, readyErr := helper(pid, "ready", "")
	// The prompt's turn is the first status change after the input box shows
	// up; the session files itself as idle before that, while it starts.
	if o.quit {
		if err := watchQuit(pid, time.Now()); err != nil {
			return err
		}
	}
	if readyErr != nil {
		fmt.Fprintf(os.Stderr, "warning: session %d is not ready for prompts: %v\n", pid, readyErr)
	} else if err := waitNamed(pid, name); err != nil {
		fmt.Fprintf(os.Stderr, "warning: -n %s may not find session %d yet: %v\n", name, pid, err)
	}

	// The PID is what this form is for, so stdout carries it and nothing else;
	// everything a person reads goes to stderr.
	fmt.Println(pid)
	fmt.Fprintf(os.Stderr, "started claude in %s as %q (pid %d)\n", dir, name, pid)
	if o.quit {
		fmt.Fprintf(os.Stderr, "session %d will end once this prompt's turn is done\n", pid)
	}
	return nil
}

// reuseSession stands in for a launch whose session is already running: the
// prompt, if any, is injected instead of opening a second session, and the PID
// is printed just as a launch prints it.
func reuseSession(s sessions.Session, prompt string, quit bool) error {
	switch {
	case quit:
		if err := promptThenQuit(s.PID, prompt); err != nil {
			return err
		}
	case prompt != "":
		if err := injectPrompt(s.PID, prompt); err != nil {
			return err
		}
	}
	fmt.Println(s.PID)
	fmt.Fprintf(os.Stderr, "claude is already running in %s as %q (pid %d); no new session started\n", s.Dir, s.Name, s.PID)
	return nil
}

// sameDir reports whether two absolute paths name the same directory, the way
// Windows compares them: ignoring case and a trailing separator.
func sameDir(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// within reports whether dir is root or somewhere inside it, ignoring case.
func within(dir, root string) bool {
	dir, root = strings.ToLower(filepath.Clean(dir)), strings.ToLower(filepath.Clean(root))
	return dir == root || strings.HasPrefix(dir, strings.TrimRight(root, `\/`)+string(filepath.Separator))
}

// moveData carries what Claude Code keeps for a working directory -- its
// transcripts and auto memory -- over to the directory's new path, so that a
// session started there picks up where the old ones left off. The directory
// has to have moved already: the data follows it, never the other way round.
func moveData(from, to string) error {
	from, err := filepath.Abs(from)
	if err != nil {
		return err
	}
	if to, err = filepath.Abs(to); err != nil {
		return err
	}
	if info, err := os.Stat(to); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a directory; move the directory first, then its data", to)
	}
	if _, err := os.Stat(from); err == nil && !sameDir(from, to) {
		return fmt.Errorf("%s still exists; -m is for after the directory has moved", from)
	}
	// A running session would go on writing where the data was, or already
	// writes where it is going.
	all, err := sessions.Running()
	if err != nil {
		return err
	}
	for _, s := range all {
		if within(s.Dir, from) || within(s.Dir, to) {
			return fmt.Errorf("session %d is working in %s; end it first (ccrun -p %d -q)", s.PID, s.Dir, s.PID)
		}
	}
	config, err := sessions.ConfigDir()
	if err != nil {
		return err
	}
	dst, err := projects.Move(config, from, to)
	if err != nil {
		return err
	}
	if dst == "" {
		fmt.Fprintf(os.Stderr, "claude keeps nothing for %s; nothing to move\n", from)
	} else {
		fmt.Fprintf(os.Stderr, "moved what claude keeps for %s to %s\n", from, dst)
	}
	return nil
}

// waitNamed blocks until the session is listed under name, so that -n finds it
// as soon as its launch returns. Claude Code files a new session under a
// placeholder name and applies the one it was given a second or two after the
// input box appears, which is where a launch would otherwise return.
func waitNamed(pid uint32, name string) error {
	deadline := time.Now().Add(nameGrace)
	for {
		all, err := sessions.Running()
		if err == nil {
			for _, s := range all {
				if s.PID == pid && strings.EqualFold(s.Name, name) {
					return nil
				}
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return fmt.Errorf("not listed under that name after %s", nameGrace)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// defaultName names a session after its working directory when -n is omitted.
// A session is never left unnamed: Claude Code would make up a name that
// changes on every claude --resume, and --remote-control's argument is
// optional on claude's side, so a bare flag would swallow the prompt that
// follows it as its value.
func defaultName(dir string) string {
	name := strings.TrimRight(filepath.ToSlash(dir), "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSuffix(name, ":")
	if name == "" {
		return "ccrun"
	}
	return name
}

// injectPrompt types prompt into the session's input box and submits it.
func injectPrompt(pid uint32, prompt string) error {
	if err := checkSession(pid); err != nil {
		return err
	}
	if _, err := helper(pid, "prompt", prompt); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "injected %d characters into pid %d\n", len([]rune(prompt)), pid)
	return nil
}

// showScreen prints what the session's console is displaying right now: its
// last messages, whatever is half-typed in its input box, or a prompt it is
// waiting on. It is the one way to see a session that is driven by PID rather
// than watched by a person.
func showScreen(pid uint32) error {
	if err := checkSession(pid); err != nil {
		return err
	}
	text, err := helper(pid, "screen", "")
	if err != nil {
		return err
	}
	fmt.Println(strings.TrimRight(text, "\n"))
	return nil
}

// quitSession ends the session, and makes sure it ends. /exit is the polite way
// and is what a session almost always takes; a session that has not gone after
// quitGrace is terminated instead. Nobody is asked to confirm that: "end this
// session" is not ambiguous, and reporting failure would only hand the same kill
// back to a caller with no better tool for it. Claude Code writes its transcript
// as it goes, so a terminated session is still there to resume.
func quitSession(pid uint32) error {
	if err := checkSession(pid); err != nil {
		return err
	}
	if _, err := helper(pid, "quit", ""); err != nil {
		return err
	}
	if coninject.WaitExit(pid, quitGrace, 250*time.Millisecond) {
		fmt.Fprintf(os.Stderr, "session %d exited\n", pid)
		return nil
	}

	// Checked again because the PID has been out of sight for quitGrace, and
	// terminating whatever inherited a recycled PID would be worse than leaving
	// this session running.
	if err := checkSession(pid); err != nil {
		return fmt.Errorf("session %d did not exit within %s, and is no longer safe to terminate: %w", pid, quitGrace, err)
	}
	fmt.Fprintf(os.Stderr, "session %d ignored /exit for %s; terminating\n", pid, quitGrace)
	if err := coninject.Terminate(pid); err != nil {
		return err
	}
	if !coninject.WaitExit(pid, 3*time.Second, 100*time.Millisecond) {
		return fmt.Errorf("session %d is still running after being terminated", pid)
	}
	fmt.Fprintf(os.Stderr, "session %d terminated\n", pid)
	return nil
}

// quitAtEndOfTurn is -q on its own. /exit takes effect the moment it is typed,
// turn or no turn, so a session is only ever sent it between turns: one that is
// idle now ends now, and one that is mid-turn is left to a background watcher
// that ends it once the turn is done, so this command does not block for as
// long as that turn takes.
func quitAtEndOfTurn(pid uint32) error {
	if err := checkSession(pid); err != nil {
		return err
	}
	// Without a readable record there is no turn to wait for that could be told
	// apart from a session that never reports one; ending it is what was asked.
	if a, err := sessions.ActivityOf(pid); err != nil || a.Status == "idle" {
		return quitSession(pid)
	}
	if err := watchQuit(pid, time.Time{}); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "session %d is mid-turn; it will end once that turn is done\n", pid)
	return nil
}

// promptThenQuit is -q with a prompt: the prompt is injected, and the session
// ends once the turn that prompt starts is done.
func promptThenQuit(pid uint32, prompt string) error {
	since := time.Now()
	if err := injectPrompt(pid, prompt); err != nil {
		return err
	}
	if err := watchQuit(pid, since); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "session %d will end once this prompt's turn is done\n", pid)
	return nil
}

// watchQuit starts the background watcher that ends the session at the end of
// its turn: the first time its record reports it idle with a change made after
// since. It is a separate, detached process so that the caller gets its answer
// -- and, with -d, the PID -- now, rather than when the turn is over.
func watchQuit(pid uint32, since time.Time) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	start := func(flags uint32) error {
		cmd := exec.Command(exe, quitCmd, strconv.FormatUint(uint64(pid), 10), strconv.FormatInt(since.UnixMilli(), 10))
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	// Detached from this console, in a process group of its own and, where the
	// job allows it, out of the caller's job: the caller's tool call ending
	// must not take the watcher with it (see launch.Start).
	const base = windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP
	if err := start(base | windows.CREATE_BREAKAWAY_FROM_JOB); err != nil {
		if err := start(base); err != nil {
			return fmt.Errorf("start the watcher that ends session %d: %w", pid, err)
		}
	}
	return nil
}

// runQuitWatch is the internal subcommand behind watchQuit.
func runQuitWatch(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: ccrun %s <pid> <since-unix-ms>", quitCmd)
	}
	pid64, err := strconv.ParseUint(args[0], 10, 32)
	if err != nil {
		return fmt.Errorf("bad pid %q: %w", args[0], err)
	}
	ms, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return fmt.Errorf("bad time %q: %w", args[1], err)
	}
	pid := uint32(pid64)
	since := time.UnixMilli(ms)

	// There is no time limit: a one-off run can take days. What ends the watch
	// instead is the session going away, told by its process creation time so
	// that a PID Windows hands to another process -- another session, even --
	// is never taken for this one.
	born, err := coninject.StartTime(pid)
	if err != nil {
		return fmt.Errorf("session %d: %w", pid, err)
	}
	for {
		if t, err := coninject.StartTime(pid); err != nil || t != born {
			return nil
		}
		// A record caught mid-write fails to parse, and is read again next time.
		//
		// Idle for idleSettle, not merely idle: a prompt queued behind the turn
		// starts the next one straight away, and ending the session in that gap
		// would drop it.
		if a, err := sessions.ActivityOf(pid); err == nil && a.Status == "idle" && a.Since.After(since) && time.Since(a.Since) >= idleSettle {
			return quitSession(pid)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// listSessions prints the running sessions that match every keyword, so a
// caller holding a name, a directory or a session URL can get the PID that -p
// takes. Making the keywords specific enough to leave one session is up to the
// caller; when several match, all of them are listed to choose from.
//
// Each session is one line on stdout, PID first, so awk '{print $1}' or
// PowerShell's .Split()[0] gets the PID. The column header is for a person and
// goes to stderr.
func listSessions(keywords []string) error {
	all, err := sessions.Running()
	if err != nil {
		return err
	}
	var found []sessions.Session
	for _, s := range all {
		if s.Matches(keywords) {
			found = append(found, s)
		}
	}
	if len(found) == 0 {
		if len(keywords) == 0 {
			return errors.New("no Claude Code session is running")
		}
		return fmt.Errorf("no running session matches %s", quoteAll(keywords))
	}

	rows := [][]string{{"PID", "NAME", "STATUS", "DIRECTORY"}}
	for _, s := range found {
		rows = append(rows, []string{strconv.FormatUint(uint64(s.PID), 10), orDash(s.Name), orDash(s.Status), orDash(s.Dir)})
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], len([]rune(cell)))
		}
	}
	for i, row := range rows {
		var line strings.Builder
		for j, cell := range row {
			line.WriteString(cell)
			if j < len(row)-1 {
				line.WriteString(strings.Repeat(" ", widths[j]-len([]rune(cell))+2))
			}
		}
		out := os.Stdout
		if i == 0 {
			out = os.Stderr
		}
		fmt.Fprintln(out, line.String())
	}

	if len(found) > 1 && len(keywords) > 0 {
		fmt.Fprintf(os.Stderr, "%d sessions match; add or lengthen keywords to leave one\n", len(found))
	}
	return nil
}

// pidByName finds the one running session called name, ignoring case. It is
// what keeps hold of a session across a restart: claude --resume brings a
// session back under a new PID but with its name. Unlike -l's keywords this is
// no partial match, because whatever it finds is about to be typed into: the
// name has to be the session's whole name, and a name two sessions share picks
// neither. A launch keeps its own names unique, but a session started by hand,
// or renamed with /rename, can still share one.
func pidByName(name string) (uint32, error) {
	found, err := sessionsNamed(name)
	if err != nil {
		return 0, err
	}
	switch len(found) {
	case 0:
		return 0, fmt.Errorf("no running session is named %q; ccrun -l lists them", name)
	case 1:
		return found[0].PID, nil
	}
	return 0, fmt.Errorf("%d running sessions are named %q (pids %s); pick one with -p", len(found), name, pidList(found))
}

// pidList renders the sessions' PIDs for an error message.
func pidList(found []sessions.Session) string {
	pids := make([]string, len(found))
	for i, s := range found {
		pids[i] = strconv.FormatUint(uint64(s.PID), 10)
	}
	return strings.Join(pids, ", ")
}

// sessionsNamed returns the running sessions whose whole name is name, ignoring
// case.
func sessionsNamed(name string) ([]sessions.Session, error) {
	all, err := sessions.Running()
	if err != nil {
		return nil, err
	}
	var found []sessions.Session
	for _, s := range all {
		if strings.EqualFold(s.Name, name) {
			found = append(found, s)
		}
	}
	return found, nil
}

// quoteAll renders keywords for an error message, each quoted so that an empty
// keyword or a stray space is visible.
func quoteAll(keywords []string) string {
	quoted := make([]string, len(keywords))
	for i, k := range keywords {
		quoted[i] = strconv.Quote(k)
	}
	return strings.Join(quoted, " ")
}

// orDash stands in for a field a session record left empty, so every line has
// the same columns.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// checkSession refuses to type into anything that is not a Claude Code session,
// so a stale or mistyped PID cannot deliver a prompt into an unrelated console
// application.
func checkSession(pid uint32) error {
	name, err := coninject.ProcessName(pid)
	if err != nil {
		return fmt.Errorf("%w (the session may have already exited)", err)
	}
	if !strings.EqualFold(name, "claude.exe") {
		return fmt.Errorf("pid %d is %s, not claude.exe", pid, name)
	}
	return nil
}

// helper runs one console operation in a child process and returns its stdout.
// The child calls FreeConsole, which invalidates console stdout/stderr --
// harmless there because its own streams are pipes back to us, which
// FreeConsole leaves intact, and this process keeps its console to report the
// outcome.
func helper(pid uint32, mode, text string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(exe, injectCmd, strconv.FormatUint(uint64(pid), 10), mode)
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	msg := strings.TrimSpace(stderr.String())
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	// What the child reports on success -- a selection it ended, say -- is for
	// whoever ran ccrun, so it is passed on.
	if msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	}
	return string(out), nil
}

// runInject is the internal subcommand: attach to the target console and either
// watch it or type into it.
func runInject(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: ccrun %s <pid> <ready|prompt|quit|screen>", injectCmd)
	}
	pid64, err := strconv.ParseUint(args[0], 10, 32)
	if err != nil {
		return fmt.Errorf("bad pid %q: %w", args[0], err)
	}
	pid := uint32(pid64)

	switch args[1] {
	case "ready":
		// Polled here rather than in the parent so the whole wait costs one
		// child process instead of one per look.
		return waitReady(pid)
	case "screen":
		text, err := coninject.ScreenText(pid)
		if err != nil {
			return err
		}
		// A frozen session looks like an idle one, so the screen cannot show
		// it. stdout stays the screen alone; the warning goes beside it.
		if sel, err := coninject.Selecting(pid); err == nil && sel {
			fmt.Fprintln(os.Stderr, "warning: the session's console has a selection in it, so the session is frozen until that selection ends; injecting a prompt ends it")
		}
		_, err = os.Stdout.WriteString(text)
		return err
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read prompt: %w", err)
	}
	var steps []coninject.Step
	switch args[1] {
	case "prompt":
		steps = promptSteps(string(data))
	case "quit":
		steps = quitSteps()
		// Injecting Ctrl+C into a console we are attached to can bounce back as
		// a control event on this process; ignoring it keeps the exit code
		// meaningful for the parent.
		signal.Ignore(os.Interrupt)
	default:
		return fmt.Errorf("unknown mode %q", args[1])
	}

	// A session whose console has a selection in it is frozen, and what is
	// typed would sit unread in its input buffer until someone ended the
	// selection. Typing goes ahead even when that fails: the buffer still takes
	// it, and the session reads it once the selection is gone.
	switch thawed, err := coninject.Thaw(pid); {
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: %v; typing anyway, and the session reads it once that selection ends\n", err)
	case thawed:
		fmt.Fprintln(os.Stderr, "ended a selection in the session's console that was freezing the session")
	}
	return coninject.Inject(pid, steps)
}

// waitReady blocks until the session is showing an empty input box, which is
// the point at which typing into it reaches Claude Code. Before that,
// keystrokes are dropped by the console mode changes the UI makes on its way in.
//
// On the way it answers the workspace-trust prompt, which Claude Code shows the
// first time it opens a directory. That prompt is unavoidable here -- ccrun
// creates the working directory when it is missing, so a first launch always
// lands on it -- and until it is answered the session has no input box and no
// Remote Control, which leaves the operator nothing to take over.
func waitReady(pid uint32) error {
	deadline := time.Now().Add(readyGrace)
	answeredTrust := false
	onTrust := false
	for {
		screen, err := coninject.ScreenText(pid)
		if err == nil {
			if hasInputBox(screen) {
				return nil
			}
			onTrust = strings.Contains(strings.ToLower(screen), trustMarker)
			if onTrust && !answeredTrust {
				if err := coninject.Inject(pid, trustSteps()); err != nil {
					return fmt.Errorf("answer workspace trust prompt: %w", err)
				}
				answeredTrust = true
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			if onTrust {
				return fmt.Errorf("still on the workspace trust prompt after %s", readyGrace)
			}
			return fmt.Errorf("no input box after %s", readyGrace)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// hasInputBox reports whether the visible console has an empty prompt line. The
// input box renders as a lone prompt glyph between two rules; every other state
// that shows one -- the trust prompt's "> No, exit", a line already typed -- has
// something after it.
//
// The glyph is "❯" when Claude Code thinks the terminal renders Unicode and ">"
// otherwise. On Windows it decides that from the environment, which the session
// inherits from ccrun's caller: WT_SESSION, TERM=xterm-256color (Git Bash, and
// so any agent's Bash tool) or TERM_PROGRAM=vscode all give "❯".
func hasInputBox(screen string) bool {
	for _, line := range strings.Split(screen, "\n") {
		switch strings.TrimSpace(line) {
		case ">", "❯":
			return true
		}
	}
	return false
}

// promptSteps types a prompt and submits it. Every line but the last ends with
// a backslash before Enter, which is Claude Code's terminal-independent way of
// putting a newline inside one message instead of sending it.
func promptSteps(text string) []coninject.Step {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	steps := make([]coninject.Step, 0, len(lines)*4+2)
	for i, line := range lines {
		if i > 0 {
			steps = append(steps,
				coninject.Text(`\`),
				coninject.Delay(newlineDelay),
				coninject.Enter(),
				coninject.Delay(newlineDelay),
			)
		}
		if line != "" {
			steps = append(steps, coninject.Text(line))
		}
	}
	return append(steps, coninject.Delay(enterDelay), coninject.Enter())
}

// trustSteps answers the workspace-trust prompt. Its selection starts on "No,
// exit", so the trusting answer is one step down.
func trustSteps() []coninject.Step {
	return []coninject.Step{
		coninject.Key(coninject.VKDown),
		coninject.Delay(newlineDelay),
		coninject.Enter(),
	}
}

// quitSteps ends the session. Ctrl+C first, because /exit only counts as a
// slash command when it starts the input box: anything already typed there
// would otherwise turn it into an ordinary message.
func quitSteps() []coninject.Step {
	return []coninject.Step{
		coninject.Ctrl('c'),
		coninject.Delay(400 * time.Millisecond),
		coninject.Text("/exit"),
		coninject.Delay(enterDelay),
		coninject.Enter(),
	}
}
