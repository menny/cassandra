package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/core"
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
	err := os.WriteFile(bodyFile, []byte("test body"), 0o644)
	assert.NoError(t, err)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.GetUser,
			github.User{Login: github.Ptr("me")},
		),
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

	err = postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "<!-- tag -->")
	assert.NoError(t, err)
}

func TestPostComment_Update(t *testing.T) {
	tmpDir := t.TempDir()
	bodyFile := filepath.Join(tmpDir, "body.md")
	err := os.WriteFile(bodyFile, []byte("test body"), 0o644)
	assert.NoError(t, err)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.GetUser,
			github.User{Login: github.Ptr("me")},
		),
		mock.WithRequestMatch(
			mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber,
			[]github.IssueComment{
				{
					ID:   github.Ptr(int64(456)),
					Body: github.Ptr("old body <!-- cassandra-main-tag -->"),
					User: &github.User{Login: github.Ptr("me")},
				},
			},
		),
		mock.WithRequestMatch(
			mock.PatchReposIssuesCommentsByOwnerByRepoByCommentId,
			github.IssueComment{ID: github.Ptr(int64(456))},
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err = postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "<!-- tag -->")
	assert.NoError(t, err)
}

func TestPostComment_CleanupRedundant(t *testing.T) {
	tmpDir := t.TempDir()
	bodyFile := filepath.Join(tmpDir, "body.md")
	err := os.WriteFile(bodyFile, []byte("test body"), 0o644)
	assert.NoError(t, err)

	deleteCalled := 0
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.GetUser,
			github.User{Login: github.Ptr("me")},
		),
		mock.WithRequestMatch(
			mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber,
			[]github.IssueComment{
				{ID: github.Ptr(int64(1)), Body: github.Ptr("tag <!-- cassandra-main-tag -->"), User: &github.User{Login: github.Ptr("me")}},
				{ID: github.Ptr(int64(2)), Body: github.Ptr("tag <!-- cassandra-main-tag -->"), User: &github.User{Login: github.Ptr("me")}},
			},
		),
		mock.WithRequestMatchHandler(
			mock.DeleteReposIssuesCommentsByOwnerByRepoByCommentId,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				deleteCalled++
				w.WriteHeader(http.StatusNoContent)
			}),
		),
		mock.WithRequestMatch(
			mock.PatchReposIssuesCommentsByOwnerByRepoByCommentId,
			github.IssueComment{ID: github.Ptr(int64(2))},
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err = postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "<!-- tag -->")
	assert.NoError(t, err)
	assert.Equal(t, 1, deleteCalled) // Should delete ID 1, keep ID 2 and update it
}

func TestGetMetadata(t *testing.T) {
	setupMock := func() *github.Client {
		mockedHTTPClient := mock.NewMockedHTTPClient(
			mock.WithRequestMatch(
				mock.GetReposPullsByOwnerByRepoByPullNumber,
				github.PullRequest{
					Number:    github.Ptr(1),
					Title:     github.Ptr("PR Title"),
					Body:      github.Ptr("PR Description"),
					User:      &github.User{Login: github.Ptr("author")},
					CreatedAt: &github.Timestamp{Time: time.Now()},
				},
			),
			mock.WithRequestMatch(
				mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber,
				[]github.IssueComment{
					{
						User:      &github.User{Login: github.Ptr("user1")},
						Body:      github.Ptr("comment 1"),
						CreatedAt: &github.Timestamp{Time: time.Now().Add(-time.Hour)},
					},
				},
			),
			mock.WithRequestMatch(
				mock.GetReposPullsCommentsByOwnerByRepoByPullNumber,
				[]github.PullRequestComment{
					{
						ID:        github.Ptr(int64(200)),
						User:      &github.User{Login: github.Ptr("cassandra")},
						Body:      github.Ptr("comment 2 <!-- cassandra-inline-tag-a -->"),
						CreatedAt: &github.Timestamp{Time: time.Now()},
						Path:      github.Ptr("file.go"),
						Line:      github.Ptr(10),
						StartLine: github.Ptr(5),
						Position:  github.Ptr(1),
					},
				},
			),
		)
		return github.NewClient(mockedHTTPClient)
	}

	t.Run("with tag-a", func(t *testing.T) {
		client := setupMock()
		metadata, err := getMetadata(context.Background(), client, "owner", "repo", 1, "<!-- tag-a -->")
		assert.NoError(t, err)
		assert.Equal(t, 2, len(metadata.Comments))
		assert.False(t, metadata.Comments[0].IsSelf)
		assert.True(t, metadata.Comments[1].IsSelf)
		assert.Equal(t, int64(200), metadata.Comments[1].ID)
		assert.False(t, metadata.Comments[1].IsOutdated)
	})
}

