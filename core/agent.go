package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/menny/cassandra/tools"
	"github.com/tmc/langchaingo/llms"
)

const (
	maxIterationsPerFile = 5
	absoluteMaxIter      = 25
)

// Agent orchestrates the ReAct (Reason + Act) loop between the LLM and the tool registry.
type Agent struct {
	llm      llms.Model
	registry *tools.Registry
	stderr   io.Writer
}

// NewAgent creates an Agent. Progress and diagnostics are written to stderr;
// the final review is returned as a string (caller routes it to stdout).
func NewAgent(llm llms.Model, registry *tools.Registry) *Agent {
	return &Agent{llm: llm, registry: registry, stderr: os.Stderr}
}

// RunReview executes the ReAct loop.
// maxIterations controls how many tool-call rounds are permitted before the loop
// is forcibly terminated. Pass 0 to use the default cap.
func (a *Agent) RunReview(ctx context.Context, systemPrompt, requestText string, maxIterations int) (string, error) {
	if maxIterations <= 0 {
		maxIterations = absoluteMaxIter
	}

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, requestText),
	}

	langchainTools := a.registry.ToLangChainTools()

	for iter := range maxIterations {
		resp, err := a.llm.GenerateContent(ctx, messages, llms.WithTools(langchainTools))
		if err != nil {
			return "", fmt.Errorf("llm call failed on iteration %d: %w", iter+1, err)
		}

		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("llm returned no choices on iteration %d", iter+1)
		}

		choice := resp.Choices[0]

		// No tool calls → LLM has produced its final review.
		if len(choice.ToolCalls) == 0 {
			fmt.Fprintln(a.stderr, "Cassandra is reviewing the code...")
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

		// Execute each tool and append its result.
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

			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tc.ID,
						Name:       name,
						Content:    result,
					},
				},
			})
		}
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

	resp, err := a.llm.GenerateContent(ctx, messages, llms.WithTools(langchainTools))
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
