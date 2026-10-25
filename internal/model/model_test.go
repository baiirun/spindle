package model

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestParseClaude(t *testing.T) {
	for name, out := range map[string]string{
		"object":  `{"type":"result","result":"hi","structured_output":{"a":1}}`,
		"verbose": `[{"type":"system","subtype":"init"},{"type":"assistant","message":{}},{"type":"result","result":"hi","structured_output":{"a":1}}]`,
	} {
		res, err := parseClaude([]byte(out))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.Result != "hi" || string(res.StructuredOutput) != `{"a":1}` {
			t.Errorf("%s: got %+v", name, res)
		}
	}
	if _, err := parseClaude([]byte(`[{"type":"system"}]`)); err == nil {
		t.Error("array without a result event: want error")
	}
}

func TestForPicksBackends(t *testing.T) {
	t.Setenv(BackendEnv, "")
	t.Setenv("SPINDLE_MODEL_EXTRACT", "")
	want := Fallback{Codex{Model: "gpt-6-luna", Effort: "low"}, Claude{Model: "sonnet"}}
	if got := For("claude", Extract); !reflect.DeepEqual(got, want) {
		t.Errorf("extract = %#v", got)
	}
	if got := For("", Dream); !reflect.DeepEqual(got, Fallback{Codex{Model: "gpt-6-luna", Effort: "medium"}, Claude{Model: "opus"}}) {
		t.Errorf("dream = %#v", got)
	}
	if got := For("claude", Select); got != (Claude{Model: "sonnet"}) {
		t.Errorf("select = %#v", got)
	}
	t.Setenv(BackendEnv, "claude")
	if got := For("codex", Extract); got != (Claude{Model: "sonnet"}) {
		t.Errorf("pinned = %#v", got)
	}
}

func TestFallback(t *testing.T) {
	fail := Func(func(ctx context.Context, system, prompt, schema string) (string, error) {
		return "", errors.New("codex: not found")
	})
	prose := Func(func(ctx context.Context, system, prompt, schema string) (string, error) { return "sorry, no", nil })
	ok := Func(func(ctx context.Context, system, prompt, schema string) (string, error) { return `{"a":1}`, nil })
	if got, err := (Fallback{fail, prose, ok}).Complete(context.Background(), "", "", `{}`); err != nil || got != `{"a":1}` {
		t.Errorf("fallback = %q, %v", got, err)
	}
	if _, err := (Fallback{fail, fail}).Complete(context.Background(), "", "", ""); err == nil {
		t.Error("all failed: want an error")
	}
}
