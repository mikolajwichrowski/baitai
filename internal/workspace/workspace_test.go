package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Workspace {
	w, err := Open(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestPathsStayInsideTheFolder(t *testing.T) {
	w := open(t)
	for _, p := range []string{"../escape.txt", "/etc/passwd", "a/../../x.txt"} {
		if _, err := w.Save(p, "x"); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(w.Dir()), "escape.txt")); err == nil {
		t.Fatal("escaped the output folder")
	}
	if _, err := w.Save(".trash/x", "x"); err == nil {
		t.Fatal("the trash must be off limits")
	}
}

func TestApplyBlocks(t *testing.T) {
	w := open(t)
	text, changed := w.Apply("Here.\n<file path=\"jokes.md\">\n1│ one\n2│ two\n</file>\n<result>done</result>")
	if got := w.Read("jokes.md"); got != "one\ntwo\n" {
		t.Fatalf("line numbers copied from a listing must be stripped, got %q", got)
	}
	if strings.Contains(text, "1│") || !strings.Contains(text, "saved `jokes.md` (2 lines)") || len(changed) != 1 {
		t.Fatalf("text=%q changed=%v", text, changed)
	}
	if text, _ := w.Apply(`<edit path="jokes.md"><old>tw</old><new>thre</new></edit>`); !strings.Contains(text, "edited") || w.Read("jokes.md") != "one\nthreo\n" {
		t.Fatalf("edit: %q %q", text, w.Read("jokes.md"))
	}
	if text, _ := w.Apply(`<edit path="jokes.md"><old>o</old><new>0</new></edit>`); !strings.Contains(text, "appears 2 times") {
		t.Fatalf("ambiguous edit must be refused: %q", text)
	}
	w.Apply(`<move from="jokes.md" to="fun/jokes.md"/> <trash path="fun/jokes.md"/>`)
	if len(w.Paths()) != 0 {
		t.Fatalf("paths: %v", w.Paths())
	}
	trash, _ := os.ReadDir(filepath.Join(w.Dir(), TrashDir))
	if len(trash) != 2 { // the version before the edit, and the trashed file
		t.Fatalf("trash has %d files", len(trash))
	}
}

func TestPathAtPicksTheClickedName(t *testing.T) {
	w := open(t)
	w.Save("a.md", "x")
	w.Save("data.md", "x")
	line := "📄 `a.md`, `data.md`"
	if p := w.PathAt(line, 4); p != "a.md" {
		t.Fatalf("got %q", p)
	}
	if p := w.PathAt(line, 14); p != "data.md" {
		t.Fatalf("got %q", p)
	}
	if p := w.PathAt("nothing here", 3); p != "" {
		t.Fatalf("got %q", p)
	}
}
