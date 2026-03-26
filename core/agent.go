package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/tmc/langchaingo/llms"
)

const (
	maxIterationsPerFile = 5
	absoluteMaxIter      = 25
)

// ToolDispatcher is the minimal interface the Agent needs from a tool registry.
// *tools.Registry satisfies this interface; tests can supply a lightweight stub.
type ToolDispatcher interface {
	ToLangChainTools() []llms.Tool
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
	llm      llms.Model
	registry ToolDispatcher
	stderr   io.Writer
}

// NewAgent creates an Agent. Diagnostic / progress output goes to os.Stderr by
// default; override with WithStderr. The final review is returned as a string
// (caller routes it to stdout).
func NewAgent(llm llms.Model, registry ToolDispatcher, opts ...AgentOption) *Agent {
	a := &Agent{llm: llm, registry: registry, stderr: os.Stderr}
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
		maxIterations = absoluteMaxIter
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, requestText),
	}

	langchainTools := a.registry.ToLangChainTools()

	for iter := range maxIterations {
		fmt.Fprintln(a.stderr, "Cassandra is reviewing the code...")
		resp, err := a.llm.GenerateContent(ctx, messages, llms.WithTools(langchainTools), llms.WithMaxTokens(maxTokens))
		if err != nil {
			return "", fmt.Errorf("llm call failed on iteration %d: %w", iter+1, err)
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("llm returned no choices on iteration %d", iter+1)
		}

		choice := resp.Choices[0]

		// No tool calls → LLM has produced its final review.
		if len(choice.ToolCalls) == 0 {
			return choice.Content, nil
		}

		// ── Handle tool calls ────────────────────────────────────────────────

		// Append the assistant's tool-call turn to history.
		assistantMsg := llms.MessageContent{
			Role: llms.ChatMessageTypeAI,
		}
		for _, tc := range choice.ToolCalls {
			assistantMsg.Parts = append(assistantMsg.Parts, tc)
		}
		messages = append(messages, assistantMsg)

		// Execute all tool calls and collect results into ONE message.
		// All ToolCallResponse parts must be in a single user-turn so the
		// provider sees a strict model→user alternation (no consecutive user turns).
		toolResultMsg := llms.MessageContent{
			Role:  llms.ChatMessageTypeTool,
			Parts: make([]llms.ContentPart, 0, len(choice.ToolCalls)),
		}
		for _, tc := range choice.ToolCalls {
			name := tc.FunctionCall.Name
			argsRaw := tc.FunctionCall.Arguments

			// Decode JSON arguments.
			var args map[string]any
			if argsRaw != "" {
				if decodeErr := json.Unmarshal([]byte(argsRaw), &args); decodeErr != nil {
					args = map[string]any{"raw": argsRaw}
				}
			}

			// Progress line: print tool name + a compact summary of args.
			fmt.Fprintf(a.stderr, "Cassandra asked to run tool %q (%s)\n", name, compactArgs(args))

			// Dispatch; on error, surface the message as the tool result so the
			// LLM can reason about it rather than crashing the whole loop.
			result, toolErr := a.registry.HandleCall(name, args)
			if toolErr != nil {
				result = fmt.Sprintf("error: %v", toolErr)
			}

			toolResultMsg.Parts = append(toolResultMsg.Parts, llms.ToolCallResponse{
				ToolCallID: tc.ID,
				Name:       name,
				Content:    result,
			})
		}
		messages = append(messages, toolResultMsg)
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

	messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, capMsg))
	fmt.Fprintln(a.stderr, "Cassandra is reviewing the code...")

	resp, err := a.llm.GenerateContent(ctx, messages, llms.WithTools(langchainTools), llms.WithMaxTokens(maxTokens))
	if err != nil {
		return "", fmt.Errorf("llm call failed on forced-final review: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices on forced-final review")
	}
	return resp.Choices[0].Content, nil
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
