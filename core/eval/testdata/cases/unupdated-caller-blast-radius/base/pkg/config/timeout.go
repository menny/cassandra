package config

import (
	"fmt"
	"strconv"
)

// ParseTimeout parses a numeric timeout string and returns the timeout in seconds.
func ParseTimeout(s string) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q: %w", s, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("timeout must be positive: %d", v)
	}
	return v, nil
}
