package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

func TestFetchPRStack(t *testing.T) {
	ctx := context.Background()

	t.Run("nil client or invalid args", func(t *testing.T) {
		_, err := FetchPRStack(ctx, nil, "owner", "repo", 1)
		require.Error(t, err)

		client := newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
			return nil, nil
		})
		_, err = FetchPRStack(ctx, client, "", "repo", 1)
		require.Error(t, err)

		_, err = FetchPRStack(ctx, client, "owner", "repo", 0)
		require.Error(t, err)
	})

	t.Run("PR not in a stack returns empty list", func(t *testing.T) {
		client := newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "/stacks") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewBufferString("[]")),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
		})

		stack, err := FetchPRStack(ctx, client, "owner", "repo", 101)
		require.NoError(t, err)
		require.Nil(t, stack)
	})

	t.Run("PR in a single PR stack returns nil", func(t *testing.T) {
		client := newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/repos/owner/repo/stacks" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`[
						{"id": 1, "number": 42, "pull_requests": [{"number": 101}]}
					]`)),
				}, nil
			}
			if req.URL.Path == "/repos/owner/repo/stacks/42" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"id": 1,
						"number": 42,
						"pull_requests": [
							{"number": 101, "title": "PR 101", "head": {"ref": "feat-1", "sha": "111"}, "base": {"ref": "main"}}
						]
					}`)),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
		})

		stack, err := FetchPRStack(ctx, client, "owner", "repo", 101)
		require.NoError(t, err)
		require.Nil(t, stack)
	})

	t.Run("PR in a multi-PR stack returns all PRs ordered", func(t *testing.T) {
		client := newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/repos/owner/repo/stacks" && req.URL.Query().Get("pull_request") == "102" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`[
						{"id": 1, "number": 42, "pull_requests": [{"number": 101}, {"number": 102}]}
					]`)),
				}, nil
			}
			if req.URL.Path == "/repos/owner/repo/stacks/42" {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: io.NopCloser(bytes.NewBufferString(`{
						"id": 1,
						"number": 42,
						"pull_requests": [
							{"number": 101, "title": "Base Model", "head": {"ref": "model", "sha": "sha1"}, "base": {"ref": "main"}},
							{"number": 102, "title": "API Layer", "head": {"ref": "api", "sha": "sha2"}, "base": {"ref": "model"}}
						]
					}`)),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
		})

		stack, err := FetchPRStack(ctx, client, "owner", "repo", 102)
		require.NoError(t, err)
		require.Len(t, stack, 2)
		require.Equal(t, 101, stack[0].PRNumber)
		require.Equal(t, "Base Model", stack[0].Title)
		require.Equal(t, "model", stack[0].Branch)
		require.Equal(t, "main", stack[0].Base)
		require.Equal(t, "sha1", stack[0].Head)

		require.Equal(t, 102, stack[1].PRNumber)
		require.Equal(t, "API Layer", stack[1].Title)
		require.Equal(t, "api", stack[1].Branch)
		require.Equal(t, "model", stack[1].Base)
		require.Equal(t, "sha2", stack[1].Head)
	})

	t.Run("404 on stacks endpoint is treated as not in stack", func(t *testing.T) {
		client := newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(`{"message": "Not Found"}`)),
			}, nil
		})

		stack, err := FetchPRStack(ctx, client, "owner", "repo", 101)
		require.NoError(t, err)
		require.Nil(t, stack)
	})
}

func TestFetchPRFiles(t *testing.T) {
	ctx := context.Background()

	client := newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/files") {
			files := []*github.CommitFile{
				{Filename: github.Ptr("main.go")},
				{Filename: github.Ptr("go.sum")},
				{Filename: github.Ptr("internal/util.go")},
				{Filename: github.Ptr("package-lock.json")},
			}
			data, _ := json.Marshal(files)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewReader(data)),
			}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
	})

	ignored := []string{"go.sum", "package-lock.json"}
	files, err := FetchPRFiles(ctx, client, "owner", "repo", 1, ignored)
	require.NoError(t, err)
	require.Equal(t, []string{"main.go", "internal/util.go"}, files)
}

func TestFetchPRDiff(t *testing.T) {
	ctx := context.Background()

	sampleDiff := `diff --git a/file1.go b/file1.go
index 111..222 100644
--- a/file1.go
+++ b/file1.go
@@ -1 +1 @@
-old
+new
diff --git a/go.sum b/go.sum
index 333..444 100644
--- a/go.sum
+++ b/go.sum
@@ -1 +1 @@
-lock old
+lock new
diff --git a/file2.go b/file2.go
index 555..666 100644
--- a/file2.go
+++ b/file2.go
@@ -1 +1 @@
-foo
+bar`

	t.Run("diff with lockfile exclusion and no paths filter", func(t *testing.T) {
		client := newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(sampleDiff)),
			}, nil
		})

		diff, err := FetchPRDiff(ctx, client, "owner", "repo", 1, nil, []string{"go.sum"})
		require.NoError(t, err)
		require.Contains(t, diff, "file1.go")
		require.Contains(t, diff, "file2.go")
		require.NotContains(t, diff, "go.sum")
	})

	t.Run("diff with paths filter", func(t *testing.T) {
		client := newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(sampleDiff)),
			}, nil
		})

		diff, err := FetchPRDiff(ctx, client, "owner", "repo", 1, []string{"file2.go"}, []string{"go.sum"})
		require.NoError(t, err)
		require.NotContains(t, diff, "file1.go")
		require.Contains(t, diff, "file2.go")
		require.NotContains(t, diff, "go.sum")
	})

	t.Run("diff 406 fallback with paths filter uses ListFiles patches", func(t *testing.T) {
		client := newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.Header.Get("Accept"), "diff") {
				return &http.Response{
					StatusCode: http.StatusNotAcceptable,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewBufferString(`{"message": "diff too large"}`)),
				}, nil
			}
			if strings.Contains(req.URL.Path, "/files") {
				files := []*github.CommitFile{
					{
						Filename: github.Ptr("path/to/target.go"),
						Patch:    github.Ptr("@@ -1,2 +1,2 @@\n-a\n+b"),
					},
					{
						Filename: github.Ptr("other.go"),
						Patch:    github.Ptr("@@ -1 +1 @@\n-x\n+y"),
					},
				}
				data, _ := json.Marshal(files)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewReader(data)),
				}, nil
			}
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
		})

		diff, err := FetchPRDiff(ctx, client, "owner", "repo", 1, []string{"path/to/target.go"}, nil)
		require.NoError(t, err)
		require.Contains(t, diff, "path/to/target.go")
		require.NotContains(t, diff, "other.go")
	})
}

func TestGetPRMetadataTool(t *testing.T) {
	origFactory := gitHubClientFactory
	defer func() { gitHubClientFactory = origFactory }()

	t.Run("missing GITHUB_TOKEN and GH_TOKEN", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")

		r := NewRegistry()
		registerGetPRMetadataTool(r, "", "", nil)

		tc := llm.ToolCall{
			ID:        "tc_meta_1",
			Name:      "get_pr_metadata",
			Arguments: `{"pr_number": 42, "repo": "owner/repo"}`,
		}
		_, err := r.HandleCall(context.Background(), tc)
		require.Error(t, err)
		require.Contains(t, err.Error(), "GITHUB_TOKEN (or GH_TOKEN) environment variable is required")
	})

	t.Run("missing pr_number", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "dummy")

		r := NewRegistry()
		registerGetPRMetadataTool(r, "", "", nil)

		tc := llm.ToolCall{
			ID:        "tc_meta_2",
			Name:      "get_pr_metadata",
			Arguments: `{"repo": "owner/repo"}`,
		}
		_, err := r.HandleCall(context.Background(), tc)
		require.Error(t, err)
		require.Contains(t, err.Error(), "pr_number is required")
	})

	t.Run("successful metadata retrieval", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "dummy")
		gitHubClientFactory = func(token string) *github.Client {
			return newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/repos/owner/repo/pulls/42" {
					pr := &github.PullRequest{
						Number: github.Ptr(42),
						Title:  github.Ptr("Refactor core components"),
						Body:   github.Ptr("Description of changes"),
						User:   &github.User{Login: github.Ptr("alice")},
						Head:   &github.PullRequestBranch{Ref: github.Ptr("feat-branch"), SHA: github.Ptr("headsha")},
						Base:   &github.PullRequestBranch{Ref: github.Ptr("main")},
					}
					data, _ := json.Marshal(pr)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(bytes.NewReader(data)),
					}, nil
				}
				if req.URL.Path == "/repos/owner/repo/pulls/42/files" {
					files := []*github.CommitFile{
						{Filename: github.Ptr("core/agent.go")},
						{Filename: github.Ptr("core/reviewer.go")},
					}
					data, _ := json.Marshal(files)
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     make(http.Header),
						Body:       io.NopCloser(bytes.NewReader(data)),
					}, nil
				}
				return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewBufferString("{}"))}, nil
			})
		}

		r := NewRegistry()
		registerGetPRMetadataTool(r, "", "", nil)

		tc := llm.ToolCall{
			ID:        "tc_meta_3",
			Name:      "get_pr_metadata",
			Arguments: `{"pr_number": 42, "repo": "owner/repo"}`,
		}
		res, err := r.HandleCall(context.Background(), tc)
		require.NoError(t, err)
		require.Contains(t, res, "### PR #42: Refactor core components")
		require.Contains(t, res, "- **Author**: alice")
		require.Contains(t, res, "- **Branch**: feat-branch")
		require.Contains(t, res, "- **Base**: main")
		require.Contains(t, res, "- **Head**: headsha")
		require.Contains(t, res, "- core/agent.go")
		require.Contains(t, res, "- core/reviewer.go")
	})
}

func TestGetPRDiffTool(t *testing.T) {
	origFactory := gitHubClientFactory
	defer func() { gitHubClientFactory = origFactory }()

	t.Run("missing paths or pr_number", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "dummy")
		r := NewRegistry()
		registerGetPRDiffTool(r, "", "", nil)

		tc1 := llm.ToolCall{
			ID:        "tc_diff_1",
			Name:      "get_pr_diff",
			Arguments: `{"pr_number": 0, "paths": ["a.go"]}`,
		}
		_, err := r.HandleCall(context.Background(), tc1)
		require.Error(t, err)
		require.Contains(t, err.Error(), "pr_number is required")

		tc2 := llm.ToolCall{
			ID:        "tc_diff_2",
			Name:      "get_pr_diff",
			Arguments: `{"pr_number": 1, "paths": []}`,
		}
		_, err = r.HandleCall(context.Background(), tc2)
		require.Error(t, err)
		require.Contains(t, err.Error(), "paths is required")
	})

	t.Run("successful diff retrieval", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "dummy")
		gitHubClientFactory = func(token string) *github.Client {
			return newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
				diff := `diff --git a/target.go b/target.go
--- a/target.go
+++ b/target.go
@@ -1 +1 @@
-old
+new`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewBufferString(diff)),
				}, nil
			})
		}

		r := NewRegistry()
		registerGetPRDiffTool(r, "", "", nil)

		tc := llm.ToolCall{
			ID:        "tc_diff_3",
			Name:      "get_pr_diff",
			Arguments: `{"pr_number": 10, "paths": ["target.go"], "repo": "owner/repo"}`,
		}
		res, err := r.HandleCall(context.Background(), tc)
		require.NoError(t, err)
		require.Contains(t, res, "diff --git a/target.go b/target.go")
	})

	t.Run("no diff found for specified paths", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "dummy")
		gitHubClientFactory = func(token string) *github.Client {
			return newMockGitHubClient(func(req *http.Request) (*http.Response, error) {
				diff := `diff --git a/other.go b/other.go
--- a/other.go
+++ b/other.go
@@ -1 +1 @@
-old
+new`
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(bytes.NewBufferString(diff)),
				}, nil
			})
		}

		r := NewRegistry()
		registerGetPRDiffTool(r, "", "", nil)

		tc := llm.ToolCall{
			ID:        "tc_diff_4",
			Name:      "get_pr_diff",
			Arguments: `{"pr_number": 10, "paths": ["nonexistent.go"], "repo": "owner/repo"}`,
		}
		res, err := r.HandleCall(context.Background(), tc)
		require.NoError(t, err)
		require.Contains(t, res, "No diff found for the specified paths in PR #10.")
	})
}
