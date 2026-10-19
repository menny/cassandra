package tools

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newMockGitHubClient(fn roundTripFunc) *github.Client {
	httpClient := &http.Client{
		Transport: fn,
	}
	return github.NewClient(httpClient)
}

func TestPRCommentTool(t *testing.T) {
	origFactory := gitHubClientFactory
	defer func() { gitHubClientFactory = origFactory }()

	t.Run("missing GITHUB_TOKEN and GH_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")

		r := NewRegistry()
		registerPRCommentTool(r, "", "")

		tc := llm.ToolCall{
			ID:        "tc_1",
			Name:      "post_pr_comment",
			Arguments: `{"comment": "Looks good", "repo": "owner/repo", "pr_number": 12}`,
		}
		_, err := r.HandleCall(context.Background(), tc)
		require.Error(t, err)
		require.Contains(t, err.Error(), "GITHUB_TOKEN (or GH_TOKEN) environment variable is required")
	})

	t.Run("missing repo and pr_number cannot be resolved", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "dummy-token")

		r := NewRegistry()
		registerPRCommentTool(r, "", "")

		tc := llm.ToolCall{
			ID:        "tc_2",
			Name:      "post_pr_comment",
			Arguments: `{"comment": "Looks good"}`,
		}
		_, err := r.HandleCall(context.Background(), tc)
		require.Error(t, err)
		require.Contains(t, err.Error(), "could not determine repository")
	})

	t.Run("post top-level PR comment successfully", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "dummy-token")

		gitHubClientFactory = func(_ string) *github.Client {
			return newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, "/repos/owner/repo/issues/42/comments", req.URL.Path)
				respJSON := `{"id":101,"html_url":"https://github.com/owner/repo/issues/42#issuecomment-101","body":"comment"}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewBufferString(respJSON)),
				}, nil
			})
		}

		r := NewRegistry()
		registerPRCommentTool(r, "", "")

		tc := llm.ToolCall{
			ID:        "tc_top",
			Name:      "post_pr_comment",
			Arguments: `{"comment": "Overall architectural design looks very solid.", "repo": "owner/repo", "pr_number": 42}`,
		}
		res, err := r.HandleCall(context.Background(), tc)
		require.NoError(t, err)
		require.Contains(t, res, `"type":"top_level"`)
		require.Contains(t, res, `"id":101`)
		require.Contains(t, res, `"owner/repo"`)
	})

	t.Run("post inline PR comment successfully", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "dummy-token")

		gitHubClientFactory = func(_ string) *github.Client {
			return newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodGet && req.URL.Path == "/repos/owner/repo/pulls/42" {
					prJSON := `{"head":{"sha":"abc123commitsha"}}`
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(bytes.NewBufferString(prJSON)),
					}, nil
				}
				if req.Method == http.MethodPost && req.URL.Path == "/repos/owner/repo/pulls/42/comments" {
					commentJSON := `{"id":202,"html_url":"https://github.com/owner/repo/pull/42#discussion_r202","path":"core/reviewer.go","line":45}`
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(bytes.NewBufferString(commentJSON)),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
				}, nil
			})
		}

		r := NewRegistry()
		registerPRCommentTool(r, "", "")

		tc := llm.ToolCall{
			ID:   "tc_inline",
			Name: "post_pr_comment",
			Arguments: `{
				"comment": "Consider handling nil pointer here.",
				"repo": "owner/repo",
				"pr_number": 42,
				"path": "core/reviewer.go",
				"line": 45,
				"start_line": 40
			}`,
		}
		res, err := r.HandleCall(context.Background(), tc)
		require.NoError(t, err)
		require.Contains(t, res, `"type":"inline"`)
		require.Contains(t, res, `"id":202`)
		require.Contains(t, res, `"core/reviewer.go"`)
	})

	t.Run("resolve target from metadata JSON file", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "dummy-token")

		tmpDir := t.TempDir()
		metaFile := filepath.Join(tmpDir, "metadata.json")
		metaJSON := `{"repo_full_name": "detected-owner/detected-repo", "pr_number": 99}`
		require.NoError(t, os.WriteFile(metaFile, []byte(metaJSON), 0o644))

		gitHubClientFactory = func(_ string) *github.Client {
			return newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
				require.Equal(t, http.MethodPost, req.Method)
				require.Equal(t, "/repos/detected-owner/detected-repo/issues/99/comments", req.URL.Path)
				respJSON := `{"id":303,"html_url":"https://github.com/detected-owner/detected-repo/issues/99#issuecomment-303"}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewBufferString(respJSON)),
				}, nil
			})
		}

		r := NewRegistry()
		registerPRCommentTool(r, "", metaFile)

		tc := llm.ToolCall{
			ID:        "tc_meta",
			Name:      "post_pr_comment",
			Arguments: `{"comment": "Comment using metadata file"}`,
		}
		res, err := r.HandleCall(context.Background(), tc)
		require.NoError(t, err)
		require.Contains(t, res, `"repo":"detected-owner/detected-repo"`)
		require.Contains(t, res, `"pr":99`)
	})

	t.Run("inline comment rejected with 422 diff line error", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "dummy-token")

		gitHubClientFactory = func(_ string) *github.Client {
			return newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
				if req.Method == http.MethodGet && req.URL.Path == "/repos/owner/repo/pulls/42" {
					prJSON := `{"head":{"sha":"abc123commitsha"}}`
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(bytes.NewBufferString(prJSON)),
					}, nil
				}
				if req.Method == http.MethodPost && req.URL.Path == "/repos/owner/repo/pulls/42/comments" {
					errJSON := `{"message":"pull_request_review_thread.line must be part of the diff"}`
					return &http.Response{
						StatusCode: http.StatusUnprocessableEntity,
						Header:     make(http.Header),
						Body:       io.NopCloser(bytes.NewBufferString(errJSON)),
					}, nil
				}
				return &http.Response{
					StatusCode: http.StatusNotFound,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
				}, nil
			})
		}

		r := NewRegistry()
		registerPRCommentTool(r, "", "")

		tc := llm.ToolCall{
			ID:   "tc_422",
			Name: "post_pr_comment",
			Arguments: `{
				"comment": "Line outside diff test",
				"repo": "owner/repo",
				"pr_number": 42,
				"path": "core/reviewer.go",
				"line": 9999
			}`,
		}
		_, err := r.HandleCall(context.Background(), tc)
		require.Error(t, err)
		require.Contains(t, err.Error(), "GitHub rejected inline comment")
	})
}

func TestRegisterLocalTools_PRComment(t *testing.T) {
	t.Run("when allowPRComment is true", func(t *testing.T) {
		r := NewRegistry()
		RegisterLocalTools(r, "", nil, "", false, true, nil)
		var hasCommentTool bool
		for _, td := range r.ToTools() {
			if td.Name == "post_pr_comment" {
				hasCommentTool = true
				break
			}
		}
		require.True(t, hasCommentTool)
	})

	t.Run("when allowPRComment is false", func(t *testing.T) {
		r := NewRegistry()
		RegisterLocalTools(r, "", nil, "", false, false, nil)
		var hasCommentTool bool
		for _, td := range r.ToTools() {
			if td.Name == "post_pr_comment" {
				hasCommentTool = true
				break
			}
		}
		require.False(t, hasCommentTool)
	})
}
