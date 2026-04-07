package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/google/go-github/v69/github"
	flag "github.com/spf13/pflag"
)

func main() {
	var repoFullName string
	var prNumber int
	var reactionContent string
	var reactionID int64
	var bodyFile string
	var tag string

	flag.StringVar(&repoFullName, "repo-full-name", "", "Full name of the repository (owner/repo)")
	flag.IntVar(&prNumber, "pr", 0, "Pull request number")
	flag.StringVar(&reactionContent, "content", "eyes", "Reaction content (e.g. eyes, rocket, heart)")
	flag.Int64Var(&reactionID, "reaction-id", 0, "Reaction ID for removal")
	flag.StringVar(&bodyFile, "file", "", "Path to the comment body file")
	flag.StringVar(&tag, "tag", "", "Tag to identify the comment for updates")

	flag.Parse()

	if repoFullName == "" {
		log.Fatal("--repo-full-name is required")
	}

	if prNumber <= 0 {
		log.Fatal("--pr is required and must be greater than 0")
	}

	if len(flag.Args()) < 1 {
		log.Fatal("Action required (add-reaction, remove-reaction, post-comment)")
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
		if bodyFile == "" || tag == "" {
			log.Fatal("--file and --tag are required for post-comment")
		}
		err := postComment(ctx, client, owner, repo, prNumber, bodyFile, tag)
		if err != nil {
			log.Fatalf("Failed to post comment: %v", err)
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

	content := string(body)
	if !strings.Contains(content, tag) {
		content = fmt.Sprintf("%s\n\n%s", content, tag)
	}

	// Find existing comment
	opts := &github.IssueListCommentsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}

	var existingCommentID int64
	for {
		comments, resp, err := client.Issues.ListComments(ctx, owner, repo, prNumber, opts)
		if err != nil {
			return fmt.Errorf("failed to list comments: %w", err)
		}
		for _, c := range comments {
			if strings.Contains(c.GetBody(), tag) {
				existingCommentID = c.GetID()
				break
			}
		}
		if existingCommentID != 0 || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	if existingCommentID != 0 {
		_, _, err := client.Issues.EditComment(ctx, owner, repo, existingCommentID, &github.IssueComment{
			Body: github.Ptr(content),
		})
		return err
	}

	_, _, err = client.Issues.CreateComment(ctx, owner, repo, prNumber, &github.IssueComment{
		Body: github.Ptr(content),
	})
	return err
}
