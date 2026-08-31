package util

import (
	"strings"
)

// SplitUnifiedDiff parses a unified git diff into individual file diff chunks,
// returning a map of relative file path to full diff chunk text.
func SplitUnifiedDiff(diffText string) map[string]string {
	if strings.TrimSpace(diffText) == "" {
		return nil
	}

	result := make(map[string]string)
	lines := strings.Split(diffText, "\n")

	var currentPath string
	var currentLines []string

	flush := func() {
		if currentPath != "" && len(currentLines) > 0 {
			result[currentPath] = strings.TrimRight(strings.Join(currentLines, "\n"), "\r\n")
		}
		currentLines = nil
		currentPath = ""
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			// Extract destination path after " b/"
			if idx := strings.LastIndex(line, " b/"); idx != -1 {
				currentPath = line[idx+3:]
			} else if idx := strings.Index(line, " a/"); idx != -1 {
				currentPath = line[idx+3:]
			}
		}
		if currentPath != "" {
			currentLines = append(currentLines, line)
		}
	}
	flush()

	return result
}

// GetFileDiffSizes returns a map of file path to the byte size of its diff chunk.
func GetFileDiffSizes(diffText string) map[string]int {
	diffs := SplitUnifiedDiff(diffText)
	if diffs == nil {
		return nil
	}
	sizes := make(map[string]int, len(diffs))
	for path, content := range diffs {
		sizes[path] = len(content)
	}
	return sizes
}
