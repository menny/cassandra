package core

import (
	"context"
	"encoding/json"
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
	HandleCall(name string, args map[string]any) (string, error)
}

// AgentOption configures an Agent.
type AgentOption func(*Agent)

// WithStderr redirects diagnostic/progress output to w instead of os.Stderr.
// Useful in tests to suppress noise (pass io.Discard).
func WithStderr(w io.Writer) AgentOption {
	return func(a *Agent) { a.stderr = w }
}

// Agent orchestrates the ReAct (Reason + Act) loop between the LLM and the tool registry.
type Agent struct {
	llm      llm.Model
	registry ToolDispatcher
	stderr   io.Writer
}

// NewAgent creates an Agent. Diagnostic / progress output goes to os.Stderr by
// default; override with WithStderr. The final review is returned as a string
// (caller routes it to stdout).
func NewAgent(model llm.Model, registry ToolDispatcher, opts ...AgentOption) *Agent {
	a := &Agent{llm: model, registry: registry, stderr: os.Stderr}
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
		fmt.Fprintln(a.stderr, "Cassandra is reviewing the code...")
		resp, err := a.llm.GenerateContent(ctx, messages, tools, maxTokens)
		if err != nil {
			return "", fmt.Errorf("llm call failed on iteration %d: %w", iter+1, err)
		}

		// No tool calls → LLM has produced its final review.
		if len(resp.ToolCalls) == 0 {
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
			// Decode JSON arguments.
			var args map[string]any
			if tc.Arguments != "" {
				if decodeErr := json.Unmarshal([]byte(tc.Arguments), &args); decodeErr != nil {
					args = map[string]any{"raw": tc.Arguments}
				}
			}

			// Progress line: print tool name + a compact summary of args.
			fmt.Fprintf(a.stderr, "Cassandra asked to run tool %q (%s)\n", tc.Name, compactArgs(args))

			// Dispatch; on error, surface the message as the tool result so the
			// LLM can reason about it rather than crashing the whole loop.
			result, toolErr := a.registry.HandleCall(tc.Name, args)
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
	fmt.Fprintf(a.stderr,
		"Warning: reached maximum ReAct iterations (%d). Forcing final review.\n",
		maxIterations,
	)

	messages = append(messages, llm.Message{Role: llm.RoleUser, Text: capMsg})
	fmt.Fprintln(a.stderr, "Cassandra is reviewing the code...")

	resp, err := a.llm.GenerateContent(ctx, messages, tools, maxTokens)
	if err != nil {
		return "", fmt.Errorf("llm call failed on forced-final review: %w", err)
	}
	return resp.Text, nil
}

// compactArgs returns a short human-readable summary of tool arguments.
func compactArgs(args map[string]any) string {
	if len(args) == 0 {
		return "no args"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("%v", args)
	}
	s := string(b)
	const maxLen = 120
	if len(s) > maxLen {
		s = s[:maxLen] + "…"
	}
	return s
}
