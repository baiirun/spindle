package llm

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseCodexOutputCapturesAnswerAndToolUse(t *testing.T) {
	raw := []byte(`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"rg decision"}}
{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"{\"decisions\":[]}"}}
{"type":"turn.completed","usage":{"input_tokens":12,"cached_input_tokens":3,"output_tokens":4,"reasoning_output_tokens":5}}
`)

	got, err := parseCodexOutput(raw, 25*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != `{"decisions":[]}` {
		t.Fatalf("answer = %q", got.Text)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Name != "command_execution" {
		t.Fatalf("tool calls = %#v", got.ToolCalls)
	}
	if got.Turns != 1 || got.DurationMS != 25 {
		t.Fatalf("run metadata = turns %d, duration %d", got.Turns, got.DurationMS)
	}
	if got.InputTokens != 12 || got.CachedInputTokens != 3 || got.OutputTokens != 4 || got.ReasoningTokens != 5 {
		t.Fatalf("usage = %#v", got)
	}
}

func TestParseCodexOutputClassifiesUsageLimit(t *testing.T) {
	raw := []byte(`{"type":"item.completed","item":{"id":"item_1","type":"error","text":"usage limit reached"}}
`)

	_, err := parseCodexOutput(raw, 0)
	if !errors.Is(err, ErrUsageLimit) {
		t.Fatalf("error = %v, want usage limit", err)
	}
}

func TestParseCodexOutputKeepsAnswersThatMentionUsageLimits(t *testing.T) {
	raw := []byte(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"{\"checklist\":[\"Labeling stopped on the usage limit.\"]}"}}
`)

	res, err := parseCodexOutput(raw, 0)
	if err != nil {
		t.Fatalf("error = %v, want the answer", err)
	}
	if !strings.Contains(res.Text, "usage limit") {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestStrictSchemaRejectsExtraFieldsAtEveryObject(t *testing.T) {
	raw := `{"type":"object","properties":{"decisions":{"type":"array","items":{"type":"object","properties":{"note":{"type":"string"}}}}}}`
	strict, err := strictSchema(raw)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(strict, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("root additionalProperties = %v", schema["additionalProperties"])
	}
	items := schema["properties"].(map[string]any)["decisions"].(map[string]any)["items"].(map[string]any)
	if items["additionalProperties"] != false {
		t.Fatalf("item additionalProperties = %v", items["additionalProperties"])
	}
}
