// Package workspace is the agents' only access to the disk: one output folder they can create, edit,
// move and trash files in. os.Root confines every operation to the folder (no "..", no symlink escapes),
// and nothing is ever deleted: trashed files and the version before every change go to .trash.
package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

const (
	TrashDir    = ".trash"
	MaxFileSize = 256 << 10
)

type Workspace struct{ root *os.Root }

func Open(dir string) (*Workspace, error) {
	if err := os.MkdirAll(filepath.Join(dir, TrashDir), 0o755); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Workspace{r}, nil
}

// Dir is the folder's path as opened.
func (w *Workspace) Dir() string { return w.root.Name() }

// Clean turns an agent-supplied path into a safe relative one, refusing the trash.
func Clean(p string) (string, error) {
	p = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(p)), "output/")
	c := strings.TrimPrefix(path.Clean("/"+p), "/")
	if c == "" {
		return "", errors.New("path required")
	}
	if c == TrashDir || strings.HasPrefix(c, TrashDir+"/") {
		return "", errors.New("the trash is off limits")
	}
	return c, nil
}

func (w *Workspace) Exists(p string) bool { _, err := w.root.Lstat(p); return err == nil }

// Paths lists files outside the trash, sorted.
func (w *Workspace) Paths() []string {
	var out []string
	fs.WalkDir(w.root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == "." {
			return nil
		}
		if p == TrashDir {
			return fs.SkipDir
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// List describes the folder for prompts: one "path (N lines)" per file.
func (w *Workspace) List() string {
	var b strings.Builder
	for _, p := range w.Paths() {
		data, _ := w.root.ReadFile(p)
		fmt.Fprintf(&b, "%s (%d lines)\n", p, Lines(string(data)))
	}
	if b.Len() == 0 {
		return "(empty)"
	}
	return strings.TrimSpace(b.String())
}

// Read returns a file's content, or "" when it doesn't exist.
func (w *Workspace) Read(p string) string {
	c, err := Clean(p)
	if err != nil {
		return ""
	}
	data, _ := w.root.ReadFile(c)
	return string(data)
}

// ModKey changes whenever the file changes; the viewer uses it as a cache key.
func (w *Workspace) ModKey(p string) (string, bool) {
	info, err := w.root.Stat(p)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s|%d|%d", p, info.ModTime().UnixNano(), info.Size()), true
}

// Save writes a whole file; an existing version is kept in the trash first.
func (w *Workspace) Save(p, content string) (string, error) {
	p, err := Clean(p)
	if err != nil {
		return "", err
	}
	if len(content) > MaxFileSize {
		return "", errors.New("file too large")
	}
	if old, err := w.root.ReadFile(p); err == nil {
		if err := w.snapshot(p, old); err != nil {
			return "", err
		}
	}
	if dir := path.Dir(p); dir != "." {
		if err := w.root.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	return p, w.root.WriteFile(p, []byte(content), 0o644)
}

// Edit replaces one exact snippet, so changing a word costs a word, not a rewrite.
func (w *Workspace) Edit(p, old, new string) (string, error) {
	p, err := Clean(p)
	if err != nil {
		return "", err
	}
	data, err := w.root.ReadFile(p)
	if err != nil {
		return "", errors.New("no such file")
	}
	s := string(data)
	switch n := strings.Count(s, old); {
	case old == "":
		return "", errors.New("the old text is empty")
	case n == 0:
		return "", errors.New("the old text is not in the file; copy it exactly")
	case n > 1:
		return "", fmt.Errorf("the old text appears %d times; include more of the surrounding text", n)
	}
	if err := w.snapshot(p, data); err != nil {
		return "", err
	}
	return p, w.root.WriteFile(p, []byte(strings.Replace(s, old, new, 1)), 0o644)
}

func (w *Workspace) Move(from, to string) (string, error) {
	from, err := Clean(from)
	if err != nil {
		return "", err
	}
	if to, err = Clean(to); err != nil {
		return "", err
	}
	if !w.Exists(from) {
		return "", errors.New("no such file")
	}
	if w.Exists(to) {
		return "", errors.New("the destination exists")
	}
	if dir := path.Dir(to); dir != "." {
		if err := w.root.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}
	return to, w.root.Rename(from, to)
}

// Trash moves a file into .trash; it is kept, never destroyed.
func (w *Workspace) Trash(p string) (string, error) {
	p, err := Clean(p)
	if err != nil {
		return "", err
	}
	if !w.Exists(p) {
		return "", errors.New("no such file")
	}
	return p, w.root.Rename(p, trashName(p))
}

func (w *Workspace) snapshot(p string, data []byte) error {
	return w.root.WriteFile(trashName(p), data, 0o644)
}

func trashName(p string) string {
	return path.Join(TrashDir, fmt.Sprintf("%d-%s", time.Now().UnixNano(), strings.ReplaceAll(p, "/", "__")))
}

// Lines counts lines the way an editor shows them.
func Lines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimRight(s, "\n"), "\n") + 1
}

// PathAt finds the file path mentioned in a line of text closest to column col (longest wins on ties).
func (w *Workspace) PathAt(line string, col int) string {
	best, bestDist := "", 1<<30
	for _, p := range w.Paths() {
		for off := 0; ; {
			i := strings.Index(line[off:], p)
			if i < 0 {
				break
			}
			start := ansi.StringWidth(line[:off+i])
			end := start + ansi.StringWidth(p)
			d := 0
			if col < start {
				d = start - col
			} else if col >= end {
				d = col - end + 1
			}
			if d < bestDist || d == bestDist && len(p) > len(best) {
				best, bestDist = p, d
			}
			off += i + len(p)
		}
	}
	if bestDist > 20 { // a click far from any name is not a file click
		return ""
	}
	return best
}
