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

type gitPathEscapeFixtures struct {
	traversalPaths       []string
	validDirSymlink      string
	validDirFileSymlink  string
	validFileSymlink     string
	brokenOutsideSymlink string
	brokenInsideSymlink  string
	trampolineSymlink    string
}

func setupGitPathEscapeFixtures(t *testing.T, dir string) gitPathEscapeFixtures {
	t.Helper()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(outsideFile, []byte("SENSITIVE DATA\n"), 0o644))

	// 1. Valid symlink pointing outside workspace root (both directory and file symlinks)
	evilDirSymlink := filepath.Join(dir, "evil_symlink")
	require.NoError(t, os.Symlink(outside, evilDirSymlink))
	evilFileSymlink := filepath.Join(dir, "evil_file.txt")
	require.NoError(t, os.Symlink(outsideFile, evilFileSymlink))

	// 2. Broken symlink pointing to a non-existent target outside/inside the root
	brokenOutside := filepath.Join(dir, "broken_outside")
	require.NoError(t, os.Symlink(filepath.Join(outside, "nonexistent"), brokenOutside))
	brokenInside := filepath.Join(dir, "broken_inside")
	require.NoError(t, os.Symlink(filepath.Join(dir, "nonexistent"), brokenInside))

	// 3. Circular / trampoline symlink (e.g. a symlink pointing to itself)
	trampoline := filepath.Join(dir, "trampoline")
	require.NoError(t, os.Symlink(trampoline, trampoline))

	return gitPathEscapeFixtures{
		traversalPaths:       []string{"../etc/passwd", "../../secret.txt", "a/../../etc/passwd"},
		validDirSymlink:      "evil_symlink",
		validDirFileSymlink:  "evil_symlink/secret.txt",
		validFileSymlink:     "evil_file.txt",
		brokenOutsideSymlink: "broken_outside",
		brokenInsideSymlink:  "broken_inside",
		trampolineSymlink:    "trampoline",
	}
}

