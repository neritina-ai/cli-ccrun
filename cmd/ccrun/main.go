// Command ccrun starts a Claude Code session and drives it by PID: inject a
// prompt, see what it is showing, or end it. It exists so that a third-party
// tool -- an OpenClaw automation, a skill -- can open a session in a chosen
// directory with Remote Control enabled, hand the operator its PID, and later
// feed it prompts or shut it down, without anyone sitting at that terminal.
//
// Forms:
//
//	ccrun -d <working-dir> [-r <remote-name>] [prompt]   start a session
//	ccrun -p <pid> <prompt>                              inject a prompt
//	ccrun -p <pid> -                                     inject a prompt from stdin
//	ccrun -p <pid> -s                                    print what the session shows
//	ccrun -p <pid> -q                                    end the session
//
// One internal form, spawned by the public ones:
//
//	ccrun __inject <pid> <ready|prompt|quit|screen>
//
// The session is launched with CREATE_NEW_CONSOLE so it owns a console, and
// every later command reaches it the way neritina-ai/self-compact reaches its
// own session: AttachConsole to that console and WriteConsoleInputW straight
// into its input buffer. Nothing depends on window focus.
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
	"time"

	"github.com/neritina-ai/cli-ccrun/internal/coninject"
	"github.com/neritina-ai/cli-ccrun/internal/launch"
)

const (
	// injectCmd is the internal subcommand that does the console work.
	injectCmd = "__inject"

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

	// quitGrace is how long "-q" waits for /exit to take effect. /exit is queued
	// behind whatever turn is in flight, so this has to allow for a turn that is
	// still wrapping up.
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
  ccrun -d <working-dir> [-r <remote-name>] [prompt]   start a session; prints its PID
  ccrun -p <pid> <prompt>                              inject a prompt into that session
  ccrun -p <pid> -                                     inject a prompt read from stdin
  ccrun -p <pid> -s                                    print what that session is showing
  ccrun -p <pid> -q                                    end that session

options:
  -d <dir>     working directory for the new session; created if missing
  -r <name>    name identifying the session under Remote Control; defaults to
               the working directory's name
  -p <pid>     act on the running session with this PID
  -s           with -p: print the session's visible console text on stdout
  -q           with -p: end the session

A launch prints the new session's PID on stdout. Everything exits 0 on success
and 1 on failure.`

func main() {
	// The internal subcommand is dispatched before flag parsing so its
	// arguments are never mistaken for ccrun's own.
	if len(os.Args) > 1 && os.Args[1] == injectCmd {
		if err := runInject(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(exitError)
		}
		return
	}

	fs := flag.NewFlagSet("ccrun", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprintln(os.Stderr, usage) }
	var (
		dir    = fs.String("d", "", "working directory for the new session")
		remote = fs.String("r", "", "name identifying the session under Remote Control")
		pid    = fs.Uint("p", 0, "act on the session with this PID")
		screen = fs.Bool("s", false, "with -p: print the session's visible console text")
		quit   = fs.Bool("q", false, "with -p: end the session")
	)
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(exitError)
	}

	err := run(options{
		dir:    *dir,
		remote: *remote,
		pid:    uint32(*pid),
		screen: *screen,
		quit:   *quit,
		args:   fs.Args(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ccrun:", err)
		os.Exit(exitError)
	}
}

type options struct {
	dir    string
	remote string
	pid    uint32
	screen bool
	quit   bool
	args   []string
}

// run dispatches to one of the public forms.
func run(o options) error {
	switch {
	case o.pid != 0 && o.dir != "":
		return fmt.Errorf("-p and -d are different modes; use one\n%s", usage)

	case o.pid != 0:
		if o.quit && o.screen {
			return fmt.Errorf("-s and -q are different modes; use one\n%s", usage)
		}
		if o.quit || o.screen {
			if len(o.args) > 0 {
				return fmt.Errorf("-s and -q take no prompt\n%s", usage)
			}
			if o.screen {
				return showScreen(o.pid)
			}
			return quitSession(o.pid)
		}
		prompt, err := readPrompt(o.args)
		if err != nil {
			return err
		}
		if prompt == "" {
			return fmt.Errorf("nothing to inject: give a prompt, \"-\" to read one from stdin, -s to see the screen, or -q to end the session\n%s", usage)
		}
		return injectPrompt(o.pid, prompt)

	case o.quit || o.screen:
		return fmt.Errorf("-s and -q need -p <pid>\n%s", usage)

	case o.dir != "":
		prompt, err := readPrompt(o.args)
		if err != nil {
			return err
		}
		return startSession(o, prompt)

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
// Remote Control enabled, and prints the PID that every later command takes.
func startSession(o options, prompt string) error {
	dir, err := launch.EnsureDir(o.dir)
	if err != nil {
		return err
	}
	exe, err := exec.LookPath("claude")
	if err != nil {
		return fmt.Errorf("claude not found on PATH: %w", err)
	}

	name := o.remote
	if name == "" {
		name = defaultRemoteName(dir)
	}
	// --remote-control turns Remote Control on and names it; --name is what
	// labels the session in the UI the operator is looking at (its prompt box,
	// the terminal title, the session picker), which is what -r is for. Both get
	// the same name.
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
	if _, err := helper(pid, "ready", ""); err != nil {
		fmt.Fprintf(os.Stderr, "warning: session %d is not ready for prompts: %v\n", pid, err)
	}

	// The PID is what this form is for, so stdout carries it and nothing else;
	// everything a person reads goes to stderr.
	fmt.Println(pid)
	fmt.Fprintf(os.Stderr, "started claude in %s as %q (pid %d)\n", dir, name, pid)
	return nil
}

// defaultRemoteName names a session after its working directory when -r is
// omitted. The name is never left empty: --remote-control's argument is
// optional on claude's side, so a bare flag would swallow the prompt that
// follows it as its value.
func defaultRemoteName(dir string) string {
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
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
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
