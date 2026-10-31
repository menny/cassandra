package core

import (
	"time"

	"github.com/menny/cassandra/tools"
)

// PRMetadata represents the context of a pull request.
type PRMetadata struct {
	RepoFullName  string        `json:"repo_full_name"`
	PRNumber      int           `json:"pr_number"`
	Title         string        `json:"title"`
	Description   string        `json:"description"`
	Author        string        `json:"author"`
	CreatedAt     time.Time     `json:"created_at"`
	Comments      []PRComment   `json:"comments"`
	IdentifiedTag string        `json:"identified_tag,omitempty"`
	Branch        string        `json:"branch,omitempty"`
	Base          string        `json:"base,omitempty"`
	Head          string        `json:"head,omitempty"`
	Stack         []PRStackItem `json:"stack,omitempty"`
}

// PRStackItem represents a single pull request in a PR stack.
type PRStackItem = tools.PRStackItem

// PRComment represents a single comment on a pull request.
type PRComment struct {
	ID         int64     `json:"id"`
	Author     string    `json:"author"`
	Body       string    `json:"body"`
	IsSelf     bool      `json:"is_self"`
	Date       time.Time `json:"date"`
	Path       string    `json:"path,omitempty"`
	Line       int       `json:"line,omitempty"`
	StartLine  int       `json:"start_line,omitempty"`
	IsOutdated bool      `json:"is_outdated"`
}
