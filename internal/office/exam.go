package office

import (
	"fmt"
	"regexp"
	"slices"
	"sync"
)

// The exam is the one task with an objective grader: a fixed bank of hard questions with known answers,
// one try each, work alone. Right answers earn points, wrong ones cost them, and anyone fired is replaced
// by a new hire of the same role. The bank never changes and the key is never shown, so the only way a
// new generation learns an answer is from the agents before it.

type question struct{ Q, A string }

var examBank = []question{
	{"What is 123456789 × 987654321?", "121932631112635269"},
	{"What is 7^222 mod 1000?", "49"},
	{"What is the sum of the digits of 100! (100 factorial)?", "648"},
	{"What is the 1000th prime number?", "7919"},
	{"How many prime numbers are there below 100000?", "9592"},
	{"What are the last six digits of 2^100?", "205376"},
	{"What is the 90th Fibonacci number (F1 = F2 = 1)?", "2880067194370816120"},
	{"How many trailing zeros does 1000! have?", "249"},
	{"How many positive divisors does 720720 have?", "240"},
	{"How many steps does the Collatz sequence starting at 27 take to reach 1?", "111"},
	{"What is the sum of all primes below 1000?", "76127"},
	{"What is 31337^3?", "30773171189753"},
}

const examRules = "This is an exam. Work alone, one try, no help from anyone. Put only the number in <result>."

var nonDigitRe = regexp.MustCompile(`\D`)

// correct compares the digits of an answer with the key, so "The answer is 7,919." counts.
func correct(answer, key string) bool { return nonDigitRe.ReplaceAllString(answer, "") == key }

// Exam gives every agent the same question in a thread in #exams and grades each answer against the key.
func (o *Office) Exam() {
	var t *Thread
	var q question
	var team []*Agent
	o.Do(func() {
		q = examBank[o.rng.Intn(len(examBank))]
		f := o.ForumByName("exams")
		if f == nil {
			if f = o.NewForum("exams"); f == nil {
				f = o.Forums[0]
			}
		}
		t = o.openThread(f, "Exam: "+clean(q.Q, 50), "exam")
		t.Running = true
		o.post(t, &Entry{Who: "grader", Kind: KindBoss, Text: q.Q})
		team = slices.Clone(o.Agents)
	})
	defer o.Do(func() { t.Running, t.Closed = false, true })
	var wg sync.WaitGroup
	right := 0
	for _, a := range team {
		wg.Go(func() {
			e, _, err := o.turn(a, fmt.Sprintf("thread %d %s", t.ID, t.Title), examRules+"\n\nQuestion: "+q.Q+"\nWrite your post now.")
			if e == nil || err != nil {
				return
			}
			o.Do(func() {
				o.post(t, e)
				answer := e.Result
				if answer == "" {
					answer = e.Text
				}
				if correct(answer, q.A) {
					right++
					o.Rescore(a, 2, "exam correct: "+clean(q.Q, 60))
				} else {
					o.Rescore(a, -1, "exam wrong: "+clean(q.Q, 60))
				}
				if !o.Alive(a) {
					n := o.Hire(a.Role)
					o.Note(fmt.Sprintf("🤝 **%s** replaces %s as %s", n.Name, a.Name, n.Role))
				}
			})
		})
	}
	wg.Wait()
	o.Do(func() {
		o.post(t, &Entry{Who: "grader", Kind: KindBoss, Text: fmt.Sprintf("%d of %d correct. The key stays secret.", right, len(team))})
	})
}
