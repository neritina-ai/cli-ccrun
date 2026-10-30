// Package projects carries what Claude Code keeps for a working directory over
// to the directory's new path when the directory is renamed or moved.
//
// Claude Code files everything it keeps for a working directory -- the
// transcripts that claude --resume offers, and the auto memory a new session
// starts from -- in <config-dir>/projects/<key>, where the key is made from the
// directory's path. The new path has a new key, so a session started there
// would begin with none of it.
package projects

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// maxKey is the longest key Claude Code uses as it is; a longer one is cut to
// this length and followed by a hash of the whole path.
const maxKey = 200

// Key is the folder name Claude Code files a working directory's data under:
// the path with every character other than an ASCII letter or digit turned
// into '-'. It counts UTF-16 code units, as Claude Code's JavaScript does, so a
// character outside the Basic Multilingual Plane becomes two dashes.
func Key(dir string) string {
	units := utf16.Encode([]rune(dir))
	key := make([]byte, len(units))
	for i, u := range units {
		if u >= 'a' && u <= 'z' || u >= 'A' && u <= 'Z' || u >= '0' && u <= '9' {
			key[i] = byte(u)
		} else {
			key[i] = '-'
		}
	}
	if len(key) <= maxKey {
		return string(key)
	}
	// The same 32-bit string hash as Claude Code's, printed the same way.
	var h int32
	for _, u := range units {
		h = 31*h + int32(u)
	}
	n := int64(h)
	if n < 0 {
		n = -n
	}
	return string(key[:maxKey]) + "-" + strconv.FormatInt(n, 36)
}

// Move renames the folder Claude Code keeps for from, under configDir, to the
// one it will look in for to. It returns where the data is now, or "" when
// Claude Code kept nothing for from. A folder already kept for to is never
// overwritten or merged into.
func Move(configDir, from, to string) (string, error) {
	root := filepath.Join(configDir, "projects")
	src, dst := filepath.Join(root, Key(from)), filepath.Join(root, Key(to))
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if src == dst {
		return dst, nil
	}
	// Keys that differ only in case are the same folder on Windows; renaming it
	// is how its name catches up with the directory's.
	if !strings.EqualFold(src, dst) {
		if _, err := os.Stat(dst); err == nil {
			return "", fmt.Errorf("claude already keeps data for %s in %s; merge or remove it, then run this again", to, dst)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	if err := os.Rename(src, dst); err != nil {
		return "", err
	}
	return dst, nil
}
