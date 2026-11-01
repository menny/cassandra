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

const OtherChoiceOption = "Other (specify below)"

// AskDeveloperRequest represents a question or prompt presented to the developer.
type AskDeveloperRequest struct {
	Question      string   `json:"question"`
	Reasoning     string   `json:"reasoning"`
	Options       []string `json:"options,omitempty"`
	IsMultiSelect bool     `json:"is_multi_select,omitempty"`
}

// AskDeveloperResponse represents the structured response from asking the developer.
type AskDeveloperResponse struct {
	Status   string   `json:"status"`             // "answered", "skipped", "timeout"
	Selected []string `json:"selected,omitempty"` // selected options, if any
	Response string   `json:"response,omitempty"` // free-text response, if provided
	Message  string   `json:"message,omitempty"`  // explanation when skipped or timed out
}

// DeveloperPrompter handles presenting clarification questions to the developer
// across different interaction modes (terminal, TUI, ACP elicitation, etc.).
type DeveloperPrompter interface {
	AskDeveloper(ctx context.Context, req AskDeveloperRequest) (AskDeveloperResponse, error)
}

type askDeveloperArgs struct {
	Question      string   `json:"question"`
	Reasoning     string   `json:"reasoning"`
	Options       []string `json:"options,omitempty"`
	IsMultiSelect bool     `json:"is_multi_select,omitempty"`
}

func registerAskDeveloper(r *Registry, prompter DeveloperPrompter, notifier UserNotifier) {
	if notifier == nil {
		notifier = NoOpUserNotifier{}
	}
	if prompter == nil {
		prompter = DefaultTerminalPrompter{}
	}

	def := llm.ToolDef{
		Name:        "ask_developer",
		Description: "Ask the developer a question when you hit an architectural ambiguity or need context on business logic. Supports open-ended questions or structured multiple-choice selections.",
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
				"options": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
					"description": "Optional list of choices for the developer to select from.",
				},
				"is_multi_select": map[string]any{
					"type":        "boolean",
					"description": "If true, the developer can select multiple options. Defaults to false (single selection).",
				},
			},
			"required": []string{"question", "reasoning"},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args askDeveloperArgs) (string, error) {
		notifier.NotifyUser()

		activePrompter := prompter
		if p, ok := ctx.Value(prompterKey{}).(DeveloperPrompter); ok && p != nil {
			activePrompter = p
		}

		req := AskDeveloperRequest{
			Question:      args.Question,
			Reasoning:     args.Reasoning,
			Options:       args.Options,
			IsMultiSelect: args.IsMultiSelect,
		}

		resp, err := activePrompter.AskDeveloper(ctx, req)
		if err != nil {
			return "", err
		}

		jsonBytes, err := json.Marshal(resp)
		if err != nil {
			return "", fmt.Errorf("marshaling ask_developer response: %w", err)
		}
		return string(jsonBytes), nil
	})
}

// DefaultTerminalPrompter prompts the developer via Charm huh forms in the terminal.
type DefaultTerminalPrompter struct{}

