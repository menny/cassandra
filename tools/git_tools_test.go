package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/menny/cassandra/llm"
	"github.com/stretchr/testify/require"
)

// callTool invokes a registered tool with JSON-marshaled arguments.
func callTool(ctx context.Context, t *testing.T, r *Registry, name string, args any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	return r.HandleCall(ctx, llm.ToolCall{ID: "tc_" + name, Name: name, Arguments: string(raw)})
}

func commitFile(t *testing.T, dir, name string, content []byte, msg string) {
	t.Helper()
	full := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, content, 0o644))
	runGitCmd(t, dir, "add", name)
	runGitCmd(t, dir, "commit", "-m", msg)
}

// newGitToolRepo builds on setupGitRepo (branches main and feature, currently on main) and adds a
// three-line file with one commit per line plus a binary file.
func newGitToolRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	setupGitRepo(t, dir)
	commitFile(t, dir, "lines.txt", []byte("line one\n"), "add line one")
	commitFile(t, dir, "lines.txt", []byte("line one\nline two\n"), "add line two")
	commitFile(t, dir, "lines.txt", []byte("line one\nline two\nline three\n"), "add line three")
	commitFile(t, dir, "blob.bin", []byte{0x00, 0x01, 0x02, 0x03}, "add binary")
	return dir
}

// newShallowClone clones src with --depth 1 (file:// is required for git to honor depth).
func newShallowClone(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "clone")
	runGitCmd(t, "", "clone", "--depth", "1", "file://"+src, dst)
	return dst
}

func TestGitCommonHelpers(t *testing.T) {
	t.Run("validateGitRev", func(t *testing.T) {
		for _, ok := range []string{"main", "HEAD", "HEAD~3", "origin/feature-x", "v1.2.3", "a1b2c3d", "main..HEAD", "main...HEAD", "@{u}", "v1^{commit}"} {
			require.NoError(t, validateGitRev("ref", ok), ok)
		}
		for _, bad := range []string{"", "-n", "--output=/tmp/x", "--upload-pack=evil", "a b", "a\nb", "a:b", "a;b", "a$(b)", "a?b", "a#b", "a*b", "a\\b", strings.Repeat("a", 201)} {
			require.Error(t, validateGitRev("ref", bad), bad)
		}
	})

	t.Run("truncateOutput keeps small output untouched", func(t *testing.T) {
		require.Equal(t, "hello", truncateOutput("hello"))
	})

	t.Run("truncateOutput caps size and cuts on a line boundary", func(t *testing.T) {
		in := strings.Repeat("0123456789\n", 10000)
		out := truncateOutput(in)
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
		require.True(t, strings.HasSuffix(out, truncationNotice))
		require.True(t, strings.HasSuffix(strings.TrimSuffix(out, truncationNotice), "0123456789"))
	})

	t.Run("truncateOutput never splits a UTF-8 sequence", func(t *testing.T) {
		out := truncateOutput(strings.Repeat("é", maxToolOutputBytes))
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
		body := strings.TrimSuffix(out, truncationNotice)
		for _, r := range body {
			require.Equal(t, 'é', r)
		}
	})

	t.Run("windowLines", func(t *testing.T) {
		require.Equal(t, "b\nc", windowLines("a\nb\nc\nd", 2, 3))
		require.Equal(t, "c\nd", windowLines("a\nb\nc\nd", 3, 0))
		require.Equal(t, "a\nb", windowLines("a\nb\nc\nd", 0, 2))
		require.Equal(t, "", windowLines("a\nb", 5, 6))
		require.Equal(t, "a\nb", windowLines("a\nb", 1, 100))
	})

	t.Run("relPathInRoot", func(t *testing.T) {
		dir := t.TempDir()
		rel, err := relPathInRoot(dir, "sub/file.go")
		require.NoError(t, err)
		require.Equal(t, "sub/file.go", rel)

		rel, err = relPathInRoot(dir, ":(top)x")
		require.NoError(t, err)
		require.Equal(t, "./:(top)x", rel, "pathspec magic must be neutralized")

		for _, bad := range []string{"", "  ", "../etc/passwd", "a/../../etc/passwd", "/etc/passwd"} {
			_, err = relPathInRoot(dir, bad)
			require.Error(t, err, bad)
		}
	})
}

