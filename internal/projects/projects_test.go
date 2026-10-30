package projects

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The expected keys were computed with the function Claude Code itself uses.
func TestKey(t *testing.T) {
	long := `D:\projects\` + strings.Repeat("very-long-project-name-", 9) + "end"
	for _, c := range []struct{ dir, key string }{
		{`D:\projects\openclaw-chat-interceptor`, "D--projects-openclaw-chat-interceptor"},
		{`D:\專案\x😀y`, "D-----x--y"},
		{long, "D--projects-" + strings.Repeat("very-long-project-name-", 8) + "very-3u3mzh"},
	} {
		if got := Key(c.dir); got != c.key {
			t.Errorf("Key(%q) = %q, want %q", c.dir, got, c.key)
		}
	}
}

func seed(t *testing.T, config, dir string) string {
	t.Helper()
	memory := filepath.Join(config, "projects", Key(dir), "memory")
	if err := os.MkdirAll(memory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memory, "MEMORY.md"), []byte(dir), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(config, "projects", Key(dir))
}

func TestMove(t *testing.T) {
	config := t.TempDir()
	src := seed(t, config, `D:\projects\ledger`)
	dst, err := Move(config, `D:\projects\ledger`, `D:\projects\books`)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(config, "projects", "D--projects-books"); dst != want {
		t.Fatalf("moved to %s, want %s", dst, want)
	}
	if data, err := os.ReadFile(filepath.Join(dst, "memory", "MEMORY.md")); err != nil || string(data) != `D:\projects\ledger` {
		t.Fatalf("memory did not move: %q, %v", data, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("the old folder is still there: %v", err)
	}
}

func TestMoveNothingKept(t *testing.T) {
	dst, err := Move(t.TempDir(), `D:\projects\ledger`, `D:\projects\books`)
	if err != nil || dst != "" {
		t.Fatalf("Move = %q, %v; want nothing moved", dst, err)
	}
}

func TestMoveNeverOverwrites(t *testing.T) {
	config := t.TempDir()
	src := seed(t, config, `D:\projects\ledger`)
	seed(t, config, `D:\projects\books`)
	if _, err := Move(config, `D:\projects\ledger`, `D:\projects\books`); err == nil || !strings.Contains(err.Error(), "already keeps data") {
		t.Fatalf("Move over existing data: %v", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the old folder was touched: %v", err)
	}
}

func TestMoveCaseOnly(t *testing.T) {
	config := t.TempDir()
	seed(t, config, `D:\projects\ledger`)
	dst, err := Move(config, `D:\projects\ledger`, `D:\projects\Ledger`)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(config, "projects"))
	if len(entries) != 1 || entries[0].Name() != "D--projects-Ledger" || filepath.Base(dst) != "D--projects-Ledger" {
		t.Fatalf("after a case-only move: %v, %s", entries, dst)
	}
}
