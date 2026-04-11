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

	err = postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "<!-- tag -->")
	assert.NoError(t, err)
}

func TestPostComment_Pagination(t *testing.T) {
	tmpDir := t.TempDir()
	bodyFile := filepath.Join(tmpDir, "body.md")
	err := os.WriteFile(bodyFile, []byte("test body"), 0o644)
	assert.NoError(t, err)

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
					_, _ = w.Write(mock.MustMarshal(comments))
				} else {
					// Page 2: return matching
					comments := []github.IssueComment{{ID: github.Ptr(int64(2)), Body: github.Ptr("found <!-- tag -->")}}
					_, _ = w.Write(mock.MustMarshal(comments))
				}
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
	assert.Equal(t, 2, callCount)
}

func TestPostComment_FileNotFound(t *testing.T) {
	client := github.NewClient(nil)
	err := postComment(context.Background(), client, "owner", "repo", 1, "non-existent.md", "<!-- tag -->")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read body file")
}

func TestPostComment_Latest(t *testing.T) {
	tmpDir := t.TempDir()
	bodyFile := filepath.Join(tmpDir, "body.md")
	err := os.WriteFile(bodyFile, []byte("test body"), 0o644)
	assert.NoError(t, err)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber,
			[]github.IssueComment{
				{
					ID:   github.Ptr(int64(1)),
					Body: github.Ptr("old body <!-- tag -->"),
				},
				{
					ID:   github.Ptr(int64(2)),
					Body: github.Ptr("newer body <!-- tag -->"),
				},
			},
		),
		mock.WithRequestMatch(
			mock.PatchReposIssuesCommentsByOwnerByRepoByCommentId,
			github.IssueComment{ID: github.Ptr(int64(2))},
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err = postComment(context.Background(), client, "owner", "repo", 1, bodyFile, "<!-- tag -->")
	assert.NoError(t, err)
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
						User:      &github.User{Login: github.Ptr("cassandra")},
						Body:      github.Ptr("comment 2 <!-- tag-a -->"),
						CreatedAt: &github.Timestamp{Time: time.Now()},
						Path:      github.Ptr("file.go"),
						Line:      github.Ptr(10),
						StartLine: github.Ptr(5),
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
		assert.Equal(t, 5, metadata.Comments[1].StartLine)
	})

	t.Run("with tag-b", func(t *testing.T) {
		client := setupMock()
		metadata, err := getMetadata(context.Background(), client, "owner", "repo", 1, "<!-- tag-b -->")
		assert.NoError(t, err)
		assert.Equal(t, 2, len(metadata.Comments))
		assert.False(t, metadata.Comments[0].IsSelf)
		assert.False(t, metadata.Comments[1].IsSelf)
	})
}

func TestPostStructuredReview(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")
	metadataFile := filepath.Join(tmpDir, "metadata.json")

	sr := core.StructuredReview{
		Approval: core.Approval{
			Approved:  true,
			Rationale: "LGTM!",
			Action:    "APPROVE",
		},
		NonSpecificReview: "General feedback",
		FilesReview: []core.FileReview{
			{
				Path:   "file1.go",
				Lines:  "10",
				Review: "New comment",
			},
			{
				Path:   "file2.go",
				Lines:  "20-25",
				Review: "Duplicate comment",
			},
		},
	}
	srBytes, _ := json.Marshal(sr)
	_ = os.WriteFile(srFile, srBytes, 0o644)

	metadata := core.PRMetadata{
		Comments: []core.PRComment{
			{
				Author: "cassandra",
				IsSelf: true,
				Path:   "file2.go",
				Line:   25,
				Body:   "Duplicate comment <!-- tag -->",
				Date:   time.Now(),
			},
		},
	}
	metadataBytes, _ := json.Marshal(metadata)
	_ = os.WriteFile(metadataFile, metadataBytes, 0o644)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber,
			[]github.IssueComment{},
		),
		mock.WithRequestMatchHandler(
			mock.PostReposIssuesCommentsByOwnerByRepoByIssueNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var comment github.IssueComment
				_ = json.NewDecoder(r.Body).Decode(&comment)
				assert.Contains(t, *comment.Body, "General feedback")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(mock.MustMarshal(github.IssueComment{ID: github.Ptr(int64(101))}))
			}),
		),
		mock.WithRequestMatchHandler(
			mock.PostReposPullsReviewsByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req github.PullRequestReviewRequest
				_ = json.NewDecoder(r.Body).Decode(&req)

				assert.Equal(t, "APPROVE", *req.Event)
				assert.Contains(t, *req.Body, "LGTM!")
				assert.NotContains(t, *req.Body, "General feedback") // Should NOT be in review body
				assert.Contains(t, *req.Body, "<!-- tag -->")

				// Only one comment should be present (the non-duplicate)
				assert.Equal(t, 1, len(req.Comments))
				assert.Equal(t, "file1.go", *req.Comments[0].Path)
				assert.Contains(t, *req.Comments[0].Body, "New comment")
				assert.Contains(t, *req.Comments[0].Body, "<!-- tag -->")
				assert.Equal(t, 10, *req.Comments[0].Line)

				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "<!-- tag -->", metadataFile, true)
	assert.NoError(t, err)
}

