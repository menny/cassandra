package core

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/menny/cassandra/llm"
)

const (
	// MaxIterationsPerFile is the recommended number of iterations per changed file.
	MaxIterationsPerFile = 5
	// AbsoluteMaxIter is the upper bound for the ReAct loop to prevent infinite recursion.
	AbsoluteMaxIter = 25
)

// CalculateMaxIterations returns a sensible iteration cap based on the number
// of changed files, bounded by AbsoluteMaxIter.
func CalculateMaxIterations(changedFiles int) int {
	if changedFiles <= 0 {
		return AbsoluteMaxIter
	}
	return min(MaxIterationsPerFile*changedFiles, AbsoluteMaxIter)
}

// ToolDispatcher is the minimal interface the Agent needs from a tool registry.
// *tools.Registry satisfies this interface; tests can supply a lightweight stub.
type ToolDispatcher interface {
	ToTools() []llm.ToolDef
	HandleCall(tc llm.ToolCall) (string, error)
}

// Reporter defines how the Agent reports progress and diagnostics.
type Reporter interface {
	ReportIteration(iter int)
	ReportToolCall(tc llm.ToolCall)
	ReportFinalReview()
	ReportCapReached(maxIterations int)
}

// defaultReporter writes progress to an io.Writer.
type defaultReporter struct {
	w io.Writer
}

func (r *defaultReporter) ReportIteration(iter int) {
	fmt.Fprintln(r.w, "Cassandra is reviewing the code...")
}

func (r *defaultReporter) ReportToolCall(tc llm.ToolCall) {
	fmt.Fprintf(r.w, "Cassandra asked to run tool %q (%s)\n", tc.Name, compactToolCallArgs(tc))
}

func (r *defaultReporter) ReportFinalReview() {
	fmt.Fprintln(r.w, "Cassandra is reviewing the code...")
}

func (r *defaultReporter) ReportCapReached(maxIterations int) {
	fmt.Fprintf(r.w, "Warning: reached maximum ReAct iterations (%d). Forcing final review.\n", maxIterations)
}

// AgentOption configures an Agent.
type AgentOption func(*Agent)

// WithStderr redirects diagnostic/progress output to w instead of os.Stderr.
// Useful in tests to suppress noise (pass io.Discard).
func WithStderr(w io.Writer) AgentOption {
	return func(a *Agent) { a.reporter = &defaultReporter{w: w} }
}

// WithReporter sets a custom reporter for the Agent.
func WithReporter(r Reporter) AgentOption {
	return func(a *Agent) { a.reporter = r }
}

// Agent orchestrates the ReAct (Reason + Act) loop between the LLM and the tool registry.
type Agent struct {
	llm      llm.Model
	registry ToolDispatcher
	reporter Reporter
}

// NewAgent creates an Agent. Diagnostic / progress output goes to os.Stderr by
// default; override with WithStderr or WithReporter. The final review is
// returned as a string (caller routes it to stdout).
func NewAgent(model llm.Model, registry ToolDispatcher, opts ...AgentOption) *Agent {
	a := &Agent{
		llm:      model,
		registry: registry,
		reporter: &defaultReporter{w: os.Stderr},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// RunReview executes the ReAct loop.
// maxIterations controls how many tool-call rounds are permitted before the
// loop is forcibly terminated. Pass 0 to use the default cap.
// maxTokens limits the length of the LLM response.
func (a *Agent) RunReview(ctx context.Context, systemPrompt, requestText string, maxIterations, maxTokens int) (string, error) {
	if maxIterations <= 0 {
		maxIterations = AbsoluteMaxIter
	}
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Text: systemPrompt},
		{Role: llm.RoleUser, Text: requestText},
	}

	tools := a.registry.ToTools()

	for iter := range maxIterations {
		a.reporter.ReportIteration(iter + 1)
		resp, err := a.llm.GenerateContent(ctx, messages, tools, maxTokens)
		if err != nil {
			return "", fmt.Errorf("llm call failed on iteration %d: %w", iter+1, err)
		}

		// No tool calls → LLM has produced its final review.
		if len(resp.ToolCalls) == 0 {
			if resp.Text == "" {
				return "", fmt.Errorf("llm returned empty content on iteration %d", iter+1)
			}
			return resp.Text, nil
		}

		// ── Handle tool calls ────────────────────────────────────────────────

		// Append the assistant's tool-call turn to history.
		messages = append(messages, llm.Message{
			Role:      llm.RoleAssistant,
			ToolCalls: resp.ToolCalls,
		})

		// Execute all tool calls and collect results into ONE RoleTool message.
		// All ToolResults must be in a single message so providers see strict
		// role alternation (no consecutive same-role turns).
		toolMsg := llm.Message{
			Role:        llm.RoleTool,
			ToolResults: make([]llm.ToolResult, 0, len(resp.ToolCalls)),
		}
		for _, tc := range resp.ToolCalls {
			// Progress line: print tool name + a compact summary of args.
			a.reporter.ReportToolCall(tc)

			// Dispatch; on error, surface the message as the tool result so the
			// LLM can reason about it rather than crashing the whole loop.
			result, toolErr := a.registry.HandleCall(tc)
			if toolErr != nil {
				result = fmt.Sprintf("error: %v", toolErr)
			}

			toolMsg.ToolResults = append(toolMsg.ToolResults, llm.ToolResult{
				ToolCallID: tc.ID,
				Name:       tc.Name,
				Content:    result,
			})
		}
		messages = append(messages, toolMsg)
	}

	// ── Cap reached ─────────────────────────────────────────────────────────
	capMsg := fmt.Sprintf(
		"[SYSTEM] The maximum number of tool-call iterations (%d) has been reached. "+
			"You MUST now produce your final code review unconditionally, based on everything "+
			"you have gathered so far. Do not request any additional tools.",
		maxIterations,
	)
	a.reporter.ReportCapReached(maxIterations)

	messages = append(messages, llm.Message{Role: llm.RoleUser, Text: capMsg})
	a.reporter.ReportFinalReview()

	// Pass nil tools so the provider cannot issue further tool calls even if
	// it ignores the cap instruction in the prompt.
	resp, err := a.llm.GenerateContent(ctx, messages, nil, maxTokens)
	if err != nil {
		return "", fmt.Errorf("llm call failed on forced-final review: %w", err)
	}
	if resp.Text == "" {
		return "", fmt.Errorf("llm returned empty content on forced-final review")
	}
	return resp.Text, nil
}

// compactToolCallArgs returns a short human-readable summary of tool arguments.
func compactToolCallArgs(tc llm.ToolCall) string {
	if tc.Arguments == "" {
		return "no args"
	}
	s := tc.Arguments
	const maxLen = 120
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}