func TestGitLogTool(t *testing.T) {
	ctx := context.Background()
	dir := newGitToolRepo(t)
	r := NewRegistry()
	registerGitLog(r, dir)

	t.Run("defaults to HEAD", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_log", map[string]any{})
		require.NoError(t, err)
		require.Contains(t, out, "add binary")
		require.Contains(t, out, "initial commit")
		require.Contains(t, out, "Test User")
		require.NotContains(t, out, "WARNING", "full clones must not get a shallow warning")
	})

	t.Run("ref and path filter", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_log", map[string]any{"ref": "feature", "path": "feature.txt"})
		require.NoError(t, err)
		require.Contains(t, out, "feature commit")
		require.NotContains(t, out, "initial commit")
	})

	t.Run("revision range", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_log", map[string]any{"ref": "feature..main"})
		require.NoError(t, err)
		require.Contains(t, out, "main new commit")
		require.NotContains(t, out, "feature commit")
	})

	t.Run("max_count", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_log", map[string]any{"max_count": 2})
		require.NoError(t, err)
		require.Len(t, strings.Split(strings.TrimSpace(out), "\n"), 2)
	})

	t.Run("max_count is capped", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_log", map[string]any{"max_count": 100000})
		require.NoError(t, err)
		require.NotEmpty(t, out)
	})

	t.Run("author filter without matches", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_log", map[string]any{"author": "nobody-at-all"})
		require.NoError(t, err)
		require.Equal(t, "No commits found.", out)
	})

	t.Run("follow a renamed file", func(t *testing.T) {
		repo := newGitToolRepo(t)
		runGitCmd(t, repo, "mv", "lines.txt", "renamed.txt")
		runGitCmd(t, repo, "commit", "-m", "rename lines")
		rr := NewRegistry()
		registerGitLog(rr, repo)

		without, err := callTool(ctx, t, rr, "git_log", map[string]any{"path": "renamed.txt"})
		require.NoError(t, err)
		require.NotContains(t, without, "add line one")

		with, err := callTool(ctx, t, rr, "git_log", map[string]any{"path": "renamed.txt", "follow": true})
		require.NoError(t, err)
		require.Contains(t, with, "add line one")
	})

	t.Run("follow requires path", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_log", map[string]any{"follow": true})
		require.ErrorContains(t, err, "requires 'path'")
	})

	t.Run("rejects option-like and malformed refs", func(t *testing.T) {
		for _, ref := range []string{"--output=" + filepath.Join(dir, "pwned"), "-n1", "a b"} {
			_, err := callTool(ctx, t, r, "git_log", map[string]any{"ref": ref})
			require.ErrorContains(t, err, "invalid ref", ref)
		}
		_, statErr := os.Stat(filepath.Join(dir, "pwned"))
		require.True(t, os.IsNotExist(statErr))
	})

	t.Run("rejects multi-line author and since", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_log", map[string]any{"author": "a\nb"})
		require.ErrorContains(t, err, "invalid author")
		_, err = callTool(ctx, t, r, "git_log", map[string]any{"since": "x\r\ny"})
		require.ErrorContains(t, err, "invalid since")
	})

	t.Run("unknown revision surfaces git output", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_log", map[string]any{"ref": "does-not-exist"})
		require.ErrorContains(t, err, "git_log failed")
		require.ErrorContains(t, err, "does-not-exist")
	})

	t.Run("path traversal is rejected", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_log", map[string]any{"path": "../etc/passwd"})
		require.ErrorContains(t, err, "invalid path")
	})

	t.Run("symlink escaping the root is rejected", func(t *testing.T) {
		outside := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644))
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "escape")))
		require.NoError(t, os.Symlink(filepath.Join(outside, "missing"), filepath.Join(dir, "broken")))

		for _, p := range []string{"escape/secret.txt", "escape", "broken"} {
			_, err := callTool(ctx, t, r, "git_log", map[string]any{"path": p})
			require.ErrorContains(t, err, "invalid path", p)
		}
	})

	t.Run("malformed arguments", func(t *testing.T) {
		_, err := r.HandleCall(ctx, llm.ToolCall{Name: "git_log", Arguments: `{"max_count": "many"}`})
		require.ErrorContains(t, err, "unmarshaling git_log arguments")
	})

	t.Run("cancelled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "git_log", map[string]any{})
		require.Error(t, err)
	})

	t.Run("shallow clone warning", func(t *testing.T) {
		clone := newShallowClone(t, dir)
		rr := NewRegistry()
		registerGitLog(rr, clone)
		out, err := callTool(ctx, t, rr, "git_log", map[string]any{})
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(out, "WARNING: this repository is a shallow clone"), out)
		require.Contains(t, out, "add binary")
	})
}

