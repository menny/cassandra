package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

func TestSubagentRegistryPolicies(t *testing.T) {
	r := NewRegistry()
	handler := func(ctx context.Context, tc llm.ToolCall) (string, error) {
		return tc.Arguments, ctx.Err()
	}
	r.RegisterTool(llm.ToolDef{Name: "builtin"}, handler)
	r.RegisterTool(llm.ToolDef{Name: "explicit_builtin"}, handler, ToolPolicy{AllowSubagents: true})
	r.RegisterTool(llm.ToolDef{Name: "privileged"}, handler, ToolPolicy{AllowSubagents: false})
	r.RegisterTool(llm.ToolDef{Name: "mcp"}, handler, ToolPolicy{AllowSubagents: true, IsMCP: true})
	r.RegisterTool(llm.ToolDef{Name: "privileged_mcp"}, handler, ToolPolicy{IsMCP: true})
	before := slices.Clone(r.ToTools())

	for _, allowMCPs := range []bool{false, true} {
		t.Run(fmt.Sprintf("allowMCPs=%t", allowMCPs), func(t *testing.T) {
			view := r.ForSubagent(allowMCPs)
			want := []string{"builtin", "explicit_builtin"}
			if allowMCPs {
				want = append(want, "mcp")
			}
			var names []string
			for _, def := range view.ToTools() {
				names = append(names, def.Name)
			}
			require.Equal(t, want, names)

			for _, def := range before {
				result, err := view.HandleCall(context.Background(), llm.ToolCall{Name: def.Name, Arguments: "payload"})
				if slices.Contains(want, def.Name) {
					require.NoError(t, err)
					require.Equal(t, "payload", result)
				} else {
					require.ErrorContains(t, err, "not allowed for subagents")
					require.Empty(t, result)
				}
				// Subagent filtering must not restrict parent access.
				result, err = r.HandleCall(context.Background(), llm.ToolCall{Name: def.Name, Arguments: "parent"})
				require.NoError(t, err)
				require.Equal(t, "parent", result)
			}
			_, err := view.HandleCall(context.Background(), llm.ToolCall{Name: "guessed_unknown"})
			require.ErrorContains(t, err, "not found")

			// Changing a returned slice must not alter this view or the parent.
			defs := view.ToTools()
			defs[0].Name = "modified"
			require.Equal(t, "builtin", view.ToTools()[0].Name)
			require.Equal(t, before, r.ToTools())
		})
	}
}

func TestSubagentRegistryPrivilegedLocalTools(t *testing.T) {
	t.Setenv("T3_ACP_MCP_NODE", "must-not-execute")
	t.Setenv("OMNIGENT", "1")
	r := NewRegistry()
	RegisterLocalTools(r, t.TempDir(), "base", "head", nil, "", true, true, nil, nil)

	blocked := []string{
		"link_pull_request",
		"list_thread_pull_requests",
		"orchestrator_capabilities",
		"t3_thread_launch",
		"omnigent_agent_capabilities",
		"omnigent_session_launch",
		"update_review_verdict",
		"post_pr_comment",
	}
	var parentNames []string
	for _, def := range r.ToTools() {
		parentNames = append(parentNames, def.Name)
	}
	for _, name := range blocked {
		require.Contains(t, parentNames, name)
		// A failing replacement handler proves denied calls never reach it,
		// even when the model guesses a hidden name with malformed arguments.
		r.handlers[name] = func(ctx context.Context, tc llm.ToolCall) (string, error) {
			t.Errorf("blocked handler invoked: %s", tc.Name)
			return "", ctx.Err()
		}
	}
	for _, allowMCPs := range []bool{false, true} {
		view := r.ForSubagent(allowMCPs)
		var names []string
		for _, def := range view.ToTools() {
			names = append(names, def.Name)
		}
		for _, name := range blocked {
			require.NotContains(t, names, name)
			_, err := view.HandleCall(context.Background(), llm.ToolCall{Name: name, Arguments: "{malformed"})
			require.ErrorContains(t, err, "not allowed for subagents")
		}
		for _, name := range parentNames {
			if !slices.Contains(blocked, name) {
				require.Contains(t, names, name)
			}
		}
	}
	for _, name := range blocked {
		r.RegisterTool(llm.ToolDef{Name: name}, r.handlers[name], ToolPolicy{AllowSubagents: true, IsMCP: true})
		_, err := r.ForSubagent(true).HandleCall(context.Background(), llm.ToolCall{Name: name})
		require.ErrorContains(t, err, "not allowed for subagents")
	}
}

