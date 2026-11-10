package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/util"
)

// githubClientForTool returns an authenticated GitHub client, or an error explaining that a token
// is needed to perform purpose (e.g. "fetch PR comments").
func githubClientForTool(purpose string) (*github.Client, error) {
	token := getGitHubToken()
	if token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN (or GH_TOKEN) environment variable is required to %s", purpose)
	}
	return gitHubClientFactory(token), nil
}

// validateGitHubRef validates a branch, tag or SHA that is sent to the GitHub API, where it may end up
// in a URL path. On top of validateGitRev it rejects dot segments and empty path segments, so a ref
// can never rewrite the API endpoint being called (e.g. "a/../../orgs/x").
func validateGitHubRef(field, ref string) error {
	if err := validateGitRev(field, ref); err != nil {
		return err
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") || strings.Contains(ref, "@{") {
		return fmt.Errorf("invalid %s %q: expected a plain branch, tag or commit SHA", field, ref)
	}
	return nil
}

// resolveRepoTarget determines owner/repo from, in order: the explicit "owner/repo" argument, the
// PR metadata file, and the origin remote of the local checkout. Unlike resolvePRTarget it never
// needs (or looks up) a pull request number.
func resolveRepoTarget(ctx context.Context, rootDir, metadataFile, repoArg string) (owner, repo string, err error) {
	if repoArg != "" {
		return parseRepoSlug(repoArg)
	}
	if metadataFile != "" {
		if data, readErr := os.ReadFile(metadataFile); readErr == nil {
			var meta prMetadataFile
			if json.Unmarshal(data, &meta) == nil && meta.RepoFullName != "" {
				if o, r, e := parseRepoSlug(meta.RepoFullName); e == nil {
					return o, r, nil
				}
			}
		}
	}
	if out, gitErr := util.RunGit(ctx, rootDir, "remote", "get-url", "origin"); gitErr == nil {
		if o, r, e := parseRepoSlug(string(out)); e == nil {
			return o, r, nil
		}
	}
	return "", "", fmt.Errorf("could not determine repository; specify 'repo' parameter as 'owner/repo'")
}

// githubAPIError wraps a GitHub API failure with an actionable hint for the common HTTP statuses.
func githubAPIError(action string, resp *github.Response, err error) error {
	if resp != nil {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("failed to %s: authentication failed, check GITHUB_TOKEN: %w", action, err)
		case http.StatusForbidden:
			return fmt.Errorf("failed to %s: forbidden; the token may lack the required permission or be rate limited: %w", action, err)
		case http.StatusNotFound:
			return fmt.Errorf("failed to %s: not found (or the token cannot access it): %w", action, err)
		}
	}
	return fmt.Errorf("failed to %s: %w", action, err)
}

func isNotFound(resp *github.Response) bool {
	return resp != nil && resp.StatusCode == http.StatusNotFound
}

// oneLine collapses s to a single line of at most max runes, for list-style output.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "..."
}

func formatGitHubTime(ts *github.Timestamp) string {
	if ts == nil || ts.IsZero() {
		return "unknown"
	}
	return ts.UTC().Format(time.RFC3339)
}

// numberFromAPIURL extracts the trailing number of an API URL such as
// https://api.github.com/repos/o/r/issues/42. It returns 0 if there is none.
func numberFromAPIURL(u string) int {
	u = strings.TrimRight(u, "/")
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		if n, err := strconv.Atoi(u[i+1:]); err == nil {
			return n
		}
	}
	return 0
}
