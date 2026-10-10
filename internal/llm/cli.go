package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ClaudeCLI runs the Claude Code CLI as a plain text model: every built-in tool, MCP server and user
// setting is switched off, and it runs in an empty temporary directory, so it can't touch your files.
type ClaudeCLI struct{ Model string }

func (c *ClaudeCLI) Chat(ctx context.Context, r Request) (string, error) {
	dir, err := os.MkdirTemp("", "office-claude-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	args := []string{"-p", "--tools", "", "--strict-mcp-config", "--setting-sources", "",
		"--system-prompt", r.System, "--output-format", "stream-json", "--include-partial-messages", "--verbose"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir, cmd.Stdin = dir, strings.NewReader(transcript(r))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("claude CLI: %w (is Claude Code installed?)", err)
	}
	var text, result string
	failed := false
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		var ev struct {
			Type, Result string
			IsError      bool `json:"is_error"`
			Event        struct {
				Type  string
				Delta struct{ Type, Text, Thinking string }
			}
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch {
		case ev.Type == "stream_event" && ev.Event.Type == "content_block_delta":
			text += ev.Event.Delta.Text
			call(r.OnText, ev.Event.Delta.Text)
			call(r.OnThink, ev.Event.Delta.Thinking)
		case ev.Type == "result":
			result, failed = ev.Result, ev.IsError
		}
	}
	waitErr := cmd.Wait()
	switch {
	case failed:
		return "", errors.New("claude CLI: " + result)
	case waitErr != nil:
		return "", fmt.Errorf("claude CLI: %v %s", waitErr, strings.TrimSpace(stderr.String()))
	case result != "":
		return strings.TrimSpace(result), nil
	}
	return strings.TrimSpace(text), nil
}
