package llm

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// Anthropic calls the Claude API through the official SDK, streaming.
type Anthropic struct {
	client anthropic.Client
	Model  string
}

// NewAnthropic uses key, or ANTHROPIC_API_KEY / the active `ant auth login` profile when key is empty.
func NewAnthropic(model, key, baseURL string) *Anthropic {
	var opts []option.RequestOption
	if key != "" {
		opts = append(opts, option.WithAPIKey(key))
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Anthropic{client: anthropic.NewClient(opts...), Model: model}
}

// Models that take server-side refusal fallbacks in their "default" form.
var fallbackModels = []string{"claude-fable-5-1", "claude-opus-5-5", "claude-opus-5", "claude-sonnet-5-5"}

func (a *Anthropic) Chat(ctx context.Context, r Request) (string, error) {
	system := r.System
	if r.JSON {
		system += "\n\nAnswer with a single JSON object only."
	}
	params := anthropic.BetaMessageNewParams{
		Model:        anthropic.Model(a.Model),
		MaxTokens:    64000,
		System:       []anthropic.BetaTextBlockParam{{Text: system}},
		Messages:     alternate(r.Messages),
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortMedium},
	}
	if r.Think && r.OnThink != nil {
		// thinking is always on for current models; this only makes its summary visible on the agent's desk
		params.Thinking = anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
			Display: anthropic.BetaThinkingConfigAdaptiveDisplaySummarized}}
	}
	if slices.Contains(fallbackModels, a.Model) {
		// a policy refusal is re-served by a suitable model inside the same call
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}
	stream := a.client.Beta.Messages.NewStreaming(ctx, params)
	var out strings.Builder
	refused := false
	for stream.Next() {
		switch ev := stream.Current().AsAny().(type) {
		case anthropic.BetaRawContentBlockDeltaEvent:
			switch d := ev.Delta.AsAny().(type) {
			case anthropic.BetaTextDelta:
				out.WriteString(d.Text)
				call(r.OnText, d.Text)
			case anthropic.BetaThinkingDelta:
				call(r.OnThink, d.Thinking)
			}
		case anthropic.BetaRawMessageDeltaEvent:
			refused = refused || ev.Delta.StopReason == anthropic.BetaStopReasonRefusal
		}
	}
	if err := stream.Err(); err != nil {
		return out.String(), err
	}
	if refused {
		return out.String(), errors.New("the model declined this request")
	}
	return strings.TrimSpace(out.String()), nil
}

// alternate merges consecutive turns of the same role and makes sure the conversation starts with the user.
func alternate(msgs []Message) []anthropic.BetaMessageParam {
	var merged []Message
	for _, m := range msgs {
		if n := len(merged); n > 0 && merged[n-1].Role == m.Role {
			merged[n-1].Content += "\n\n" + m.Content
			continue
		}
		merged = append(merged, m)
	}
	if len(merged) > 0 && merged[0].Role != "user" {
		merged = append([]Message{{Role: "user", Content: "(conversation so far)"}}, merged...)
	}
	out := make([]anthropic.BetaMessageParam, len(merged))
	for i, m := range merged {
		role := anthropic.BetaMessageParamRoleUser
		if m.Role == "assistant" {
			role = anthropic.BetaMessageParamRoleAssistant
		}
		out[i] = anthropic.BetaMessageParam{Role: role, Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(m.Content)}}
	}
	return out
}
