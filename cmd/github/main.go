package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/core"
	flag "github.com/spf13/pflag"
)

func main() {
	var repoFullName string
	var prNumber int
	var reactionContent string
	var reactionID int64
	var bodyFile string
	var tag string
	var outputFile string
	var metadataFile string
	var allowReviewAction bool

	flag.StringVar(&repoFullName, "repo-full-name", "", "Full name of the repository (owner/repo)")
	flag.IntVar(&prNumber, "pr", 0, "Pull request number")
	flag.StringVar(&reactionContent, "content", "eyes", "Reaction content (e.g. eyes, rocket, heart)")
	flag.Int64Var(&reactionID, "reaction-id", 0, "Reaction ID for removal")
	flag.StringVar(&bodyFile, "file", "", "Path to the comment body file")
	flag.StringVar(&tag, "tag", "", "Tag to identify the comment for updates or self-identification")
	flag.StringVar(&outputFile, "output", "", "Path to the output file (for get-metadata)")
	flag.StringVar(&metadataFile, "metadata-file", "", "Path to the metadata file (for post-structured-review)")
	flag.BoolVar(&allowReviewAction, "allow-review-action", false, "Whether to allow the AI's suggested review action (APPROVE/REQUEST_CHANGES). If false, forces COMMENT.")

	flag.Parse()

	if repoFullName == "" {
		log.Fatal("--repo-full-name is required")
	}

	if prNumber <= 0 {
		log.Fatal("--pr is required and must be greater than 0")
	}

	if len(flag.Args()) < 1 {
		log.Fatal("Action required (add-reaction, remove-reaction, post-comment, get-metadata)")
	}

	// Process tag: only the inner text is provided, we wrap it in HTML comment tags.
	// Default to 'cassandra-ai-review' if empty.
	if tag == "" {
		tag = "cassandra-ai-review"
	}
	tag = fmt.Sprintf("<!-- %s -->", tag)

	action := flag.Arg(0)
	ctx := context.Background()
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		log.Fatal("GITHUB_TOKEN environment variable is required")
	}

	client := github.NewClient(nil).WithAuthToken(token)

	owner, repo, err := parseRepo(repoFullName)
	if err != nil {
		log.Fatalf("Invalid repo-full-name: %v", err)
	}

	switch action {
	case "add-reaction":
		id, err := addReaction(ctx, client, owner, repo, prNumber, reactionContent)
		if err != nil {
			log.Fatalf("Failed to add reaction: %v", err)
		}
		fmt.Println(id)

	case "remove-reaction":
		if reactionID == 0 {
			log.Fatal("--reaction-id is required for remove-reaction")
		}
		err := removeReaction(ctx, client, owner, repo, prNumber, reactionID)
		if err != nil {
			log.Fatalf("Failed to remove reaction: %v", err)
		}

	case "post-comment":
		if bodyFile == "" {
			log.Fatal("--file is required for post-comment")
		}
		err := postComment(ctx, client, owner, repo, prNumber, bodyFile, tag)
		if err != nil {
			log.Fatalf("Failed to post comment: %v", err)
		}

	case "post-structured-review":
		if bodyFile == "" {
			log.Fatal("--file is required for post-structured-review")
		}
		err := postStructuredReview(ctx, client, owner, repo, prNumber, bodyFile, tag, metadataFile, allowReviewAction)
		if err != nil {
			log.Fatalf("Failed to post structured review: %v", err)
		}

	case "get-metadata":
		metadata, err := getMetadata(ctx, client, owner, repo, prNumber, tag)
		if err != nil {
			log.Fatalf("Failed to get metadata: %v", err)
		}
		metadata.RepoFullName = repoFullName

		bytes, err := json.MarshalIndent(metadata, "", "  ")
		if err != nil {
			log.Fatalf("Failed to marshal metadata: %v", err)
		}

		if outputFile != "" {
			if err := os.WriteFile(outputFile, bytes, 0o644); err != nil {
				log.Fatalf("Failed to write metadata to %s: %v", outputFile, err)
			}
		} else {
			fmt.Println(string(bytes))
		}

	default:
		log.Fatalf("Unknown action: %s", action)
	}
}

func parseRepo(fullName string) (owner, repo string, err error) {
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("expected owner/repo, got %s", fullName)
	}
	return parts[0], parts[1], nil
}

func addReaction(ctx context.Context, client *github.Client, owner, repo string, prNumber int, content string) (int64, error) {
	reaction, _, err := client.Reactions.CreateIssueReaction(ctx, owner, repo, prNumber, content)
	if err != nil {
		return 0, err
	}
	return reaction.GetID(), nil
}

func removeReaction(ctx context.Context, client *github.Client, owner, repo string, prNumber int, reactionID int64) error {
	_, err := client.Reactions.DeleteIssueReaction(ctx, owner, repo, prNumber, reactionID)
	return err
}

