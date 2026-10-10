// Package office is the simulation: a boss you chat with, a team of agents with traits and scores,
// forums where tasks are worked out, and the output folder they write to.
package office

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mikolajwichrowski/baitai/internal/config"
	"github.com/mikolajwichrowski/baitai/internal/llm"
	"github.com/mikolajwichrowski/baitai/internal/workspace"
)

const (
	StartScore = 5
	MaxScore   = 10
	maxForums  = 10
)

// Entry kinds decide how the UI styles an entry.
const (
	KindYou       = "you"
	KindBoss      = "boss"
	KindAgent     = "agent"
	KindThinking  = "thinking"
	KindNote      = "note"
	KindScoreUp   = "score+"
	KindScoreDown = "score-"
	KindError     = "error"
)

var (
	Traits     = []string{"diligence", "creativity", "caution", "teamwork", "verbosity"}
	firstNames = []string{"Mira", "Kai", "Juno", "Otto", "Wren", "Ezra", "Nova", "Iris", "Felix", "Lena", "Rex", "Tess", "Bram", "Yuki", "Sol", "Ada", "Nico", "Pia"}
	quirks     = []string{
		"terse and to the point", "loves bullet lists", "uses sports metaphors", "always double-checks numbers",
		"slightly sarcastic", "explains reasoning explicitly", "enthusiastic, uses exclamation marks",
		"dry British humour", "thinks out loud step by step", "warm and encouraging",
	}
	// Palette holds the agents' colours; the seed picks one.
	Palette     = []string{"#F472B6", "#FB923C", "#FACC15", "#4ADE80", "#2DD4BF", "#60A5FA", "#A78BFA", "#F87171"}
	defaultBase = map[string]string{
		"researcher": "You find facts, options and trade-offs.",
		"coder":      "You write working, minimal code with brief notes.",
		"writer":     "You write clear, well-structured prose.",
		"critic":     "You review work for flaws, risks and missing pieces.",
	}
	agentNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,19}$`)
	forumNameRe = regexp.MustCompile(`[^a-z0-9-]+`)
)

// Entry is one message anywhere in the office; the same entry can show on a desk, in a thread and in "all".
type Entry struct {
	Who, Kind, Where, Text string
	Result                 string `json:",omitempty"` // an agent's deliverable, from <result>…</result>
	Col                    string `json:",omitempty"` // agent colour
}

type Agent struct {
	Name, Role, Quirk string
	Base              string // editable base prompt
	Seed              int64
	Stats             map[string]int
	Score             int
	History           []string // why the score changed, newest last
	Legend            bool
	Col               string
	Inherited         []string `json:",omitempty"` // backroom notes passed down from the legend they take after

	Log             []*Entry `json:"-"` // the desk: everything the agent wrote, thought or was told
	Working, Unread bool     `json:"-"`
}

// Assignment is one person's part of a task.
type Assignment struct{ Agent, Todo string }

// Thread is a forum thread. Boss threads are tasks with a goal, parts and a lead; yours are discussions.
type Thread struct {
	ID      int
	Title   string
	Owner   string // "boss" or "you"
	Closed  bool
	Request string // the client's words that started it
	Goal    string // the full requirement as the boss understood it
	File    string // the deliverable file, when one was asked for
	Lead    string // who assembles the final deliverable
	Assign  []Assignment
	Files   []string // files changed while working on it

	Posts     []*Entry `json:"-"`
	Running   bool     `json:"-"`
	queue     []handoff
	volunteer bool // the next round asks everyone, who may PASS
}

type handoff struct {
	a   *Agent
	ask string
}

type Forum struct {
	Name    string
	Threads []*Thread `json:"-"`
	Unread  bool      `json:"-"`
}

type Legend struct {
	Name, Role, Quirk, Base string
	Stats                   map[string]int
	Uses                    int
	Notes                   []string `json:",omitempty"` // their last backroom notes, handed to future hires
}

// Office holds all state. Exported fields are guarded by the embedded mutex; use Do for short changes.
type Office struct {
	sync.Mutex
	Boss     *Agent
	Agents   []*Agent
	Forums   []*Forum
	Feed     []*Entry // the "all" view
	Legends  []Legend
	Files    *workspace.Workspace
	Settings *config.Store
	Dir      string // state directory

	chat     []llm.Message // the boss's memory of your conversation
	nextID   int
	bossMu   sync.Mutex // boss turns share one memory, so they run one at a time
	provider atomic.Pointer[providerBox]
	rng      *rand.Rand
	lastSave []byte
}

type providerBox struct{ llm.Provider }

// New opens the office in stateDir with agents' files in outDir, restoring saved state when there is any.
func New(stateDir, outDir string, settings *config.Store) (*Office, error) {
	ws, err := workspace.Open(outDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, err
	}
	o := &Office{Files: ws, Settings: settings, Dir: stateDir, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
	if b, err := os.ReadFile(o.path("legends.json")); err == nil {
		json.Unmarshal(b, &o.Legends)
	}
	if err := o.load(); err != nil {
		if !os.IsNotExist(err) { // unreadable (e.g. from an older version): keep it aside, start fresh
			os.Rename(o.path("state.json"), o.path("state.json.bak"))
		}
		o.Reset()
	}
	return o, o.UseSettings()
}

// UseSettings (re)builds the model connector from the current settings.
func (o *Office) UseSettings() error {
	p, err := llm.New(o.Settings.Get())
	if err != nil {
		return err
	}
	o.provider.Store(&providerBox{p})
	return nil
}

// Provider is the current model connector.
func (o *Office) Provider() llm.Provider { return o.provider.Load().Provider }

func (o *Office) path(name string) string { return filepath.Join(o.Dir, name) }

// Do runs f under the lock.
func (o *Office) Do(f func()) {
	o.Lock()
	defer o.Unlock()
	f()
}

// Reset starts a fresh office: default team, one forum, no history. Output files are left alone.
func (o *Office) Reset() {
	o.Boss = &Agent{Name: "boss", Role: "boss"}
	o.Agents, o.Feed, o.chat, o.nextID = nil, nil, nil, 0
	o.Forums = []*Forum{{Name: "general"}}
	for _, r := range []string{"researcher", "coder", "writer", "critic"} {
		o.Hire(r)
	}
	o.backroom()
}

// ---------- roster (everything below expects the lock to be held) ----------

// say adds to your conversation with the boss, the only thing the boss tab shows.
func (o *Office) say(e *Entry) {
	o.Boss.Log = append(o.Boss.Log, e)
	o.Boss.Unread = true
	o.Feed = append(o.Feed, e)
}

// Say adds a message to the boss conversation (used by the UI for errors).
func (o *Office) Say(kind, text string) { o.say(&Entry{Who: kind, Kind: kind, Text: text}) }

// Note leaves an office announcement in "all".
func (o *Office) Note(text string) {
	o.Feed = append(o.Feed, &Entry{Who: "office", Kind: KindNote, Text: text})
}

func (o *Office) toDesk(a *Agent, e *Entry, feed bool) {
	a.Log = append(a.Log, e)
	a.Unread = true
	if feed {
		o.Feed = append(o.Feed, e)
	}
}

// Find matches an agent by name, falling back to role so the boss can assign a hire it hasn't met yet.
func (o *Office) Find(name string) *Agent {
	name = strings.Trim(strings.TrimSpace(name), "@:,.")
	for _, a := range o.Agents {
		if strings.EqualFold(a.Name, name) {
			return a
		}
	}
	for _, a := range o.Agents {
		if strings.EqualFold(a.Role, name) {
			return a
		}
	}
	return nil
}

func (o *Office) Alive(a *Agent) bool { return slices.Contains(o.Agents, a) }

// Reseed sets the seed and re-derives what it controls: quirk and colour.
func Reseed(a *Agent, seed int64) {
	r := rand.New(rand.NewSource(seed))
	a.Seed, a.Quirk, a.Col = seed, quirks[r.Intn(len(quirks))], Palette[r.Intn(len(Palette))]
}

// Hire adds an agent for role. Half the time a new hire takes after the least-used legend of that role,
// with seed jitter so no two hires are the same.
func (o *Office) Hire(role string) *Agent {
	a := &Agent{Role: role, Score: StartScore, Stats: map[string]int{}, Base: defaultBase[role]}
	if a.Base == "" {
		a.Base = "You are the office's " + role + "."
	}
	Reseed(a, o.rng.Int63n(100000))
	r := rand.New(rand.NewSource(a.Seed + 1))
	for _, n := range r.Perm(len(firstNames)) {
		if o.Find(firstNames[n]) == nil {
			a.Name = firstNames[n]
			break
		}
	}
	if a.Name == "" {
		a.Name = fmt.Sprintf("%s%d", firstNames[r.Intn(len(firstNames))], a.Seed%100)
	}
	var base *Legend
	if l := o.pickLegend(role); l != nil && r.Intn(2) == 0 {
		base = l
		l.Uses++
		o.SaveLegends()
		a.Base = l.Base
		a.Inherited = slices.Clone(l.Notes)
		a.History = append(a.History, "modelled after legend "+l.Name)
	}
	for _, t := range Traits {
		if base != nil {
			a.Stats[t] = clamp(base.Stats[t]+r.Intn(5)-2, 1, 10)
		} else {
			a.Stats[t] = 1 + r.Intn(10)
		}
	}
	o.Agents = append(o.Agents, a)
	return a
}

func (o *Office) pickLegend(role string) *Legend {
	var best *Legend
	for i := range o.Legends {
		if l := &o.Legends[i]; strings.EqualFold(l.Role, role) && (best == nil || l.Uses < best.Uses) {
			best = l
		}
	}
	return best
}

func (o *Office) SaveLegends() {
	b, _ := json.MarshalIndent(o.Legends, "", "  ")
	os.WriteFile(o.path("legends.json"), b, 0o644)
}

// Rename gives an agent a new name; names stay unique and @mention-able.
func (o *Office) Rename(a *Agent, name string) bool {
	if !agentNameRe.MatchString(name) || strings.EqualFold(name, "boss") || strings.EqualFold(name, "you") || strings.EqualFold(name, "handoff") {
		return false
	}
	for _, x := range o.Agents {
		if x != a && strings.EqualFold(x.Name, name) {
			return false
		}
	}
	o.Note(fmt.Sprintf("✏️ **%s** is now called **%s**", a.Name, name))
	a.Name = name
	return true
}

func (o *Office) Fire(a *Agent, reason string) {
	o.Agents = slices.DeleteFunc(o.Agents, func(x *Agent) bool { return x == a })
	o.Note(fmt.Sprintf("🔥 **%s** (%s) was fired: %s", a.Name, a.Role, reason))
}

// Rescore changes a score with a reason the agent sees. 0 fires them; 10 makes them a legend.
func (o *Office) Rescore(a *Agent, delta int, reason string) {
	if delta == 0 {
		return
	}
	a.Score = clamp(a.Score+delta, 0, MaxScore)
	line := fmt.Sprintf("%+d → %d/10: %s", delta, a.Score, reason)
	a.History = append(a.History, line)
	kind := KindScoreUp
	if delta < 0 {
		kind = KindScoreDown
	}
	o.toDesk(a, &Entry{Who: "score", Kind: kind, Where: a.Name, Text: line}, true)
	switch {
	case a.Score == 0:
		o.Fire(a, "score hit 0")
	case a.Score == MaxScore && !a.Legend:
		a.Legend = true
		stats := make(map[string]int, len(a.Stats))
		for k, v := range a.Stats {
			stats[k] = v
		}
		o.Legends = append(o.Legends, Legend{a.Name, a.Role, a.Quirk, a.Base, stats, 0, o.notesBy(a.Name, 3)})
		o.SaveLegends()
		o.Note(fmt.Sprintf("🏆 **%s** reached 10/10 and became a legend; future %ss may take after them", a.Name, a.Role))
	}
}

func traitLine(a *Agent) string {
	parts := make([]string, len(Traits))
	for i, t := range Traits {
		parts[i] = fmt.Sprintf("%s %d", t, a.Stats[t])
	}
	return strings.Join(parts, ", ")
}

// ---------- forums ----------

func forumName(s string) string {
	return clean(strings.Trim(forumNameRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "#"))), "-"), "-"), 24)
}

func (o *Office) ForumByName(name string) *Forum {
	name = forumName(name)
	for _, f := range o.Forums {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func (o *Office) NewForum(name string) *Forum {
	name = forumName(name)
	if name == "" || o.ForumByName(name) != nil || len(o.Forums) >= maxForums {
		return nil
	}
	f := &Forum{Name: name}
	o.Forums = append(o.Forums, f)
	o.Note("🗂 New forum **#" + name + "**")
	return f
}

func (o *Office) RenameForum(f *Forum, name string) bool {
	if name = forumName(name); name == "" || o.ForumByName(name) != nil {
		return false
	}
	f.Name = name
	return true
}

// WipeForum removes every thread; running work stops at its next step.
func (o *Office) WipeForum(f *Forum) {
	for _, t := range f.Threads {
		t.Closed = true
	}
	f.Threads = nil
}

func (o *Office) DeleteForum(f *Forum) {
	o.WipeForum(f)
	o.Forums = slices.DeleteFunc(o.Forums, func(x *Forum) bool { return x == f })
}

// Backroom is the forum with the agents' private thread: the boss never reads it, you do.
const Backroom = "backroom"

// backroom returns the backroom thread, creating it when missing.
func (o *Office) backroom() *Thread {
	f := o.ForumByName(Backroom)
	if f == nil {
		f = &Forum{Name: Backroom}
		o.Forums = append(o.Forums, f)
	}
	if len(f.Threads) == 0 {
		o.openThread(f, "the backroom", Backroom)
	}
	return f.Threads[0]
}

// notesBy is an agent's last n backroom notes.
func (o *Office) notesBy(name string, n int) []string {
	var out []string
	for _, e := range o.backroom().Posts {
		if e.Who == name {
			out = append(out, clean(e.Text, 500))
		}
	}
	return out[max(0, len(out)-n):]
}

// WipeChat clears your conversation and the boss's memory of it.
func (o *Office) WipeChat() { o.Boss.Log, o.chat = nil, nil }

func (o *Office) thread(id int) (*Forum, *Thread) {
	for _, f := range o.Forums {
		for _, t := range f.Threads {
			if t.ID == id {
				return f, t
			}
		}
	}
	return nil, nil
}

func (o *Office) forumOf(t *Thread) *Forum {
	for _, f := range o.Forums {
		if slices.Contains(f.Threads, t) {
			return f
		}
	}
	return &Forum{} // forum deleted meanwhile: post into a detached one
}

func (o *Office) openThread(f *Forum, title, owner string) *Thread {
	o.nextID++
	t := &Thread{ID: o.nextID, Title: title, Owner: owner}
	f.Threads = append(f.Threads, t)
	return t
}

func (o *Office) post(t *Thread, e *Entry) {
	f := o.forumOf(t)
	e.Where = fmt.Sprintf("#%s · %d %s", f.Name, t.ID, t.Title)
	t.Posts = append(t.Posts, e)
	if !slices.Contains(o.Feed, e) {
		o.Feed = append(o.Feed, e)
	}
	f.Unread = true
}

func clean(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }
