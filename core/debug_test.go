package main

// 直操模式里两处值得钉住的**机制**（不是它的输出 —— 输出靠 `./microchat -debug …` 真跑）：
// 参数解析（打错的 flag 不许被静默忽略）与跨进程锁（同一会话同时只许一个）。

import (
	"testing"

	"microchat/internal/config"
)

// 参数：两种写法都收；打错的名字 / 缺值 / 光秃秃的位置参数都**报错**。
func TestParseNamedFlags(t *testing.T) {
	values, err := parseNamedFlags([]string{"--title", "标题", "--provider=p", "--model", "m"},
		"title", "provider", "model")
	if err != nil {
		t.Fatal(err)
	}
	if values["title"] != "标题" || values["provider"] != "p" || values["model"] != "m" {
		t.Fatalf("解析结果 = %+v", values)
	}
	for _, args := range [][]string{
		{"--titel", "x"}, // 打错的名字（最容易静默变成"没写标题"）
		{"--title"},      // 缺值
		{"x"},            // 位置参数
	} {
		if _, err := parseNamedFlags(args, "title"); err == nil {
			t.Fatalf("%v 该报错", args)
		}
	}
}

// 跨进程锁：同一会话**同时只许一个** send；放锁之后能再来。
//
// 这条是踩出来的：早先用 `O_CREATE|O_EXCL` 建文件、之后再写 pid —— 中间有个
// "文件在、内容还没有"的窗口，后到的进程会读到空文件、当残留清掉 ⇒ 两个进程同时跑同一个会话。
// 现在锁文件的内容与它的出现是**同一件事**（临时文件 + `os.Link`），这个测试钉住"拿不到就是拿不到"。
func TestTurnLockExcludesAndReleases(t *testing.T) {
	paths := config.Paths{DataDir: t.TempDir()}

	first, busy, err := acquireTurnLock(paths, "s1")
	if err != nil || busy {
		t.Fatalf("第一次该拿到：busy=%v err=%v", busy, err)
	}
	second, busy, err := acquireTurnLock(paths, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !busy || second != nil {
		t.Fatalf("同一会话第二次该是忙：busy=%v lock=%v", busy, second)
	}
	// 别的会话互不影响（锁是按会话分的）
	other, busy, err := acquireTurnLock(paths, "s2")
	if err != nil || busy {
		t.Fatalf("别的会话该拿到：busy=%v err=%v", busy, err)
	}
	other.release()

	first.release()
	if _, live := readTurnLock(paths, "s1"); live {
		t.Fatal("放锁之后就不该还在")
	}
	again, busy, err := acquireTurnLock(paths, "s1")
	if err != nil || busy {
		t.Fatalf("放锁之后该能再来：busy=%v err=%v", busy, err)
	}
	again.release()
}

// 预览：按**字符**切（中文按字节切会切出残字）。
func TestPreviewCutsByRune(t *testing.T) {
	if got := preview("你好世界", 2); got != "你好…" {
		t.Fatalf("preview = %q", got)
	}
	if got := preview("abc", 10); got != "abc" {
		t.Fatalf("没超长就别加省略号：%q", got)
	}
}
