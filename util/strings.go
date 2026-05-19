package util

import (
	"strings"
	"unicode/utf8"
)

// TruncateString ensures s is no longer than maxBytes. If it is longer, it is
// truncated and the suffix "\n... (truncated)" is appended. The total length
// will not exceed maxBytes. It ensures the truncation point is at a valid
// UTF-8 rune boundary.
func TruncateString(s string, maxBytes int) string {
	const suffix = "\n... (truncated)"
	if len(s) <= maxBytes {
		return s
	}

	if maxBytes <= len(suffix) {
		if maxBytes <= 0 {
			return ""
		}
		return safeTruncate(s, maxBytes)
	}

	return safeTruncate(s, maxBytes-len(suffix)) + suffix
}

// TruncateLines joins lines into a single string, ensuring the result is no
// longer than maxBytes. It appends the suffix "\n... (truncated)" if the
// result is truncated. It ensures the truncation point is at a valid
// UTF-8 rune boundary.
func TruncateLines(lines []string, maxBytes int) string {
	const suffix = "\n... (truncated)"
	if len(lines) == 0 {
		return ""
	}

	// Calculate total length including newlines.
	totalLength := 0
	for i, line := range lines {
		if i > 0 {
			totalLength++ // \n
		}
		totalLength += len(line)
	}

	if totalLength <= maxBytes {
		return strings.Join(lines, "\n")
	}

	if maxBytes <= len(suffix) {
		if maxBytes <= 0 {
			return ""
		}
		// For very small limits, we just join what we can and truncate.
		return safeTruncate(strings.Join(lines, "\n"), maxBytes)
	}

	var sb strings.Builder
	for i, line := range lines {
		prefix := ""
		if i > 0 {
			prefix = "\n"
		}

		if sb.Len()+len(prefix)+len(line) > maxBytes-len(suffix) {
			remain := maxBytes - sb.Len() - len(suffix)
			if remain > 0 {
				linePartLimit := remain - len(prefix)
				if linePartLimit > 0 {
					sb.WriteString(prefix)
					sb.WriteString(safeTruncate(line, linePartLimit))
				}
			}
			sb.WriteString(suffix)
			return sb.String()
		}

		sb.WriteString(prefix)
		sb.WriteString(line)
	}

	return sb.String()
}

// safeTruncate returns a prefix of s that is at most maxBytes long and ends
// at a valid UTF-8 rune boundary.
func safeTruncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	s = s[:maxBytes]
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			s = s[:len(s)-1]
		} else {
			break
		}
	}
	return s
}
