package coninject

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

type procRow struct {
	ppid uint32
	name string
}

func snapshotProcs() (map[uint32]procRow, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	if err := windows.Process32First(snap, &e); err != nil {
		return nil, fmt.Errorf("Process32First: %w", err)
	}

	procs := make(map[uint32]procRow, 256)
	for {
		procs[e.ProcessID] = procRow{
			ppid: e.ParentProcessID,
			name: windows.UTF16ToString(e.ExeFile[:]),
		}
		if err := windows.Process32Next(snap, &e); err != nil {
			break
		}
	}
	return procs, nil
}

// ProcessName returns the image name of pid, or an error if no such process is
// running. Callers use it to confirm a caller-supplied PID really is a Claude
// Code session before typing into its console.
func ProcessName(pid uint32) (string, error) {
	procs, err := snapshotProcs()
	if err != nil {
		return "", err
	}
	row, ok := procs[pid]
	if !ok {
		return "", fmt.Errorf("no process with pid %d", pid)
	}
	return row.name, nil
}

// ProcessNames maps every running PID to its image name, from one snapshot.
func ProcessNames() (map[uint32]string, error) {
	procs, err := snapshotProcs()
	if err != nil {
		return nil, err
	}
	names := make(map[uint32]string, len(procs))
	for pid, row := range procs {
		names[pid] = row.name
	}
	return names, nil
}

// StartTime returns when pid was created, as a FILETIME: 100-nanosecond
// intervals since 1601 UTC. Together with the PID it identifies one process,
// since Windows recycles PIDs but never a creation time under the same PID.
func StartTime(pid uint32) (uint64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, fmt.Errorf("open pid %d: %w", pid, err)
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return 0, fmt.Errorf("GetProcessTimes pid %d: %w", pid, err)
	}
	return uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime), nil
}

// Alive reports whether pid is still a running process. A process that has
// exited but whose handle is still open (a "zombie") counts as gone.
func Alive(pid uint32) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259 // STILL_ACTIVE
	return code == stillActive
}
