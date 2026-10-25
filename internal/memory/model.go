package memory

import (
	"context"
	"os"
	"strings"
	"time"

	"spindle/internal/llm"
)

// ModelEnv overrides the model the worker uses for extraction and dreaming.
const ModelEnv = "SPINDLE_MODEL"

// Model is the inference the worker needs. Tests swap in a fake.
type Model interface {
	// Text returns a free-form answer.
	Text(ctx context.Context, system, prompt string) (string, error)
	// JSON decodes a structured answer matching schema into out.
	JSON(ctx context.Context, system, prompt, schema string, out any) error
}

// CodexModel runs the pinned eval model through Codex CLI (internal/llm), so
// the live memory and the eval arms are written by the same model.
type CodexModel struct{ Name string }

// DefaultModel is CodexModel with SPINDLE_MODEL or the pinned extractor.
func DefaultModel() CodexModel {
	name := strings.TrimSpace(os.Getenv(ModelEnv))
	if name == "" {
		name = llm.Extractor
	}
	return CodexModel{Name: name}
}

func (m CodexModel) Text(ctx context.Context, system, prompt string) (string, error) {
	res, err := llm.Run(ctx, llm.Request{Model: m.Name, System: system, Prompt: prompt, Timeout: 15 * time.Minute})
	return res.Text, err
}

func (m CodexModel) JSON(ctx context.Context, system, prompt, schema string, out any) error {
	_, err := llm.JSONRequest(ctx, llm.Request{Model: m.Name, System: system, Prompt: prompt, Schema: schema, Timeout: 20 * time.Minute}, out)
	return err
}
