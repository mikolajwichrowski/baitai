// Package llm talks to language models through interchangeable connectors: Ollama, the OpenAI and
// Anthropic APIs, any OpenAI-compatible endpoint, and the Claude Code CLI.
//
// Agents never get tools from a connector. File work goes through the office's own text protocol
// (see package workspace), so every backend behaves the same and none of them gets shell access.
package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/mikolajwichrowski/baitai/internal/config"
)

// Message is one conversation turn: role is "user" or "assistant".
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is one model call.
type Request struct {
	System   string
	Messages []Message
	JSON     bool         // the answer must be a single JSON object
	Think    bool         // allow reasoning before answering, if the backend supports it
	OnText   func(string) // streamed answer chunks
	OnThink  func(string) // streamed reasoning chunks
}

// Provider answers a Request with the model's text.
type Provider interface {
	Chat(ctx context.Context, r Request) (string, error)
}

// Progress reports setup progress, e.g. a model download; total is 0 when unknown.
type Progress func(status string, done, total float64)

// Preparer is implemented by connectors that need setup before the first call (Ollama pulls models).
type Preparer interface {
	Prepare(ctx context.Context, progress Progress) error
}

// New builds the connector chosen in settings.
func New(s config.Settings) (Provider, error) {
	switch s.Connector {
	case config.Ollama:
		return &Ollama{Host: s.BaseURL(), Model: s.Model(), Context: s.Context}, nil
	case config.OpenAI, config.OpenAICompatible:
		key := s.APIKey()
		if key == "" && s.Connector == config.OpenAI {
			key = envKey("OPENAI_API_KEY")
		}
		return &OpenAI{BaseURL: s.BaseURL(), Model: s.Model(), APIKey: key, JSONMode: s.Connector == config.OpenAI}, nil
	case config.Anthropic:
		return NewAnthropic(s.Model(), s.APIKey(), s.BaseURLs[config.Anthropic]), nil
	case config.ClaudeCLI:
		return &ClaudeCLI{Model: s.Model()}, nil
	}
	return nil, fmt.Errorf("unknown connector %q", s.Connector)
}

// transcript flattens a conversation for connectors that take a single prompt (the CLIs).
func transcript(r Request) string {
	var b strings.Builder
	for _, m := range r.Messages {
		fmt.Fprintf(&b, "%s:\n%s\n\n", strings.ToUpper(m.Role), m.Content)
	}
	if len(r.Messages) > 1 {
		b.WriteString("Reply as the ASSISTANT to the last USER message.")
	}
	if r.JSON {
		b.WriteString("\nAnswer with a single JSON object only.")
	}
	return strings.TrimSpace(b.String())
}

func call(f func(string), s string) {
	if f != nil && s != "" {
		f(s)
	}
}
