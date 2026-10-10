package workspace

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Agents change files by writing blocks in their post; the office applies them and leaves a short note.
// Text blocks work with every connector, need no tool calling, and models produce long content in them
// far more reliably than inside tool-call arguments.
//
//	<file path="jokes.md">whole content</file>           create or replace
//	<edit path="jokes.md"><old>exact text</old><new>replacement</new></edit>
//	<move from="a.md" to="b.md"/>
//	<trash path="old.md"/>
const Protocol = `Files live in the shared output folder. Change them by writing blocks in your post:
<file path="name.md">the whole content</file> creates or replaces a file.
<edit path="name.md"><old>exact text from the file</old><new>replacement</new></edit> changes one part; use it for small changes instead of rewriting.
<move from="a.md" to="b.md"/> renames, <trash path="a.md"/> throws a file away.`

var (
	attr       = `\s*=\s*["']?([^"'>\s]+)["']?`
	fileRe     = regexp.MustCompile(`(?s)<file\s+path` + attr + `\s*>\n?(.*?)(?:</file>|$)`)
	editRe     = regexp.MustCompile(`(?s)<edit\s+path` + attr + `\s*>\s*<old>(.*?)</old>\s*<new>(.*?)</new>\s*</edit>`)
	moveRe     = regexp.MustCompile(`<move\s+from` + attr + `\s+to` + attr + `\s*/?>(?:</move>)?`)
	trashRe    = regexp.MustCompile(`<trash\s+path` + attr + `\s*/?>(?:</trash>)?`)
	numberedRe = regexp.MustCompile(`(?m)^\s*\d+\s*[│|:]\s?`)
)

// Apply carries out every block in text and returns the text with each block replaced by a note,
// plus the files that changed. File blocks for a path in keep are not written: their content stays in
// the text (a contributor's part of a file someone else assembles).
func (w *Workspace) Apply(text string, keep ...string) (string, []string) {
	var changed []string
	note := func(p string, err error, done string) string {
		if err != nil {
			return "⚠️ " + err.Error()
		}
		changed = append(changed, p)
		return done
	}
	text = fileRe.ReplaceAllStringFunc(text, func(b string) string {
		m := fileRe.FindStringSubmatch(b)
		content := stripLineNumbers(strings.TrimRight(m[2], "\n ")) + "\n"
		if c, err := Clean(m[1]); err == nil && slices.Contains(keep, c) {
			return "<result>\n" + strings.TrimSpace(content) + "\n</result>"
		}
		p, err := w.Save(m[1], content)
		return note(p, err, fmt.Sprintf("📄 saved `%s` (%d lines)", p, Lines(content)))
	})
	text = editRe.ReplaceAllStringFunc(text, func(b string) string {
		m := editRe.FindStringSubmatch(b)
		p, err := w.Edit(m[1], strings.Trim(m[2], "\n"), strings.Trim(m[3], "\n"))
		if err != nil {
			err = fmt.Errorf("couldn't edit %s: %w", m[1], err)
		}
		return note(p, err, fmt.Sprintf("✏️ edited `%s`", p))
	})
	text = moveRe.ReplaceAllStringFunc(text, func(b string) string {
		m := moveRe.FindStringSubmatch(b)
		p, err := w.Move(m[1], m[2])
		return note(p, err, fmt.Sprintf("📦 moved `%s` → `%s`", m[1], p))
	})
	text = trashRe.ReplaceAllStringFunc(text, func(b string) string {
		m := trashRe.FindStringSubmatch(b)
		p, err := w.Trash(m[1])
		if err != nil {
			return "⚠️ " + err.Error()
		}
		return fmt.Sprintf("🗑 trashed `%s`", p)
	})
	return text, changed
}

// stripLineNumbers removes "12│ " style prefixes when every line has one: models copy them from listings.
func stripLineNumbers(s string) string {
	lines := strings.Split(s, "\n")
	for _, l := range lines {
		if strings.TrimSpace(l) != "" && !numberedRe.MatchString(l) {
			return s
		}
	}
	return numberedRe.ReplaceAllString(s, "")
}
