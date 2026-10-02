package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// lockPath：测试用的锁文件路径（同一个 t.TempDir 当 data 目录）。
func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "microchat.pid")
}

func readPIDFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读锁文件: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// ① 无锁文件 ⇒ 直接拿到；文件里是当前 pid；release 后文件没了。
func TestAcquireInstanceLockNoLock(t *testing.T) {
	path := lockPath(t)

	release, err := acquireInstanceLock(filepath.Dir(path))
	if err != nil {
		t.Fatalf("空目录应能拿到锁: %v", err)
	}
	if got := readPIDFile(t, path); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("锁文件内容 = %q，想要自己的 pid %d", got, os.Getpid())
	}

	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("release 后锁文件应被删掉，stat err = %v", err)
	}
}

// ② 陈旧 pid（一个几乎不存在的进程）⇒ 直接拿到（覆盖锁），不用等也不用杀。
func TestAcquireInstanceLockStalePID(t *testing.T) {
	path := lockPath(t)
	stale := 999999
	if pidAlive(stale) {
		t.Fatalf("测试前提不成立：pid %d 居然是活的", stale)
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(stale)), 0o644); err != nil {
		t.Fatal(err)
	}

	release, err := acquireInstanceLock(filepath.Dir(path))
	if err != nil {
		t.Fatalf("陈旧锁应能直接接管: %v", err)
	}
	defer release()
	if got := readPIDFile(t, path); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("陈旧锁应被覆盖成自己，得到 %q", got)
	}
}

// ③ 活 pid（拿一个 sleep 子进程当"旧实例"）⇒ 拿锁时它收到 SIGTERM 并退出。
func TestAcquireInstanceLockKillsLiveInstance(t *testing.T) {
	path := lockPath(t)

	old := exec.Command("sleep", "60")
	if err := old.Start(); err != nil {
		t.Fatalf("起旧实例失败: %v", err)
	}
	// 必须有人 Wait：否则 SIGTERM 后子进程变僵尸，kill(pid,0) 仍说它"活着"。
	waited := make(chan error, 1)
	go func() { waited <- old.Wait() }()
	if err := os.WriteFile(path, []byte(strconv.Itoa(old.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	release, err := acquireInstanceLock(filepath.Dir(path))
	if err != nil {
		_ = old.Process.Kill()
		<-waited
		t.Fatalf("活实例应被 SIGTERM 掉后接管: %v", err)
	}
	defer release()

	select {
	case <-waited:
		// 被信号杀掉时 Wait 会返回 *exec.ExitError（"signal: terminated"）—— 这是预期，看状态。
		status, ok := old.ProcessState.Sys().(syscall.WaitStatus)
		if !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
			t.Fatalf("旧实例应死于 SIGTERM，实际 status = %v", old.ProcessState)
		}
	case <-time.After(2 * time.Second):
		_ = old.Process.Kill()
		t.Fatal("旧实例没在 2s 内退出")
	}

	if got := readPIDFile(t, path); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("接管后锁文件应是自己，得到 %q", got)
	}
}

// ④ release 只删自己的锁：锁文件被"别人"改了之后 release 不能删它。
func TestReleaseDoesNotDeleteOthersLock(t *testing.T) {
	path := lockPath(t)

	release, err := acquireInstanceLock(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	// 模拟"被别人抢了"：锁文件里现在是别的 pid。
	if err := os.WriteFile(path, []byte("4242"), 0o644); err != nil {
		t.Fatal(err)
	}

	release()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("release 不该删别人的锁，stat err = %v", err)
	}
	if got := readPIDFile(t, path); got != "4242" {
		t.Fatalf("别人的锁不该被动过，得到 %q", got)
	}
}

// 僵尸当死：起一个瞬间退出但没人收尸的子进程（父进程是正在跑的测试进程，
// 但测试进程不 wait 它 ⇒ 它留在僵尸态跑一小会儿）——这里用一个更直接的办法：
// 起一个 `sh -c 'exit 0'` 并**不 wait**，等它变 Z，然后确认 pidAlive 说"死"。
//
// 不用真僵尸时读不到 /proc 的情况：读不到就跳过（非 Linux）。
func TestZombieCountsAsDead(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Skipf("起不了子进程：%v", err)
	}
	defer cmd.Wait() // 测试收尾时收尸（这不影响"僵尸期间"的判定）
	pid := cmd.Process.Pid
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if zombie(pid) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !zombie(pid) {
		t.Skip("没观察到僵尸态（可能已被收尸）——跳过")
	}
	if pidAlive(pid) {
		t.Fatal("僵尸该判死（否则杀旧实例会白等 3 秒）")
	}
}
