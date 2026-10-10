package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// OpenAI speaks the Chat Completions API, which OpenAI and most other hosts (OpenRouter, Groq, LM Studio,
// vLLM, Mistral, DeepSeek, Gemini's OpenAI endpoint…) implement.
type OpenAI struct {
	BaseURL  string
	Model    string
	APIKey   string
	JSONMode bool // send response_format; not every compatible host supports it
}

func envKey(name string) string { return os.Getenv(name) }

func (o *OpenAI) Chat(ctx context.Context, r Request) (string, error) {
	if o.Model == "" {
		return "", errors.New("no model set: choose one in settings")
	}
	msgs := append([]Message{{Role: "system", Content: r.System}}, r.Messages...)
	body := map[string]any{"model": o.Model, "messages": msgs, "stream": true}
	if r.JSON && o.JSONMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t, _ := io.ReadAll(res.Body)
		return "", fmt.Errorf("%s %d: %s", o.BaseURL, res.StatusCode, strings.TrimSpace(string(t)))
	}
	var out strings.Builder
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data:")
		if data = strings.TrimSpace(data); !ok || data == "" || data == "[DONE]" {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content          string
					Reasoning        string // OpenRouter
					ReasoningContent string `json:"reasoning_content"` // DeepSeek, vLLM
				}
			}
			Error *struct{ Message string }
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		if ev.Error != nil {
			return out.String(), errors.New(ev.Error.Message)
		}
		for _, c := range ev.Choices {
			out.WriteString(c.Delta.Content)
			call(r.OnText, c.Delta.Content)
			call(r.OnThink, c.Delta.Reasoning+c.Delta.ReasoningContent)
		}
	}
	return strings.TrimSpace(out.String()), sc.Err()
}
