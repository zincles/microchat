package update

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Rebase 是用户明确点的“分叉时变基”：把本地未推送提交重放到远端最新提交上。
// 用临时 bare remote + 两个 clone 做真实 git 验证（不碰仓库本体）。
func TestRebaseReplaysLocalCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	local := filepath.Join(root, "local")
	other := filepath.Join(root, "other")

	runGit(t, "", "init", "--bare", remote)
	runGit(t, "", "init", "-b", "main", local)
	t.Chdir(local)
	gitConfig(t, local)
	writeFile(t, "base.txt", "base\n")
	runGit(t, local, "add", ".")
	runGit(t, local, "commit", "-m", "base")
	runGit(t, local, "remote", "add", "origin", remote)
	runGit(t, local, "push", "-u", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	runGit(t, "", "clone", remote, other)
	gitConfig(t, other)
	writeFile(t, filepath.Join(other, "remote.txt"), "remote\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "remote C")
	runGit(t, other, "push", "origin", "main")

	writeFile(t, "local.txt", "local\n")
	runGit(t, local, "add", ".")
	runGit(t, local, "commit", "-m", "local B")
	before := runGit(t, local, "rev-parse", "HEAD")

	result, err := Rebase(ctx)
	if err != nil {
		t.Fatalf("Rebase 失败：%v\n%s", err, result.Output)
	}
	after := runGit(t, local, "rev-parse", "HEAD")
	if before == after || !result.Changed {
		t.Fatalf("变基后提交没变：before=%s after=%s changed=%v", before, after, result.Changed)
	}
	log := runGit(t, local, "log", "--format=%s", "-3")
	for _, want := range []string{"remote C", "local B"} {
		if !strings.Contains(log, want) {
			t.Fatalf("变基历史里缺少 %q：\n%s", want, log)
		}
	}
	if _, err := os.Stat(filepath.Join(local, ".git", "rebase-merge")); err == nil {
		t.Fatal("变基成功后还留着 .git/rebase-merge")
	}
}

// 冲突时必须自动 abort，不能把用户扔在半个 rebase 里。
func TestRebaseConflictAbortsToOriginalCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("没有 git")
	}
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	local := filepath.Join(root, "local")
	other := filepath.Join(root, "other")

	runGit(t, "", "init", "--bare", remote)
	runGit(t, "", "init", "-b", "main", local)
	t.Chdir(local)
	gitConfig(t, local)
	writeFile(t, "same.txt", "base\n")
	runGit(t, local, "add", ".")
	runGit(t, local, "commit", "-m", "base")
	runGit(t, local, "remote", "add", "origin", remote)
	runGit(t, local, "push", "-u", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")

	runGit(t, "", "clone", remote, other)
	gitConfig(t, other)
	writeFile(t, filepath.Join(other, "same.txt"), "remote\n")
	runGit(t, other, "add", ".")
	runGit(t, other, "commit", "-m", "remote change")
	runGit(t, other, "push", "origin", "main")

	writeFile(t, "same.txt", "local\n")
	runGit(t, local, "add", ".")
	runGit(t, local, "commit", "-m", "local change")
	before := runGit(t, local, "rev-parse", "HEAD")

	result, err := Rebase(ctx)
	if err == nil {
		t.Fatalf("冲突变基该失败，却成功了：%+v", result)
	}
	after := runGit(t, local, "rev-parse", "HEAD")
	if before != after {
		t.Fatalf("冲突后没有回到原提交：before=%s after=%s\n%s", before, after, result.Output)
	}
	if _, statErr := os.Stat(filepath.Join(local, ".git", "rebase-merge")); statErr == nil {
		t.Fatal("冲突后还留着 .git/rebase-merge")
	}
	if _, statErr := os.Stat(filepath.Join(local, ".git", "rebase-apply")); statErr == nil {
		t.Fatal("冲突后还留着 .git/rebase-apply")
	}
}

func gitConfig(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "config", "user.name", "microchat test")
	runGit(t, dir, "config", "user.email", "test@example.invalid")
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=microchat test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=microchat test", "GIT_COMMITTER_EMAIL=test@example.invalid",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s 失败：%v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
