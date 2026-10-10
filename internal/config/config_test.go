package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsPerConnectorAndSavedPrivately(t *testing.T) {
	dir := t.TempDir()
	st := Load(dir)
	if s := st.Get(); s.Connector != Ollama || s.Model() != DefaultModel[Ollama] {
		t.Fatalf("defaults: %+v", s)
	}
	st.Update(func(s *Settings) {
		s.Connector = Anthropic
		s.APIKeys[Anthropic] = "sk-test"
		s.Models[Ollama] = "llama3.2:3b"
	})
	s := Load(dir).Get()
	if s.Model() != "claude-opus-5-5" || s.APIKey() != "sk-test" || s.Models[Ollama] != "llama3.2:3b" {
		t.Fatalf("reloaded: %+v", s)
	}
	info, _ := os.Stat(filepath.Join(dir, "settings.json"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings hold API keys and must be owner-only, got %v", info.Mode().Perm())
	}
}
