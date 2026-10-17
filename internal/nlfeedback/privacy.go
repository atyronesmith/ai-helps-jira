package nlfeedback

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CheckPrivatePath permits output outside a Git worktree. Within a worktree,
// the target must be ignored by Git and absent from the index.
func CheckPrivatePath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("feedback path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve feedback path: %w", err)
	}
	resolved, err := resolveOutputPath(abs)
	if err != nil {
		return fmt.Errorf("resolve feedback target: %w", err)
	}
	root, ok := findWorktreeRoot(resolved)
	if !ok {
		return nil
	}

	gitDir, err := gitOutput(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("check private feedback path: git metadata lookup failed: %w", err)
	}
	gitDir = strings.TrimSpace(gitDir)
	if gitDir == "" {
		return errors.New("check private feedback path: git metadata lookup returned an empty path")
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(root, gitDir)
	}
	gitDir, err = filepath.Abs(gitDir)
	if err != nil {
		return fmt.Errorf("resolve git metadata path: %w", err)
	}
	if resolvedGitDir, err := filepath.EvalSymlinks(gitDir); err == nil {
		gitDir = resolvedGitDir
	} else {
		return fmt.Errorf("resolve git metadata path: %w", err)
	}
	if within(gitDir, resolved) {
		return errors.New("feedback path is inside Git metadata")
	}

	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("feedback path could not be mapped inside its Git worktree")
	}
	pathspec := ":(literal)" + filepath.ToSlash(rel)

	// Query tracked state first. A tracked file is unsafe even if an ignore rule
	// was added after it entered the index.
	cmd := privatePathGitCommand(root, "ls-files", "--error-unmatch", "--", pathspec)
	_, err = cmd.CombinedOutput()
	if err == nil {
		return errors.New("feedback path is tracked by Git")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return fmt.Errorf("check private feedback path: git tracked-file check failed: %w", err)
	}

	cmd = privatePathGitCommand(root, "check-ignore", "-q", "--no-index", "--", filepath.ToSlash(rel))
	if err := cmd.Run(); err == nil {
		return nil
	} else if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return errors.New("feedback path is not ignored by Git")
	} else {
		return fmt.Errorf("check private feedback path: git ignore check failed: %w", err)
	}
}

func resolveOutputPath(path string) (string, error) {
	current := filepath.Clean(path)
	var suffix []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func findWorktreeRoot(path string) (string, bool) {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		marker := filepath.Join(current, ".git")
		if _, err := os.Stat(marker); err == nil {
			return current, true
		}
		if filepath.Dir(current) == current {
			return "", false
		}
	}
}

func gitOutput(root string, args ...string) (string, error) {
	out, err := privatePathGitCommand(root, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func privatePathGitCommand(root string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	// A caller's GIT_DIR, alternate index, or injected config must not redirect
	// the privacy check to a different repository from the target worktree.
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	return cmd
}

func within(parent, target string) bool {
	rel, err := filepath.Rel(parent, target)
	return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)))
}
