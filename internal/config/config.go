// Package config holds the settings edited in the settings popup and persisted in the state directory.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Connector kinds, in the order the settings popup cycles through them.
const (
	Ollama           = "ollama"
	OpenAI           = "openai"
	Anthropic        = "anthropic"
	OpenAICompatible = "openai-compatible"
	ClaudeCLI        = "claude-cli"
)

var Connectors = []string{Ollama, Anthropic, OpenAI, OpenAICompatible, ClaudeCLI}

// DefaultModel is what a connector uses when no model is set; empty means the tool's own default.
var DefaultModel = map[string]string{
	Ollama:           "huihui_ai/qwen3.5-abliterated:9b",
	Anthropic:        "claude-opus-5-5",
	OpenAI:           "gpt-5",
	OpenAICompatible: "",
	ClaudeCLI:        "",
}

// DefaultBaseURL is the endpoint a connector talks to when none is set.
var DefaultBaseURL = map[string]string{
	Ollama:           "http://localhost:11434",
	OpenAI:           "https://api.openai.com/v1",
	OpenAICompatible: "http://localhost:1234/v1", // LM Studio; also OpenRouter, Groq, vLLM, Gemini's OpenAI endpoint…
}

// Settings are the tunables. Model, BaseURL and APIKey are per connector so switching back and forth keeps them.
type Settings struct {
	Connector string
	Models    map[string]string
	BaseURLs  map[string]string
	APIKeys   map[string]string // stored in a 0600 file; the env var (OPENAI_API_KEY, ANTHROPIC_API_KEY) is used when empty
	Thinking  bool              // let agents reason before answering, when the model supports it
	Context   int               // tokens of context per call (Ollama)
	MaxAgents int
	Rounds    int // reply rounds per thread
	FollowUps int // times the boss may send work back before reporting
}

func Defaults() Settings {
	return Settings{Connector: Ollama, Models: map[string]string{}, BaseURLs: map[string]string{}, APIKeys: map[string]string{},
		Thinking: false, Context: 8192, MaxAgents: 8, Rounds: 4, FollowUps: 2}
}

func (s Settings) Model() string {
	if m := s.Models[s.Connector]; m != "" {
		return m
	}
	return DefaultModel[s.Connector]
}

func (s Settings) BaseURL() string {
	if u := s.BaseURLs[s.Connector]; u != "" {
		return u
	}
	return DefaultBaseURL[s.Connector]
}

func (s Settings) APIKey() string { return s.APIKeys[s.Connector] }

// Store is the thread-safe settings holder; changes are written to disk immediately.
type Store struct {
	mu   sync.Mutex
	s    Settings
	path string
}

// Load reads dir/settings.json over the defaults. MODEL and OLLAMA_URL in the environment win for Ollama.
func Load(dir string) *Store {
	s := Defaults()
	if b, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil {
		json.Unmarshal(b, &s)
	}
	for _, m := range []*map[string]string{&s.Models, &s.BaseURLs, &s.APIKeys} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	if !slices.Contains(Connectors, s.Connector) { // e.g. the removed codex-cli connector
		s.Connector = Ollama
	}
	if v := os.Getenv("MODEL"); v != "" {
		s.Models[Ollama] = v
	}
	if v := os.Getenv("OLLAMA_URL"); v != "" {
		s.BaseURLs[Ollama] = v
	}
	return &Store{s: s, path: filepath.Join(dir, "settings.json")}
}

// Get returns a copy of the current settings.
func (st *Store) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.s
	s.Models, s.BaseURLs, s.APIKeys = clone(s.Models), clone(s.BaseURLs), clone(s.APIKeys)
	return s
}

// Update applies change and saves. The file is 0600 because it may hold API keys.
func (st *Store) Update(change func(*Settings)) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	change(&st.s)
	if st.path == "" {
		return nil
	}
	b, _ := json.MarshalIndent(st.s, "", "  ")
	return os.WriteFile(st.path, b, 0o600)
}

func clone(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

// InMemory is a store that never touches disk, for tests.
func InMemory(s Settings) *Store { return &Store{s: s} }
