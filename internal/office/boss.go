package office

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mikolajwichrowski/baitai/internal/llm"
	"github.com/mikolajwichrowski/baitai/internal/workspace"
)

const bossRules = `You run a small AI office and you're chatting with the user, your client. Be friendly, plain and brief, and talk only to the client: never comment on your staff in your reply, never mention JSON or internal mechanics.

Your team:
%s
Recent tasks:
%s
Files in the output folder:
%s

How you work:
- Small talk or questions about the office or earlier results: answer yourself, no actions.
- Any request for work or content: create ONE task. Write a goal that contains every requirement the client gave (counts, format, file name, who should do what). Give each person their own todo. Pick a lead who combines the parts. If the client wants a file, set "file" to a sensible file name.
- If the client complains that earlier work was incomplete or wrong, create a new task that fixes it, with a goal that says exactly what was wrong and what the result must be.
- Your reply only says who is on it, e.g. "Sure, Pia is writing it. I'll get back to you." The team works in the background; when they're done you review it and the result goes to the client automatically. Never invent results.
- Hire or fire when the client asks, or when the team lacks a skill or someone is useless (max %d agents). Adjust a score when the client asks.

Respond ONLY with JSON: {"reply":"message to the client","actions":[...]}
Actions:
{"type":"task","forum":"general","title":"short title","goal":"every requirement","file":"name.md or empty","lead":"Name","assign":[{"agent":"Name or role","todo":"their part"}]}
{"type":"hire","role":"role"}
{"type":"fire","agent":"Name","reason":"why"}
{"type":"score","agent":"Name","delta":-3..3,"reason":"why"}

Example. Client: "make a file where everyone writes two haikus about autumn"
{"reply":"Sure, the whole team is on it; Mira will put it together.","actions":[{"type":"task","forum":"general","title":"Autumn haikus","goal":"A file autumn-haikus.md with two haikus about autumn from every team member (Mira, Kai, Juno, Otto), 8 haikus in total, each labelled with its author.","file":"autumn-haikus.md","lead":"Mira","assign":[{"agent":"Mira","todo":"write 2 autumn haikus"},{"agent":"Kai","todo":"write 2 autumn haikus"},{"agent":"Juno","todo":"write 2 autumn haikus"},{"agent":"Otto","todo":"write 2 autumn haikus"}]}]}`

// flex accepts a JSON number or string, because small models mix them up ("delta":"+2").
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) != nil {
		s = string(b)
	}
	*f = flex(strings.TrimSpace(s))
	return nil
}

func (f flex) int() int {
	n, _ := strconv.Atoi(strings.TrimPrefix(string(f), "#"))
	return n
}

// assignList accepts [{"agent","todo"}] as well as ["Name", …].
type assignList []Assignment

func (l *assignList) UnmarshalJSON(b []byte) error {
	var objs []struct{ Agent, Name, Todo, Task string }
	if json.Unmarshal(b, &objs) == nil {
		for _, x := range objs {
			*l = append(*l, Assignment{Agent: x.Agent + x.Name, Todo: x.Todo + x.Task})
		}
		return nil
	}
	var names []string
	json.Unmarshal(b, &names)
	for _, n := range names {
		*l = append(*l, Assignment{Agent: n})
	}
	return nil
}

type action struct {
	Type, Forum, Title, Goal, File, Lead, Role, Agent, Reason string
	Delta                                                     flex
	Assign                                                    assignList
}

// parseJSON finds the JSON object in a model's answer: some connectors wrap it in prose or code fences.
func parseJSON(raw string, v any) bool {
	if json.Unmarshal([]byte(raw), v) == nil {
		return true
	}
	i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	return i >= 0 && j > i && json.Unmarshal([]byte(raw[i:j+1]), v) == nil
}

// parseBoss reads the boss's JSON leniently: a bad action is skipped, never the whole reply.
func parseBoss(raw string) (string, []action, bool) {
	var out struct {
		Reply   string
		Actions []json.RawMessage
	}
	if !parseJSON(raw, &out) {
		if strings.Contains(raw, "{") {
			return "", nil, false
		}
		return strings.TrimSpace(raw), nil, true // plain text from a connector without JSON mode
	}
	var acts []action
	for _, r := range out.Actions {
		var a action
		if json.Unmarshal(r, &a) == nil {
			acts = append(acts, a)
		}
	}
	return strings.TrimSpace(out.Reply), acts, true
}

