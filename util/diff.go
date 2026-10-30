package util

import (
	"strconv"
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
			currentPath = parseGitDiffHeader(line)
		}
		if currentPath != "" {
			currentLines = append(currentLines, line)
		}
	}
	flush()

	return result
}

// parseGitDiffHeader extracts the target file path from a "diff --git <src> <dst>" header line.
// It handles quoted paths (e.g. paths containing spaces, tabs, or quotes) and avoids corrupting
// the path if an "a/" fallback is required.
func parseGitDiffHeader(line string) string {
	line = strings.TrimPrefix(line, "diff --git ")
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	// If line starts with a quote, token 1 is quoted: e.g. "a/foo" "b/bar"
	if strings.HasPrefix(line, "\"") {
		token1, rest, ok := parseQuotedToken(line)
		if ok {
			rest = strings.TrimSpace(rest)
			if strings.HasPrefix(rest, "\"") {
				token2, _, ok2 := parseQuotedToken(rest)
				if ok2 {
					p := cleanDiffPrefix(token2)
					if p != "" && p != "/dev/null" {
						return p
					}
				}
			} else if rest != "" {
				p := cleanDiffPrefix(rest)
				if p != "" && p != "/dev/null" {
					return p
				}
			}
			// Fallback to token1
			p := cleanDiffPrefix(token1)
			if p != "" && p != "/dev/null" {
				return p
			}
		}
	}

	// If destination path is quoted: e.g. a/foo "b/bar with spaces"
	if idx := strings.LastIndex(line, " \"b/"); idx != -1 {
		token2, _, ok := parseQuotedToken(line[idx+1:])
		if ok {
			p := cleanDiffPrefix(token2)
			if p != "" && p != "/dev/null" {
				return p
			}
		}
	}

	// Standard unquoted destination path after " b/"
	if idx := strings.LastIndex(line, " b/"); idx != -1 {
		dst := strings.TrimSpace(line[idx+3:])
		dst = strings.Trim(dst, "\"")
		p := cleanDiffPrefix(dst)
		if p != "" && p != "/dev/null" {
			return p
		}
	}

	// Fallback to "a/" token only when "b/" is absent or /dev/null
	if idx := strings.Index(line, "a/"); idx != -1 {
		src := line[idx+2:]
		if spaceIdx := strings.IndexByte(src, ' '); spaceIdx != -1 {
			src = src[:spaceIdx]
		}
		src = strings.Trim(src, "\"")
		p := cleanDiffPrefix(src)
		if p != "" && p != "/dev/null" {
			return p
		}
	}

	return ""
}

func parseQuotedToken(s string) (token string, remainder string, ok bool) {
	if !strings.HasPrefix(s, "\"") {
		return "", s, false
	}
	escaped := false
	for i := 1; i < len(s); i++ {
		if escaped {
			escaped = false
			continue
		}
		if s[i] == '\\' {
			escaped = true
			continue
		}
		if s[i] == '"' {
			quoted := s[:i+1]
			unquoted, err := strconv.Unquote(quoted)
			if err != nil {
				unquoted = s[1:i]
			}
			return unquoted, s[i+1:], true
		}
	}
	return strings.Trim(s, "\""), "", true
}

func cleanDiffPrefix(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "b/") {
		return strings.TrimPrefix(p, "b/")
	}
	if strings.HasPrefix(p, "a/") {
		return strings.TrimPrefix(p, "a/")
	}
	return p
}

// GetFileDiffSizes returns a map of file path to the byte size of its diff chunk.
func GetFileDiffSizes(diffText string) map[string]int {
	diffs := SplitUnifiedDiff(diffText)
	if diffs == nil {
		return nil
	}
	return DiffMapToSizes(diffs)
}

// DiffMapToSizes converts a diff chunk map into a map of file path to byte size.
func DiffMapToSizes(diffs map[string]string) map[string]int {
	if diffs == nil {
		return nil
	}
	sizes := make(map[string]int, len(diffs))
	for path, content := range diffs {
		sizes[path] = len(content)
	}
	return sizes
}