func TestPostStructuredReview_SyncInline(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")
	metadataFile := filepath.Join(tmpDir, "metadata.json")

	sr := core.StructuredReview{
		Approval: core.Approval{Approved: true, Rationale: "LGTM!", Action: "APPROVE"},
		FilesReview: []core.FileReview{
			{Path: "file1.go", Lines: "10", Review: "Updated comment"},
			{Path: "file2.go", Lines: "20", Review: "New comment"},
		},
	}
	srBytes, _ := json.Marshal(sr)
	_ = os.WriteFile(srFile, srBytes, 0o644)

	metadata := core.PRMetadata{
		Comments: []core.PRComment{
			{
				ID: 100, Author: "me", IsSelf: true, Path: "file1.go", Line: 10,
				Body: "Old comment <!-- cassandra-inline-tag -->",
			},
		},
	}
	metadataBytes, _ := json.Marshal(metadata)
	_ = os.WriteFile(metadataFile, metadataBytes, 0o644)

	editCalled := false
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetUser, github.User{Login: github.Ptr("me")}),
		mock.WithRequestMatch(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber, []github.IssueComment{}),
		mock.WithRequestMatch(mock.GetReposPullsReviewsByOwnerByRepoByPullNumber, []github.PullRequestReview{}),
		mock.WithRequestMatchHandler(
			mock.PatchReposPullsCommentsByOwnerByRepoByCommentId,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				editCalled = true
				var comment github.PullRequestComment
				_ = json.NewDecoder(r.Body).Decode(&comment)
				assert.Contains(t, *comment.Body, "Updated comment")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestComment{ID: github.Ptr(int64(100))}))
			}),
		),
		mock.WithRequestMatchHandler(
			mock.PostReposPullsReviewsByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req github.PullRequestReviewRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				// Should only have file2.go since file1.go was edited
				assert.Equal(t, 1, len(req.Comments))
				assert.Equal(t, "file2.go", *req.Comments[0].Path)
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "<!-- tag -->", metadataFile, true)
	assert.NoError(t, err)
	assert.True(t, editCalled)
}

func TestPostStructuredReview_422Fallback(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")

	sr := core.StructuredReview{
		Approval:    core.Approval{Approved: true, Rationale: "Summary", Action: "APPROVE"},
		FilesReview: []core.FileReview{{Path: "file.go", Lines: "999", Review: "Hallucinated"}},
	}
	srBytes, _ := json.Marshal(sr)
	_ = os.WriteFile(srFile, srBytes, 0o644)

	callCount := 0
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetUser, github.User{Login: github.Ptr("me")}),
		mock.WithRequestMatch(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber, []github.IssueComment{}),
		mock.WithRequestMatch(mock.GetReposPullsReviewsByOwnerByRepoByPullNumber, []github.PullRequestReview{}),
		mock.WithRequestMatchHandler(
			mock.PostReposPullsReviewsByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount++
				if callCount == 1 {
					w.WriteHeader(422)
					_, _ = w.Write([]byte(`{"message": "Unprocessable Entity"}`))
				} else {
					var req github.PullRequestReviewRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					assert.Equal(t, 0, len(req.Comments))
					assert.Contains(t, *req.Body, "Detailed Inline Feedback (Fallback)")
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
				}
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "<!-- tag -->", "", true)
	assert.NoError(t, err)
}

func TestDismissPreviousReviews(t *testing.T) {
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetUser, github.User{Login: github.Ptr("me")}),
		mock.WithRequestMatch(
			mock.GetReposPullsReviewsByOwnerByRepoByPullNumber,
			[]github.PullRequestReview{
				{ID: github.Ptr(int64(1)), Body: github.Ptr("old <!-- tag -->"), State: github.Ptr("APPROVED")},
				{ID: github.Ptr(int64(789)), Body: github.Ptr("current <!-- tag -->"), State: github.Ptr("PENDING")},
			},
		),
		mock.WithRequestMatchHandler(
			mock.PutReposPullsReviewsDismissalsByOwnerByRepoByPullNumberByReviewId,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(1))}))
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := dismissPreviousReviews(context.Background(), client, "owner", "repo", 1, "<!-- tag -->", 789)
	assert.NoError(t, err)
}
