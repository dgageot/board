package loops

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectorUsesLatestSessionCostsAndMessage(t *testing.T) {
	collector := newCollector()
	for _, line := range []string{
		`{"type":"stream_started","timestamp":"2026-10-20T10:00:00Z"}`,
		`{"type":"agent_choice","message_id":"old","content":"Discard"}`,
		`{"type":"agent_choice","message_id":"final","content":"Hello"}`,
		`{"type":"agent_choice","message_id":"final","content":" world"}`,
		`{"type":"token_usage","session_id":"root","usage":{"cost":0.1,"last_message":{"Model":"provider/model","input_tokens":100,"cached_input_tokens":50,"cached_write_tokens":10,"output_tokens":20}}}`,
		`{"type":"token_usage","session_id":"root","usage":{"cost":0.15,"snapshot_only":true}}`,
		`{"type":"stream_stopped","reason":"normal","timestamp":"2026-10-20T10:00:02Z"}`,
	} {
		require.NoError(t, collector.observe([]byte(line)))
	}
	result := collector.result()
	assert.Equal(t, "Hello world", result.Output)
	require.NotNil(t, result.Metrics.Cost)
	assert.InDelta(t, .15, *result.Metrics.Cost, .000001)
	assert.Equal(t, 1, result.Metrics.Calls)
	assert.Equal(t, int64(160), result.Metrics.InputTokens)
	assert.Equal(t, int64(2000), result.Metrics.ModelMS)
	assert.Equal(t, "provider/model", result.Metrics.Model)
}

func TestCollectorDoesNotCallUnknownPricingFree(t *testing.T) {
	collector := newCollector()
	require.NoError(t, collector.observe([]byte(`{"type":"token_usage","usage":{"cost":0}}`)))
	assert.Nil(t, collector.result().Metrics.Cost)
	require.NoError(t, collector.observe([]byte(`{"type":"token_usage","usage":{"cost":0.1}}`)))
	require.NoError(t, collector.observe([]byte(`{"type":"budget_usage","budgets":[{"unpriced":true}]}`)))
	assert.Nil(t, collector.result().Metrics.Cost)
}

func TestExecutableConfigRejectsCapabilities(t *testing.T) {
	for _, field := range []string{"toolsets: [{type: shell}]", "hooks: {}", "skills: true", "sub_agents: [evil]", "add_environment_info: true", "commands: {}"} {
		_, err := executableConfig("agents:\n  root:\n    model: auto\n    instruction: summarize\n    " + field + "\n")
		require.ErrorIs(t, err, ErrInvalid, field)
	}
	for _, extra := range []string{"providers: {}", "runtime: {}", "---\nagents: {}", "\nvalue: ${evil}"} {
		_, err := executableConfig("agents:\n  root:\n    model: auto\n    instruction: summarize\n" + extra)
		require.ErrorIs(t, err, ErrInvalid, extra)
	}
	data, err := executableConfig("agents:\n  root:\n    model: auto\n    instruction: summarize\n")
	require.NoError(t, err)
	assert.Contains(t, string(data), "skills: false")
	assert.Contains(t, string(data), "max_iterations: 3")
}

func TestProcessRunnerRequiresCompleteOutput(t *testing.T) {
	for _, tc := range []struct {
		name, events, exit string
		failed             bool
	}{
		{"success", `{"type":"agent_choice","content":"Answer"}\n{"type":"stream_stopped","reason":"normal"}`, "0", false},
		{"partial", `{"type":"agent_choice","content":"Answer"}`, "0", true},
		{"exit failure", `{"type":"agent_choice","content":"Answer"}\n{"type":"stream_stopped","reason":"normal"}`, "1", true},
		{"truncated", `{"type":"agent_choice","content":"Answer"}\n{"type":"stream_stopped","reason":"normal","finish_reason":"length"}`, "0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "fake-agent")
			require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\nprintf '%b\\n' '"+tc.events+"'\nexit "+tc.exit+"\n"), 0o700))
			runner := &ProcessRunner{Directory: filepath.Join(dir, "attempts"), Command: []string{script}}
			result, err := runner.Run(t.Context(), Request{ID: strings.Repeat("a", 32), Config: []byte("agents: {}"), Input: "test"}, func(Result) {})
			if tc.failed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, "Answer", result.Output)
			file, err := runner.Log(strings.Repeat("a", 32), "events")
			require.NoError(t, err)
			require.NoError(t, file.Close())
			_, err = runner.Log("../escape", "events")
			require.ErrorIs(t, err, ErrNotFound)
		})
	}
}

func TestProcessRunnerCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runner := &ProcessRunner{Directory: t.TempDir(), Command: []string{"/usr/bin/true"}}
	_, err := runner.Run(ctx, Request{ID: newID(), Input: "input"}, func(Result) {})
	require.Error(t, err)
}

func TestExecutableConfigRejectsEscapedExpansion(t *testing.T) {
	for _, field := range []string{"instruction", "description"} {
		config := "agents:\n  root:\n    model: auto\n    instruction: summarize\n"
		if field == "instruction" {
			config = "agents:\n  root:\n    model: auto\n"
		}
		config += "    " + field + ": \"\\u0024{env.HOME}\"\n"
		_, err := executableConfig(config)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestProcessRunnerCancelsDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX subprocess fixture")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "wrapper")
	child := filepath.Join(dir, "child")
	marker := filepath.Join(dir, "escaped")
	require.NoError(t, os.WriteFile(child, []byte("#!/bin/sh\nsleep 2\ntouch "+marker+"\n"), 0o700))
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"+child+" &\nwait\n"), 0o700))
	runner := &ProcessRunner{Directory: filepath.Join(dir, "runs"), Command: []string{script}}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runner.Run(ctx, Request{ID: newID(), Input: "test"}, func(Result) {})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), time.Second)
	time.Sleep(2100 * time.Millisecond)
	_, err = os.Stat(marker)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestProcessLogIsBoundedWithoutBlockingWriter(t *testing.T) {
	var output strings.Builder
	writer := boundedLog{writer: &output, remaining: 4}
	n, err := writer.Write([]byte("abcdef"))
	require.NoError(t, err)
	assert.Equal(t, 6, n)
	n, err = writer.Write([]byte("more"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, "abcd", output.String())
}
