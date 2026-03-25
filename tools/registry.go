package tools

import (
	"github.com/tmc/langchaingo/llms"
)

type Registry struct {
	tools []llms.Tool
}

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) ToLangChainTools() []llms.Tool {
	return r.tools
}

func RegisterLocalTools(r *Registry, diffBranch string) {
	// TODO: Register tools for local git diffs (e.g. read_file, glob_files using os level paths)
}

func RegisterPRTools(r *Registry, prNumber int) {
	// TODO: Register tools for remote PRs (e.g. fetching directly from GH API)
}
