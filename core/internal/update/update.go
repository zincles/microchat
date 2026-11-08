// Package update：仓库版本查询 / 拉取 + 给 entrypoint 的“重启前后端”信号。
//
// 它只做两件事：
//   - 用 git 读「当前提交」并执行 `git pull --ff-only`；
//   - 往 data 目录写一个标记文件，容器 entrypoint 看见后**原地**重新编译并重启两个进程。
//
// 为什么是标记文件而不是直接杀 PID 1：容器里 PID 1 是 entrypoint，杀它要靠
// docker/podman 的 restart policy；而 entrypoint 已经掌握两个子进程的 PID，
// 让它自己收尾再 exec 自己，重启范围更小、也不依赖容器的重启策略。
package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// processStartedAt：本进程的启动时间。前端重启后用它判断“回来的已经是新进程”
// —— 只看 /health 会被还没被 entrypoint 收掉的旧进程骗过去。
var processStartedAt = time.Now().UTC().Format(time.RFC3339Nano)

// Status 是 GET /update 的响应。
type Status struct {
	Commit       string `json:"commit"`
	Short        string `json:"short"`
	Branch       string `json:"branch"`
	Subject      string `json:"subject"`
	Dirty        bool   `json:"dirty"`
	ChangedFiles int    `json:"changed_files"`
	StartedAt    string `json:"started_at"`
	// RestartSupported = 当前部署有 entrypoint 看家循环（容器）。宿主二进制只拉取，
	// 没法替人重启 —— 前端据此不要假装“马上回来”。
	RestartSupported bool `json:"restart_supported"`
}

// PullResult 是 POST /update/pull 的响应。
type PullResult struct {
	Before     Status `json:"before"`
	After      Status `json:"after"`
	Output     string `json:"output"`
	Changed    bool   `json:"changed"`
	Restarting bool   `json:"restarting"`
}

// RepoRoot：容器里的工作目录就是 /app（compose 的 working_dir），宿主上
// `./run.sh` 也从仓库根启动。需要时可用环境变量显式覆盖（测试 / 非标准布局）。
func RepoRoot() string {
	if root := strings.TrimSpace(os.Getenv("MICROCHAT_REPO_ROOT")); root != "" {
		return root
	}
	wd, err := os.Getwd()
	if err != nil {
		return "/app"
	}
	return wd
}

// RestartFile：entrypoint 监听的标记文件。默认住 data 目录（与库、配置一起
// 持久化，但不入库）；容器里可显式给环境变量。
func RestartFile(dataDir string) string {
	if v := strings.TrimSpace(os.Getenv("MICROCHAT_RESTART_FILE")); v != "" {
		return v
	}
	return filepath.Join(dataDir, ".restart-request")
}

// Status：读 git 的当前状态。仓库不是 git 仓库 / 没装 git 时原样报错，
// 前端显示“读不到版本”，而不是假装自己是最新。
func ReadStatus(ctx context.Context) (Status, error) {
	root := RepoRoot()
	out := Status{StartedAt: processStartedAt, RestartSupported: RestartSupported()}
	var err error
	if out.Commit, err = git(ctx, root, "rev-parse", "HEAD"); err != nil {
		return out, err
	}
	// short/hash 是同一颗提交；short 用 git 自己的缩写（尊重 core.abbrev）。
	out.Short, _ = git(ctx, root, "rev-parse", "--short", "HEAD")
	out.Branch, _ = git(ctx, root, "branch", "--show-current")
	out.Subject, _ = git(ctx, root, "log", "-1", "--pretty=%s")
	if porcelain, statusErr := git(ctx, root, "status", "--porcelain"); statusErr == nil {
		out.Dirty = strings.TrimSpace(porcelain) != ""
		if out.Dirty {
			out.ChangedFiles = len(strings.Split(strings.TrimSpace(porcelain), "\n"))
		}
	}
	return out, nil
}

// Pull：只走 `git pull --ff-only`（绝不自动 merge、绝不 stash）。
// 工作区脏 / 本地提交与远端分叉时让 git 自己拒绝，把原文交给前端看。
func Pull(ctx context.Context) (PullResult, error) {
	root := RepoRoot()
	before, err := ReadStatus(ctx)
	if err != nil {
		return PullResult{Before: before}, err
	}
	out, pullErr := git(ctx, root, "pull", "--ff-only")
	after, afterErr := ReadStatus(ctx)
	result := PullResult{
		Before:  before,
		After:   after,
		Output:  out,
		Changed: before.Commit != "" && after.Commit != "" && before.Commit != after.Commit,
	}
	if pullErr != nil {
		return result, pullErr
	}
	if afterErr != nil {
		return result, fmt.Errorf("拉取成功，但读不到新的提交：%w", afterErr)
	}
	return result, nil
}

// RequestRestart：写标记文件。entrypoint 的看家循环会在下一拍看见它、
// 收掉两个子进程，再 exec 自己（重新编译后端与前端）。
func RestartSupported() bool {
	return strings.TrimSpace(os.Getenv("MICROCHAT_RESTART_FILE")) != ""
}

func RequestRestart(dataDir string) error {
	file := RestartFile(dataDir)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o644)
}

// git：所有 git 调用只从这里走 —— 统一 safe.directory（容器里 /app 是 bind 进来的，
// 属主通常是宿主用户；root 直接跑 git 会被 “dubious ownership” 拦住）、
// 统一关掉交互式凭据提示（没有 TTY，等密码只会挂住）。
func git(ctx context.Context, root string, args ...string) (string, error) {
	full := append([]string{"-c", "safe.directory=*", "-C", root}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("%w: %s", err, text)
		}
		return text, err
	}
	return text, nil
}
