package coninject

import (
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procReadConsoleOutputCharacter = kernel32.NewProc("ReadConsoleOutputCharacterW")

// coord mirrors COORD, which WriteConsoleInputW's siblings take by value.
type coord struct {
	x, y int16
}

// ScreenText returns the text currently visible in the target process's console
// window, one line per row with trailing blanks trimmed.
//
// It is how ccrun tells what state a session is in without a human looking at
// the window -- in particular whether it is sitting on the workspace-trust
// prompt instead of its normal input box. Like Inject, it calls FreeConsole, so
// the caller must not need its own console afterward.
func ScreenText(targetPID uint32) (string, error) {
	procFreeConsole.Call()

	r, _, err := procAttachConsole.Call(uintptr(targetPID))
	if r == 0 {
		return "", fmt.Errorf("AttachConsole(%d) failed: %w", targetPID, err)
	}
	defer procFreeConsole.Call()

	name, _ := windows.UTF16PtrFromString("CONOUT$")
	handle, err := windows.CreateFile(name, genericRW, shareRW, nil, openExisting, 0, 0)
	if err != nil {
		return "", fmt.Errorf("open CONOUT$: %w", err)
	}
	defer windows.CloseHandle(handle)

	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(handle, &info); err != nil {
		return "", fmt.Errorf("GetConsoleScreenBufferInfo: %w", err)
	}

	width := int(info.Size.X)
	if width <= 0 {
		return "", fmt.Errorf("console reports width %d", width)
	}
	buf := make([]uint16, width)
	var out strings.Builder
	for y := info.Window.Top; y <= info.Window.Bottom; y++ {
		var read uint32
		at := coord{x: 0, y: y}
		r, _, err := procReadConsoleOutputCharacter.Call(
			uintptr(handle),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(width),
			*(*uintptr)(unsafe.Pointer(&at)),
			uintptr(unsafe.Pointer(&read)),
		)
		if r == 0 {
			return "", fmt.Errorf("ReadConsoleOutputCharacterW row %d: %w", y, err)
		}
		out.WriteString(strings.TrimRight(windows.UTF16ToString(buf[:read]), " "))
		out.WriteByte('\n')
	}
	return out.String(), nil
}