func TestPostStructuredReview_NoMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")

	sr := core.StructuredReview{
		Approval: core.Approval{
			Approved:  false,
			Rationale: "Issues found",
			Action:    "REQUEST_CHANGES",
		},
		FilesReview: []core.FileReview{
			{
				Path:   "file1.go",
				Lines:  "5-10",
				Review: "Range comment",
			},
		},
	}
	srBytes, _ := json.Marshal(sr)
	_ = os.WriteFile(srFile, srBytes, 0o644)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatchHandler(
			mock.PostReposPullsReviewsByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req github.PullRequestReviewRequest
				_ = json.NewDecoder(r.Body).Decode(&req)

				assert.Equal(t, "REQUEST_CHANGES", *req.Event)
				assert.Equal(t, 1, len(req.Comments))
				assert.Equal(t, 5, *req.Comments[0].StartLine)
				assert.Equal(t, 10, *req.Comments[0].Line)

				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "<!-- tag -->", "", true)
	assert.NoError(t, err)
}

func TestPostStructuredReview_OverrideAction(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")

	sr := core.StructuredReview{
		Approval: core.Approval{
			Approved:  true,
			Rationale: "LGTM!",
			Action:    "APPROVE",
		},
		FilesReview: []core.FileReview{},
	}
	srBytes, _ := json.Marshal(sr)
	_ = os.WriteFile(srFile, srBytes, 0o644)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatchHandler(
			mock.PostReposPullsReviewsByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req github.PullRequestReviewRequest
				_ = json.NewDecoder(r.Body).Decode(&req)

				assert.Equal(t, "COMMENT", *req.Event) // Overridden from APPROVE to COMMENT

				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "<!-- tag -->", "", false) // allowReviewAction = false
	assert.NoError(t, err)
}

func TestDismissPreviousReviews(t *testing.T) {
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatch(
			mock.GetReposPullsReviewsByOwnerByRepoByPullNumber,
			[]github.PullRequestReview{
				{
					ID:    github.Ptr(int64(1)),
					Body:  github.Ptr("Old review <!-- tag -->"),
					State: github.Ptr("APPROVED"),
				},
				{
					ID:    github.Ptr(int64(2)),
					Body:  github.Ptr("Another review <!-- tag -->"),
					State: github.Ptr("DISMISSED"),
				},
				{
					ID:    github.Ptr(int64(3)),
					Body:  github.Ptr("A review from someone else"),
					State: github.Ptr("APPROVED"),
				},
			},
		),
		mock.WithRequestMatchHandler(
			mock.PutReposPullsReviewsDismissalsByOwnerByRepoByPullNumberByReviewId,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Verify it's the right review ID being dismissed
				// In the mock, it would be called for ID 1
				var req github.PullRequestReviewDismissalRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				assert.Equal(t, "Superseded by a new AI review.", *req.Message)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(1))}))
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := dismissPreviousReviews(context.Background(), client, "owner", "repo", 1, "<!-- tag -->")
	assert.NoError(t, err)
}

