package main

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/go-github/v69/github"
	"github.com/migueleliasweb/go-github-mock/src/mock"
	"github.com/stretchr/testify/assert"
)

func failingHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}
}

func metadataPR() github.PullRequest {
	return github.PullRequest{
		Number:    github.Ptr(1),
		Title:     github.Ptr("PR Title"),
		User:      &github.User{Login: github.Ptr("author")},
		CreatedAt: &github.Timestamp{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
}

// TestGetMetadataComments pins down how conversation and inline review comments are mapped into the
// PR metadata the reviewer consumes: ordering, ownership detection by tag, and inline fields.
func TestGetMetadataComments(t *testing.T) {
	const tag = "tag-a"
	base := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *github.Timestamp { return &github.Timestamp{Time: base.Add(d)} }
	mainTag := wrapTag(tag, tagPrefixMain)
	inlineTag := wrapTag(tag, tagPrefixInline)
	reviewTag := wrapTag(tag, "")
	otherTag := wrapTag("other-tag", tagPrefixMain)
	bot := &github.User{Login: github.Ptr("cassandra")}
	human := &github.User{Login: github.Ptr("alice")}

	issueComments := []github.IssueComment{
		{ID: github.Ptr(int64(1)), User: human, Body: github.Ptr("human conversation"), CreatedAt: at(1 * time.Hour)},
		{ID: github.Ptr(int64(2)), User: bot, Body: github.Ptr("summary " + mainTag), CreatedAt: at(4 * time.Hour)},
		{ID: github.Ptr(int64(3)), User: bot, Body: github.Ptr("verdict " + reviewTag), CreatedAt: at(2 * time.Hour)},
		{ID: github.Ptr(int64(4)), User: bot, Body: github.Ptr("another run " + otherTag), CreatedAt: at(5 * time.Hour)},
		{ID: github.Ptr(int64(5)), User: bot, Body: github.Ptr("inline tag in a conversation comment " + inlineTag), CreatedAt: at(6 * time.Hour)},
	}
	reviewComments := []github.PullRequestComment{
		{
			ID: github.Ptr(int64(10)), User: bot, Body: github.Ptr("inline " + inlineTag), CreatedAt: at(3 * time.Hour),
			Path: github.Ptr("a.go"), Line: github.Ptr(10), StartLine: github.Ptr(5), Position: github.Ptr(1),
		},
		{
			ID: github.Ptr(int64(11)), User: human, Body: github.Ptr("stale"), CreatedAt: at(7 * time.Hour),
			Path: github.Ptr("b.go"), OriginalLine: github.Ptr(8), OriginalPosition: github.Ptr(3),
		},
		{
			ID: github.Ptr(int64(12)), User: bot, Body: github.Ptr("main tag in an inline comment " + mainTag), CreatedAt: at(30 * time.Minute),
			Path: github.Ptr("c.go"), Line: github.Ptr(2), Position: github.Ptr(2),
		},
	}

	newClient := func(opts ...mock.MockBackendOption) *github.Client {
		return github.NewClient(mock.NewMockedHTTPClient(append([]mock.MockBackendOption{
			mock.WithRequestMatch(mock.GetReposPullsByOwnerByRepoByPullNumber, metadataPR()),
		}, opts...)...))
	}

	t.Run("maps, orders and attributes comments", func(t *testing.T) {
		client := newClient(
			mock.WithRequestMatch(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber, issueComments),
			mock.WithRequestMatch(mock.GetReposPullsCommentsByOwnerByRepoByPullNumber, reviewComments),
		)
		metadata, err := getMetadata(context.Background(), client, "owner", "repo", 1, tag)
		assert.NoError(t, err)

		var ids []int64
		byID := map[int64]int{}
		for i, c := range metadata.Comments {
			ids = append(ids, c.ID)
			byID[c.ID] = i
		}
		assert.Equal(t, []int64{12, 1, 3, 10, 2, 4, 5, 11}, ids, "chronological across both kinds")

		self := func(id int64) bool { return metadata.Comments[byID[id]].IsSelf }
		assert.False(t, self(1), "human comment")
		assert.True(t, self(2), "conversation comment with the main tag")
		assert.True(t, self(3), "conversation comment with the review tag")
		assert.False(t, self(4), "conversation comment from another tag")
		assert.False(t, self(5), "inline tag only counts for inline comments")
		assert.True(t, self(10), "inline comment with the inline tag")
		assert.False(t, self(11), "human inline comment")
		assert.False(t, self(12), "main tag only counts for conversation comments")

		inline := metadata.Comments[byID[10]]
		assert.Equal(t, "cassandra", inline.Author)
		assert.Equal(t, "a.go", inline.Path)
		assert.Equal(t, 10, inline.Line)
		assert.Equal(t, 5, inline.StartLine)
		assert.False(t, inline.IsOutdated)
		assert.Equal(t, base.Add(3*time.Hour), inline.Date)

		stale := metadata.Comments[byID[11]]
		assert.True(t, stale.IsOutdated, "no current position but an original one")
		assert.Equal(t, 0, stale.Line, "the bot keeps the current line, not the original one")

		conversation := metadata.Comments[byID[1]]
		assert.Empty(t, conversation.Path)
		assert.False(t, conversation.IsOutdated)
	})

	t.Run("without a tag nothing is attributed to the bot", func(t *testing.T) {
		client := newClient(
			mock.WithRequestMatch(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber, issueComments),
			mock.WithRequestMatch(mock.GetReposPullsCommentsByOwnerByRepoByPullNumber, reviewComments),
		)
		metadata, err := getMetadata(context.Background(), client, "owner", "repo", 1, "")
		assert.NoError(t, err)
		assert.Len(t, metadata.Comments, 8)
		for _, c := range metadata.Comments {
			assert.False(t, c.IsSelf, c.ID)
		}
	})

	t.Run("reads every page of both lists", func(t *testing.T) {
		client := newClient(
			mock.WithRequestMatchPages(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber,
				[]github.IssueComment{issueComments[0]}, []github.IssueComment{issueComments[1]}, []github.IssueComment{issueComments[2]}),
			mock.WithRequestMatchPages(mock.GetReposPullsCommentsByOwnerByRepoByPullNumber,
				[]github.PullRequestComment{reviewComments[0]}, []github.PullRequestComment{reviewComments[1]}),
		)
		metadata, err := getMetadata(context.Background(), client, "owner", "repo", 1, tag)
		assert.NoError(t, err)
		assert.Len(t, metadata.Comments, 5)
	})

	t.Run("listing failures are fatal", func(t *testing.T) {
		client := newClient(
			mock.WithRequestMatchHandler(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber, failingHandler(http.StatusInternalServerError)),
			mock.WithRequestMatch(mock.GetReposPullsCommentsByOwnerByRepoByPullNumber, reviewComments),
		)
		_, err := getMetadata(context.Background(), client, "owner", "repo", 1, tag)
		assert.ErrorContains(t, err, "failed to list issue comments")

		client = newClient(
			mock.WithRequestMatch(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber, issueComments),
			mock.WithRequestMatchHandler(mock.GetReposPullsCommentsByOwnerByRepoByPullNumber, failingHandler(http.StatusInternalServerError)),
		)
		_, err = getMetadata(context.Background(), client, "owner", "repo", 1, tag)
		assert.ErrorContains(t, err, "failed to list review comments")

		client = newClient(
			mock.WithRequestMatch(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber, issueComments),
			mock.WithRequestMatchHandler(mock.GetReposPullsCommentsByOwnerByRepoByPullNumber, failingHandler(http.StatusNotFound)),
		)
		_, err = getMetadata(context.Background(), client, "owner", "repo", 1, tag)
		assert.ErrorContains(t, err, "failed to list review comments", "a pull request must have a review comment list")
	})

	t.Run("cancelled context", func(t *testing.T) {
		client := newClient(
			mock.WithRequestMatch(mock.GetReposIssuesCommentsByOwnerByRepoByIssueNumber, issueComments),
			mock.WithRequestMatch(mock.GetReposPullsCommentsByOwnerByRepoByPullNumber, reviewComments),
		)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := getMetadata(ctx, client, "owner", "repo", 1, tag)
		assert.Error(t, err)
	})
}
