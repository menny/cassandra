package eval

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/menny/cassandra/util"
)

// Sandbox provides a high-fidelity git environment for evaluating the agent.
type Sandbox struct {
	RootDir string
}

// NewSandbox initializes a new git-backed sandbox.
// It copies or extracts files from baseState (if non-empty), initializes git,
// commits the base state, applies the diff, and commits the final state.
// baseState can be a directory path or a .tar.gz file path.
func NewSandbox(ctx context.Context, baseState string, diff string) (*Sandbox, error) {
	tmp, err := os.MkdirTemp("", "cassandra-eval-sandbox-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	s := &Sandbox{RootDir: tmp}

	// 1. Setup base files
	if baseState != "" {
		info, err := os.Stat(baseState)
		if err != nil {
			s.Cleanup()
			return nil, fmt.Errorf("failed to stat base state %q: %w", baseState, err)
		}

		if info.IsDir() {
			if err := copyDir(baseState, tmp); err != nil {
				s.Cleanup()
				return nil, fmt.Errorf("failed to copy base directory: %w", err)
			}
		} else if strings.HasSuffix(baseState, ".tar.gz") {
			if err := extractTarGz(baseState, tmp); err != nil {
				s.Cleanup()
				return nil, fmt.Errorf("failed to extract base tarball: %w", err)
			}
		} else {
			s.Cleanup()
			return nil, fmt.Errorf("unsupported base state type (must be dir or .tar.gz): %s", baseState)
		}
	}

	// 2. Git init
	if err := s.runGit(ctx, "init", "--initial-branch=main"); err != nil {
		s.Cleanup()
		return nil, err
	}
	// Configure git identity for the sandbox
	if err := s.runGit(ctx, "config", "user.email", "eval@cassandra.ai"); err != nil {
		s.Cleanup()
		return nil, err
	}
	if err := s.runGit(ctx, "config", "user.name", "Cassandra Eval"); err != nil {
		s.Cleanup()
		return nil, err
	}

	// 3. Commit base state
	// Unconditionally add and commit with --allow-empty to avoid "nothing to commit"
	// errors when there are no base files or only the .git directory exists.
	if err := s.runGit(ctx, "add", "."); err != nil {
		s.Cleanup()
		return nil, err
	}
	if err := s.runGit(ctx, "commit", "--allow-empty", "-m", "base state"); err != nil {
		s.Cleanup()
		return nil, err
	}

	// 4. Apply diff
	if diff != "" {
		diffFile := filepath.Join(tmp, "input.diff")
		if err := os.WriteFile(diffFile, []byte(diff), 0o644); err != nil {
			s.Cleanup()
			return nil, fmt.Errorf("failed to write diff file: %w", err)
		}
		// Use git apply for higher fidelity
		if err := s.runGit(ctx, "apply", "input.diff"); err != nil {
			s.Cleanup()
			return nil, fmt.Errorf("failed to apply diff: %w", err)
		}
		if err := os.Remove(diffFile); err != nil {
			s.Cleanup()
			return nil, fmt.Errorf("failed to remove diff file: %w", err)
		}

		// 5. Commit diff state
		if err := s.runGit(ctx, "add", "."); err != nil {
			s.Cleanup()
			return nil, err
		}
		// Use --allow-empty in case the diff is functionally empty or ignored.
		if err := s.runGit(ctx, "commit", "--allow-empty", "-m", "applied diff"); err != nil {
			s.Cleanup()
			return nil, err
		}
	}

	return s, nil
}

// Cleanup removes the sandbox directory.
func (s *Sandbox) Cleanup() {
	if s.RootDir != "" {
		os.RemoveAll(s.RootDir)
	}
}

func (s *Sandbox) runGit(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.RootDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v failed in %s: %w\nOutput: %s", args, s.RootDir, err, string(out))
	}
	return nil
}

func copyDir(src string, dst string) error {
	return os.CopyFS(dst, os.DirFS(src))
}

func extractTarGz(gzipPath, dst string) error {
	f, err := os.Open(gzipPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dst, header.Name)

		// Security: Prevent "Tar Slip" (directory traversal and symlink escape)
		if _, err := util.ValidatePathInRoot(dst, header.Name); err != nil {
			return fmt.Errorf("tar slip detected: %w", err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			// Use O_TRUNC to ensure existing files are completely overwritten.
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Security: Validate that the symlink target is also within the root.
			// Use the physical directory to resolve relative targets to prevent trampoline escapes.
			physicalDir, err := util.ValidatePathInRoot(dst, filepath.Dir(header.Name))
			if err != nil {
				return fmt.Errorf("invalid symlink directory: %w", err)
			}

			linkTarget := header.Linkname
			if !filepath.IsAbs(linkTarget) {
				// Relative targets are relative to the symlink's physical location.
				linkTarget = filepath.Join(physicalDir, linkTarget)
			}

			// Validate the resolved target and capture the safe physical path.
			safeAbsTarget, err := util.ValidatePathInRoot(dst, linkTarget)
			if err != nil {
				return fmt.Errorf("malicious symlink target detected: %w", err)
			}

			safeLinkname := header.Linkname
			if !filepath.IsAbs(header.Linkname) {
				// Recompute the relative linkname from the physical directory
				// to neutralize any physical path traversal bypasses.
				safeLinkname, err = filepath.Rel(physicalDir, safeAbsTarget)
				if err != nil {
					return fmt.Errorf("failed to compute safe linkname: %w", err)
				}
			}

			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(safeLinkname, target); err != nil {
				return err
			}
		}
	}
	return nil
}
