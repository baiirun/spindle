package eval

import (
	"fmt"
	"strings"
	"testing"

	"spindle/internal/llm"
)

func TestContinuationSystemIncludesExactResumeCommand(t *testing.T) {
	prompt := continuationSystem(ContinuationOptions{
		CorpusRoot: "data/corpus", EpisodeRoot: "data/episodes", Source: "codex", Session: "session-123",
	}, "/tmp/spin")
	if !strings.Contains(prompt, "'/tmp/spin' resume --episodes 'data/episodes' --source 'codex' --session 'session-123'") {
		t.Fatalf("prompt = %q", prompt)
	}
	if !strings.Contains(prompt, "Do not edit files") {
		t.Fatalf("prompt lacks read-only constraint: %q", prompt)
	}
}

func TestResumeTraceRequiresSuccessfulCommandExecution(t *testing.T) {
	failed := []llm.ToolCall{{Name: "command_execution", Input: commandEventJSON(`spin resume --source codex`, 1)}}
	if !attemptedResume(failed) || usedResume(failed) {
		t.Fatal("failed resume command was not classified correctly")
	}
	completed := []llm.ToolCall{{Name: "command_execution", Input: commandEventJSON(`'/tmp/spin' resume --source codex`, 0)}}
	if !usedResume(completed) {
		t.Fatal("successful resume command was not detected")
	}
	if attemptedResume([]llm.ToolCall{{Name: "command_execution", Input: commandEventJSON(`spin wake`, 0)}}) {
		t.Fatal("wake command counted as resume")
	}
}

func commandEventJSON(command string, exitCode int) []byte {
	return []byte(fmt.Sprintf(`{"item":{"command":%q,"exit_code":%d}}`, command, exitCode))
}
