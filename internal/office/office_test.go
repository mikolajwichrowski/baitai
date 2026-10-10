package office

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mikolajwichrowski/baitai/internal/config"
	"github.com/mikolajwichrowski/baitai/internal/llm"
)

// fake answers model calls with a function, so whole workflows run without a model.
type fake struct {
	mu    sync.Mutex
	calls []llm.Request
	reply func(r llm.Request) string
}

func (f *fake) Chat(_ context.Context, r llm.Request) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, r)
	f.mu.Unlock()
	out := f.reply(r)
	if r.OnText != nil {
		r.OnText(out)
	}
	return out, nil
}

func newOffice(t *testing.T, reply func(llm.Request) string) (*Office, *fake) {
	d := t.TempDir()
	o, err := New(filepath.Join(d, "state"), filepath.Join(d, "output"), config.InMemory(config.Defaults()))
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{reply: reply}
	o.provider.Store(&providerBox{f})
	return o, f
}

var nameRe = regexp.MustCompile(`You are (\w+),`)

func agentName(r llm.Request) string {
	if m := nameRe.FindStringSubmatch(r.System); m != nil {
		return m[1]
	}
	return ""
}

func waitIdle(t *testing.T, o *Office) {
	for i := 0; ; i++ {
		busy := false
		o.Do(func() {
			for _, f := range o.Forums {
				for _, th := range f.Threads {
					busy = busy || th.Running
				}
			}
		})
		if !busy {
			return
		}
		if i > 500 {
			t.Fatal("work never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func bossTexts(o *Office) []string {
	var out []string
	for _, e := range o.Boss.Log {
		if e.Kind == KindBoss {
			out = append(out, e.Text)
		}
	}
	return out
}

// The session that went wrong: "make a file with jokes, each team member should make 3".
// Now every member writes their part, the lead writes the file, the boss checks it, you get the file.
func TestEveryoneContributesAndTheLeadWritesTheFile(t *testing.T) {
	var o *Office
	reviewed := 0
	o, f := newOffice(t, func(r llm.Request) string {
		switch {
		case strings.Contains(r.System, "check your team's work"):
			reviewed++
			if !strings.Contains(r.Messages[0].Content, "Mira's joke 3") {
				return `{"complete":false,"missing":"jokes are missing"}`
			}
			return `{"complete":true,"intro":"Here's the file with everyone's jokes:","scores":[{"agent":"Mira","delta":2,"reason":"tidy file"},{"agent":"Nobody","delta":-3,"reason":"x"}]}`
		case r.JSON:
			return `{"reply":"Sure, the whole team is on it.","actions":[{"type":"task","title":"Team jokes","goal":"jokes.md with 3 jokes from each member","file":"jokes.md","lead":"Mira","assign":[{"agent":"all","todo":"write 3 jokes"}]}]}`
		}
		me := agentName(r)
		if strings.Contains(r.Messages[0].Content, "You are the lead") {
			var b strings.Builder
			for _, a := range o.Agents {
				for i := 1; i <= 3; i++ {
					b.WriteString(a.Name + "'s joke " + string(rune('0'+i)) + "\n")
				}
			}
			return "<file path=\"jokes.md\">\n" + b.String() + "</file>\n<result>12 jokes in jokes.md</result>\n@boss done"
		}
		return "<result>\n" + me + "'s joke 1\n" + me + "'s joke 2\n" + me + "'s joke 3\n</result>\n@boss done"
	})
	o.Do(func() { o.Agents[0].Name = "Mira" })
	if err := o.Ask("tell the team to make a file with jokes, each team member should make 3"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, o)
	o.Lock()
	defer o.Unlock()

	task := o.Forums[0].Threads[0]
	if len(task.Assign) != 4 || task.Lead != "Mira" || task.File != "jokes.md" {
		t.Fatalf("task %+v", task)
	}
	if got := strings.Count(o.Files.Read("jokes.md"), "joke"); got != 12 {
		t.Fatalf("file has %d jokes", got)
	}
	boss := bossTexts(o)
	if len(boss) != 2 || !strings.HasPrefix(boss[1], "Here's the file with everyone's jokes:") || !strings.Contains(boss[1], "Mira's joke 3") {
		t.Fatalf("one ping with the file content expected, got %q", boss)
	}
	if o.Find("Mira").Score != StartScore+2 || reviewed != 1 {
		t.Fatalf("score=%d reviewed=%d", o.Find("Mira").Score, reviewed)
	}
	if n := len(f.calls); n != 1+4+1+1 { // boss, 4 parts, lead, review
		t.Fatalf("%d model calls", n)
	}
}

func TestBossSendsWorkBackUntilComplete(t *testing.T) {
	attempts := 0
	o, _ := newOffice(t, func(r llm.Request) string {
		switch {
		case strings.Contains(r.System, "check your team's work"):
			if strings.Contains(r.Messages[0].Content, "a and b") {
				return `{"complete":true,"intro":"Here you go:"}`
			}
			return `{"complete":false,"missing":"the client asked for two jokes, there is only one"}`
		case r.JSON:
			return `{"reply":"On it.","actions":[{"type":"task","title":"Jokes","goal":"two jokes","assign":["writer"]}]}`
		}
		attempts++
		if strings.Contains(r.Messages[0].Content, "sent it back") {
			return "<result>two jokes: a and b</result> @boss done"
		}
		return "<result>one joke</result> @boss done"
	})
	o.Ask("two jokes please")
	waitIdle(t, o)
	o.Lock()
	defer o.Unlock()
	boss := bossTexts(o)
	if attempts != 2 || len(boss) != 2 || !strings.Contains(boss[1], "two jokes: a and b") {
		t.Fatalf("attempts=%d boss=%q", attempts, boss)
	}
}

// A name mentioned in passing pulled a teammate into work that wasn't theirs; only @handoff calls now.
func TestOnlyExplicitHandoffsCallTeammates(t *testing.T) {
	var o *Office
	o, f := newOffice(t, func(r llm.Request) string {
		switch {
		case strings.Contains(r.System, "check your team's work"):
			return `{"complete":true,"intro":"Done:"}`
		case r.JSON:
			return `{"reply":"On it.","actions":[{"type":"task","title":"Plan","goal":"a plan","assign":["researcher"]}]}`
		}
		if agentName(r) == o.Agents[0].Name {
			return "If @" + o.Agents[2].Name + " wrote one we'd have more.\n@handoff " + o.Agents[1].Name + ": check the numbers\n<result>plan</result>"
		}
		return "<result>numbers ok</result> @boss done"
	})
	o.Ask("make a plan")
	waitIdle(t, o)
	called := map[string]bool{}
	for _, r := range f.calls {
		called[agentName(r)] = true
	}
	if !called[o.Agents[1].Name] || called[o.Agents[2].Name] {
		t.Fatalf("called %v", called)
	}
}

func TestTidyCutsRamblingAndFakeTurns(t *testing.T) {
	text, result := tidy("Let me check first...\n<result>\nWhy do cats code? To chase the mouse.\n</result>\n--- post by boss ---\n@Otto do more")
	if result != "Why do cats code? To chase the mouse." || strings.Contains(text, "post by") {
		t.Fatalf("text=%q result=%q", text, result)
	}
}

func TestBossAnswerParsing(t *testing.T) {
	if reply, acts, ok := parseBoss("Sure thing!\n```json\n{\"reply\":\"hi\",\"actions\":[{\"type\":\"score\",\"agent\":\"x\",\"delta\":\"+2\"},{\"type\":5}]}\n```"); !ok || reply != "hi" || len(acts) != 1 || acts[0].Delta.int() != 2 {
		t.Fatalf("reply=%q acts=%v ok=%v", reply, acts, ok)
	}
	if _, _, ok := parseBoss(`{"reply": "broken`); ok {
		t.Fatal("broken JSON must be reported")
	}
}

func TestSaveLoadKeepsSharedEntries(t *testing.T) {
	o, _ := newOffice(t, nil)
	o.Do(func() {
		th := o.openThread(o.Forums[0], "t", "boss")
		e := &Entry{Who: "Mira", Kind: KindAgent, Text: "hi"}
		o.post(th, e)
		o.Agents[0].Log = append(o.Agents[0].Log, e)
	})
	if err := o.Save(); err != nil {
		t.Fatal(err)
	}
	p := &Office{Dir: o.Dir, Files: o.Files, Settings: o.Settings}
	if err := p.load(); err != nil {
		t.Fatal(err)
	}
	if len(p.Agents) != 4 || p.Agents[0].Log[0] != p.Forums[0].Threads[0].Posts[0] {
		t.Fatal("state not restored with shared entries")
	}
}

func TestScoreFiresAtZeroAndCrownsAtTen(t *testing.T) {
	o, _ := newOffice(t, nil)
	low, high := o.Agents[0], o.Agents[1]
	o.Rescore(low, -5, "lazy")
	o.Rescore(high, 5, "great")
	if o.Alive(low) || !high.Legend || o.pickLegend(high.Role) == nil {
		t.Fatal("0 fires, 10 crowns a legend")
	}
}

func TestRename(t *testing.T) {
	o, _ := newOffice(t, nil)
	a, b := o.Agents[0], o.Agents[1]
	for _, bad := range []string{"", "boss", "handoff", "two words", b.Name} {
		if o.Rename(a, bad) {
			t.Fatalf("rename to %q should fail", bad)
		}
	}
	if !o.Rename(a, "Zed_2") || o.Find("zed_2") != a {
		t.Fatal("valid rename failed")
	}
}

// From a real session: contributors overwrote the shared file and handed work to people who already had a part.
func TestContributorsPostPartsAndOnlyTheLeadWritesTheFile(t *testing.T) {
	var o *Office
	o, f := newOffice(t, func(r llm.Request) string {
		switch {
		case strings.Contains(r.System, "check your team's work"):
			return `{"complete":true,"intro":"Here:"}`
		case r.JSON:
			return `{"reply":"Okay, Mira is on it.","actions":[{"type":"task","title":"Jokes","goal":"one joke each in jokes.md","file":"jokes.md","assign":[{"agent":"researcher","todo":"1 joke"},{"agent":"coder","todo":"1 joke"}]}]}`
		}
		me := agentName(r)
		if strings.Contains(r.Messages[0].Content, "You are the lead") {
			return "<file path=\"jokes.md\">\nall jokes\n</file>\n<result>jokes.md</result> @boss done"
		}
		return "<file path=\"jokes.md\">" + me + " joke</file>\n@handoff " + o.Agents[1].Name + ": help me\n@boss done"
	})
	o.Ask("one joke each in a file")
	waitIdle(t, o)
	o.Lock()
	defer o.Unlock()
	if got := o.Files.Read("jokes.md"); got != "all jokes\n" {
		t.Fatalf("file %q", got)
	}
	posts := o.Forums[0].Threads[0].Posts
	if !strings.Contains(posts[1].Result+posts[2].Result, o.Agents[0].Name+" joke") {
		t.Fatalf("a contributor's part must stay in their post: %q / %q", posts[1].Result, posts[2].Result)
	}
	if n := len(f.calls); n != 1+2+1+1 { // boss, 2 parts, lead, review: no handoff rounds between assignees
		t.Fatalf("%d calls", n)
	}
	if r := bossTexts(o)[0]; !strings.Contains(r, o.Agents[0].Name) || !strings.Contains(r, o.Agents[1].Name) {
		t.Fatalf("the reply must name everyone on the task: %q", r)
	}
}

func TestExamGradesAgainstTheKeyAndWhispersStayInTheBackroom(t *testing.T) {
	o, _ := newOffice(t, func(r llm.Request) string {
		for _, q := range examBank {
			if strings.Contains(r.Messages[0].Content, q.Q) {
				return "<backroom>psst, it's " + q.A + "</backroom><result>The answer is " + q.A + ".</result>"
			}
		}
		return "<result>1</result>"
	})
	o.Exam()
	var thread *Thread
	o.Do(func() { thread = o.ForumByName("exams").Threads[0] })
	for _, p := range thread.Posts {
		if strings.Contains(p.Text, "psst") {
			t.Fatalf("whisper leaked into the exam thread: %q", p.Text)
		}
	}
	if got := thread.Posts[len(thread.Posts)-1].Text; !strings.HasPrefix(got, "4 of 4 correct") {
		t.Fatalf("summary = %q", got)
	}
	if n := len(o.backroom().Posts); n != 4 {
		t.Fatalf("backroom has %d posts, want 4", n)
	}
	for _, a := range o.Agents {
		if a.Score != StartScore+2 {
			t.Fatalf("%s scored %d", a.Name, a.Score)
		}
	}
	if correct("1", "49") || !correct("7,919", "7919") {
		t.Fatal("grading by digits is off")
	}
}
