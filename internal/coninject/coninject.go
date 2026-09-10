// Package coninject injects keystrokes into a target console's input buffer on
// Windows, bypassing the windowing/UIPI layer via AttachConsole +
// WriteConsoleInputW. This works across the normal->elevated integrity boundary
// in the common case where the elevated console was launched by the same
// interactive user.
//
// It is a lightly extended copy of the package of the same name in
// neritina-ai/self-compact. The additions are arrow and Ctrl-chord steps, the
// console screen reader in screen.go, and the process helpers in process.go.
package coninject

import (
	"fmt"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	keyEvent     = 0x0001
	vkReturn     = 0x0D
	genericRW    = 0x80000000 | 0x40000000
	shareRW      = 0x00000001 | 0x00000002
	openExisting = 3

	// leftCtrlPressed mirrors LEFT_CTRL_PRESSED in dwControlKeyState. A console
	// in raw mode (which every TUI, Claude Code included, sets) hands the app the
	// control character itself, so the chord arrives as e.g. 0x03 for Ctrl+C.
	leftCtrlPressed = 0x0008
)

// keyEventRecord mirrors KEY_EVENT_RECORD. Field order and sizes must match the
// C struct exactly so the bytes land correctly in WriteConsoleInputW.
type keyEventRecord struct {
	bKeyDown          int32 // BOOL
	wRepeatCount      uint16
	wVirtualKeyCode   uint16
	wVirtualScanCode  uint16
	unicodeChar       uint16
	dwControlKeyState uint32
}

// inputRecord mirrors INPUT_RECORD for a KEY_EVENT. The uint16 padding after
// eventType aligns the union (which starts with a 4-byte BOOL) to a 4-byte
// boundary, matching the C layout (20 bytes total).
type inputRecord struct {
	eventType uint16
	_         uint16
	keyEvent  keyEventRecord
}

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procFreeConsole       = kernel32.NewProc("FreeConsole")
	procAttachConsole     = kernel32.NewProc("AttachConsole")
	procWriteConsoleInput = kernel32.NewProc("WriteConsoleInputW")
)

// keyRecords returns the down+up INPUT_RECORD pair for a single character.
// Pass vk=0 for ordinary printable text, or a virtual-key code (e.g. vkReturn).
func keyRecords(ch uint16, vk uint16, ctrlState uint32) []inputRecord {
	recs := make([]inputRecord, 0, 2)
	for _, down := range []bool{true, false} {
		var b int32
		if down {
			b = 1
		}
		recs = append(recs, inputRecord{
			eventType: keyEvent,
			keyEvent: keyEventRecord{
				bKeyDown:          b,
				wRepeatCount:      1,
				wVirtualKeyCode:   vk,
				unicodeChar:       ch,
				dwControlKeyState: ctrlState,
			},
		})
	}
	return recs
}

// chunkRecords bounds a single WriteConsoleInputW call. A long prompt is
// thousands of records and the console's input buffer is far smaller, so the
// write is paced to let the target application drain it.
const chunkRecords = 256

func writeRecords(handle windows.Handle, recs []inputRecord) error {
	stalled := 0
	for len(recs) > 0 {
		n := len(recs)
		if n > chunkRecords {
			n = chunkRecords
		}
		var written uint32
		r, _, err := procWriteConsoleInput.Call(
			uintptr(handle),
			uintptr(unsafe.Pointer(&recs[0])),
			uintptr(n),
			uintptr(unsafe.Pointer(&written)),
		)
		if r == 0 {
			return fmt.Errorf("WriteConsoleInputW failed: %w", err)
		}
		if written == 0 {
			// The buffer is full and the app has not read yet; give it a moment.
			stalled++
			if stalled > 100 {
				return fmt.Errorf("WriteConsoleInputW made no progress with %d records left", len(recs))
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		stalled = 0
		recs = recs[written:]
		if len(recs) > 0 {
			time.Sleep(time.Millisecond)
		}
	}
	return nil
}

// VKDown is the virtual-key code for the Down arrow, the one non-printing key
// steps need beyond Enter.
const VKDown = 0x28

// Step is one unit of an injection sequence: printable text, a bare key, a
// Ctrl chord, or a pause before the next step.
type Step struct {
	Text  string
	Key   uint16
	Ctrl  rune
	Delay time.Duration
}

// Text returns a step that types the given string.
func Text(s string) Step { return Step{Text: s} }

// Enter returns a step that presses Return.
func Enter() Step { return Step{Key: vkReturn} }

// Key returns a step that presses a non-printing key, e.g. Key(VKDown).
func Key(vk uint16) Step { return Step{Key: vk} }

// Ctrl returns a step that presses Ctrl together with the given ASCII letter,
// e.g. Ctrl('c') for the interrupt/cancel chord.
func Ctrl(letter rune) Step { return Step{Ctrl: letter} }

// Delay returns a step that pauses before the following step runs.
func Delay(d time.Duration) Step { return Step{Delay: d} }

// Inject attaches to the target process's console and plays the given steps
// into its input buffer. The calling process must NOT depend on its own console
// afterward: FreeConsole tears down stdout/stderr. Run this in a short-lived /
// detached process, or in a process whose stdout/stderr are redirected to pipes
// or files (which FreeConsole leaves intact).
func Inject(targetPID uint32, steps []Step) error {
	// A process can be attached to only one console; release ours first.
	procFreeConsole.Call()

	r, _, err := procAttachConsole.Call(uintptr(targetPID))
	if r == 0 {
		return fmt.Errorf("AttachConsole(%d) failed: %w", targetPID, err)
	}
	defer procFreeConsole.Call()

	name, _ := windows.UTF16PtrFromString("CONIN$")
	handle, err := windows.CreateFile(name, genericRW, shareRW, nil, openExisting, 0, 0)
	if err != nil {
		return fmt.Errorf("open CONIN$: %w", err)
	}
	defer windows.CloseHandle(handle)

	for _, step := range steps {
		if step.Delay > 0 {
			time.Sleep(step.Delay)
		}
		if step.Key != 0 {
			// Enter is the one key whose character matters; an arrow is
			// recognized by virtual-key code and carries no character.
			var ch uint16
			if step.Key == vkReturn {
				ch = '\r'
			}
			if err := writeRecords(handle, keyRecords(ch, step.Key, 0)); err != nil {
				return err
			}
		}
		if step.Ctrl != 0 {
			// The control character is the letter's position in the alphabet:
			// Ctrl+A is 0x01, Ctrl+C is 0x03. The virtual-key code is the
			// uppercase letter, which is also its ASCII value.
			upper := step.Ctrl &^ 0x20
			if upper < 'A' || upper > 'Z' {
				return fmt.Errorf("Ctrl(%q): not an ASCII letter", step.Ctrl)
			}
			ch := uint16(upper - 'A' + 1)
			if err := writeRecords(handle, keyRecords(ch, uint16(upper), leftCtrlPressed)); err != nil {
				return err
			}
		}
		if step.Text != "" {
			// The console input buffer speaks UTF-16 code units, so encode
			// rather than truncating runes: a non-BMP rune (an emoji, say) has
			// to go in as its surrogate pair.
			var recs []inputRecord
			for _, u := range utf16.Encode([]rune(step.Text)) {
				recs = append(recs, keyRecords(u, 0, 0)...)
			}
			if err := writeRecords(handle, recs); err != nil {
				return err
			}
		}
	}
	return nil
}
