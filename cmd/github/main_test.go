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

func TestWrapTag(t *testing.T) {
	tests := []struct {
		slug   string
		prefix string
		want   string
	}{
		{"tag", "prefix-", "<!-- prefix-tag -->"},
		{"tag with spaces", "", "<!-- tag with spaces -->"},
		{"tag--with--hyphens", "p-", "<!-- p-tag__with__hyphens -->"},
		{"tag <breakout>", "p-", "<!-- p-tag breakout -->"},
		{"  trimmed  ", "p-", "<!-- p-trimmed -->"},
	}

	for _, tt := range tests {
		got := wrapTag(tt.slug, tt.prefix)
		assert.Equal(t, tt.want, got)
	}
}

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

	err = postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "tag")
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

	err = postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "tag")
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

	err = postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "tag")
	assert.NoError(t, err)
	assert.Equal(t, 1, deleteCalled)
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
		metadata, err := getMetadata(context.Background(), client, "owner", "repo", 1, "tag-a")
		assert.NoError(t, err)
		assert.Equal(t, 2, len(metadata.Comments))
		assert.False(t, metadata.Comments[0].IsSelf)
		assert.True(t, metadata.Comments[1].IsSelf)
	})
}

func TestPostStructuredReview(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")

	sr := core.StructuredReview{
		Approval:    core.Approval{Approved: true, Rationale: "LGTM!", Action: "APPROVE"},
		FilesReview: []core.FileReview{{Path: "file1.go", Lines: "10", Review: "Comment"}},
	}
	srBytes, _ := json.Marshal(sr)
	_ = os.WriteFile(srFile, srBytes, 0o644)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetUser, github.User{Login: github.Ptr("me")}),
		mock.WithRequestMatch(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber, []github.IssueComment{}),
		mock.WithRequestMatch(mock.GetReposPullsReviewsByOwnerByRepoByPullNumber, []github.PullRequestReview{}),
		mock.WithRequestMatchHandler(
			mock.PostReposPullsReviewsByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req github.PullRequestReviewRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				assert.Contains(t, *req.Body, "<!-- tag -->")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "tag", "", true, true)
	assert.NoError(t, err)
}

func TestPostStructuredReview_DeleteOld(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")
	metadataFile := filepath.Join(tmpDir, "metadata.json")

	sr := core.StructuredReview{
		Approval:    core.Approval{Approved: true, Rationale: "LGTM!", Action: "APPROVE"},
		FilesReview: []core.FileReview{{Path: "file1.go", Lines: "10", Review: "Comment"}},
	}
	srBytes, _ := json.Marshal(sr)
	_ = os.WriteFile(srFile, srBytes, 0o644)

	metadata := core.PRMetadata{
		Comments: []core.PRComment{
			{ID: 100, Author: "me", IsSelf: true, Body: "Old <!-- cassandra-inline-tag -->"},
		},
	}
	metadataBytes, _ := json.Marshal(metadata)
	_ = os.WriteFile(metadataFile, metadataBytes, 0o644)

	deleteCalled := false
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetUser, github.User{Login: github.Ptr("me")}),
		mock.WithRequestMatch(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber, []github.IssueComment{}),
		mock.WithRequestMatch(mock.GetReposPullsReviewsByOwnerByRepoByPullNumber, []github.PullRequestReview{}),
		mock.WithRequestMatchHandler(
			mock.DeleteReposPullsCommentsByOwnerByRepoByCommentId,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				deleteCalled = true
				w.WriteHeader(http.StatusNoContent)
			}),
		),
		mock.WithRequestMatchHandler(
			mock.PostReposPullsReviewsByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "tag", metadataFile, true, true)
	assert.NoError(t, err)
	assert.True(t, deleteCalled)
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

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "tag", "", true, true)
	assert.NoError(t, err)
}

func TestDismissPreviousReviews(t *testing.T) {
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(mock.GetUser, github.User{Login: github.Ptr("me")}),
		mock.WithRequestMatch(
			mock.GetReposPullsReviewsByOwnerByRepoByPullNumber,
			[]github.PullRequestReview{
				{ID: github.Ptr(int64(1)), Body: github.Ptr("old <!-- tag -->"), State: github.Ptr("APPROVED")},
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

func TestPostStructuredReview_PermissionFallback(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")

	sr := core.StructuredReview{
		Approval:    core.Approval{Approved: true, Rationale: "Summary", Action: "APPROVE"},
		FilesReview: []core.FileReview{},
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
					_, _ = w.Write([]byte(`{"message": "GitHub Actions is not permitted to approve pull requests."}`))
				} else {
					var req github.PullRequestReviewRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					assert.Equal(t, "COMMENT", *req.Event)
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
				}
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "tag", "", true, true)
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestPostComment_UserGetError(t *testing.T) {
	tmpDir := t.TempDir()
	bodyFile := filepath.Join(tmpDir, "body.md")
	err := os.WriteFile(bodyFile, []byte("test body"), 0o644)
	assert.NoError(t, err)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatchHandler(
			mock.GetUser,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"message": "Resource not accessible by integration"}`))
			}),
		),
		mock.WithRequestMatch(
			mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber,
			[]github.IssueComment{
				{
					ID:   github.Ptr(int64(456)),
					Body: github.Ptr("old body <!-- cassandra-main-tag -->"),
					User: &github.User{Login: github.Ptr("any-user")},
				},
			},
		),
		mock.WithRequestMatch(
			mock.PatchReposIssuesCommentsByOwnerByRepoByCommentId,
			github.IssueComment{ID: github.Ptr(int64(456))},
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err = postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "tag")
	assert.NoError(t, err)
}