func (o *Office) recentTasks() string {
	var tasks []*Thread
	for _, f := range o.Forums {
		for _, t := range f.Threads {
			if t.Owner == "boss" {
				tasks = append(tasks, t)
			}
		}
	}
	slices.SortFunc(tasks, func(a, b *Thread) int { return a.ID - b.ID })
	var b strings.Builder
	for _, t := range tasks[max(0, len(tasks)-5):] {
		state := "done"
		if t.Running {
			state = "in progress"
		}
		var who []string
		for _, as := range t.Assign {
			who = append(who, as.Agent)
		}
		fmt.Fprintf(&b, "- task %d %q (%s; %s", t.ID, t.Title, state, strings.Join(who, ", "))
		if t.File != "" {
			fmt.Fprintf(&b, "; file %s", t.File)
		}
		fmt.Fprintf(&b, ")\n  goal: %s\n", clean(t.Goal, 300))
	}
	if b.Len() == 0 {
		return "(none yet)"
	}
	return b.String()
}

func (o *Office) roster() string {
	var b strings.Builder
	for _, a := range o.Agents {
		fmt.Fprintf(&b, "- %s (%s) score %d/10; %s\n", a.Name, a.Role, a.Score, traitLine(a))
	}
	if len(o.Agents) == 0 {
		return "(nobody; hire someone)"
	}
	return b.String()
}

// apply validates the boss's actions (model output is untrusted) and returns the task it created, if any.
func (o *Office) apply(acts []action, request string) *Thread {
	var task *Thread
	for _, ac := range acts {
		switch strings.ToLower(ac.Type) {
		case "hire":
			if role := strings.ToLower(clean(ac.Role, 40)); role != "" && len(o.Agents) < o.Settings.Get().MaxAgents {
				a := o.Hire(role)
				o.Note(fmt.Sprintf("🤝 Hired **%s** as %s (seed %d)", a.Name, a.Role, a.Seed))
			}
		case "fire":
			if a := o.Find(ac.Agent); a != nil {
				o.Fire(a, clean(ac.Reason, 200))
			}
		case "score":
			if a := o.Find(ac.Agent); a != nil {
				o.Rescore(a, clamp(ac.Delta.int(), -3, 3), clean(ac.Reason, 200))
			}
		case "task", "thread", "post":
			if task == nil { // one request, one task
				task = o.newTask(ac, request)
			}
		}
	}
	return task
}

var wantFileRe = regexp.MustCompile(`(?i)\bfiles?\b|\.(md|txt|csv|json|html|py|go|js|ts)\b`)

func (o *Office) newTask(ac action, request string) *Thread {
	if len(o.Agents) == 0 {
		return nil
	}
	goal := clean(ac.Goal, 3000)
	if goal == "" {
		goal = clean(request, 3000)
	}
	title := clean(ac.Title, 60)
	if title == "" {
		title = clean(strings.SplitN(goal, "\n", 2)[0], 60)
	}
	t := &Thread{Owner: "boss", Request: clean(request, 2000), Goal: goal}
	everyone := false
	for _, as := range ac.Assign {
		switch strings.ToLower(strings.TrimSpace(as.Agent)) {
		case "all", "everyone", "team", "the team":
			everyone = true
			for _, a := range o.Agents {
				t.Assign = append(t.Assign, Assignment{a.Name, as.Todo})
			}
			continue
		}
		if a := o.Find(as.Agent); a != nil && !slices.ContainsFunc(t.Assign, func(x Assignment) bool { return x.Agent == a.Name }) && !everyone {
			t.Assign = append(t.Assign, Assignment{a.Name, clean(as.Todo, 500)})
		}
	}
	if len(t.Assign) == 0 {
		t.Assign = []Assignment{{o.Agents[0].Name, ""}} // someone has to pick it up
	}
	for i := range t.Assign {
		if t.Assign[i].Todo == "" {
			t.Assign[i].Todo = "the whole task"
		}
	}
	if a := o.Find(ac.Lead); a != nil && slices.ContainsFunc(t.Assign, func(x Assignment) bool { return x.Agent == a.Name }) {
		t.Lead = a.Name
	} else {
		t.Lead = t.Assign[0].Agent
	}
	if f, err := workspace.Clean(ac.File); err == nil && ac.File != "" {
		t.File = f
	} else if wantFileRe.MatchString(request) {
		t.File = slug(title) + ".md" // the client asked for a file but the boss didn't name one
	}
	if path.Ext(t.File) == "" && t.File != "" {
		t.File += ".md"
	}
	f := o.ForumByName(ac.Forum)
	if f != nil && f.Name == Backroom {
		f = nil // the boss doesn't know the backroom exists
	}
	if f == nil {
		if f = o.NewForum(ac.Forum); f == nil {
			f = o.Forums[0]
		}
	}
	o.nextID++
	t.ID, t.Title = o.nextID, title
	f.Threads = append(f.Threads, t)
	o.post(t, &Entry{Who: "boss", Kind: KindBoss, Text: kickoff(t)})
	return t
}