func TestSubagentRegistryForwardsCallAndCancellation(t *testing.T) {
	for _, withArgs := range []bool{false, true} {
		t.Run(fmt.Sprintf("withArgs=%t", withArgs), func(t *testing.T) {
			r := NewRegistry()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type args struct {
				Value string `json:"value"`
			}
			wantArgs := args{Value: "forwarded"}
			payload, err := json.Marshal(wantArgs)
			require.NoError(t, err)
			call := llm.ToolCall{ID: "call-id", Name: "test", Arguments: string(payload)}
			handlerErr := errors.New("handler failed")
			calls := 0
			if withArgs {
				RegisterToolWithArgs(r, llm.ToolDef{Name: call.Name}, func(received context.Context, got args) (string, error) {
					calls++
					require.Same(t, ctx, received)
					require.Equal(t, wantArgs, got)
					if err := received.Err(); err != nil {
						return "", err
					}
					return "partial", handlerErr
				}, ToolPolicy{AllowSubagents: true})
			} else {
				r.RegisterTool(llm.ToolDef{Name: call.Name}, func(received context.Context, got llm.ToolCall) (string, error) {
					calls++
					require.Same(t, ctx, received)
					require.Equal(t, call, got)
					if err := received.Err(); err != nil {
						return "", err
					}
					return "partial", handlerErr
				})
			}
			view := r.ForSubagent(false)
			result, err := view.HandleCall(ctx, call)
			require.Equal(t, "partial", result)
			require.ErrorIs(t, err, handlerErr)
			cancel()
			_, err = view.HandleCall(ctx, call)
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, 2, calls)
			if withArgs {
				call.Arguments = "{malformed"
				_, err = view.HandleCall(ctx, call)
				require.ErrorContains(t, err, "unmarshaling test arguments")
				require.Equal(t, 2, calls)
			}
		})
	}
}

func TestRegistryReplacementUpdatesPolicy(t *testing.T) {
	r := NewRegistry()
	def := llm.ToolDef{Name: "replace"}
	handler := func(ctx context.Context, tc llm.ToolCall) (string, error) {
		return tc.Name, ctx.Err()
	}
	r.RegisterTool(def, handler)
	require.Len(t, r.ForSubagent(false).ToTools(), 1)
	RegisterToolWithArgs(r, def, func(ctx context.Context, _ struct{}) (string, error) {
		return "replacement", ctx.Err()
	}, ToolPolicy{AllowSubagents: false})
	require.Empty(t, r.ForSubagent(true).ToTools())
	_, err := r.ForSubagent(true).HandleCall(context.Background(), llm.ToolCall{Name: def.Name})
	require.ErrorContains(t, err, "not allowed for subagents")
	r.RegisterTool(def, handler)
	require.Empty(t, r.ForSubagent(true).ToTools())
	r.RegisterTool(def, handler, ToolPolicy{AllowSubagents: true, IsMCP: true})
	require.Empty(t, r.ForSubagent(true).ToTools(), "MCP replacement must not reopen a denied name")
	// An explicit built-in policy may deliberately grant access again.
	r.RegisterTool(def, handler, ToolPolicy{AllowSubagents: true})
	require.Len(t, r.ForSubagent(false).ToTools(), 1)
	r.RegisterTool(def, handler, ToolPolicy{AllowSubagents: true, IsMCP: true})
	require.Empty(t, r.ForSubagent(false).ToTools())
	require.Len(t, r.ForSubagent(true).ToTools(), 1)
	r.RegisterTool(def, handler)
	require.Len(t, r.ToTools(), 1)
	require.Empty(t, r.ForSubagent(false).ToTools(), "omitted policy must preserve MCP provenance")
	require.Len(t, r.ForSubagent(true).ToTools(), 1)
}

func TestSubagentRegistryConcurrentViews(t *testing.T) {
	r := NewRegistry()
	r.RegisterTool(llm.ToolDef{Name: "builtin"}, func(ctx context.Context, tc llm.ToolCall) (string, error) {
		return tc.Name, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				view := r.ForSubagent(false)
				if len(view.ToTools()) != 1 {
					t.Error("unexpected tool count")
				}
				result, err := view.HandleCall(ctx, llm.ToolCall{Name: "builtin"})
				if err != nil || result != "builtin" {
					t.Errorf("dispatch = %q, %v", result, err)
				}
			}
		}()
	}
	wg.Wait()
	require.Len(t, r.ToTools(), 1)
}
