package client

import (
	"net/http"
	"time"

	"github.com/menny/cassandra/core/eval/testdata/cases/unupdated-caller-blast-radius/base/pkg/config"
)

// NewHTTPClient creates an HTTP client using the configured timeout string.
func NewHTTPClient(timeoutStr string) (*http.Client, error) {
	sec, err := config.ParseTimeout(timeoutStr)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: time.Duration(sec) * time.Second,
	}, nil
}
