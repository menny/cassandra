package core

import (
	"regexp"
	"testing"

	"go.uber.org/goleak"
)

var ansiRegexp = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string {
	return ansiRegexp.ReplaceAllString(s, "")
}

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
