package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/menny/cassandra/llm"
	"golang.org/x/term"
)

var askDeveloperTimeout = 2 * time.Minute

type askDeveloperArgs struct {
	Question  string `json:"question"`
	Reasoning string `json:"reasoning"`
}

func registerAskDeveloper(r *Registry, notifier UserNotifier) {
	def := llm.ToolDef{
		Name:        "ask_developer",
		Description: "Ask the developer a question when you hit an architectural ambiguity or need context on business logic.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The specific question for the developer.",
				},
				"reasoning": map[string]any{
					"type":        "string",
					"description": "Why this information is blocking the review.",
				},
			},
			"required": []string{"question", "reasoning"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args askDeveloperArgs) (string, error) {
		notifier.NotifyUser()
		var response string

		noteText := fmt.Sprintf("**Question:** %s\n\n**Reasoning:** %s", args.Question, args.Reasoning)

		form := huh.NewForm(
			huh.NewGroup(
				huh.NewNote().
					Title("Cassandra AI Reviewer").
					Description(noteText),
				huh.NewInput().
					Title("Your Response").
					Value(&response),
			),
		)

		// Apply custom Cassandra styling
		theme := huh.ThemeCharm()
		// Customize green and peach colors matching Cassandra's TUI palette
		theme.Focused.Title = theme.Focused.Title.Foreground(lipgloss.Color("216")).Bold(true)            // Peach/Warm Amber
		theme.Focused.Description = theme.Focused.Description.Foreground(lipgloss.Color("243"))           // Muted Gray
		theme.Focused.Base = theme.Focused.Base.BorderLeft(true).BorderForeground(lipgloss.Color("107"))  // Green border
		theme.Focused.TextInput.Prompt = theme.Focused.TextInput.Prompt.Foreground(lipgloss.Color("178")) // Soft amber spinner color
		theme.Focused.TextInput.Text = theme.Focused.TextInput.Text.Foreground(lipgloss.Color("223"))     // Cream

		var input io.Reader = os.Stdin
		if in, ok := ctx.Value(stdinKey{}).(io.Reader); ok {
			input = in
		}
		var output io.Writer = os.Stderr
		if out, ok := ctx.Value(stderrKey{}).(io.Writer); ok {
			output = out
		}

		accessible := !term.IsTerminal(int(os.Stdin.Fd()))
		if ctx.Value(stdinKey{}) != nil {
			accessible = true
		}
		form.WithAccessible(accessible)

		form.WithTheme(theme)
		form.WithInput(input)
		form.WithOutput(output)

		timeout := askDeveloperTimeout
		if t, ok := ctx.Value(timeoutKey{}).(time.Duration); ok {
			timeout = t
		}

		timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		// Defensive goroutine to unblock any blocking read (e.g. in accessible mode / tests)
		// by closing the input stream if it implements io.Closer and is not os.Stdin.
		go func() {
			<-timeoutCtx.Done()
			if closer, ok := input.(io.Closer); ok && input != os.Stdin {
				_ = closer.Close()
			}
		}()

		err := form.RunWithContext(timeoutCtx)

		// Check context status first since context cancellation/timeout might cause
		// the form to exit with nil error or huh.ErrUserAborted.
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if timeoutCtx.Err() != nil {
			res := map[string]string{
				"status":  "timeout",
				"message": fmt.Sprintf("The developer did not respond within %v. Proceed with your best assumption and note it in the review.", timeout),
			}
			jsonBytes, _ := json.Marshal(res)
			return string(jsonBytes), nil
		}

		if err != nil {
			if errors.Is(err, huh.ErrUserAborted) {
				res := map[string]string{
					"status":  "skipped",
					"message": "The developer did not respond. Proceed with your best assumption and note it in the review.",
				}
				jsonBytes, _ := json.Marshal(res)
				return string(jsonBytes), nil
			}
			return "", fmt.Errorf("ask_developer failed: %w", err)
		}

		if strings.TrimSpace(response) == "" {
			res := map[string]string{
				"status":  "skipped",
				"message": "The developer did not respond. Proceed with your best assumption and note it in the review.",
			}
			jsonBytes, _ := json.Marshal(res)
			return string(jsonBytes), nil
		}

		// We do not enforce a 40 KB output size cap on developer responses because
		// it is direct, trusted interactive developer input specifically gathered
		// to clarify review context, rather than unverified external file/network data.
		res := map[string]string{
			"status":   "answered",
			"response": response,
		}
		jsonBytes, _ := json.Marshal(res)
		return string(jsonBytes), nil
	})
}

type (
	stdinKey   struct{}
	stderrKey  struct{}
	timeoutKey struct{}
)

// WithTestStreams wraps a context to override standard input/output of ask_developer.
func WithTestStreams(ctx context.Context, in io.Reader, out io.Writer) context.Context {
	ctx = context.WithValue(ctx, stdinKey{}, in)
	return context.WithValue(ctx, stderrKey{}, out)
}

// WithAskDeveloperTimeout wraps a context to override the ask_developer timeout.
func WithAskDeveloperTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, timeoutKey{}, d)
}
