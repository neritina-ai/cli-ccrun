// Package sessions finds the Claude Code sessions running on this machine.
//
// Every interactive Claude Code session keeps a record of itself in
// <config-dir>/sessions/<pid>.json while it runs: its name, working directory,
// session IDs and whether it is busy. That is what identifies a session to a
// person, so it is what a session is looked up by. A record outlives a session
// that crashed, so each one is checked against the process it names before it
// is believed.
package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/neritina-ai/cli-ccrun/internal/coninject"
)

// Session is one running Claude Code session.
type Session struct {
	PID       uint32
	Name      string // shown in the session's prompt box and the Remote Control list
	Status    string // "busy", "idle", ... as Claude Code reports it
	Dir       string // working directory
	SessionID string // local transcript ID, the one --resume takes
	RemoteID  string // Remote Control session ID, as in claude.ai/code/<id>
	Started   time.Time
}

// record is the part of Claude Code's own session record that is used here.
type record struct {
	PID             uint32 `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	StartedAt       int64  `json:"startedAt"` // Unix milliseconds
	ProcStart       string `json:"procStart"` // process creation time as a FILETIME
	Name            string `json:"name"`
	Status          string `json:"status"`
	BridgeSessionID string `json:"bridgeSessionId"`
}

// ConfigDir is where Claude Code keeps its state, which CLAUDE_CONFIG_DIR moves.
func ConfigDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// Running returns every running session, oldest first.
func Running() ([]Session, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(dir, "sessions", "*.json"))
	if err != nil {
		return nil, err
	}
	procs, err := coninject.ProcessNames()
	if err != nil {
		return nil, err
	}

	var found []Session
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var r record
		// A record caught mid-write does not parse; its session is listed again
		// the next time round.
		if json.Unmarshal(data, &r) != nil || r.PID == 0 {
			continue
		}
		if !strings.EqualFold(procs[r.PID], "claude.exe") || !sameProcess(r) {
			continue
		}
		found = append(found, Session{
			PID:       r.PID,
			Name:      r.Name,
			Status:    r.Status,
			Dir:       r.Cwd,
			SessionID: r.SessionID,
			RemoteID:  r.BridgeSessionID,
			Started:   time.UnixMilli(r.StartedAt),
		})
	}
	slices.SortFunc(found, func(a, b Session) int { return a.Started.Compare(b.Started) })
	return found, nil
}

// Activity is what a session's record says it is doing right now.
type Activity struct {
	Status string    // "busy", "idle", ... as Claude Code reports it
	Since  time.Time // when Status last changed
}

// ActivityOf reads the record of the session with this PID alone, which is
// cheap enough to poll. It fails once the session has exited, or when the
// record belongs to a process that no longer holds the PID.
func ActivityOf(pid uint32) (Activity, error) {
	dir, err := ConfigDir()
	if err != nil {
		return Activity{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "sessions", strconv.FormatUint(uint64(pid), 10)+".json"))
	if err != nil {
		return Activity{}, err
	}
	var r struct {
		record
		StatusUpdatedAt int64 `json:"statusUpdatedAt"` // Unix milliseconds
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Activity{}, err
	}
	if r.PID != pid || !sameProcess(r.record) {
		return Activity{}, fmt.Errorf("the record for pid %d belongs to an earlier process", pid)
	}
	return Activity{Status: r.Status, Since: time.UnixMilli(r.StatusUpdatedAt)}, nil
}

// sameProcess reports whether the record was written by the process that now
// holds its PID, rather than left behind by a crashed session whose PID has
// since gone to another claude.exe. A record without a creation time, or a
// process whose creation time cannot be read, is given the benefit of the
// doubt: the image-name check has already passed.
func sameProcess(r record) bool {
	want, err := strconv.ParseUint(r.ProcStart, 10, 64)
	if err != nil {
		return true
	}
	got, err := coninject.StartTime(r.PID)
	if err != nil {
		return true
	}
	return got == want
}

// Matches reports whether every keyword occurs, ignoring case, in the session's
// name, working directory or either session ID. Several keywords narrow the
// search; none matches every session.
func (s Session) Matches(keywords []string) bool {
	fields := []string{s.Name, s.Dir, s.SessionID, s.RemoteID}
	for _, k := range keywords {
		k = strings.ToLower(k)
		if !slices.ContainsFunc(fields, func(f string) bool {
			return strings.Contains(strings.ToLower(f), k)
		}) {
			return false
		}
	}
	return true
}
