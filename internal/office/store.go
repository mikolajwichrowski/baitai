package office

import (
	"bytes"
	"encoding/json"
	"os"

	"github.com/mikolajwichrowski/baitai/internal/llm"
)

// The saved state stores every entry once; desks, threads and the feed refer to entries by index,
// so an entry shown in several places stays one entry after loading.
type snapshot struct {
	Version int
	Entries []*Entry
	Boss    []int
	Chat    []llm.Message
	Agents  []savedAgent
	Forums  []savedForum
	Feed    []int
	NextID  int
}

const stateVersion = 2

type savedAgent struct {
	A   *Agent
	Log []int
}

type savedThread struct {
	T     *Thread
	Posts []int
}

type savedForum struct {
	Name    string
	Threads []savedThread
}

// Save writes the state atomically when it changed since the last save.
func (o *Office) Save() error {
	o.Lock()
	s := snapshot{Version: stateVersion}
	ids := map[*Entry]int{}
	ref := func(list []*Entry) []int {
		out := make([]int, 0, len(list))
		for _, e := range list {
			id, ok := ids[e]
			if !ok {
				id = len(s.Entries)
				ids[e] = id
				s.Entries = append(s.Entries, e)
			}
			out = append(out, id)
		}
		return out
	}
	s.Boss, s.Chat, s.NextID = ref(o.Boss.Log), o.chat, o.nextID
	for _, a := range o.Agents {
		s.Agents = append(s.Agents, savedAgent{a, ref(a.Log)})
	}
	for _, f := range o.Forums {
		sf := savedForum{Name: f.Name}
		for _, t := range f.Threads {
			sf.Threads = append(sf.Threads, savedThread{t, ref(t.Posts)})
		}
		s.Forums = append(s.Forums, sf)
	}
	s.Feed = ref(o.Feed)
	b, err := json.Marshal(s)
	o.Unlock()
	if err != nil || bytes.Equal(b, o.lastSave) {
		return err
	}
	tmp := o.path("state.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	o.lastSave = b
	return os.Rename(tmp, o.path("state.json"))
}

type versionError struct{}

func (versionError) Error() string { return "saved state is from another version" }

func (o *Office) load() error {
	b, err := os.ReadFile(o.path("state.json"))
	if err != nil {
		return err
	}
	var s snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s.Version != stateVersion {
		return versionError{}
	}
	deref := func(ids []int) []*Entry {
		var out []*Entry
		for _, id := range ids {
			if id >= 0 && id < len(s.Entries) && s.Entries[id] != nil {
				out = append(out, s.Entries[id])
			}
		}
		return out
	}
	o.Boss = &Agent{Name: "boss", Role: "boss", Log: deref(s.Boss)}
	o.chat, o.nextID, o.Feed = s.Chat, s.NextID, deref(s.Feed)
	for _, sa := range s.Agents {
		if sa.A != nil {
			sa.A.Log = deref(sa.Log)
			o.Agents = append(o.Agents, sa.A)
		}
	}
	for _, sf := range s.Forums {
		f := &Forum{Name: sf.Name}
		for _, st := range sf.Threads {
			if st.T != nil {
				st.T.Posts = deref(st.Posts)
				st.T.Closed = true // work that was running when the app quit doesn't resume
				f.Threads = append(f.Threads, st.T)
			}
		}
		o.Forums = append(o.Forums, f)
	}
	if len(o.Forums) == 0 {
		o.Forums = []*Forum{{Name: "general"}}
	}
	o.backroom()
	o.lastSave = b
	return nil
}