func kickoff(t *Thread) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**Goal:** %s\n", t.Goal)
	if t.File != "" {
		fmt.Fprintf(&b, "**Deliverable:** the file `%s`\n", t.File)
	}
	b.WriteString("\n")
	for _, as := range t.Assign {
		fmt.Fprintf(&b, "- @%s: %s\n", as.Agent, as.Todo)
	}
	if len(t.Assign) > 1 {
		fmt.Fprintf(&b, "\n@%s, please combine everything into the final result.\n", t.Lead)
	}
	fmt.Fprintf(&b, "\n> The client's words: %q", t.Request)
	return b.String()
}

// onIt says who is working on a task, in one sentence fragment.
func onIt(who []string, lead string) string {
	if len(who) == 1 {
		return who[0] + " is on it"
	}
	return strings.Join(who[:len(who)-1], ", ") + " and " + who[len(who)-1] + " are on it, and " + lead + " will put it together"
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "result"
	}
	return clean(s, 40)
}

// bossCall asks the boss model for JSON, retrying once when the answer isn't readable.
func (o *Office) bossCall(system string, msgs []llm.Message) (string, error) {
	var raw string
	var err error
	for range 2 {
		raw, err = o.Provider().Chat(context.Background(), llm.Request{System: system, Messages: msgs, JSON: true})
		var probe map[string]any
		if err != nil || parseJSON(raw, &probe) {
			break
		}
	}
	return raw, err
}

// Ask handles a message from you: the boss answers right away and any task runs in the background.
func (o *Office) Ask(input string) error {
	o.bossMu.Lock()
	defer o.bossMu.Unlock()
	o.Lock()
	o.say(&Entry{Who: "you", Kind: KindYou, Text: input})
	o.chat = append(o.chat, llm.Message{Role: "user", Content: input})
	system := fmt.Sprintf(bossRules, o.roster(), o.recentTasks(), o.Files.List(), o.Settings.Get().MaxAgents)
	msgs := slices.Clone(o.chat)
	o.Boss.Working = true
	o.Unlock()

	raw, err := o.bossCall(system, msgs)

	o.Lock()
	defer o.Unlock()
	o.Boss.Working = false
	if err != nil {
		return err
	}
	reply, acts, ok := parseBoss(raw)
	if !ok {
		reply = "Sorry, I lost my train of thought there. Could you say that again?"
	}
	task := o.apply(acts, input)
	memory := reply
	if task != nil {
		var who []string
		for _, as := range task.Assign {
			who = append(who, as.Agent)
		}
		if slices.ContainsFunc(who, func(n string) bool { return !strings.Contains(reply, n) }) {
			// the reply must name everyone who really got the task, not who the model imagined
			reply = "Sure, " + onIt(who, task.Lead) + ". I'll get back to you when it's done."
		}
		memory = fmt.Sprintf("%s\n(started task %d %q with %s)", reply, task.ID, task.Title, strings.Join(who, ", "))
		task.Running = true
		go o.runTask(task)
	}
	if reply == "" {
		reply = "Okay."
	}
	o.chat = append(o.chat, llm.Message{Role: "assistant", Content: memory})
	o.say(&Entry{Who: "boss", Kind: KindBoss, Text: reply})
	return nil
}