func runPathEscapeSubtests(t *testing.T, fixtures gitPathEscapeFixtures, allowsDirectory bool, testFn func(t *testing.T, path string)) {
	t.Helper()

	t.Run("path traversal outside root should error", func(t *testing.T) {
		for _, p := range fixtures.traversalPaths {
			testFn(t, p)
		}
	})

	t.Run("valid symlink pointing outside should be blocked", func(t *testing.T) {
		targets := []string{fixtures.validDirFileSymlink, fixtures.validFileSymlink}
		if allowsDirectory {
			targets = append(targets, fixtures.validDirSymlink)
		}
		for _, p := range targets {
			testFn(t, p)
		}
	})

	t.Run("broken symlink should be blocked", func(t *testing.T) {
		for _, p := range []string{fixtures.brokenOutsideSymlink, fixtures.brokenInsideSymlink} {
			testFn(t, p)
		}
	})

	t.Run("trampoline symlink should be blocked", func(t *testing.T) {
		testFn(t, fixtures.trampolineSymlink)
	})
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

		for _, bad := range []string{"", "  ", "/etc/passwd"} {
			_, err = relPathInRoot(dir, bad)
			require.Error(t, err, bad)
		}

		fixtures := setupGitPathEscapeFixtures(t, dir)
		runPathEscapeSubtests(t, fixtures, true, func(t *testing.T, path string) {
			t.Helper()
			_, err := relPathInRoot(dir, path)
			require.ErrorContains(t, err, "invalid path", path)
		})
	})

	t.Run("relFilePathInRoot", func(t *testing.T) {
		ctx := context.Background()
		dir := newGitToolRepo(t)
		require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o755))
		commitFile(t, dir, "subdir/nested.txt", []byte("nested\n"), "add nested")

		rel, err := relFilePathInRoot(ctx, dir, "subdir/nested.txt", "HEAD")
		require.NoError(t, err)
		require.Equal(t, "subdir/nested.txt", rel)

		// Directory on disk / in git should be rejected
		for _, dirPath := range []string{".", "subdir"} {
			_, err = relFilePathInRoot(ctx, dir, dirPath, "HEAD")
			require.ErrorContains(t, err, "path is a directory, not a file")
		}

		fixtures := setupGitPathEscapeFixtures(t, dir)
		runPathEscapeSubtests(t, fixtures, false, func(t *testing.T, path string) {
			t.Helper()
			_, err := relFilePathInRoot(ctx, dir, path, "HEAD")
			require.ErrorContains(t, err, "invalid path", path)
		})
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

	t.Run("follow rejects directory", func(t *testing.T) {
		repo := newGitToolRepo(t)
		require.NoError(t, os.Mkdir(filepath.Join(repo, "sub"), 0o755))
		commitFile(t, repo, "sub/file.txt", []byte("x\n"), "add file")
		rr := NewRegistry()
		registerGitLog(rr, repo)

		_, err := callTool(ctx, t, rr, "git_log", map[string]any{"path": "sub", "follow": true})
		require.ErrorContains(t, err, "path is a directory, not a file")

		// Directory without follow succeeds
		out, err := callTool(ctx, t, rr, "git_log", map[string]any{"path": "sub"})
		require.NoError(t, err)
		require.Contains(t, out, "add file")
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

	fixtures := setupGitPathEscapeFixtures(t, dir)
	runPathEscapeSubtests(t, fixtures, true, func(t *testing.T, path string) {
		t.Helper()
		_, err := callTool(ctx, t, r, "git_log", map[string]any{"path": path})
		require.ErrorContains(t, err, "invalid path", path)
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

	t.Run("rejects directory path", func(t *testing.T) {
		commitFile(t, dir, "blame_sub/file.txt", []byte("blame\n"), "add blame sub")
		_, err := callTool(ctx, t, r, "git_blame", map[string]any{"path": "blame_sub", "start_line": 1})
		require.ErrorContains(t, err, "path is a directory, not a file")
	})

	fixtures := setupGitPathEscapeFixtures(t, dir)
	runPathEscapeSubtests(t, fixtures, false, func(t *testing.T, path string) {
		t.Helper()
		_, err := callTool(ctx, t, r, "git_blame", map[string]any{"path": path, "start_line": 1})
		require.ErrorContains(t, err, "invalid path", path)
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

	t.Run("contents on directory is rejected", func(t *testing.T) {
		commitFile(t, dir, "show_sub/file.txt", []byte("show\n"), "add show sub")
		_, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD", "path": "show_sub", "contents": true})
		require.ErrorContains(t, err, "path is a directory, not a file")

		// Patch on directory with contents: false succeeds
		out, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD", "path": "show_sub", "contents": false})
		require.NoError(t, err)
		require.Contains(t, out, "show_sub/file.txt")
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

	fixtures := setupGitPathEscapeFixtures(t, dir)
	for _, contents := range []bool{false, true} {
		t.Run(fmt.Sprintf("contents=%v", contents), func(t *testing.T) {
			runPathEscapeSubtests(t, fixtures, !contents, func(t *testing.T, path string) {
				t.Helper()
				_, err := callTool(ctx, t, r, "git_show", map[string]any{"ref": "HEAD", "path": path, "contents": contents})
				require.ErrorContains(t, err, "invalid path", path)
			})
		})
	}

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

	fixtures := setupGitPathEscapeFixtures(t, dir)
	runPathEscapeSubtests(t, fixtures, true, func(t *testing.T, path string) {
		t.Helper()
		_, err := callTool(ctx, t, r, "git_diff_stat", map[string]any{"base": "main", "path": path})
		require.ErrorContains(t, err, "invalid path", path)
	})

	t.Run("cancelled context", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := callTool(cctx, t, r, "git_diff_stat", map[string]any{"base": "main", "head": "feature"})
		require.Error(t, err)
	})
}
