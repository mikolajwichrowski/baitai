package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"
)

// Ollama runs models locally. Prepare starts `ollama serve` if needed and pulls a missing model.
type Ollama struct {
	Host    string
	Model   string
	Context int

	thinking atomic.Bool // the model can reason separately from its answer
}

func (o *Ollama) post(ctx context.Context, path string, body any) (*http.Response, error) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Host+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		t, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return nil, fmt.Errorf("ollama %d: %s", res.StatusCode, strings.TrimSpace(string(t)))
	}
	return res, nil
}

func (o *Ollama) Chat(ctx context.Context, r Request) (string, error) {
	msgs := append([]Message{{Role: "system", Content: r.System}}, r.Messages...)
	body := map[string]any{"model": o.Model, "messages": msgs, "stream": true, "keep_alive": "30m",
		"options": map[string]any{"num_ctx": o.Context}}
	if o.thinking.Load() {
		body["think"] = r.Think
	}
	if r.JSON {
		body["format"] = "json"
	}
	res, err := o.post(ctx, "/api/chat", body)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var b strings.Builder
	for dec := json.NewDecoder(res.Body); ; {
		var ev struct {
			Message struct{ Content, Thinking string }
			Error   string
		}
		if err := dec.Decode(&ev); err == io.EOF {
			break
		} else if err != nil {
			return b.String(), err
		}
		if ev.Error != "" {
			return b.String(), errors.New(ev.Error)
		}
		b.WriteString(ev.Message.Content)
		call(r.OnText, ev.Message.Content)
		call(r.OnThink, ev.Message.Thinking)
	}
	return strings.TrimSpace(b.String()), nil
}

// Prepare makes sure the server runs and the model is there, downloading it if needed.
func (o *Ollama) Prepare(ctx context.Context, progress Progress) error {
	if err := o.ensureServer(); err != nil {
		return err
	}
	err := o.show(ctx)
	if err == nil || !strings.Contains(err.Error(), "404") {
		return err
	}
	res, err := o.post(ctx, "/api/pull", map[string]any{"model": o.Model})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	for dec := json.NewDecoder(res.Body); ; {
		var ev struct {
			Status, Error    string
			Total, Completed float64
		}
		if err := dec.Decode(&ev); err == io.EOF {
			break
		} else if err != nil {
			return err
		}
		if ev.Error != "" {
			return errors.New(ev.Error)
		}
		progress(ev.Status, ev.Completed, ev.Total)
	}
	return o.show(ctx)
}

func (o *Ollama) show(ctx context.Context) error {
	res, err := o.post(ctx, "/api/show", map[string]any{"model": o.Model})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var info struct{ Capabilities []string }
	json.NewDecoder(res.Body).Decode(&info)
	think := false
	for _, c := range info.Capabilities {
		think = think || c == "thinking"
	}
	o.thinking.Store(think)
	return nil
}

func (o *Ollama) up() bool {
	c := http.Client{Timeout: time.Second}
	res, err := c.Get(o.Host + "/api/version")
	if err != nil {
		return false
	}
	res.Body.Close()
	return true
}

// ensureServer starts a local `ollama serve` when the host is local and nothing answers.
func (o *Ollama) ensureServer() error {
	if o.up() {
		return nil
	}
	if u, err := url.Parse(o.Host); err != nil || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") {
		return fmt.Errorf("no Ollama server answering at %s", o.Host)
	}
	bin, err := exec.LookPath("ollama")
	if err != nil {
		return errors.New("ollama is not installed: `brew install ollama` or https://ollama.com/download")
	}
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), "OLLAMA_NUM_PARALLEL=4") // agents work in parallel
	if err := cmd.Start(); err != nil {
		return err
	}
	for range 50 {
		if o.up() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("started `ollama serve` but it did not come up on " + o.Host)
}

// Installed lists local models, best effort.
func (o *Ollama) Installed() []string {
	c := http.Client{Timeout: time.Second}
	res, err := c.Get(o.Host + "/api/tags")
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	var tags struct{ Models []struct{ Name string } }
	json.NewDecoder(res.Body).Decode(&tags)
	var names []string
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	return names
}
