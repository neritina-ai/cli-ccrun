# ccrun

Start a Claude Code session and drive it by PID, on Windows.

Claude Code is built to be started by a person in a terminal. When something
else wants to start one -- an OpenClaw automation, a skill -- there is nothing to
hand back and no way to say one more thing to a session that is already running.
`ccrun` opens the session in a console of its own with Remote Control enabled,
under a name you choose, prints its PID, and takes that name or PID for
everything after:

```
ccrun -d <working-dir> [-n <name>] [prompt]          start a session, or prompt the one
                                                     already running; prints its PID
ccrun -n <name> <prompt>                             inject a prompt into that session
ccrun -n <name> -                                    inject a prompt read from stdin
ccrun -n <name> -s                                   print what that session is showing
ccrun -n <name> -q                                   end that session
ccrun -p <pid> <prompt> | - | -s | -q                the same, picking the session by PID
ccrun -l [keyword ...]                               list running sessions and their PIDs
ccrun -m <old-dir> <new-dir>                         after a working directory was renamed or
                                                     moved, move what Claude Code keeps for it
```

So the person who launched it can pick it up from their phone or from
`claude.ai/code`, while an automation keeps feeding the same session from the
machine -- by the name it gave the session, which, unlike the PID, still holds
after `claude --resume` brings the session back. A session started by hand, or
whose name was not kept, is found by keyword with `-l`.

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
ccrun -d D:\work\nightly -n nightly-build "run the release checklist"
```

The working directory is created if it does not exist. The prompt is optional
(`-` reads it from stdin) and is submitted as the session's opening message.

`-n` names the session, and that one name is what everything shows -- its prompt
box, the Remote Control list on your phone and at `claude.ai/code`, the
`/resume` picker -- and what `-n` picks it by afterwards. Leave it out and the
directory's name is used.

Running the same command again does not open a second session. A session
with that name already running in that directory is the session this would
start, so the prompt is injected into it instead; only when it is gone --
ended, or its window closed by accident -- does a new one start. A scheduled
task can therefore always run `ccrun -d ... -n ... "<prompt>"` and keep one
session going in that directory. A session with that name running in another
directory is a different one: nothing starts, and ccrun exits 1. So a name you
hand out always picks out the one session.

The PID is printed on stdout, alone -- progress goes to stderr -- so
`$sessionPid = ccrun -d ...` captures it, whether the session was just
started or already running. Exit code is 0 or 1, as everywhere else.

It returns once the session is actually ready to be typed into and known by
its name, so a prompt injected straight afterwards -- by name or by PID -- is not
lost. The workspace-trust question Claude Code asks the first time it opens a
directory is answered on the way; left unanswered, a session has no input box
and no Remote Control.

The session's window starts minimized and never takes focus, so starting one --
or ten -- does not interrupt whatever you are doing. It is on the taskbar if you
want to open it and take over by hand.

### Pick a session by name or PID

```
ccrun -n nightly-build "now write the release notes"
ccrun -p 5416 "now write the release notes"
```

Every form below takes either. The name is the one given with `-n` at launch,
matched whole (case does not matter); the PID is the one the launch printed. A
PID lasts only as long as the process: a session brought back with
`claude --resume` -- by hand, or after a reboot -- has a new one but keeps its
name, so a long-running session is best kept hold of by name.

A name that more than one running session has picks neither: `-n` refuses and
lists their PIDs. ccrun's own launches never cause that, but sessions started by
hand can. A session started by hand can be given a name with
`claude --name <name>`, or `/rename <name>` once it is running; one with
neither has a name Claude Code made up, which changes with every
`claude --resume`.

### Find a session's PID

```
ccrun -l
ccrun -l nightly
ccrun -l nightly D:\work
```

Lists the Claude Code sessions running on this machine -- the ones ccrun
started and the ones started by hand -- one per line, PID first:

```
PID    NAME           STATUS  DIRECTORY
5416   nightly-build  idle    D:\work\nightly
7720   cli-ccrun      busy    D:\projects\cli-ccrun
```

Keywords are matched, ignoring case, against each session's name, its working
directory and its session IDs: the `session_...` at the end of its
`claude.ai/code` URL, and the local one `claude --resume` takes. Every keyword
has to match somewhere, so each one added narrows the list. Picking keywords
that leave exactly one session is up to you: every match is listed, with a note
on stderr when there is more than one. `-l` exits 1 when nothing matches.

The header goes to stderr, so stdout carries only the rows, each starting with
the PID; a script that has narrowed the list to one line takes its first field.

### Inject a prompt

```
ccrun -n nightly-build "now write the release notes"
type notes.md | ccrun -n nightly-build -
```

The text is typed into the input box and submitted. Multi-line text arrives as
one message with its line breaks intact. Prompts are capped at 8000 characters;
anything longer belongs in a file the prompt points at.

### See what a session is showing

```
ccrun -n nightly-build -s
```

Prints the session's visible console text: its last messages, whatever is
half-typed in its input box, or a prompt it is waiting on. Nobody is watching
that window, so this is how a caller sees what state it is in.

A frozen session looks just like an idle one, so when the session's console has
a selection in it -- which freezes it, see Notes -- a warning on stderr says so.
stdout is the screen either way.

### End a session

```
ccrun -n nightly-build -q
```

`/exit` is queued behind whatever the session is doing, so it gets 10 seconds; a
session that has not gone by then is terminated. Claude Code writes its
transcript as it goes, so either way the conversation is still there to resume.

### Rename a working directory

```
ren D:\work\nightly nightly-builds
ccrun -m D:\work\nightly D:\work\nightly-builds
```

Claude Code keeps a working directory's transcripts and auto memory in a folder
named after the directory's path, under `~/.claude/projects` (or
`%CLAUDE_CONFIG_DIR%\projects`). Rename or move the directory and a session
started there begins with none of it: no memory, nothing for `/resume`. `-m`
renames that folder for the new path. Auto memory is filed under the git
repository's root rather than the working directory, so it moves with the
directory when that is a repository's root or not in a repository at all; a
subdirectory's or a worktree's memory stays under the repository's folder.

Move the directory first: `-m` refuses while the new path does not exist or the
old one still does, and while a session is working in either. It never
overwrites or merges into what Claude Code already keeps for the new path. When
nothing was kept for the old path it says so and exits 0. Workspace trust and
the other per-directory settings in `~/.claude.json` stay with the old path;
`-d` answers the trust question again in the new one.

## Notes

- Injection is typing, so pending text in the input box gets whatever is
  injected appended to it, and a session showing a dialog reads it as an answer
  to that dialog. `-s` shows which it is.
- `-s` reads the visible window; scrollback is not in it.
- `-p` and `-n` only target `claude.exe`, so a mistyped PID cannot type into
  an unrelated console. PIDs are recycled by Windows, which that check catches only
  in the common case.
- A launched session is a session of its own -- its own transcript, resumable --
  even when ccrun was run from inside another Claude Code session.
- A minimized session takes prompts exactly like a visible one, and stays
  minimized when one arrives -- it never pops to the front.
- A selection in a session's console window -- a click or drag in it with
  QuickEdit on, or Mark or Select All from its window menu -- freezes the
  session. The console holds the session's output while the selection lasts,
  and Claude Code stops at its next write: no turn starts, no background-task
  notification is taken, nothing typed is read. When the selection ends,
  everything that waited goes through at once. The only sign is the title bar,
  where "Select" or "Mark" comes before the name, and a minimized window does
  not show it. So before minimizing a session window you have been reading,
  look at its title and press Escape if it shows a selection -- only then,
  because Escape anywhere else interrupts Claude Code.
- Injecting a prompt, and `-q`, end such a selection first and say so on
  stderr. They do it by sending Escape to the window, which the console keeps
  for itself while selecting, and only when there is a selection. A selection
  that cannot be ended gets a warning, and the prompt is typed anyway: it waits
  in the input buffer until the selection is gone. Between injections nothing
  ends it, so a session frozen this way also misses its background-task
  notifications until then.
- `-l` and `-n` read the record Claude Code keeps of each running session under
  `~/.claude/sessions` (or `%CLAUDE_CONFIG_DIR%\sessions`), checked against the
  live process so a record left behind by a crashed session is not listed. That
  record is Claude Code's internal state rather than a documented interface, so
  a Claude Code release can change it.

## Development

Built with **Claude Opus 5** and **Claude Opus 5.5**.

```
go build ./cmd/ccrun
go vet ./...
go test ./...
```

## License

MIT -- see `LICENSE`.
