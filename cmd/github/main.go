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
	"github.com/menny/cassandra/tools"
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
	var deleteOldComments bool

	flag.StringVar(&repoFullName, "repo-full-name", "", "Full name of the repository (owner/repo)")
	flag.IntVar(&prNumber, "pr", 0, "Pull request number")
	flag.StringVar(&reactionContent, "content", "eyes", "Reaction content (e.g. eyes, rocket, heart)")
	flag.Int64Var(&reactionID, "reaction-id", 0, "Reaction ID for removal")
	flag.StringVar(&bodyFile, "file", "", "Path to the comment body file")
	flag.StringVar(&tag, "tag", "", "Tag to identify the comment for updates or self-identification")
	flag.StringVar(&outputFile, "output", "", "Path to the output file (for get-metadata)")
	flag.StringVar(&metadataFile, "metadata-file", "", "Path to the metadata file (for post-structured-review)")
	flag.BoolVar(&allowReviewAction, "allow-review-action", false, "Whether to allow the AI's suggested review action (APPROVE/REQUEST_CHANGES). If false, forces COMMENT.")
	flag.BoolVar(&deleteOldComments, "delete-old-comments", true, "Whether to delete previous bot-authored inline comments before posting a new review.")

	flag.Parse()

	if repoFullName == "" {
		log.Fatal("--repo-full-name is required")
	}

	if prNumber <= 0 {
		log.Fatal("--pr is required and must be greater than 0")
	}

	if len(flag.Args()) < 1 {
		log.Fatal("Action required (add-reaction, remove-reaction, post-comment, post-structured-review, get-metadata, get-diff, get-files, get-commits)")
	}

	// Process tag: only the inner text is provided.
	// Default to 'cassandra-ai-review' if empty.
	if tag == "" {
		tag = "cassandra-ai-review"
	}

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
		err := postStructuredReview(ctx, client, owner, repo, prNumber, bodyFile, tag, metadataFile, allowReviewAction, deleteOldComments)
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

	case "get-diff":
		diff, err := getDiff(ctx, client, owner, repo, prNumber)
		if err != nil {
			log.Fatalf("Failed to get diff: %v", err)
		}
		if outputFile != "" {
			if err := os.WriteFile(outputFile, []byte(diff), 0o644); err != nil {
				log.Fatalf("Failed to write diff to %s: %v", outputFile, err)
			}
		} else {
			fmt.Println(diff)
		}

	case "get-files":
		files, err := getFiles(ctx, client, owner, repo, prNumber)
		if err != nil {
			log.Fatalf("Failed to get files: %v", err)
		}
		content := strings.Join(files, "\n")
		if outputFile != "" {
			if err := os.WriteFile(outputFile, []byte(content), 0o644); err != nil {
				log.Fatalf("Failed to write files to %s: %v", outputFile, err)
			}
		} else {
			fmt.Println(content)
		}

	case "get-commits":
		commits, err := getCommits(ctx, client, owner, repo, prNumber)
		if err != nil {
			log.Fatalf("Failed to get commits: %v", err)
		}
		content := strings.Join(commits, "\n")
		if outputFile != "" {
			if err := os.WriteFile(outputFile, []byte(content), 0o644); err != nil {
				log.Fatalf("Failed to write commits to %s: %v", outputFile, err)
			}
		} else {
			fmt.Println(content)
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

// wrapTag wraps a raw slug into a hidden HTML comment tag with an optional prefix.
// It sanitizes the slug to ensure it cannot break out of the HTML comment.
func wrapTag(slug, prefix string) string {
	// Sanitize slug:
	// 1. Replace '--' with '__' because HTML comments cannot contain '--'.
	// 2. Remove '<' and '>' to prevent breakout.
	s := strings.ReplaceAll(slug, "--", "__")
	s = strings.ReplaceAll(s, "<", "")
	s = strings.ReplaceAll(s, ">", "")
	s = strings.TrimSpace(s)

	return fmt.Sprintf("<!-- %s%s -->", prefix, s)
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

	mainTag := wrapTag(tag, "cassandra-main-")
	return postCommentText(ctx, client, owner, repo, prNumber, string(body), mainTag)
}

func postCommentText(ctx context.Context, client *github.Client, owner, repo string, prNumber int, content, tag string) error {
	if tag != "" && !strings.Contains(content, tag) {
		content = fmt.Sprintf("%s\n\n%s", content, tag)
	}

	self, _, err := client.Users.Get(ctx, "")
	selfLogin := ""
	if err != nil {
		log.Printf("Warning: failed to get self user (likely due to GITHUB_TOKEN permissions): %v. Deduplication will rely solely on the tag.", err)
	} else {
		selfLogin = self.GetLogin()
	}

	// Find existing comment
	opts := &github.IssueListCommentsOptions{
		ListOptions: github.ListOptions{
			PerPage: 100,
		},
	}

	var latestCommentID int64
	var redundantCommentIDs []int64
	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return fmt.Errorf("failed to list comments: %w", err)
		}
		for _, c := range comments {
			if tag != "" && strings.Contains(c.GetBody(), tag) {
				// If we have a selfLogin, we use it to be sure.
				// If not, we trust the unique tag.
				if selfLogin == "" || c.GetUser().GetLogin() == selfLogin {
					if latestCommentID != 0 {
						redundantCommentIDs = append(redundantCommentIDs, latestCommentID)
					}
					// We found a matching comment. Since the API returns results in
					// ascending chronological order, the last one we find is the latest.
					latestCommentID = c.GetID()
				}
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Delete redundant comments first
	for _, id := range redundantCommentIDs {
		if _, err := client.Issues.DeleteComment(ctx, owner, repo, id); err != nil {
			log.Printf("Warning: failed to delete redundant comment %d: %v", id, err)
		}
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

	mainTag := wrapTag(tag, "cassandra-main-")
	inlineTag := wrapTag(tag, "cassandra-inline-")
	reviewTag := wrapTag(tag, "")

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
			isSelf := tag != "" && (strings.Contains(body, mainTag) || strings.Contains(body, reviewTag))
			metadata.Comments = append(metadata.Comments, core.PRComment{
				ID:     c.GetID(),
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
			isSelf := tag != "" && strings.Contains(body, inlineTag)
			metadata.Comments = append(metadata.Comments, core.PRComment{
				ID:         c.GetID(),
				Author:     c.GetUser().GetLogin(),
				Body:       body,
				IsSelf:     isSelf,
				Date:       getCreatedAt(c.CreatedAt),
				Path:       c.GetPath(),
				Line:       c.GetLine(),
				StartLine:  c.GetStartLine(),
				IsOutdated: c.Position == nil && c.OriginalPosition != nil,
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

func postStructuredReview(ctx context.Context, client *github.Client, owner, repo string, prNumber int, bodyFile, tag, metadataFile string, allowReviewAction, deleteOldComments bool) error {
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
		if err != nil {
			log.Printf("Warning: failed to read metadata file: %v. Deduplication will be limited.", err)
		} else {
			if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
				log.Printf("Warning: failed to unmarshal metadata: %v", err)
			}
		}
	}

	reviewTag := wrapTag(tag, "")
	inlineTag := wrapTag(tag, "cassandra-inline-")
	mainTag := wrapTag(tag, "cassandra-main-")

	// 1. Dismiss previous reviews BEFORE providing new review
	if tag != "" {
		if err := dismissPreviousReviews(ctx, client, owner, repo, prNumber, reviewTag, 0); err != nil {
			log.Printf("Warning: failed to dismiss previous reviews: %v", err)
		}
	}

	// 2. Delete old inline comments if requested
	if tag != "" && deleteOldComments {
		for _, c := range metadata.Comments {
			if c.IsSelf && strings.Contains(c.Body, inlineTag) {
				if _, err := client.PullRequests.DeleteComment(ctx, owner, repo, c.ID); err != nil {
					log.Printf("Warning: failed to delete old inline comment %d: %v", c.ID, err)
				}
			}
		}
	}

	// 3. Post Non-Specific Review as a separate comment
	if sr.NonSpecificReview != "" {
		if err := postCommentText(ctx, client, owner, repo, prNumber, sr.NonSpecificReview, mainTag); err != nil {
			log.Printf("Warning: failed to post non-specific review comment: %v", err)
		}
	}

	comments := []*github.DraftReviewComment{}
	reviewRationale := sr.Approval.Rationale

	for _, fr := range sr.FilesReview {
		startLine, endLine, err := fr.ParseLines()
		if err != nil {
			log.Printf("Warning: failed to parse lines for %s: %v. Appending to main review rationale.", fr.Path, err)
			reviewRationale = fmt.Sprintf("%s\n\n- **%s**: %s", reviewRationale, fr.Path, fr.Review)
			continue
		}

		commentBody := fr.Review
		if tag != "" {
			commentBody = fmt.Sprintf("%s\n\n%s", commentBody, inlineTag)
		}

		// New location (or after deletion): create new comment in the review
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
			// Fallback for file-level
			reviewRationale = fmt.Sprintf("%s\n\n- **%s** (file-level): %s", reviewRationale, fr.Path, fr.Review)
		}
	}

	reviewBody := reviewRationale
	if tag != "" {
		reviewBody = fmt.Sprintf("%s\n\n%s", reviewBody, reviewTag)
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
		// 422 Fallback logic
		if resp != nil && resp.StatusCode == 422 {
			errStr := err.Error()
			isPermissionError := strings.Contains(errStr, "not permitted to approve")
			hasComments := len(reviewRequest.Comments) > 0

			if isPermissionError {
				log.Printf("Warning: GITHUB_TOKEN is not permitted to approve PRs. Falling back to COMMENT review.")
				reviewRequest.Event = github.Ptr("COMMENT")

				// Try again with COMMENT action, keeping inline comments
				_, resp, err = client.PullRequests.CreateReview(ctx, owner, repo, prNumber, reviewRequest)
				if err == nil {
					return nil
				}
				// If it still fails with 422, it's likely a line hallucination issue
				if resp == nil || resp.StatusCode != 422 {
					return err
				}
				errStr = err.Error()
			}

			if hasComments {
				log.Printf("Warning: failed to post structured review (likely due to line hallucinations): %v. Retrying without inline comments.", errStr)
				reviewRequest.Comments = nil
				var sb strings.Builder
				sb.WriteString(reviewRequest.GetBody())
				sb.WriteString("\n\n### Detailed Inline Feedback (Fallback)\n")
				for _, fr := range sr.FilesReview {
					_, endLine, err := fr.ParseLines()
					if err == nil && endLine > 0 {
						sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", fr.Path, fr.Lines, fr.Review))
					}
				}
				reviewRequest.Body = github.Ptr(sb.String())

				_, _, err = client.PullRequests.CreateReview(ctx, owner, repo, prNumber, reviewRequest)
				return err
			}
		}
		return err
	}

	return nil
}

func dismissPreviousReviews(ctx context.Context, client *github.Client, owner, repo string, prNumber int, tag string, skipReviewID int64) error {
	opts := &github.ListOptions{PerPage: 100}
	for {
		reviews, resp, err := client.PullRequests.ListReviews(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return err
		}

		for _, r := range reviews {
			if r.GetID() == skipReviewID {
				continue
			}
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

func getDiff(ctx context.Context, client *github.Client, owner, repo string, prNumber int) (string, error) {
	diff, _, err := client.PullRequests.GetRaw(ctx, owner, repo, prNumber, github.RawOptions{Type: github.Diff})
	if err != nil {
		return "", err
	}

	// Filter out lockfiles from the raw diff text.
	// Unified diff format separates file chunks with "diff --git a/... b/..."
	lines := strings.Split(diff, "\n")
	var filteredLines []string
	skipping := false

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			// Extract file paths from "diff --git a/path/to/file b/path/to/file"
			parts := strings.Fields(line)
			isLockFile := false
			if len(parts) >= 4 {
				// parts[2] is a/file, parts[3] is b/file
				pathB := strings.TrimPrefix(parts[3], "b/")
				for _, lf := range tools.LockFiles {
					if pathB == lf || strings.HasSuffix(pathB, "/"+lf) {
						isLockFile = true
						break
					}
				}
			}
			skipping = isLockFile
		}

		if !skipping {
			filteredLines = append(filteredLines, line)
		}
	}

	return strings.Join(filteredLines, "\n"), nil
}

func getFiles(ctx context.Context, client *github.Client, owner, repo string, prNumber int) ([]string, error) {
	opts := &github.ListOptions{
		PerPage: 100,
	}
	var allFiles []string

	for {
		files, resp, err := client.PullRequests.ListFiles(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			path := f.GetFilename()
			isLockFile := false
			for _, lf := range tools.LockFiles {
				if path == lf || strings.HasSuffix(path, "/"+lf) {
					isLockFile = true
					break
				}
			}
			if !isLockFile {
				allFiles = append(allFiles, path)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return allFiles, nil
}

func getCommits(ctx context.Context, client *github.Client, owner, repo string, prNumber int) ([]string, error) {
	opts := &github.ListOptions{
		PerPage: 100,
	}
	var allCommits []string
	for {
		commits, resp, err := client.PullRequests.ListCommits(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return nil, err
		}
		for _, c := range commits {
			msg := c.GetCommit().GetMessage()
			// Extract only the first line (subject)
			subject := strings.SplitN(msg, "\n", 2)[0]
			allCommits = append(allCommits, "- "+subject)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return allCommits, nil
}
