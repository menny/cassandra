package util

// ParseRequired converts a "required" JSON schema field (often unmarshaled as
// []interface{}) into a []string.
func ParseRequired(req any) []string {
	switch reqs := req.(type) {
	case []string:
		return reqs
	case []interface{}:
		var out []string
		for _, r := range reqs {
			if s, ok := r.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
