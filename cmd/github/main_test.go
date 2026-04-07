package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-github/v69/github"
	"github.com/migueleliasweb/go-github-mock/src/mock"
	"github.com/stretchr/testify/assert"
)

func TestParseRepo(t *testing.T) {
	tests := []struct {
		input     string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"owner/repo", "owner", "repo", false},
		{"", "", "", true},
		{"owner", "", "", true},
		{"owner/repo/extra", "", "", true},
	}

	for _, tt := range tests {
		owner, repo, err := parseRepo(tt.input)
		if tt.wantErr {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
			assert.Equal(t, tt.wantOwner, owner)
			assert.Equal(t, tt.wantRepo, repo)
		}
	}
}

func TestAddReaction(t *testing.T) {
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.PostReposIssuesReactionsByOwnerByRepoByIssueNumber,
			github.Reaction{ID: github.Ptr(int64(123))},
		),
	)
	client := github.NewClient(mockedHTTPClient)

	id, err := addReaction(context.Background(), client, "owner", "repo", 1, "eyes")
	assert.NoError(t, err)
	assert.Equal(t, int64(123), id)
}

func TestRemoveReaction(t *testing.T) {
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.DeleteReposIssuesReactionsByOwnerByRepoByIssueNumberByReactionId,
			nil,
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := removeReaction(context.Background(), client, "owner", "repo", 1, 123)
	assert.NoError(t, err)
}

func TestPostComment_Create(t *testing.T) {
	tmpDir := t.TempDir()
	bodyFile := filepath.Join(tmpDir, "body.md")
	os.WriteFile(bodyFile, []byte("test body"), 0o644)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber,
			[]github.IssueComment{}, // No existing comments
		),
		mock.WithRequestMatch(
			mock.PostReposIssuesCommentsByOwnerByRepoByIssueNumber,
			github.IssueComment{ID: github.Ptr(int64(456))},
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "<!-- tag -->")
	assert.NoError(t, err)
}

func TestPostComment_Update(t *testing.T) {
	tmpDir := t.TempDir()
	bodyFile := filepath.Join(tmpDir, "body.md")
	os.WriteFile(bodyFile, []byte("new body"), 0o644)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber,
			[]github.IssueComment{
				{
					ID:   github.Ptr(int64(456)),
					Body: github.Ptr("old body <!-- tag -->"),
				},
			},
		),
		mock.WithRequestMatch(
			mock.PatchReposIssuesCommentsByOwnerByRepoByCommentId,
			github.IssueComment{ID: github.Ptr(int64(456))},
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "<!-- tag -->")
	assert.NoError(t, err)
}

func TestPostComment_Pagination(t *testing.T) {
	tmpDir := t.TempDir()
	bodyFile := filepath.Join(tmpDir, "body.md")
	os.WriteFile(bodyFile, []byte("new body"), 0o644)

	// Custom handler to simulate pagination
	callCount := 0
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatchHandler(
			mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount++
				if callCount == 1 {
					// Page 1: return non-matching, with Link header to page 2
					w.Header().Set("Link", `<https://api.github.com/repositories/1/issues/1/comments?page=2>; rel="next"`)
					comments := []github.IssueComment{{ID: github.Ptr(int64(1)), Body: github.Ptr("no tag")}}
					w.Write(mock.MustMarshal(comments))
				} else {
					// Page 2: return matching
					comments := []github.IssueComment{{ID: github.Ptr(int64(2)), Body: github.Ptr("found <!-- tag -->")}}
					w.Write(mock.MustMarshal(comments))
				}
			}),
		),
		mock.WithRequestMatch(
			mock.PatchReposIssuesCommentsByOwnerByRepoByCommentId,
			github.IssueComment{ID: github.Ptr(int64(2))},
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "<!-- tag -->")
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestPostComment_FileNotFound(t *testing.T) {
	client := github.NewClient(nil)
	err := postComment(context.Background(), client, "owner", "repo", 1, "non-existent.md", "<!-- tag -->")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read body file")
}
