package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
)

const (
	commentKindIssue         = PRCommentKindIssue
	commentKindReview        = PRCommentKindReview
	commentKindReviewSummary = PRCommentKindReviewSummary

	// maxCommentListPages bounds list mode: 10 pages x 100 comments per comment type.
	maxCommentListPages = 10
)

type getPRCommentArgs struct {
	CommentURL string `json:"comment_url,omitempty"`
	CommentID  int64  `json:"comment_id,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Repo       string `json:"repo,omitempty"`
	PRNumber   int    `json:"pr_number,omitempty"`
}

// commentRef identifies a single GitHub comment parsed from a URL.
type commentRef struct {
	Owner, Repo string
	Number      int
	ID          int64
	Kind        string
}

// parseGitHubCommentURL parses comment permalinks such as
//
//	https://github.com/o/r/pull/42#issuecomment-123      (conversation comment)
//	https://github.com/o/r/issues/42#issuecomment-123    (issue comment)
//	https://github.com/o/r/pull/42#discussion_r456       (inline review comment; also #r456)
//	https://github.com/o/r/pull/42#pullrequestreview-789 (review summary)
//
// Only github.com URLs are accepted. The URL is only parsed, never fetched.
func parseGitHubCommentURL(raw string) (*commentRef, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid comment_url: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("invalid comment_url %q: expected an https://github.com/... URL", raw)
	}
	if host := strings.ToLower(u.Hostname()); host != "github.com" && host != "www.github.com" {
		return nil, fmt.Errorf("invalid comment_url %q: only github.com URLs are supported", raw)
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 4 || segs[0] == "" || segs[1] == "" || (segs[2] != "pull" && segs[2] != "issues") {
		return nil, fmt.Errorf("invalid comment_url %q: expected .../<owner>/<repo>/pull/<n>#... or .../issues/<n>#...", raw)
	}
	number, err := strconv.Atoi(segs[3])
	if err != nil || number <= 0 {
		return nil, fmt.Errorf("invalid comment_url %q: bad pull request or issue number", raw)
	}

	ref := &commentRef{Owner: segs[0], Repo: segs[1], Number: number}
	frag := u.Fragment
	var idText string
	switch {
	case strings.HasPrefix(frag, "issuecomment-"):
		ref.Kind, idText = commentKindIssue, strings.TrimPrefix(frag, "issuecomment-")
	case strings.HasPrefix(frag, "discussion_r"):
		ref.Kind, idText = commentKindReview, strings.TrimPrefix(frag, "discussion_r")
	case strings.HasPrefix(frag, "pullrequestreview-"):
		ref.Kind, idText = commentKindReviewSummary, strings.TrimPrefix(frag, "pullrequestreview-")
	case strings.HasPrefix(frag, "r") && len(frag) > 1:
		ref.Kind, idText = commentKindReview, frag[1:]
	default:
		return nil, fmt.Errorf("invalid comment_url %q: missing comment fragment (e.g. #issuecomment-123 or #discussion_r123)", raw)
	}
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("invalid comment_url %q: bad comment id %q", raw, idText)
	}
	ref.ID = id
	return ref, nil
}

func formatCommentView(owner, repo string, v PRCommentRecord) string {
	var sb strings.Builder
	label := map[string]string{
		commentKindIssue:         "Conversation comment",
		commentKindReview:        "Inline review comment",
		commentKindReviewSummary: "Review",
	}[v.Kind]
	fmt.Fprintf(&sb, "### %s %d", label, v.ID)
	if v.Number > 0 {
		fmt.Fprintf(&sb, " on %s/%s#%d", owner, repo, v.Number)
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "- **Author**: %s\n", v.Author)
	fmt.Fprintf(&sb, "- **Created**: %s\n", v.Created.UTC().Format(time.RFC3339))
	if !v.Updated.IsZero() && !v.Updated.Equal(v.Created) {
		fmt.Fprintf(&sb, "- **Updated**: %s\n", v.Updated.UTC().Format(time.RFC3339))
	}
	if v.State != "" {
		fmt.Fprintf(&sb, "- **State**: %s\n", v.State)
	}
	if v.Path != "" {
		loc := v.Path
		if line := v.DisplayLine(); line > 0 {
			loc = fmt.Sprintf("%s:%d", v.Path, line)
		}
		fmt.Fprintf(&sb, "- **Location**: %s\n", loc)
	}
	if v.Kind == commentKindReview {
		fmt.Fprintf(&sb, "- **Outdated**: %t\n", v.Outdated)
	}
	fmt.Fprintf(&sb, "- **URL**: %s\n", v.URL)
	fmt.Fprintf(&sb, "\n%s\n", v.Body)
	return sb.String()
}

func fetchIssueComment(ctx context.Context, client *github.Client, owner, repo string, id int64) (PRCommentRecord, *github.Response, error) {
	c, resp, err := client.Issues.GetComment(ctx, owner, repo, id)
	if err != nil {
		return PRCommentRecord{}, resp, err
	}
	return issueCommentRecord(c), resp, nil
}

func fetchReviewComment(ctx context.Context, client *github.Client, owner, repo string, id int64) (PRCommentRecord, *github.Response, error) {
	c, resp, err := client.PullRequests.GetComment(ctx, owner, repo, id)
	if err != nil {
		return PRCommentRecord{}, resp, err
	}
	return reviewCommentRecord(c), resp, nil
}

// fetchSingleComment fetches one comment. With an empty kind it tries a conversation comment first
// and falls back to an inline review comment on 404.
func fetchSingleComment(ctx context.Context, client *github.Client, owner, repo, kind string, id int64, prNumber int) (PRCommentRecord, error) {
	switch kind {
	case commentKindIssue:
		v, resp, err := fetchIssueComment(ctx, client, owner, repo, id)
		if err != nil {
			return PRCommentRecord{}, githubAPIError(fmt.Sprintf("get issue comment %d", id), resp, err)
		}
		return v, nil
	case commentKindReview:
		v, resp, err := fetchReviewComment(ctx, client, owner, repo, id)
		if err != nil {
			return PRCommentRecord{}, githubAPIError(fmt.Sprintf("get review comment %d", id), resp, err)
		}
		return v, nil
	case commentKindReviewSummary:
		rv, resp, err := client.PullRequests.GetReview(ctx, owner, repo, prNumber, id)
		if err != nil {
			return PRCommentRecord{}, githubAPIError(fmt.Sprintf("get review %d", id), resp, err)
		}
		return reviewSummaryRecord(rv, prNumber), nil
	}

	v, resp, err := fetchIssueComment(ctx, client, owner, repo, id)
	if err == nil {
		return v, nil
	}
	if !isNotFound(resp) {
		return PRCommentRecord{}, githubAPIError(fmt.Sprintf("get issue comment %d", id), resp, err)
	}
	v, resp, err = fetchReviewComment(ctx, client, owner, repo, id)
	if err == nil {
		return v, nil
	}
	if !isNotFound(resp) {
		return PRCommentRecord{}, githubAPIError(fmt.Sprintf("get review comment %d", id), resp, err)
	}
	return PRCommentRecord{}, fmt.Errorf("comment %d not found in %s/%s as a conversation comment or an inline review comment", id, owner, repo)
}

// formatCommentList renders a compact one-line-per-comment listing. When the listing exceeds the
// output cap, the oldest comments are dropped so the most recent ones survive. fetchLimited says
// that fetching itself stopped early, so even older comments exist that were never read.
func formatCommentList(owner, repo string, number int, views []PRCommentRecord, fetchLimited bool) string {
	var issueCount, reviewCount int
	lines := make([]string, len(views))
	for i, v := range views {
		var line string
		if v.Kind == commentKindReview {
			reviewCount++
			loc := v.Path
			if line := v.DisplayLine(); line > 0 {
				loc = fmt.Sprintf("%s:%d", v.Path, line)
			}
			outdated := ""
			if v.Outdated {
				outdated = " (outdated)"
			}
			line = fmt.Sprintf("- [review] id=%d %s %s %s%s: %s", v.ID, v.Author, v.Created.UTC().Format(time.RFC3339), loc, outdated, oneLine(v.Body, 160))
		} else {
			issueCount++
			line = fmt.Sprintf("- [issue] id=%d %s %s: %s", v.ID, v.Author, v.Created.UTC().Format(time.RFC3339), oneLine(v.Body, 160))
		}
		lines[i] = line
	}

	header := fmt.Sprintf("### Comments on %s/%s#%d (%d conversation, %d inline review)\n", owner, repo, number, issueCount, reviewCount)
	footer := "\nUse get_pr_comment with comment_id (and kind 'issue' or 'review') to read a full comment.\n"
	budget := maxToolOutputBytes - len(header) - len(footer) - 100
	start := len(lines)
	used := 0
	for start > 0 && used+len(lines[start-1])+1 <= budget {
		start--
		used += len(lines[start]) + 1
	}

	var sb strings.Builder
	sb.WriteString(header)
	if len(lines) == 0 {
		sb.WriteString("(no comments)\n")
	}
	if start > 0 {
		fmt.Fprintf(&sb, "... (%d older comments omitted)\n", start)
	}
	if fetchLimited {
		fmt.Fprintf(&sb, "... (only the first %d comments of each kind were fetched; more exist)\n", maxCommentListPages*100)
	}
	for _, l := range lines[start:] {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	sb.WriteString(footer)
	return sb.String()
}

func registerGetPRCommentTool(r *Registry, rootDir, metadataFile string) {
	def := llm.ToolDef{
		Name: "get_pr_comment",
		Description: "Fetch GitHub comments live, so you see comments posted after your review context was generated. " +
			"Single mode: pass 'comment_url' (a permalink such as .../pull/42#issuecomment-123, #discussion_r456 or #pullrequestreview-789) " +
			"or 'comment_id' to get that comment's body, author, timestamps and permalink. " +
			"List mode: pass neither, and the conversation comments plus inline review comments of the pull request (or issue) 'pr_number' " +
			"are listed with their IDs. Works for pull requests and plain issues.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"comment_url": map[string]any{
					"type":        "string",
					"description": "Permalink of a comment on github.com. Takes precedence over repo/pr_number.",
				},
				"comment_id": map[string]any{
					"type":        "integer",
					"description": "Numeric comment ID, as an alternative to comment_url.",
				},
				"kind": map[string]any{
					"type":        "string",
					"enum":        []string{commentKindIssue, commentKindReview, commentKindReviewSummary},
					"description": "With comment_id: 'issue' (conversation comment), 'review' (inline code comment) or 'review_summary' (a review's top-level body; needs pr_number). If omitted, 'issue' and then 'review' are tried.",
				},
				"repo": map[string]any{
					"type":        "string",
					"description": "Optional repository in 'owner/repo' format. Defaults to the auto-detected repository.",
				},
				"pr_number": map[string]any{
					"type":        "integer",
					"description": "Pull request or issue number. Required for list mode unless it can be auto-detected; required for kind 'review_summary' with comment_id.",
				},
			},
		},
	}

	RegisterToolWithArgs(r, def, func(ctx context.Context, args getPRCommentArgs) (string, error) {
		switch args.Kind {
		case "", commentKindIssue, commentKindReview, commentKindReviewSummary:
		default:
			return "", fmt.Errorf("invalid kind %q: expected 'issue', 'review' or 'review_summary'", args.Kind)
		}
		if args.CommentURL != "" && args.CommentID != 0 {
			return "", errors.New("specify either comment_url or comment_id, not both")
		}
		if args.CommentID < 0 {
			return "", errors.New("comment_id must be greater than 0")
		}

		var (
			owner, repo string
			kind        = args.Kind
			id          = args.CommentID
			number      = args.PRNumber
		)
		singleMode := args.CommentURL != "" || args.CommentID != 0
		if args.CommentURL != "" {
			ref, err := parseGitHubCommentURL(args.CommentURL)
			if err != nil {
				return "", err
			}
			owner, repo, kind, id = ref.Owner, ref.Repo, ref.Kind, ref.ID
			if args.Repo != "" {
				wantOwner, wantRepo, err := parseRepoSlug(args.Repo)
				if err != nil {
					return "", err
				}
				if !strings.EqualFold(wantOwner, owner) || !strings.EqualFold(wantRepo, repo) {
					return "", fmt.Errorf("comment_url belongs to %s/%s but repo is %s/%s", owner, repo, wantOwner, wantRepo)
				}
			}
			if number > 0 && number != ref.Number {
				return "", fmt.Errorf("comment_url refers to #%d but pr_number is %d", ref.Number, number)
			}
			number = ref.Number
		}

		client, err := githubClientForTool("fetch PR comments")
		if err != nil {
			return "", err
		}

		switch {
		case singleMode:
			if args.CommentURL == "" {
				if kind == commentKindReviewSummary {
					o, rp, n, err := resolvePRTarget(ctx, rootDir, metadataFile, args.Repo, number)
					if err != nil {
						return "", err
					}
					owner, repo, number = o, rp, n
				} else {
					owner, repo, err = resolveRepoTarget(ctx, rootDir, metadataFile, args.Repo)
					if err != nil {
						return "", err
					}
				}
			}
			view, err := fetchSingleComment(ctx, client, owner, repo, kind, id, number)
			if err != nil {
				return "", err
			}
			return truncateOutput(formatCommentView(owner, repo, view)), nil
		default:
			owner, repo, number, err = resolvePRTarget(ctx, rootDir, metadataFile, args.Repo, number)
			if err != nil {
				return "", err
			}
			views, limited, err := FetchPRComments(ctx, client, owner, repo, number, FetchPRCommentsOptions{
				MaxPagesPerKind:             maxCommentListPages,
				IgnoreMissingReviewComments: true,
			})
			if err != nil {
				return "", err
			}
			return truncateOutput(formatCommentList(owner, repo, number, views, limited)), nil
		}
	})
}
