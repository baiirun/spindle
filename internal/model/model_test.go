package model

import "testing"

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
