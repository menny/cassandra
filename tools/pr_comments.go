package tools

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/go-github/v69/github"
)

// Kinds of comments on a pull request or issue.
const (
	// PRCommentKindIssue is a conversation comment (the issue comments API).
	PRCommentKindIssue = "issue"
	// PRCommentKindReview is an inline code comment on the diff.
	PRCommentKindReview = "review"
	// PRCommentKindReviewSummary is the top-level body of a submitted review.
	PRCommentKindReviewSummary = "review_summary"
)

// PRCommentRecord is the neutral, API-shaped form of a comment on a pull request or issue. It is
// shared by the comment tools and by the cmd/github utility, which maps it to its own types.
type PRCommentRecord struct {
	Kind    string
	ID      int64
	Number  int // pull request or issue number; 0 when the API response does not say
	Author  string
	Body    string
	Created time.Time
	Updated time.Time
	URL     string
	State   string // review summaries only, e.g. APPROVED

	// Inline review comments only.
	Path         string
	Line         int
	StartLine    int
	OriginalLine int
	Outdated     bool
}

// DisplayLine returns the line to show for an inline comment: the current line, or the line the
// comment was originally made on when the code has since changed.
func (c PRCommentRecord) DisplayLine() int {
	if c.Line > 0 {
		return c.Line
	}
	return c.OriginalLine
}

func timeOf(ts *github.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.Time
}

func issueCommentRecord(c *github.IssueComment) PRCommentRecord {
	return PRCommentRecord{
		Kind: PRCommentKindIssue, ID: c.GetID(), Number: numberFromAPIURL(c.GetIssueURL()),
		Author: c.GetUser().GetLogin(), Body: c.GetBody(),
		Created: timeOf(c.CreatedAt), Updated: timeOf(c.UpdatedAt), URL: c.GetHTMLURL(),
	}
}

func reviewCommentRecord(c *github.PullRequestComment) PRCommentRecord {
	return PRCommentRecord{
		Kind: PRCommentKindReview, ID: c.GetID(), Number: numberFromAPIURL(c.GetPullRequestURL()),
		Author: c.GetUser().GetLogin(), Body: c.GetBody(),
		Created: timeOf(c.CreatedAt), Updated: timeOf(c.UpdatedAt), URL: c.GetHTMLURL(),
		Path: c.GetPath(), Line: c.GetLine(), StartLine: c.GetStartLine(), OriginalLine: c.GetOriginalLine(),
		Outdated: c.Position == nil && c.OriginalPosition != nil,
	}
}

func reviewSummaryRecord(rv *github.PullRequestReview, number int) PRCommentRecord {
	return PRCommentRecord{
		Kind: PRCommentKindReviewSummary, ID: rv.GetID(), Number: number,
		Author: rv.GetUser().GetLogin(), Body: rv.GetBody(),
		Created: timeOf(rv.SubmittedAt), URL: rv.GetHTMLURL(), State: rv.GetState(),
	}
}

// FetchPRCommentsOptions tunes FetchPRComments. The zero value fetches every page and fails on any
// API error.
type FetchPRCommentsOptions struct {
	// MaxPagesPerKind limits how many pages (of 100 comments) are read for each of the conversation
	// and inline review comment lists. Zero means no limit.
	MaxPagesPerKind int
	// IgnoreMissingReviewComments treats a 404 from the review comments endpoint as "none". It lets
	// the same call work for plain issues, which have no inline comments.
	IgnoreMissingReviewComments bool
}

// FetchPRComments lists the conversation comments and the inline review comments of a pull request
// (or, with IgnoreMissingReviewComments, an issue), oldest first. truncated reports that
// MaxPagesPerKind stopped the listing before all comments were read.
func FetchPRComments(ctx context.Context, client *github.Client, owner, repo string, number int, opts FetchPRCommentsOptions) (comments []PRCommentRecord, truncated bool, err error) {
	if client == nil {
		return nil, false, errors.New("github client is nil")
	}
	if owner == "" || repo == "" || number <= 0 {
		return nil, false, errors.New("owner, repo, and valid number are required")
	}
	more := func(page int) bool { return opts.MaxPagesPerKind == 0 || page < opts.MaxPagesPerKind }

	issueOpts := &github.IssueListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for page := 0; more(page); page++ {
		batch, resp, err := client.Issues.ListComments(ctx, owner, repo, number, issueOpts)
		if err != nil {
			return nil, false, githubAPIError("list issue comments", resp, err)
		}
		for _, c := range batch {
			comments = append(comments, issueCommentRecord(c))
		}
		if resp.NextPage == 0 {
			break
		}
		issueOpts.Page = resp.NextPage
		if !more(page + 1) {
			truncated = true
		}
	}

	reviewOpts := &github.PullRequestListCommentsOptions{ListOptions: github.ListOptions{PerPage: 100}}
	for page := 0; more(page); page++ {
		batch, resp, err := client.PullRequests.ListComments(ctx, owner, repo, number, reviewOpts)
		if err != nil {
			if opts.IgnoreMissingReviewComments && isNotFound(resp) {
				break
			}
			return nil, false, githubAPIError("list review comments", resp, err)
		}
		for _, c := range batch {
			comments = append(comments, reviewCommentRecord(c))
		}
		if resp.NextPage == 0 {
			break
		}
		reviewOpts.Page = resp.NextPage
		if !more(page + 1) {
			truncated = true
		}
	}

	sort.SliceStable(comments, func(i, j int) bool { return comments[i].Created.Before(comments[j].Created) })
	return comments, truncated, nil
}