func postComment(ctx context.Context, client *github.Client, owner, repo string, prNumber int, bodyFile, tag string) error {
	body, err := os.ReadFile(bodyFile)
	if err != nil {
		return fmt.Errorf("failed to read body file: %w", err)
	}

	return postCommentText(ctx, client, owner, repo, prNumber, string(body), tag)
}

func postCommentText(ctx context.Context, client *github.Client, owner, repo string, prNumber int, content, tag string) error {
	if tag != "" && !strings.Contains(content, tag) {
		content = fmt.Sprintf("%s\n\n%s", content, tag)
	}

	self, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return fmt.Errorf("failed to get self user: %w", err)
	}
	selfLogin := self.GetLogin()

	// Find existing comment
	opts := &github.IssueListCommentsOptions{
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	var latestCommentID int64
	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return fmt.Errorf("failed to list comments: %w", err)
		}
		for _, c := range comments {
			if tag != "" && strings.Contains(c.GetBody(), tag) && c.GetUser().GetLogin() == selfLogin {
				// We found a matching comment from ourselves. Since the API returns results in
				// ascending chronological order, the last one we find is the latest.
				latestCommentID = c.GetID()
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	if latestCommentID != 0 {
		_, _, err := client.Issues.EditComment(ctx, owner, repo, latestCommentID, &github.IssueComment{
			Body: github.Ptr(content),
		})
		return err
	}

	_, _, err = client.Issues.CreateComment(ctx, owner, repo, prNumber, &github.IssueComment{
		Body: github.Ptr(content),
	})
	return err
}

func getMetadata(ctx context.Context, client *github.Client, owner, repo string, prNumber int, tag string) (*core.PRMetadata, error) {
	pr, _, err := client.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR: %w", err)
	}

	commentTag := tag
	if tag != "" {
		commentTag = strings.Replace(tag, "<!-- ", "<!-- comment-", 1)
	}

	metadata := &core.PRMetadata{
		PRNumber:      prNumber,
		Title:         pr.GetTitle(),
		Description:   pr.GetBody(),
		Author:        pr.GetUser().GetLogin(),
		CreatedAt:     getCreatedAt(pr.CreatedAt),
		IdentifiedTag: tag,
	}

	opts := &github.IssueListCommentsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}

	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list issue comments: %w", err)
		}

		for _, c := range comments {
			body := c.GetBody()
			isSelf := tag != "" && strings.Contains(body, tag)
			metadata.Comments = append(metadata.Comments, core.PRComment{
				Author: c.GetUser().GetLogin(),
				Body:   body,
				IsSelf: isSelf,
				Date:   getCreatedAt(c.CreatedAt),
			})
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Fetch PR Review Comments (inline code comments)
	reviewOpts := &github.PullRequestListCommentsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for {
		comments, resp, err := client.PullRequests.ListComments(ctx, owner, repo, prNumber, reviewOpts)
		if err != nil {
			return nil, fmt.Errorf("failed to list review comments: %w", err)
		}

		for _, c := range comments {
			body := c.GetBody()
			// Inline comments use the special commentTag
			isSelf := commentTag != "" && strings.Contains(body, commentTag)
			metadata.Comments = append(metadata.Comments, core.PRComment{
				Author:    c.GetUser().GetLogin(),
				Body:      body,
				IsSelf:    isSelf,
				Date:      getCreatedAt(c.CreatedAt),
				Path:      c.GetPath(),
				Line:      c.GetLine(),
				StartLine: c.GetStartLine(),
			})
		}

		if resp.NextPage == 0 {
			break
		}
		reviewOpts.Page = resp.NextPage
	}

	// Sort comments by date to provide chronological context
	sort.Slice(metadata.Comments, func(i, j int) bool {
		return metadata.Comments[i].Date.Before(metadata.Comments[j].Date)
	})

	return metadata, nil
}

func getCreatedAt(ts *github.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.Time
}