func TestGitBlameTool(t *testing.T) {
	ctx := context.Background()
	dir := newGitToolRepo(t)
	r := NewRegistry()
	registerGitBlame(r, dir)

	t.Run("blames a range", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_blame", map[string]any{"path": "lines.txt", "start_line": 1, "end_line": 3})
		require.NoError(t, err)
		require.Contains(t, out, "Test User")
		require.Contains(t, out, "line one")
		require.Contains(t, out, "line three")
		require.Len(t, strings.Split(strings.TrimSpace(out), "\n"), 3)
	})

	t.Run("single line defaults end_line", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_blame", map[string]any{"path": "lines.txt", "start_line": 2})
		require.NoError(t, err)
		require.Contains(t, out, "line two")
		require.NotContains(t, out, "line three")
	})

	t.Run("blame at a ref", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_blame", map[string]any{"path": "feature.txt", "start_line": 1, "ref": "feature"})
		require.NoError(t, err)
		require.Contains(t, out, "feature content")
	})

	t.Run("invalid ranges", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_blame", map[string]any{"path": "lines.txt"})
		require.ErrorContains(t, err, "start_line is required")
		_, err = callTool(ctx, t, r, "git_blame", map[string]any{"path": "lines.txt", "start_line": 3, "end_line": 2})
		require.ErrorContains(t, err, "end_line must not be smaller")
		_, err = callTool(ctx, t, r, "git_blame", map[string]any{"path": "lines.txt", "start_line": 1, "end_line": maxGitBlameLines + 5})
		require.ErrorContains(t, err, "line range too large")
		_, err = callTool(ctx, t, r, "git_blame", map[string]any{"path": "lines.txt", "start_line": 99})
		require.ErrorContains(t, err, "git_blame failed")
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_blame", map[string]any{"path": "nope.txt", "start_line": 1})
		require.ErrorContains(t, err, "git_blame failed")
	})

	t.Run("path traversal and symlinks are rejected", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_blame", map[string]any{"path": "../../etc/passwd", "start_line": 1})
		require.ErrorContains(t, err, "invalid path")

		outside := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "f.txt"), []byte("x\n"), 0o644))
		require.NoError(t, os.Symlink(filepath.Join(outside, "f.txt"), filepath.Join(dir, "link.txt")))
		_, err = callTool(ctx, t, r, "git_blame", map[string]any{"path": "link.txt", "start_line": 1})
		require.ErrorContains(t, err, "invalid path")
	})

	t.Run("rejects option-like ref", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_blame", map[string]any{"path": "lines.txt", "start_line": 1, "ref": "--contents=/etc/passwd"})
		require.ErrorContains(t, err, "invalid ref")
	})

	t.Run("cancelled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "git_blame", map[string]any{"path": "lines.txt", "start_line": 1})
		require.Error(t, err)
	})

	t.Run("shallow clone warning", func(t *testing.T) {
		clone := newShallowClone(t, dir)
		rr := NewRegistry()
		registerGitBlame(rr, clone)
		out, err := callTool(ctx, t, rr, "git_blame", map[string]any{"path": "lines.txt", "start_line": 1, "end_line": 3})
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(out, "WARNING: this repository is a shallow clone"), out)
		require.Contains(t, out, "line three")
	})
}