func (DefaultTerminalPrompter) AskDeveloper(ctx context.Context, req AskDeveloperRequest) (AskDeveloperResponse, error) {
	noteText := fmt.Sprintf("**Question:** %s\n\n**Reasoning:** %s", req.Question, req.Reasoning)

	var selectedOptions []string
	var chosenOption string
	var response string

	fields := []huh.Field{
		huh.NewNote().
			Title("Cassandra AI Reviewer").
			Description(noteText),
	}

	if len(req.Options) > 0 {
		if req.IsMultiSelect {
			opts := make([]huh.Option[string], 0, len(req.Options))
			for _, o := range req.Options {
				opts = append(opts, huh.NewOption(o, o))
			}
			fields = append(fields,
				huh.NewMultiSelect[string]().
					Title("Select option(s)").
					Options(opts...).
					Value(&selectedOptions),
				huh.NewInput().
					Title("Additional Response / Notes (Optional)").
					Value(&response),
			)
		} else {
			opts := make([]huh.Option[string], 0, len(req.Options)+1)
			for _, o := range req.Options {
				opts = append(opts, huh.NewOption(o, o))
			}
			opts = append(opts, huh.NewOption(OtherChoiceOption, OtherChoiceOption))
			fields = append(fields,
				huh.NewSelect[string]().
					Title("Select an option").
					Options(opts...).
					Value(&chosenOption),
				huh.NewInput().
					Title("Your Response / Details").
					Value(&response),
			)
		}
	} else {
		fields = append(fields,
			huh.NewInput().
				Title("Your Response").
				Value(&response),
		)
	}

	form := huh.NewForm(huh.NewGroup(fields...))

	// Apply custom Cassandra styling
	theme := huh.ThemeCharm()
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
	if t, ok := ctx.Value(TimeoutKey{}).(time.Duration); ok {
		timeout = t
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Defensive goroutine to unblock any blocking read by closing input stream if it implements io.Closer
	go func() {
		<-timeoutCtx.Done()
		if closer, ok := input.(io.Closer); ok && input != os.Stdin {
			_ = closer.Close()
		}
	}()

	err := form.RunWithContext(timeoutCtx)

	if ctx.Err() != nil {
		return AskDeveloperResponse{}, ctx.Err()
	}
	if timeoutCtx.Err() != nil {
		return AskDeveloperResponse{
			Status:  "timeout",
			Message: fmt.Sprintf("The developer did not respond within %v. Proceed with your best assumption and note it in the review.", timeout),
		}, nil
	}

	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return AskDeveloperResponse{
				Status:  "skipped",
				Message: "The developer did not respond. Proceed with your best assumption and note it in the review.",
			}, nil
		}
		return AskDeveloperResponse{}, fmt.Errorf("ask_developer failed: %w", err)
	}

	response = strings.TrimSpace(response)

	if len(req.Options) > 0 {
		if req.IsMultiSelect {
			if len(selectedOptions) == 0 && response == "" {
				return AskDeveloperResponse{
					Status:  "skipped",
					Message: "The developer did not respond. Proceed with your best assumption and note it in the review.",
				}, nil
			}
			return AskDeveloperResponse{
				Status:   "answered",
				Selected: selectedOptions,
				Response: response,
			}, nil
		}

		var selected []string
		if chosenOption != "" && chosenOption != OtherChoiceOption {
			selected = []string{chosenOption}
		}
		if len(selected) == 0 && response == "" {
			return AskDeveloperResponse{
				Status:  "skipped",
				Message: "The developer did not respond. Proceed with your best assumption and note it in the review.",
			}, nil
		}
		return AskDeveloperResponse{
			Status:   "answered",
			Selected: selected,
			Response: response,
		}, nil
	}

	if response == "" {
		return AskDeveloperResponse{
			Status:  "skipped",
			Message: "The developer did not respond. Proceed with your best assumption and note it in the review.",
		}, nil
	}

	return AskDeveloperResponse{
		Status:   "answered",
		Response: response,
	}, nil
}

type (
	stdinKey    struct{}
	stderrKey   struct{}
	TimeoutKey  struct{}
	prompterKey struct{}
)

// WithTestStreams wraps a context to override standard input/output of ask_developer.
func WithTestStreams(ctx context.Context, in io.Reader, out io.Writer) context.Context {
	ctx = context.WithValue(ctx, stdinKey{}, in)
	return context.WithValue(ctx, stderrKey{}, out)
}

// WithAskDeveloperTimeout wraps a context to override the ask_developer timeout.
func WithAskDeveloperTimeout(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, TimeoutKey{}, d)
}

// WithDeveloperPrompter wraps a context to override the DeveloperPrompter.
func WithDeveloperPrompter(ctx context.Context, p DeveloperPrompter) context.Context {
	return context.WithValue(ctx, prompterKey{}, p)
}
