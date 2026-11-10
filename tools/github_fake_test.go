package tools

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/google/go-github/v69/github"
	"github.com/menny/cassandra/llm"
)

// fakeGitHub is a tiny in-memory GitHub API. Handlers are registered per "METHOD /path"; any other
// request gets a 404. It honors request cancellation like a real transport.
type fakeGitHub struct {
	t        *testing.T
	mu       sync.Mutex
	handlers map[string]func(req *http.Request) *http.Response
	requests []string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	return &fakeGitHub{t: t, handlers: map[string]func(req *http.Request) *http.Response{}}
}

func (f *fakeGitHub) handle(route string, fn func(req *http.Request) *http.Response) {
	f.handlers[route] = fn
}

// json registers a route that always answers with status and body.
func (f *fakeGitHub) json(route string, status int, body string) {
	f.handle(route, func(*http.Request) *http.Response { return jsonResp(status, body, nil) })
}

func (f *fakeGitHub) roundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	route := req.Method + " " + req.URL.Path
	f.mu.Lock()
	f.requests = append(f.requests, route+"?"+req.URL.RawQuery)
	fn, ok := f.handlers[route]
	f.mu.Unlock()
	if !ok {
		return jsonResp(http.StatusNotFound, `{"message":"Not Found"}`, nil), nil
	}
	return fn(req), nil
}

func (f *fakeGitHub) client() *github.Client {
	return newMockGitHubClient(f.roundTrip)
}

// install makes the GitHub tools use this fake and provides a token.
func (f *fakeGitHub) install() {
	f.t.Helper()
	orig := gitHubClientFactory
	f.t.Cleanup(func() { gitHubClientFactory = orig })
	f.t.Setenv("GITHUB_TOKEN", "dummy-token")
	f.t.Setenv("GH_TOKEN", "")
	gitHubClientFactory = func(string) *github.Client { return f.client() }
}

func jsonResp(status int, body string, header map[string]string) *http.Response {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	for k, v := range header {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewBufferString(body))}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func llmToolCall(name, args string) llm.ToolCall {
	return llm.ToolCall{ID: "tc_" + name, Name: name, Arguments: args}
}