func TestPostStructuredReview_422Fallback(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")

	sr := core.StructuredReview{
		Approval: core.Approval{
			Approved:  true,
			Rationale: "Summary feedback",
			Action:    "APPROVE",
		},
		FilesReview: []core.FileReview{
			{
				Path:   "file.go",
				Lines:  "999", // Hallucinated line
				Review: "Something important",
			},
		},
	}
	srBytes, _ := json.Marshal(sr)
	_ = os.WriteFile(srFile, srBytes, 0o644)

	callCount := 0
	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatchHandler(
			mock.PostReposPullsReviewsByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount++
				if callCount == 1 {
					// First attempt: fail with 422
					w.WriteHeader(422)
					_, _ = w.Write([]byte(`{"message": "Unprocessable Entity"}`))
				} else {
					// Second attempt: verify fallback payload
					var req github.PullRequestReviewRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					assert.Equal(t, 0, len(req.Comments))
					assert.Contains(t, *req.Body, "Detailed Inline Feedback (Fallback)")
					assert.Contains(t, *req.Body, "file.go")
					assert.Contains(t, *req.Body, "Something important")
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
				}
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "<!-- tag -->", "", true)
	assert.NoError(t, err)
	assert.Equal(t, 2, callCount)
}

func TestPostStructuredReview_WhitespaceAndOrder(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")

	sr := core.StructuredReview{
		Approval: core.Approval{
			Approved:  true,
			Rationale: "LGTM!",
			Action:    "approve", // lowercase
		},
		FilesReview: []core.FileReview{
			{
				Path:   "file1.go",
				Lines:  " 10 - 20 ", // whitespace
				Review: "Range comment",
			},
			{
				Path:   "file2.go",
				Lines:  "50-40", // reverse order
				Review: "Reverse comment",
			},
		},
	}
	srBytes, _ := json.Marshal(sr)
	_ = os.WriteFile(srFile, srBytes, 0o644)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatchHandler(
			mock.PostReposPullsReviewsByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req github.PullRequestReviewRequest
				_ = json.NewDecoder(r.Body).Decode(&req)

				assert.Equal(t, "APPROVE", *req.Event) // normalized
				assert.Equal(t, 2, len(req.Comments))

				// Verify first comment
				assert.Equal(t, "file1.go", *req.Comments[0].Path)
				assert.Equal(t, 10, *req.Comments[0].StartLine)
				assert.Equal(t, 20, *req.Comments[0].Line)

				// Verify second comment (swapped)
				assert.Equal(t, "file2.go", *req.Comments[1].Path)
				assert.Equal(t, 40, *req.Comments[1].StartLine)
				assert.Equal(t, 50, *req.Comments[1].Line)

				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "<!-- tag -->", "", true)
	assert.NoError(t, err)
}

func TestPostStructuredReview_FileLevel(t *testing.T) {
	tmpDir := t.TempDir()
	srFile := filepath.Join(tmpDir, "review.json")

	sr := core.StructuredReview{
		Approval: core.Approval{
			Approved:  true,
			Rationale: "LGTM!",
			Action:    "APPROVE",
		},
		FilesReview: []core.FileReview{
			{
				Path:   "README.md",
				Lines:  "", // file-level
				Review: "Good documentation",
			},
		},
	}
	srBytes, _ := json.Marshal(sr)
	_ = os.WriteFile(srFile, srBytes, 0o644)

	mockedHTTPClient := mock.NewMockedHTTPClient(
		mock.WithRequestMatchHandler(
			mock.PostReposPullsReviewsByOwnerByRepoByPullNumber,
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req github.PullRequestReviewRequest
				_ = json.NewDecoder(r.Body).Decode(&req)

				assert.Equal(t, 0, len(req.Comments)) // appends to body instead
				assert.Contains(t, *req.Body, "README.md")
				assert.Contains(t, *req.Body, "Good documentation")

				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(mock.MustMarshal(github.PullRequestReview{ID: github.Ptr(int64(789))}))
			}),
		),
	)
	client := github.NewClient(mockedHTTPClient)

	err := postStructuredReview(context.Background(), client, "owner", "repo", 1, srFile, "<!-- tag -->", "", true)
	assert.NoError(t, err)
}
