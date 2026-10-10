package office

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/mikolajwichrowski/baitai/internal/llm"
	"github.com/mikolajwichrowski/baitai/internal/workspace"
)

// runTask works a boss task to the end and reports to you exactly once:
//  1. every assignee does their own part, in parallel;
//  2. anyone they hand work to (@handoff Name: …) gets a turn;
//  3. the lead combines the parts into the deliverable (and writes the file, when one was asked for);
//  4. the boss checks the result against the client's request and sends it back with what's missing, or
//  5. pings you with his one-line intro and the result itself.
func (o *Office) runTask(t *Thread) {
	defer o.Do(func() { t.Running, t.Closed, t.queue = false, true, nil })
	o.contribute(t)
	o.discuss(t, o.Settings.Get().Rounds-1)
	note := ""
	for attempt := 0; ; attempt++ {
		if o.closed(t) {
			return
		}
		if attempt > 0 || o.needsAssembly(t) {
			o.assemble(t, note)
		}
		last := attempt >= o.Settings.Get().FollowUps
		if t.File != "" && strings.TrimSpace(o.Files.Read(t.File)) == "" && !last {
			note = fmt.Sprintf("The file `%s` is missing or empty. Write it with a <file path=\"%s\"> block containing the complete deliverable.", t.File, t.File)
			o.Do(func() { o.post(t, &Entry{Who: "boss", Kind: KindBoss, Text: "@" + t.Lead + " " + note}) })
			continue
		}
		v := o.review(t)
		if !v.Complete && v.Missing != "" && !last {
			note = v.Missing
			o.Do(func() { o.post(t, &Entry{Who: "boss", Kind: KindBoss, Text: "@" + t.Lead + " not done yet: " + note}) })
			continue
		}
		o.Do(func() { o.ping(t, v.Intro) })
		return
	}
}

func (o *Office) closed(t *Thread) (c bool) {
	o.Do(func() { c = t.Closed })
	return
}

func (o *Office) needsAssembly(t *Thread) (need bool) {
	o.Do(func() {
		need = len(t.Assign) > 1 || t.File != "" && !slices.Contains(t.Files, t.File)
	})
	return
}

// contribute runs every assignee on their own part at the same time.
func (o *Office) contribute(t *Thread) {
	var wg sync.WaitGroup
	for _, as := range t.Assign {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var a *Agent
			var prompt string
			o.Do(func() {
				a = o.Find(as.Agent)
				prompt = o.taskPrompt(t, "Your part: "+as.Todo+o.partNote(t), "")
			})
			if a != nil {
				var keep []string
				if len(t.Assign) > 1 && t.File != "" {
					keep = []string{t.File} // only the lead writes the deliverable; parts stay in the posts
				}
				o.work(t, a, prompt, keep...)
			}
		}()
	}
	wg.Wait()
}

func (o *Office) partNote(t *Thread) string {
	if len(t.Assign) > 1 {
		return fmt.Sprintf("\nOnly do your own part; %s combines everyone's parts afterwards, so don't write the final file yourself.", t.Lead)
	}
	if t.File != "" {
		return fmt.Sprintf("\nDeliver it as the file `%s`.", t.File)
	}
	return ""
}

// assemble has the lead turn the parts into the final deliverable, or fix what the boss found missing.
func (o *Office) assemble(t *Thread, missing string) {
	var a *Agent
	var prompt string
	o.Do(func() {
		a = o.Find(t.Lead)
		if a == nil && len(o.Agents) > 0 {
			a, t.Lead = o.Agents[0], o.Agents[0].Name
		}
		var b strings.Builder
		b.WriteString("You are the lead: produce the final deliverable that meets the goal exactly, using your teammates' parts above.")
		if t.File != "" {
			fmt.Fprintf(&b, " Write the complete file with <file path=%q>…</file>, or fix it with <edit> blocks. Put a one-line summary in <result>.", t.File)
		} else {
			b.WriteString(" Put the complete final deliverable in <result>.")
		}
		if missing != "" {
			b.WriteString("\nThe boss sent it back: " + missing)
		}
		prompt = o.taskPrompt(t, b.String(), t.File)
	})
	if a != nil {
		o.work(t, a, prompt)
	}
}

// work runs one agent turn on a task and posts the result in the thread.
func (o *Office) work(t *Thread, a *Agent, prompt string, keep ...string) {
	e, changed, err := o.turn(a, fmt.Sprintf("thread %d %s", t.ID, t.Title), prompt, keep...)
	if e == nil {
		return
	}
	o.Do(func() {
		for _, p := range changed {
			if !slices.Contains(t.Files, p) {
				t.Files = append(t.Files, p)
			}
		}
		if err == nil && !isPass(e.Text) {
			o.post(t, e)
			o.queueHandoffs(t, a, e.Text)
		}
	})
}

