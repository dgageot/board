package loops

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Request is one tool-free process with a dedicated configuration and workspace.
type Request struct {
	ID     string
	Config []byte
	Input  string
}

// Result preserves the final answer and measured usage independently of success.
type Result struct {
	Output  string
	Metrics Metrics
}

// Runner executes one attempt. Implementations must respect cancellation.
type Runner interface {
	Run(ctx context.Context, request Request, progress func(Result)) (Result, error)
}

// ProcessRunner delegates execution to the installed docker agent CLI.
type ProcessRunner struct {
	Directory string
	Command   []string
}

func (r *ProcessRunner) Run(ctx context.Context, request Request, progress func(Result)) (Result, error) {
	var result Result
	started := time.Now()
	dir := filepath.Join(r.Directory, request.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.yaml"), request.Config, 0o600); err != nil {
		return result, err
	}
	for _, name := range []string{"config", "data", "cache", "work"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			return result, err
		}
	}
	file, err := os.OpenFile(filepath.Join(dir, "events.ndjson"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return result, err
	}
	defer file.Close()
	stderr, err := os.OpenFile(filepath.Join(dir, "stderr.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return result, err
	}
	defer stderr.Close()
	command := r.Command
	if len(command) == 0 {
		command = []string{"docker", "agent"}
	}
	args := append([]string{}, command[1:]...)
	args = append(args, "run", "--exec", "--json", "--safety", "restricted",
		"--config-dir", filepath.Join(dir, "config"), "--data-dir", filepath.Join(dir, "data"), "--cache-dir", filepath.Join(dir, "cache"),
		"--working-dir", filepath.Join(dir, "work"), "--", filepath.Join(dir, "agent.yaml"), "-")
	cmd := exec.CommandContext(ctx, command[0], args...)
	cmd.Dir = filepath.Join(dir, "work")
	cmd.Stdin = strings.NewReader(request.Input)
	cmd.Stderr = stderr
	cmd.Env = processEnvironment(dir)
	cmd.WaitDelay = 3 * time.Second
	isolateProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, err
	}
	if err := cmd.Start(); err != nil {
		return result, fmt.Errorf("start docker agent: %w", err)
	}
	// Closing the pipe also unblocks Scan if a descendant retained stdout.
	stopClose := context.AfterFunc(ctx, func() { _ = stdout.Close() })
	defer stopClose()
	collector := newCollector()
	scan := bufio.NewScanner(stdout)
	scan.Buffer(nil, 1024*1024)
	lastProgress := time.Time{}
	total := 0
	var streamErr error
	for scan.Scan() {
		line := scan.Bytes()
		total += len(line) + 1
		if total > 16*1024*1024 {
			streamErr = errors.New("event output exceeded 16 MiB")
			break
		}
		if _, err := file.Write(append(append([]byte{}, line...), '\n')); err != nil {
			streamErr = err
			break
		}
		if err := collector.observe(line); err != nil {
			streamErr = err
			break
		}
		if time.Since(lastProgress) >= 300*time.Millisecond {
			result = collector.result()
			result.Metrics.DurationMS = time.Since(started).Milliseconds()
			progress(result)
			lastProgress = time.Now()
		}
	}
	if err := scan.Err(); err != nil {
		streamErr = err
	}
	if streamErr != nil {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	result = collector.result()
	result.Metrics.DurationMS = time.Since(started).Milliseconds()
	if err := file.Sync(); err != nil && streamErr == nil {
		streamErr = err
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if streamErr != nil {
		return result, fmt.Errorf("read agent events: %w", streamErr)
	}
	if waitErr != nil {
		return result, fmt.Errorf("docker agent failed: %w (see process log)", waitErr)
	}
	if !collector.completed || collector.reason != "normal" || collector.finish == "length" || strings.TrimSpace(result.Output) == "" {
		return result, fmt.Errorf("incomplete answer (stop=%q, finish=%q)", collector.reason, collector.finish)
	}
	return result, nil
}

// Keep provider credentials, but don't inherit ambient execution hooks or configs.
func processEnvironment(dir string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if (strings.HasPrefix(name, "DOCKER_AGENT_") && name != "DOCKER_AGENT_MODELS_GATEWAY") || strings.HasPrefix(name, "CAGENT_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "DOCKER_AGENT_HIDE_TELEMETRY_BANNER=1", "TELEMETRY_ENABLED=false", "XDG_CONFIG_HOME="+filepath.Join(dir, "config"))
}

type eventCollector struct {
	output    strings.Builder
	message   string
	metrics   Metrics
	costs     map[string]float64
	seenUsage bool
	unpriced  bool
	completed bool
	reason    string
	finish    string
	started   time.Time
	stopped   time.Time
}

func newCollector() *eventCollector { return &eventCollector{costs: map[string]float64{}} }

func (c *eventCollector) observe(line []byte) error {
	var event struct {
		Type      string    `json:"type"`
		Content   string    `json:"content"`
		MessageID string    `json:"message_id"`
		SessionID string    `json:"session_id"`
		Reason    string    `json:"reason"`
		Finish    string    `json:"finish_reason"`
		Timestamp time.Time `json:"timestamp"`
		Usage     *struct {
			Cost     float64 `json:"cost"`
			Snapshot bool    `json:"snapshot_only"`
			Last     *struct {
				Model  string `json:"Model"`
				Input  int64  `json:"input_tokens"`
				Output int64  `json:"output_tokens"`
			} `json:"last_message"`
		} `json:"usage"`
		Budgets []struct {
			Unpriced bool `json:"unpriced"`
		} `json:"budgets"`
	}
	if err := json.Unmarshal(line, &event); err != nil {
		return fmt.Errorf("invalid JSON event: %w", err)
	}
	if event.Type == "" {
		return errors.New("event has no type")
	}
	switch event.Type {
	case "stream_started":
		if c.started.IsZero() {
			c.started = event.Timestamp
		}
	case "agent_choice":
		if event.MessageID != c.message {
			c.output.Reset()
			c.message = event.MessageID
		}
		c.output.WriteString(event.Content)
	case "stream_stopped":
		c.completed, c.reason, c.finish, c.stopped = true, event.Reason, event.Finish, event.Timestamp
	case "budget_usage":
		for _, budget := range event.Budgets {
			c.unpriced = c.unpriced || budget.Unpriced
		}
	case "token_usage":
		if event.Usage != nil {
			c.seenUsage = true
			c.costs[event.SessionID] = event.Usage.Cost
			if last := event.Usage.Last; last != nil && !event.Usage.Snapshot {
				c.metrics.Calls++
				c.metrics.InputTokens += last.Input
				c.metrics.OutputTokens += last.Output
				c.metrics.Model = last.Model
			}
		}
	case "error", "tool_call", "tool_call_confirmation", "max_iterations_reached", "budget_exceeded":
		return fmt.Errorf("unexpected %s in text-only attempt", event.Type)
	}
	return nil
}

func (c *eventCollector) result() Result {
	metrics := c.metrics
	var cost float64
	for _, value := range c.costs {
		cost += value
	}
	// Zero spend is ambiguous without a pricing-status event; never call it free.
	if c.seenUsage && !c.unpriced && cost > 0 {
		metrics.Cost = &cost
	}
	if !c.started.IsZero() && !c.stopped.IsZero() && c.stopped.After(c.started) {
		metrics.ModelMS = c.stopped.Sub(c.started).Milliseconds()
	}
	return Result{Output: c.output.String(), Metrics: metrics}
}

// Log opens the immutable raw event or stderr artifact of an attempt.
func (r *ProcessRunner) Log(id, kind string) (io.ReadCloser, error) {
	if !regexpID(id) {
		return nil, ErrNotFound
	}
	name := "events.ndjson"
	if kind == "stderr" {
		name = "stderr.log"
	}
	return os.Open(filepath.Join(r.Directory, id, name))
}

func regexpID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
