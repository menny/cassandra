package core

import (
	"context"

	"github.com/menny/cassandra/tools"
	"github.com/tmc/langchaingo/llms"
)

type Agent struct {
	llm      llms.Model
	registry *tools.Registry
}

func NewAgent(llm llms.Model, registry *tools.Registry) *Agent {
	return &Agent{llm: llm, registry: registry}
}

// RunReview executes the React loop
func (a *Agent) RunReview(ctx context.Context, systemPrompt, requestText string) (string, error) {
	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, requestText),
	}

	// Simple 1-pass execution for demonstration purposes
	resp, err := a.llm.GenerateContent(ctx, messages, llms.WithTools(a.registry.ToLangChainTools()))
	if err != nil {
		return "", err
	}

	// In a fully developed ReAct loop, we would loop here indefinitely responding to ToolCalls:
	// e.g. if len(resp.Choices[0].ToolCalls) > 0 { execute tools, append results to messages, generating content again }

	return resp.Choices[0].Content, nil
}