var handoffRe = regexp.MustCompile(`(?im)^\W*@handoff\s+@?([A-Za-z][\w-]*)\s*:?\s*(.*)$`)

// queueHandoffs gives a turn to teammates an agent explicitly handed work to. A name mentioned in
// passing does not count, and neither does handing work to someone who already has a part in the task:
// both pulled people into rounds of chatter instead of work.
func (o *Office) queueHandoffs(t *Thread, from *Agent, text string) {
	for _, m := range handoffRe.FindAllStringSubmatch(text, -1) {
		a := o.Find(m[1])
		assigned := a != nil && slices.ContainsFunc(t.Assign, func(as Assignment) bool { return as.Agent == a.Name })
		if a != nil && a != from && !assigned && !slices.ContainsFunc(t.queue, func(h handoff) bool { return h.a == a }) {
			t.queue = append(t.queue, handoff{a, strings.TrimSpace(m[2])})
		}
	}
}

// discuss gives queued agents their turn, round after round, until nobody is queued.
func (o *Office) discuss(t *Thread, rounds int) {
	for range max(rounds, 0) {
		var queue []handoff
		var volunteer bool
		o.Do(func() { queue, volunteer, t.queue, t.volunteer = t.queue, t.volunteer, nil, false })
		if len(queue) == 0 || o.closed(t) {
			return
		}
		var wg sync.WaitGroup
		for _, h := range queue {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var prompt string
				o.Do(func() {
					ask := "Someone asked for your help: " + h.ask
					switch {
					case volunteer:
						ask = "Nobody was tagged. Reply only if this clearly fits your role and nobody is better placed; otherwise reply exactly PASS."
					case t.Owner == "you":
						ask = "The client wants your answer in this thread."
					}
					prompt = o.taskPrompt(t, ask, "")
				})
				o.work(t, h.a, prompt)
			}()
		}
		wg.Wait()
	}
}

// taskPrompt is the user message for an agent turn: the task, the thread so far and the relevant files.
func (o *Office) taskPrompt(t *Thread, yourTurn, file string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %d: %s\n", t.ID, t.Title)
	if t.Goal != "" {
		fmt.Fprintf(&b, "Goal: %s\n", t.Goal)
	}
	if t.Request != "" {
		fmt.Fprintf(&b, "The client's words: %q\n", t.Request)
	}
	b.WriteString("\nThe thread so far:\n")
	for _, p := range t.Posts {
		body := p.Result
		if body == "" {
			body = p.Text
		}
		fmt.Fprintf(&b, "--- post by %s ---\n%s\n\n", p.Who, clean(body, 3000))
	}
	files := t.Files
	if file != "" && !slices.Contains(files, file) {
		files = append(files, file)
	}
	for _, f := range files {
		if c := o.Files.Read(f); c != "" {
			fmt.Fprintf(&b, "Current content of `%s`:\n```\n%s\n```\n\n", f, clean(c, 8000))
		}
	}
	var team []string
	for _, x := range o.Agents {
		team = append(team, fmt.Sprintf("%s (%s)", x.Name, x.Role))
	}
	fmt.Fprintf(&b, "Team: %s\n\n%s\nWrite your post now.", strings.Join(team, ", "), yourTurn)
	return b.String()
}

type verdict struct {
	Complete bool
	Missing  string
	Intro    string
	Scores   []struct {
		Agent, Reason string
		Delta         flex
	}
}

const reviewRules = `You run a small AI office and check your team's work before it goes to the client. Be strict: compare the work with every requirement in the client's request (counts, format, file, who should contribute). Respond ONLY with JSON:
{"complete":true|false,"missing":"if not complete: exactly what is missing or wrong, as an instruction to the team","intro":"one friendly sentence to the client introducing the result, no comments about staff","scores":[{"agent":"Name","delta":-3..3,"reason":"why"}]}
Score only people who worked on it: reward focused, complete work that fits their traits; deduct for rambling, overcomplicating, going off-task or delivering less than asked.`

