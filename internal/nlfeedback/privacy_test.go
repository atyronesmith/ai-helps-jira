package nlfeedback

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCheckPrivatePath(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}

	t.Run("outside worktree", func(t *testing.T) {
		outside := t.TempDir()
		if err := CheckPrivatePath(filepath.Join(outside, "feedback.jsonl")); err != nil {
			t.Fatalf("outside path rejected: %v", err)
		}
	})

	t.Run("inside worktree", func(t *testing.T) {
		root := t.TempDir()
		gitRun(t, git, root, "init", "-q")

		ignore := filepath.Join(root, ".gitignore")
		if err := os.WriteFile(ignore, []byte("custom-private.data\nignored-tracked.data\nprivate-link.data\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		tracked := filepath.Join(root, "ignored-tracked.data")
		if err := os.WriteFile(tracked, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitRun(t, git, root, "add", ".gitignore")
		gitRun(t, git, root, "add", "-f", "ignored-tracked.data")

		if err := CheckPrivatePath(filepath.Join(root, "custom-private.data")); err != nil {
			t.Errorf("ignored custom filename rejected: %v", err)
		}
		if err := CheckPrivatePath(tracked); err == nil {
			t.Error("ignored but tracked file was allowed")
		}
		t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "unrelated-git-dir"))
		t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "unrelated-index"))
		if err := CheckPrivatePath(filepath.Join(root, "custom-private.data")); err != nil {
			t.Errorf("ambient Git override changed the target repository: %v", err)
		}
		if err := CheckPrivatePath(tracked); err == nil {
			t.Error("ambient Git override bypassed tracked-file protection")
		}
		if err := CheckPrivatePath(filepath.Join(root, "ordinary.jsonl")); err == nil {
			t.Error("unignored file was allowed")
		}
		privatePath := filepath.Join(root, "ordinary.jsonl")
		if err := Append(privatePath, validRecord("private attempt")); err == nil {
			t.Error("Append bypassed private path check")
		}
		if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
			t.Error("refused private output created a file")
		}
		if err := CheckPrivatePath(filepath.Join(root, ".git", "private.data")); err == nil {
			t.Error("Git metadata path was allowed")
		}

		link := filepath.Join(root, "private-link.data")
		if err := os.Symlink(tracked, link); err != nil {
			t.Fatal(err)
		}
		if err := CheckPrivatePath(link); err == nil {
			t.Error("ignored symlink targeting tracked file was allowed")
		}
	})
}

func gitRun(t *testing.T, git, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, out)
	}
}
