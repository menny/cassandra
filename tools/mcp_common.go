package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// unpackMCPTextEnvelope extracts the plain text result from a standard MCP tool call JSON envelope.
// If the body is not a JSON envelope, it returns the body as a raw string.
// If the envelope indicates isError: true, it returns a formatted error.
func unpackMCPTextEnvelope(body []byte, toolDesc string) (string, error) {
	var env struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(body, &env); err == nil && (len(env.Content) > 0 || len(env.StructuredContent) > 0 || env.IsError) {
		if env.IsError {
			var errMsgs []string
			for _, c := range env.Content {
				if c.Text != "" {
					errMsgs = append(errMsgs, c.Text)
				}
			}
			if len(errMsgs) > 0 {
				return "", fmt.Errorf("calling %s failed: %s", toolDesc, strings.Join(errMsgs, "; "))
			}
			return "", fmt.Errorf("calling %s failed", toolDesc)
		}
		if len(env.StructuredContent) > 0 && string(env.StructuredContent) != "null" {
			return string(env.StructuredContent), nil
		} else if len(env.Content) > 0 {
			var texts []string
			for _, c := range env.Content {
				if c.Text != "" {
					texts = append(texts, c.Text)
				}
			}
			if len(texts) > 0 {
				return strings.Join(texts, "\n"), nil
			}
		}
	}
	return string(body), nil
}

// normalizeReviewFixTitle formats a session or thread title with the standard "Review Fix" prefix.
func normalizeReviewFixTitle(title string) string {
	trimmed := strings.TrimSpace(title)
	if strings.HasPrefix(trimmed, "Review Fix") {
		rest := strings.TrimLeft(strings.TrimPrefix(trimmed, "Review Fix"), " -:")
		if rest != "" {
			return "Review Fix " + rest
		}
		return "Review Fix"
	} else if trimmed != "" {
		return "Review Fix " + trimmed
	}
	return "Review Fix"
}