func postStructuredReview(ctx context.Context, client *github.Client, owner, repo string, prNumber int, bodyFile, tag, metadataFile string, allowReviewAction bool) error {
	reviewBytes, err := os.ReadFile(bodyFile)
	if err != nil {
		return fmt.Errorf("failed to read structured review file: %w", err)
	}

	var sr core.StructuredReview
	if err := json.Unmarshal(reviewBytes, &sr); err != nil {
		return fmt.Errorf("failed to unmarshal structured review: %w", err)
	}

	var metadata core.PRMetadata
	if metadataFile != "" {
		metadataBytes, err := os.ReadFile(metadataFile)
		if err == nil {
			if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
				log.Printf("Warning: failed to unmarshal metadata: %v", err)
			}
		}
	}

	// 1. Post Non-Specific Review as a separate comment
	if sr.NonSpecificReview != "" {
		if err := postCommentText(ctx, client, owner, repo, prNumber, sr.NonSpecificReview, tag); err != nil {
			log.Printf("Warning: failed to post non-specific review comment: %v", err)
		}
	}

	comments := []*github.DraftReviewComment{}
	reviewRationale := sr.Approval.Rationale

	commentTag := tag
	if tag != "" {
		// Distinguish between the main (non-specific) comment and the inline review comments
		// by using a different prefix for the latter.
		commentTag = strings.Replace(tag, "<!-- ", "<!-- comment-", 1)
	}

	for _, fr := range sr.FilesReview {
		startLine, endLine, err := fr.ParseLines()
		if err != nil {
			log.Printf("Warning: failed to parse lines for %s: %v. Appending to main review rationale.", fr.Path, err)
			reviewRationale = fmt.Sprintf("%s\n\n- **%s**: %s", reviewRationale, fr.Path, fr.Review)
			continue
		}

		// Check if we've already made this exact comment at this exact location
		alreadyCommented := false
		for _, c := range metadata.Comments {
			if c.IsSelf && c.Path == fr.Path && c.Line == endLine && strings.Contains(c.Body, strings.TrimSpace(fr.Review)) {
				alreadyCommented = true
				break
			}
		}

		if alreadyCommented {
			continue
		}

		commentBody := fr.Review
		if commentTag != "" {
			commentBody = fmt.Sprintf("%s\n\n%s", commentBody, commentTag)
		}

		comment := &github.DraftReviewComment{
			Path: github.Ptr(fr.Path),
			Body: github.Ptr(commentBody),
		}

		if endLine > 0 {
			comment.Line = github.Ptr(endLine)
			if startLine != endLine {
				comment.StartLine = github.Ptr(startLine)
				comment.StartSide = github.Ptr("RIGHT")
			}
			comments = append(comments, comment)
		} else {
			// File-level comment - go-github v69 doesn't support SubjectType: file on DraftReviewComment.
			// Fallback: append to the main review rationale.
			reviewRationale = fmt.Sprintf("%s\n\n- **%s** (file-level): %s", reviewRationale, fr.Path, fr.Review)
		}
	}

	reviewBody := reviewRationale
	if tag != "" {
		reviewBody = fmt.Sprintf("%s\n\n%s", reviewBody, tag)
	}

	// Dismiss previous reviews with the same tag to keep the PR timeline clean.
	if tag != "" {
		if err := dismissPreviousReviews(ctx, client, owner, repo, prNumber, tag); err != nil {
			log.Printf("Warning: failed to dismiss previous reviews: %v", err)
		}
	}

	action := strings.ToUpper(strings.TrimSpace(sr.Approval.Action))
	if !allowReviewAction || (action != "APPROVE" && action != "REQUEST_CHANGES" && action != "COMMENT") {
		action = "COMMENT"
	}

	reviewRequest := &github.PullRequestReviewRequest{
		Body:     github.Ptr(reviewBody),
		Event:    github.Ptr(action),
		Comments: comments,
	}

	_, resp, err := client.PullRequests.CreateReview(ctx, owner, repo, prNumber, reviewRequest)
	if err != nil {
		// If we get a 422 error, it might be due to a line hallucination (line not in diff).
		// Fallback: post the review without inline comments so we don't lose the summary feedback.
		if resp != nil && resp.StatusCode == 422 && len(comments) > 0 {
			log.Printf("Warning: failed to post structured review (likely due to line hallucinations): %v. Retrying without inline comments.", err)
			reviewRequest.Comments = nil

			// Append the skipped comments to the body so they aren't lost
			var sb strings.Builder
			sb.WriteString(reviewRequest.GetBody())
			sb.WriteString("\n\n### Detailed Inline Feedback (Fallback)\n")
			for _, c := range comments {
				loc := ""
				if c.Line != nil {
					loc = fmt.Sprintf(" at line %d", *c.Line)
				}
				sb.WriteString(fmt.Sprintf("- **%s**%s: %s\n", c.GetPath(), loc, c.GetBody()))
			}
			reviewRequest.Body = github.Ptr(sb.String())

			_, _, err = client.PullRequests.CreateReview(ctx, owner, repo, prNumber, reviewRequest)
			return err
		}
		return err
	}
	return nil
}

func dismissPreviousReviews(ctx context.Context, client *github.Client, owner, repo string, prNumber int, tag string) error {
	opts := &github.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := client.PullRequests.ListReviews(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return err
		}

		for _, r := range reviews {
			if strings.Contains(r.GetBody(), tag) && r.GetState() != "DISMISSED" {
				_, _, err := client.PullRequests.DismissReview(ctx, owner, repo, prNumber, r.GetID(), &github.PullRequestReviewDismissalRequest{
					Message: github.Ptr("Superseded by a new AI review."),
				})
				if err != nil {
					log.Printf("Warning: failed to dismiss review %d: %v", r.GetID(), err)
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return nil
}
