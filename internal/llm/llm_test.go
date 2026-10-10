package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIStreamsTextAndReasoning(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing key")
		}
		for _, chunk := range []string{`{"choices":[{"delta":{"reasoning_content":"hmm"}}]}`, `{"choices":[{"delta":{"content":"Hel"}}]}`, `{"choices":[{"delta":{"content":"lo"}}]}`} {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	thought := ""
	out, err := (&OpenAI{BaseURL: srv.URL, Model: "m", APIKey: "k", JSONMode: true}).Chat(context.Background(),
		Request{System: "sys", Messages: []Message{{Role: "user", Content: "hi"}}, JSON: true, OnThink: func(s string) { thought += s }})
	if err != nil || out != "Hello" || thought != "hmm" {
		t.Fatalf("out=%q thought=%q err=%v", out, thought, err)
	}
	if got["response_format"] == nil || got["messages"].([]any)[0].(map[string]any)["role"] != "system" {
		t.Fatalf("request: %v", got)
	}
}

func TestOllamaSeparatesThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/show" {
			fmt.Fprint(w, `{"capabilities":["completion","thinking"]}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["think"] != true || body["keep_alive"] == nil {
			t.Errorf("request: %v", body)
		}
		fmt.Fprintln(w, `{"message":{"thinking":"let me see"}}`)
		fmt.Fprintln(w, `{"message":{"content":"answer"}}`)
		fmt.Fprintln(w, `{"done":true}`)
	}))
	defer srv.Close()
	o := &Ollama{Host: srv.URL, Model: "m", Context: 2048}
	if err := o.show(context.Background()); err != nil {
		t.Fatal(err)
	}
	thought := ""
	out, err := o.Chat(context.Background(), Request{Think: true, OnThink: func(s string) { thought += s }})
	if err != nil || out != "answer" || thought != "let me see" {
		t.Fatalf("out=%q thought=%q err=%v", out, thought, err)
	}
}

func TestAnthropicConversationAlternates(t *testing.T) {
	got := alternate([]Message{{Role: "assistant", Content: "a"}, {Role: "user", Content: "b"}, {Role: "user", Content: "c"}})
	if len(got) != 3 || got[0].Role != "user" || got[2].Role != "user" {
		t.Fatalf("got %+v", got)
	}
}