// review has the boss judge the result against the request and score the people who worked on it.
func (o *Office) review(t *Thread) verdict {
	var msg string
	o.Do(func() {
		var b strings.Builder
		fmt.Fprintf(&b, "The client asked: %q\nGoal: %s\n\n", t.Request, t.Goal)
		for _, p := range t.Posts {
			if p.Kind == KindAgent {
				fmt.Fprintf(&b, "--- %s posted ---\n%s\n\n", p.Who, clean(p.Text, 2000))
			}
		}
		if t.File != "" {
			if c := o.Files.Read(t.File); c != "" {
				fmt.Fprintf(&b, "The file `%s` (%d lines):\n```\n%s\n```\n", t.File, workspace.Lines(c), clean(c, 8000))
			} else {
				fmt.Fprintf(&b, "The file `%s` does not exist.\n", t.File)
			}
		}
		msg = b.String()
	})
	raw, err := o.bossCall(reviewRules, []llm.Message{{Role: "user", Content: msg}})
	var v verdict
	if err != nil || !parseJSON(raw, &v) {
		return verdict{Complete: true} // can't judge: deliver what's there rather than loop
	}
	o.Do(func() {
		for _, s := range v.Scores {
			if a := o.Find(s.Agent); a != nil && postedIn(t, a) { // only judge who actually worked
				o.Rescore(a, clamp(s.Delta.int(), -3, 3), clean(s.Reason, 200))
			}
		}
	})
	return v
}

func postedIn(t *Thread, a *Agent) bool {
	return slices.ContainsFunc(t.Posts, func(e *Entry) bool { return e.Who == a.Name })
}

// ping tells you a task is done: the boss's intro, then the result itself, and the files.
func (o *Office) ping(t *Thread, intro string) {
	intro = clean(strings.SplitN(strings.TrimSpace(intro), "\n", 2)[0], 300)
	if intro == "" {
		intro = "The team finished **" + t.Title + "**:"
	}
	var body string
	if t.File != "" {
		body = preview(t.File, o.Files.Read(t.File))
	} else {
		for i := len(t.Posts) - 1; i >= 0 && body == ""; i-- {
			if p := t.Posts[i]; p.Kind == KindAgent {
				body = p.Result
				if body == "" {
					body = p.Text
				}
			}
		}
	}
	text := intro + "\n\n" + body
	if strings.TrimSpace(body) == "" {
		text = "Sorry, the team couldn't get **" + t.Title + "** done."
	}
	var others []string
	for _, f := range t.Files {
		if f != t.File && o.Files.Exists(f) {
			others = append(others, "`"+f+"`")
		}
	}
	if len(others) > 0 {
		text += "\n\nAlso changed: " + strings.Join(others, ", ")
	}
	o.say(&Entry{Who: "boss", Kind: KindBoss, Text: text})
	o.chat = append(o.chat, llm.Message{Role: "assistant", Content: clean(text, 1500)}) // remember what you saw
}

// preview shows a file in a ping: markdown as is, anything else as code, long files cut short.
func preview(name, content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	more := ""
	if len(lines) > 60 {
		lines, more = lines[:60], fmt.Sprintf("\n\n… %d more lines, open the file to see everything.", workspace.Lines(content)-60)
	}
	shown := strings.Join(lines, "\n")
	if ext := strings.TrimPrefix(path.Ext(name), "."); ext != "md" && ext != "markdown" && ext != "txt" {
		shown = "```" + ext + "\n" + shown + "\n```"
	}
	return fmt.Sprintf("📄 `%s`\n\n%s%s", name, shown, more)
}

var replyRe = regexp.MustCompile(`^#(\d+)\s+`)

// UserPost lets you post in a forum: "#<id> text" replies to a thread, anything else opens one.
// @mentioned agents answer; with no mentions everyone may chime in or pass.
func (o *Office) UserPost(f *Forum, text string) {
	o.Lock()
	var t *Thread
	if m := replyRe.FindStringSubmatch(text); m != nil {
		id, _ := strconv.Atoi(m[1])
		if _, tt := o.thread(id); tt != nil {
			t, text = tt, strings.TrimSpace(text[len(m[0]):])
		}
	}
	if t == nil {
		t = o.openThread(f, clean(strings.SplitN(text, "\n", 2)[0], 60), "you")
	}
	t.Closed = false
	o.post(t, &Entry{Who: "you", Kind: KindYou, Text: text})
	for _, a := range o.Agents {
		if strings.Contains(strings.ToLower(text), "@"+strings.ToLower(a.Name)) && !slices.ContainsFunc(t.queue, func(h handoff) bool { return h.a == a }) {
			t.queue = append(t.queue, handoff{a, text})
		}
	}
	if len(t.queue) == 0 {
		t.volunteer = true
		for _, a := range o.Agents {
			t.queue = append(t.queue, handoff{a, text})
		}
	}
	start := !t.Running
	t.Running = true
	o.Unlock()
	if start {
		go func() {
			o.discuss(t, o.Settings.Get().Rounds)
			o.Do(func() { t.Running = false })
		}()
	}
}
