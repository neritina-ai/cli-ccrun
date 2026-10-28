package coninject

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A console with a selection in it -- a click or drag in the window while
// QuickEdit is on, or Mark or Select All from its system menu -- holds every
// write its programs make until the selection ends. Claude Code writes its
// screen on its main thread, so its next write blocks and the whole session
// stops: no turn starts, no task notification is taken, nothing is read from
// the input buffer. That buffer is not held, so keystrokes still go in and
// Inject still succeeds; they just wait. The one sign is the window title,
// which conhost prefixes with "Select" or "Mark", and a minimized window does
// not show its title. Ending the selection lets everything that waited through
// at once.

var (
	user32                      = windows.NewLazySystemDLL("user32.dll")
	procPostMessage             = user32.NewProc("PostMessageW")
	procGetConsoleWindow        = kernel32.NewProc("GetConsoleWindow")
	procGetConsoleSelectionInfo = kernel32.NewProc("GetConsoleSelectionInfo")
)

const (
	// selectionInProgress mirrors CONSOLE_SELECTION_IN_PROGRESS. It is set for
	// as long as the selection holds the console's output: from a mouse click
	// or Mark until the selection is copied or dismissed.
	selectionInProgress = 0x0001

	wmKeydown = 0x0100
	wmKeyup   = 0x0101
	vkEscape  = 0x1B

	// The lParam of an Escape key press: repeat count 1 and scan code 1, with
	// the previous-state and transition bits set on the release.
	escapeDown = 0x00010001
	escapeUp   = 0xC0010001

	// thawGrace is how long the console window is given to act on Escape.
	// conhost handles it as soon as its window thread gets to it, which takes
	// milliseconds.
	thawGrace = 2 * time.Second
)

// consoleSelectionInfo mirrors CONSOLE_SELECTION_INFO.
type consoleSelectionInfo struct {
	flags  uint32
	anchor coord
	rect   struct{ left, top, right, bottom int16 }
}

// selecting reports whether the console this process is attached to has a
// selection in progress.
func selecting() (bool, error) {
	var info consoleSelectionInfo
	r, _, err := procGetConsoleSelectionInfo.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return false, fmt.Errorf("GetConsoleSelectionInfo: %w", err)
	}
	return info.flags&selectionInProgress != 0, nil
}

// Selecting reports whether the target process's console has a selection in
// progress, which is to say whether the session in it is frozen. Like
// ScreenText, it calls FreeConsole.
func Selecting(targetPID uint32) (bool, error) {
	procFreeConsole.Call()

	r, _, err := procAttachConsole.Call(uintptr(targetPID))
	if r == 0 {
		return false, fmt.Errorf("AttachConsole(%d) failed: %w", targetPID, err)
	}
	defer procFreeConsole.Call()

	return selecting()
}

// Thaw ends a selection in the target process's console, if it has one, and
// reports whether it did. An error means a selection may still be holding the
// session.
//
// The selection is ended by posting Escape to the console window, which
// conhost takes for itself while selecting: the key never reaches the program.
// Escape is sent only when there is a selection, because otherwise it does
// reach the program, and Claude Code takes Escape as "interrupt this turn".
// Posting a message neither restores a minimized window nor takes focus. Like
// Inject, Thaw calls FreeConsole.
func Thaw(targetPID uint32) (bool, error) {
	procFreeConsole.Call()

	r, _, err := procAttachConsole.Call(uintptr(targetPID))
	if r == 0 {
		return false, fmt.Errorf("AttachConsole(%d) failed: %w", targetPID, err)
	}
	defer procFreeConsole.Call()

	sel, err := selecting()
	if err != nil {
		return false, fmt.Errorf("check the console for a selection: %w", err)
	}
	if !sel {
		return false, nil
	}

	hwnd, _, _ := procGetConsoleWindow.Call()
	if hwnd == 0 {
		return false, fmt.Errorf("the console has a selection but no window to send Escape to")
	}
	if r, _, err := procPostMessage.Call(hwnd, wmKeydown, vkEscape, escapeDown); r == 0 {
		return false, fmt.Errorf("the console has a selection, and sending Escape to its window failed: %w", err)
	}
	procPostMessage.Call(hwnd, wmKeyup, vkEscape, escapeUp)

	deadline := time.Now().Add(thawGrace)
	for {
		time.Sleep(20 * time.Millisecond)
		sel, err := selecting()
		if err != nil {
			return false, fmt.Errorf("check the console for a selection: %w", err)
		}
		if !sel {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("the console still has a selection %s after Escape was sent to its window", thawGrace)
		}
	}
}
