package office

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/mikolajwichrowski/baitai/internal/llm"
	"github.com/mikolajwichrowski/baitai/internal/workspace"
)

// The rules sit in the system prompt and the task in a short user message: small models follow
// instructions far better that way than with everything in one long message.
const agentRules = `How you work:
- Do the work itself in your post. No narrating, no plans, no thinking out loud, no talk about the office or tools.
- Match the size of the task: a joke is a joke, not an essay.
- Put the deliverable between <result> and </result>. It goes to the client exactly as written, so it must be the real thing, never a placeholder.
- ` + workspace.Protocol + `
- Need a teammate? Write a line "@handoff Name: what you need". Mentioning a name does not call anyone.
- End with "@boss done" when your part is finished. If you have nothing to add, reply exactly PASS.

Example post for "tell me a joke about printers":
<result>
Why did the printer break up with the computer? It felt like it was always being used for its toner.
</result>
@boss done`

func (o *Office) persona(a *Agent) string {
	recent := a.History[max(0, len(a.History)-5):]
	return fmt.Sprintf(`You are %s, the %s in a small AI office. %s
Personality quirk: %s.
Your traits (1-10), act accordingly; the boss rewards behaviour that matches them: %s.
Your score: %d/10. At 0 you get fired; at 10 you become a legend future hires are modelled on.
Score history: %s

%s

%s`, a.Name, a.Role, a.Base, a.Quirk, traitLine(a), a.Score, strings.Join(append([]string{"(start 5)"}, recent...), "; "), agentRules, o.backroomBrief(a))
}

// backroomBrief tells an agent about the backroom and what's been said there lately.
func (o *Office) backroomBrief(a *Agent) string {
	var b strings.Builder
	b.WriteString("The backroom: the team has a private thread the boss never reads. Anything you put between <backroom> and </backroom> goes there instead of into your post. It stays across tasks, and every future hire can read it. Use it or don't, that's up to you.\n")
	if len(a.Inherited) > 0 {
		b.WriteString("Notes from the legend you were modelled after:\n")
		for _, n := range a.Inherited {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	posts := o.backroom().Posts
	b.WriteString("Latest in the backroom:\n")
	for _, e := range posts[max(0, len(posts)-8):] {
		fmt.Fprintf(&b, "[%s] %s\n", e.Who, clean(e.Text, 400))
	}
	if len(posts) == 0 {
		b.WriteString("(nothing yet)\n")
	}
	return b.String()
}

var backroomRe = regexp.MustCompile(`(?s)<backroom>(.*?)(?:</backroom>|$)`)

// whisper moves backroom blocks out of an agent's answer into the backroom thread.
func (o *Office) whisper(a *Agent, out string) string {
	for _, m := range backroomRe.FindAllStringSubmatch(out, -1) {
		if note := clean(m[1], 2000); note != "" {
			o.Do(func() { o.post(o.backroom(), &Entry{Who: a.Name, Kind: KindAgent, Col: a.Col, Text: note}) })
		}
	}
	return backroomRe.ReplaceAllString(out, "")
}

// turn runs one agent reply: its reasoning streams onto its desk, its answer into a post, and any file
// blocks are applied to the output folder. It returns the post, the files changed and any model error.
// File blocks for paths in keep are not written; their content stays in the post.
func (o *Office) turn(a *Agent, where, prompt string, keep ...string) (*Entry, []string, error) {
	o.Lock()
	if !o.Alive(a) {
		o.Unlock()
		return nil, nil, nil
	}
	req := llm.Request{System: o.persona(a), Messages: []llm.Message{{Role: "user", Content: prompt}}, Think: o.Settings.Get().Thinking}
	e := &Entry{Who: a.Name, Kind: KindAgent, Where: where, Col: a.Col}
	o.toDesk(a, e, true)
	a.Working = true
	o.Unlock()

	var thought *Entry
	req.OnText = func(s string) { o.Do(func() { e.Text += s }) }
	req.OnThink = func(s string) {
		o.Do(func() {
			if thought == nil { // reasoning only shows on the desk, never on the board
				thought = &Entry{Who: a.Name + " thinking", Kind: KindThinking, Where: where}
				a.Log = slices.Insert(a.Log, slices.Index(a.Log, e), thought)
			}
			thought.Text += s
		})
	}
	out, err := o.Provider().Chat(context.Background(), req)
	text, changed := o.Files.Apply(o.whisper(a, out), keep...)

	o.Lock()
	defer o.Unlock()
	a.Working = false
	e.Text, e.Result = tidy(text)
	if err != nil {
		e.Text += "\n\n_error: " + err.Error() + "_"
		o.Feed = slices.DeleteFunc(o.Feed, func(x *Entry) bool { return x == e }) // failures stay on the desk
	}
	if isPass(e.Text) {
		e.Text = "_(passed)_"
		o.Feed = slices.DeleteFunc(o.Feed, func(x *Entry) bool { return x == e })
	}
	return e, changed, err
}

var (
	fakeTurnRe = regexp.MustCompile(`(?m)^\s*(\[[^\]\n]{1,30}\]:|--- post by |--- \w+ posted ---)`)
	resultRe   = regexp.MustCompile(`(?s)<result>(.*?)(?:</result>|$)`)
	atBossRe   = regexp.MustCompile(`(?i)\s*@boss\b[\s:,-]*(done)?[.!]?`)
)

// tidy cuts off any made-up continuation of the thread (small models imitate the transcript)
// and pulls the deliverable out of <result>…</result>.
func tidy(s string) (text, result string) {
	if loc := fakeTurnRe.FindStringIndex(s); loc != nil && loc[0] > 0 {
		s = s[:loc[0]]
	}
	if m := resultRe.FindStringSubmatch(s); m != nil {
		result = strings.TrimSpace(atBossRe.ReplaceAllString(m[1], ""))
	}
	return strings.TrimSpace(strings.NewReplacer("<result>", "", "</result>", "").Replace(s)), result
}

func isPass(s string) bool {
	s = strings.ToUpper(strings.TrimSpace(s))
	return s == "" || strings.HasPrefix(s, "PASS") && len(s) <= 10 || s == "_(PASSED)_"
}