func TestGitShowTool(t *testing.T) {
	ctx := context.Background()
	dir := newGitToolRepo(t)
	r := NewRegistry()
	registerGitShow(r, dir, []string{"go.sum"})

	t.Run("shows a commit with stat and patch", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "feature"})
		require.NoError(t, err)
		require.Contains(t, out, "feature commit")
		require.Contains(t, out, "feature.txt")
		require.Contains(t, out, "+feature content")
	})

	t.Run("limits the patch to a path", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD~1", "path": "lines.txt"})
		require.NoError(t, err)
		require.Contains(t, out, "+line three")
		out, err = callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD", "path": "lines.txt"})
		require.NoError(t, err)
		require.NotContains(t, out, "blob.bin")
	})

	t.Run("omits ignored lock files", func(t *testing.T) {
		repo := newGitToolRepo(t)
		runGitCmd(t, repo, "checkout", "-q", "main")
		require.NoError(t, os.WriteFile(filepath.Join(repo, "go.sum"), []byte("sum-contents\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(repo, "code.go"), []byte("package x\n"), 0o644))
		runGitCmd(t, repo, "add", "go.sum", "code.go")
		runGitCmd(t, repo, "commit", "-m", "touch lock and code")
		rr := NewRegistry()
		registerGitShow(rr, repo, []string{"go.sum"})
		out, err := callTool(ctx, t, rr, "git_show", map[string]any{"ref": "HEAD"})
		require.NoError(t, err)
		require.Contains(t, out, "code.go")
		require.NotContains(t, out, "sum-contents")
	})

	t.Run("file contents at a ref", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "feature", "path": "feature.txt", "contents": true})
		require.NoError(t, err)
		require.Equal(t, "feature content", out)

		_, err = callTool(ctx, t, r, "git_show", map[string]any{"ref": "main~10", "path": "feature.txt", "contents": true})
		require.ErrorContains(t, err, "git_show failed")
		_, err = callTool(ctx, t, r, "git_show", map[string]any{"ref": "main", "path": "feature.txt", "contents": true})
		require.ErrorContains(t, err, "git_show failed", "feature.txt does not exist on main")
	})

	t.Run("contents with a line window", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD", "path": "lines.txt", "contents": true, "line_start": 2, "line_end": 2})
		require.NoError(t, err)
		require.Equal(t, "line two", out)
	})

	t.Run("binary contents are not dumped", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD", "path": "blob.bin", "contents": true})
		require.NoError(t, err)
		require.Contains(t, out, "binary file")
	})

	t.Run("large contents are truncated", func(t *testing.T) {
		repo := newGitToolRepo(t)
		var big bytes.Buffer
		for i := 0; i < 20000; i++ {
			fmt.Fprintf(&big, "line %d of a very large file\n", i)
		}
		commitFile(t, repo, "big.txt", big.Bytes(), "add big file")
		rr := NewRegistry()
		registerGitShow(rr, repo, nil)
		out, err := callTool(ctx, t, rr, "git_show", map[string]any{"ref": "HEAD", "path": "big.txt", "contents": true})
		require.NoError(t, err)
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
		require.True(t, strings.HasSuffix(out, truncationNotice))
		require.Equal(t, 1, strings.Count(out, truncationNotice))
	})

	t.Run("large patches are truncated", func(t *testing.T) {
		repo := newGitToolRepo(t)
		var big bytes.Buffer
		for i := 0; i < 20000; i++ {
			fmt.Fprintf(&big, "line %d of a very large file\n", i)
		}
		commitFile(t, repo, "big.txt", big.Bytes(), "add big file")
		rr := NewRegistry()
		registerGitShow(rr, repo, nil)
		out, err := callTool(ctx, t, rr, "git_show", map[string]any{"ref": "HEAD"})
		require.NoError(t, err)
		require.LessOrEqual(t, len(out), maxToolOutputBytes)
		require.True(t, strings.HasSuffix(out, truncationNotice))
	})

	t.Run("argument validation", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_show", map[string]any{})
		require.ErrorContains(t, err, "ref is required")
		_, err = callTool(ctx, t, r, "git_show", map[string]any{"ref": "--output=" + filepath.Join(dir, "pwned")})
		require.ErrorContains(t, err, "invalid ref")
		_, statErr := os.Stat(filepath.Join(dir, "pwned"))
		require.True(t, os.IsNotExist(statErr))
		_, err = callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD", "contents": true})
		require.ErrorContains(t, err, "'path' is required")
		_, err = callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD:secret"})
		require.ErrorContains(t, err, "invalid ref")
	})

	t.Run("path traversal and symlinks are rejected", func(t *testing.T) {
		for _, contents := range []bool{false, true} {
			_, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD", "path": "../etc/passwd", "contents": contents})
			require.ErrorContains(t, err, "invalid path")
		}
		outside := t.TempDir()
		require.NoError(t, os.Symlink(outside, filepath.Join(dir, "escape")))
		_, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD", "path": "escape/x", "contents": true})
		require.ErrorContains(t, err, "invalid path")
	})

	t.Run("pathspec magic in path cannot widen the query", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD~1", "path": ":(top)lines.txt"})
		require.NoError(t, err)
		require.NotContains(t, out, "+line three")
	})

	t.Run("cancelled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		for _, contents := range []bool{false, true} {
			_, err := callTool(cctx, t, r, "git_show", map[string]any{"ref": "HEAD", "path": "lines.txt", "contents": contents})
			require.Error(t, err)
		}
	})

	t.Run("missing revision in a shallow clone hints at the shallow history", func(t *testing.T) {
		clone := newShallowClone(t, dir)
		rr := NewRegistry()
		registerGitShow(rr, clone, nil)
		_, err := callTool(ctx, t, rr, "git_show", map[string]any{"ref": "HEAD~5"})
		require.ErrorContains(t, err, "shallow clone")
	})
}

