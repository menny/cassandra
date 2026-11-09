package tools

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func fileContentJSON(name, content string) string {
	return fmt.Sprintf(`{"type":"file","encoding":"base64","size":%d,"name":%q,"path":%q,"content":%q}`,
		len(content), name, name, base64.StdEncoding.EncodeToString([]byte(content)))
}

func TestCleanRepoPath(t *testing.T) {
	for in, want := range map[string]string{
		"a/b.go": "a/b.go", "./a/b.go": "a/b.go", "/a": "a", ".": "", "/": "", "a//b": "a/b", "a/./b": "a/b", "dir/": "dir",
	} {
		got, err := cleanRepoPath(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "   ", "..", "../x", "a/../../b", "a/..", "a\\b", "a\x00b"} {
		_, err := cleanRepoPath(bad)
		require.Error(t, err, bad)
	}
}

func TestGetFileAtRefTool(t *testing.T) {
	ctx := context.Background()
	newTool := func(t *testing.T, f *fakeGitHub) *Registry {
		f.install()
		r := NewRegistry()
		registerGetFileAtRefTool(r, "", "")
		return r
	}

	t.Run("reads a file at a ref", func(t *testing.T) {
		f := newFakeGitHub(t)
		var gotRef string
		f.handle("GET /repos/o/r/contents/core/agent.go", func(req *http.Request) *http.Response {
			gotRef = req.URL.Query().Get("ref")
			return jsonResp(http.StatusOK, fileContentJSON("agent.go", "package core\n\nfunc A() {}\n"), nil)
		})
		r := newTool(t, f)

		out, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "core/agent.go", "ref": "release/1.0", "repo": "o/r"})
		require.NoError(t, err)
		require.Equal(t, "release/1.0", gotRef)
		require.Contains(t, out, "# o/r:core/agent.go @ release/1.0 (3 lines, 26 bytes)")
		require.Contains(t, out, "func A() {}")
	})

	t.Run("defaults to the default branch and normalizes the path", func(t *testing.T) {
		f := newFakeGitHub(t)
		gotRef := "unset"
		f.handle("GET /repos/o/r/contents/a/b.txt", func(req *http.Request) *http.Response {
			gotRef = req.URL.Query().Get("ref")
			return jsonResp(http.StatusOK, fileContentJSON("b.txt", "x"), nil)
		})
		r := newTool(t, f)
		out, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "./a/b.txt", "repo": "o/r"})
		require.NoError(t, err)
		require.Empty(t, gotRef)
		require.Contains(t, out, "@ default branch (1 lines, 1 bytes)")
	})

	t.Run("line window", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/contents/f.txt", http.StatusOK, fileContentJSON("f.txt", "one\ntwo\nthree\nfour\n"))
		r := newTool(t, f)
		out, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "f.txt", "repo": "o/r", "line_start": 2, "line_end": 3})
		require.NoError(t, err)
		require.Contains(t, out, "(4 lines, 19 bytes)\ntwo\nthree")
		require.NotContains(t, out, "four")
		require.NotContains(t, out, "\none\n")
	})

	t.Run("lists directories", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/contents/core", http.StatusOK,
			`[{"type":"file","name":"agent.go","size":120},{"type":"dir","name":"prompts"},{"type":"symlink","name":"link"}]`)
		r := newTool(t, f)
		out, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "core", "ref": "main", "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "# o/r:core @ main (directory, 3 entries)")
		require.Contains(t, out, "agent.go (120 bytes)")
		require.Contains(t, out, "prompts/")
		require.Contains(t, out, "link (symlink)")
	})

	t.Run("lists the repository root", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/contents/", http.StatusOK, `[{"type":"file","name":"README.md","size":5}]`)
		r := newTool(t, f)
		out, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": ".", "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "o/r:/ @ default branch (directory, 1 entries)")
		require.Contains(t, out, "README.md")
	})

	t.Run("very large directories are capped", func(t *testing.T) {
		var items []string
		for i := 0; i < maxDirectoryEntries+50; i++ {
			items = append(items, fmt.Sprintf(`{"type":"file","name":"f%04d.txt","size":1}`, i))
		}
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/contents/big", http.StatusOK, "["+strings.Join(items, ",")+"]")
		r := newTool(t, f)
		out, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "big", "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "50 more entries not shown")
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
	})

	t.Run("binary, oversized, symlink and submodule entries", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/contents/bin.dat", http.StatusOK, fileContentJSON("bin.dat", "ab\x00cd"))
		f.json("GET /repos/o/r/contents/huge.bin", http.StatusOK, `{"type":"file","encoding":"none","size":2000000,"name":"huge.bin","content":""}`)
		f.json("GET /repos/o/r/contents/link", http.StatusOK, `{"type":"symlink","target":"../target.txt","name":"link"}`)
		f.json("GET /repos/o/r/contents/sub", http.StatusOK, `{"type":"submodule","submodule_git_url":"https://github.com/x/y.git","name":"sub"}`)
		r := newTool(t, f)

		out, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "bin.dat", "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "binary file")
		require.NotContains(t, out, "ab")

		_, err = callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "huge.bin", "repo": "o/r"})
		require.ErrorContains(t, err, "too large for the GitHub contents API")

		out, err = callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "link", "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "symlink to ../target.txt")

		out, err = callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "sub", "repo": "o/r"})
		require.NoError(t, err)
		require.Contains(t, out, "submodule: https://github.com/x/y.git")
	})

	t.Run("large files are truncated", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/contents/big.txt", http.StatusOK, fileContentJSON("big.txt", strings.Repeat("a line of text\n", 20000)))
		r := newTool(t, f)
		out, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "big.txt", "repo": "o/r"})
		require.NoError(t, err)
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
		require.True(t, strings.HasSuffix(out, truncationNotice))
		require.Contains(t, out, "(20000 lines")
	})

	t.Run("not found and forbidden are explained", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/contents/private.txt", http.StatusForbidden, `{"message":"no"}`)
		r := newTool(t, f)
		_, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "missing.txt", "ref": "main", "repo": "o/r"})
		require.ErrorContains(t, err, "not found")
		require.ErrorContains(t, err, "o/r:missing.txt at main")
		_, err = callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "private.txt", "repo": "o/r"})
		require.ErrorContains(t, err, "forbidden")
	})

	t.Run("argument validation", func(t *testing.T) {
		f := newFakeGitHub(t)
		r := newTool(t, f)
		for _, p := range []string{"", "../etc/passwd", "a/../../b", "a\\b"} {
			_, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": p, "repo": "o/r"})
			require.Error(t, err, p)
		}
		for _, ref := range []string{"--x", "a b", "a:b", "a?x=1", "a#b", "a/../../x", "a//b"} {
			_, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "a.go", "ref": ref, "repo": "o/r"})
			require.ErrorContains(t, err, "invalid ref", ref)
		}
		_, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "a.go", "repo": "bad"})
		require.ErrorContains(t, err, "owner/repo")
		_, err = r.HandleCall(ctx, llmToolCall("get_file_at_ref", `{"path": 5}`))
		require.ErrorContains(t, err, "unmarshaling get_file_at_ref arguments")
		require.Empty(t, f.requests)
	})

	t.Run("missing token", func(t *testing.T) {
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("GH_TOKEN", "")
		r := NewRegistry()
		registerGetFileAtRefTool(r, "", "")
		_, err := callTool(ctx, t, r, "get_file_at_ref", map[string]any{"path": "a.go", "repo": "o/r"})
		require.ErrorContains(t, err, "GITHUB_TOKEN")
	})

	t.Run("cancelled context", func(t *testing.T) {
		f := newFakeGitHub(t)
		f.json("GET /repos/o/r/contents/a.go", http.StatusOK, fileContentJSON("a.go", "x"))
		r := newTool(t, f)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "get_file_at_ref", map[string]any{"path": "a.go", "repo": "o/r"})
		require.Error(t, err)
	})
}
