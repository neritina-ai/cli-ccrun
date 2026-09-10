# ccrun

Start a Claude Code session and drive it by PID, on Windows.

Claude Code is built to be started by a person in a terminal. When something
else wants to start one -- an OpenClaw automation, a skill -- there is nothing to
hand back and no way to say one more thing to a session that is already running.
`ccrun` opens the session in a console of its own with Remote Control enabled,
prints its PID, and takes that PID for everything after:

```
ccrun -d <working-dir> [-r <remote-name>] [prompt]   start a session; prints its PID
ccrun -p <pid> <prompt>                              inject a prompt into that session
ccrun -p <pid> -                                     inject a prompt read from stdin
ccrun -p <pid> -s                                    print what that session is showing
ccrun -p <pid> -q                                    end that session
```

So the person who launched it can pick it up from their phone or from
`claude.ai/code`, while an automation keeps feeding the same session from the
machine.

## Requirements

Windows, Claude Code installed as `claude.exe` on `PATH`, and
[Go](https://go.dev/dl/) 1.26+ with `~/go/bin` on your `PATH`.

## Install

```
go install github.com/neritina-ai/cli-ccrun/cmd/ccrun@latest
```

## Usage

### Start a session

```
ccrun -d D:\work\nightly -r nightly-build "run the release checklist"
```

The working directory is created if it does not exist. `-r` names the session
for the Remote Control list and its own prompt box; leave it out and the
directory's name is used. The prompt is optional (`-` reads it from stdin) and
is submitted as the session's opening message.

The PID is printed on stdout, alone -- progress goes to stderr -- so
`$sessionPid = ccrun -d ...` captures it. Exit code is 0 or 1, as everywhere
else.

It returns once the session is actually ready to be typed into, so a prompt
injected straight afterwards is not lost. The workspace-trust question Claude
Code asks the first time it opens a directory is answered on the way; left
unanswered, a session has no input box and no Remote Control.

The session's window starts minimized and never takes focus, so starting one --
or ten -- does not interrupt whatever you are doing. It is on the taskbar if you
want to open it and take over by hand.

### Inject a prompt

```
ccrun -p 5416 "now write the release notes"
type notes.md | ccrun -p 5416 -
```

The text is typed into the input box and submitted. Multi-line text arrives as
one message with its line breaks intact. Prompts are capped at 8000 characters;
anything longer belongs in a file the prompt points at.

### See what a session is showing

```
ccrun -p 5416 -s
```

Prints the session's visible console text: its last messages, whatever is
half-typed in its input box, or a prompt it is waiting on. Nobody is watching
that window, so this is how a caller sees what state it is in.

### End a session

```
ccrun -p 5416 -q
```

`/exit` is queued behind whatever the session is doing, so it gets 10 seconds; a
session that has not gone by then is terminated. Claude Code writes its
transcript as it goes, so either way the conversation is still there to resume.

## Notes

- Injection is typing, so pending text in the input box gets whatever is
  injected appended to it, and a session showing a dialog reads it as an answer
  to that dialog. `-s` shows which it is.
- `-s` reads the visible window; scrollback is not in it.
- `-p` only targets `claude.exe`, so a mistyped PID cannot type into an
  unrelated console. PIDs are recycled by Windows, which that check catches only
  in the common case.
- A launched session is a session of its own -- its own transcript, resumable --
  even when ccrun was run from inside another Claude Code session.
- A minimized session takes prompts exactly like a visible one, and stays
  minimized when one arrives -- it never pops to the front.

## Development

Built with **Claude Opus 5**.

```
go build ./cmd/ccrun
go vet ./...
```

## License

MIT -- see `LICENSE`.