func TestGitDiffStatTool(t *testing.T) {
	ctx := context.Background()
	dir := newGitToolRepo(t)
	r := NewRegistry()
	registerGitDiffStat(r, dir, []string{"go.sum"})

	t.Run("compares against the merge base", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_diff_stat", map[string]any{"base": "main", "head": "feature"})
		require.NoError(t, err)
		require.Contains(t, out, "feature.txt")
		require.NotContains(t, out, "main_new.txt")
		require.Contains(t, out, "1 file changed")
	})

	t.Run("head defaults to HEAD", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_diff_stat", map[string]any{"base": "feature"})
		require.NoError(t, err)
		require.Contains(t, out, "main_new.txt")
		require.Contains(t, out, "lines.txt")
	})

	t.Run("no differences", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_diff_stat", map[string]any{"base": "main", "head": "main"})
		require.NoError(t, err)
		require.Equal(t, "No differences found.", out)
	})

	t.Run("path filter", func(t *testing.T) {
		out, err := callTool(ctx, t, r, "git_diff_stat", map[string]any{"base": "feature", "path": "lines.txt"})
		require.NoError(t, err)
		require.Contains(t, out, "lines.txt")
		require.NotContains(t, out, "main_new.txt")
	})

	t.Run("ignored lock files are excluded", func(t *testing.T) {
		repo := newGitToolRepo(t)
		commitFile(t, repo, "go.sum", []byte("sum\n"), "touch go.sum")
		commitFile(t, repo, "code.go", []byte("package x\n"), "add code")
		rr := NewRegistry()
		registerGitDiffStat(rr, repo, []string{"go.sum"})
		out, err := callTool(ctx, t, rr, "git_diff_stat", map[string]any{"base": "feature"})
		require.NoError(t, err)
		require.Contains(t, out, "code.go")
		require.NotContains(t, out, "go.sum")
	})

	t.Run("argument validation", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_diff_stat", map[string]any{})
		require.ErrorContains(t, err, "base is required")
		_, err = callTool(ctx, t, r, "git_diff_stat", map[string]any{"base": "--output=" + filepath.Join(dir, "pwned")})
		require.ErrorContains(t, err, "invalid base")
		_, statErr := os.Stat(filepath.Join(dir, "pwned"))
		require.True(t, os.IsNotExist(statErr))
		_, err = callTool(ctx, t, r, "git_diff_stat", map[string]any{"base": "main", "head": "-p"})
		require.ErrorContains(t, err, "invalid head")
		_, err = callTool(ctx, t, r, "git_diff_stat", map[string]any{"base": "a..b"})
		require.ErrorContains(t, err, "not ranges")
		_, err = callTool(ctx, t, r, "git_diff_stat", map[string]any{"base": "nope"})
		require.ErrorContains(t, err, "git_diff_stat failed")
	})

	t.Run("path traversal is rejected", func(t *testing.T) {
		_, err := callTool(ctx, t, r, "git_diff_stat", map[string]any{"base": "main", "path": "../x"})
		require.ErrorContains(t, err, "invalid path")
	})

	t.Run("cancelled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "git_diff_stat", map[string]any{"base": "main", "head": "feature"})
		require.Error(t, err)
	})
}
