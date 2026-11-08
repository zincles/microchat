package server

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"microchat/internal/update"
)

// 版本更新（2026-11-09 用户定）：左栏「更新」入口显示当前提交，
// 一键 git pull --ff-only + 让容器 entrypoint 重启前后端。
//
// 两个口：
//
//	GET  /api/v1/update       当前提交 / 分支 / 工作区是否脏
//	POST /api/v1/update/pull  拉取并请求重启（202；真正重启由 entrypoint 接手）
//
// 只有 POST 要求同源：这是一条能把代码换成另一份的写口。本机浏览器里
// 打开任意恶意网页也能向 127.0.0.1 发请求（CORS 全开），Origin 是这里唯一
// 能区分“我们的页面”和“别人借你的浏览器”的东西。curl 不带 Origin，照旧放行。
var updateMu sync.Mutex

func (s *Server) updateStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	status, err := update.ReadStatus(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "读取 git 状态失败："+err.Error())
		return
	}
	// 远端查询单独短超时：没网/私有仓库也不该让本地版本面板转 8 秒。
	remoteCtx, cancelRemote := context.WithTimeout(r.Context(), 6*time.Second)
	remote := update.ReadRemote(remoteCtx, status.Branch)
	cancelRemote()
	status.RemoteCommit = remote.Commit
	status.RemoteShort = remote.Short
	status.RemoteError = remote.Error
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) updatePull(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "invalid", "更新只允许从当前页面发起（Origin 不匹配）")
		return
	}
	// 拉取是慢活（网络），但不该无限挂住：90 秒足够一次正常 pull。
	if !update.RestartSupported() {
		writeError(w, http.StatusConflict, "conflict", "当前不是容器部署（未设置 MICROCHAT_RESTART_FILE），拉取后无法自动重启")
		return
	}
	updateMu.Lock()
	defer updateMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	result, err := update.Pull(ctx)
	if err != nil {
		// 把 git 原文一起回给前端（工作区脏、分叉、没网、没装 git 都要能看懂）。
		writeError(w, http.StatusInternalServerError, "internal", "拉取失败："+err.Error())
		return
	}
	result.Restarting = true
	writeJSON(w, http.StatusAccepted, result)
	// 先让响应出门，再写重启标记：entrypoint 看见标记就会收掉本进程，
	// 太早写会把这一发 202 剪断（前端会误以为失败）。
	go func() {
		time.Sleep(1200 * time.Millisecond)
		if err := update.RequestRestart(s.paths.DataDir); err != nil {
			log.Printf("写重启标记失败：%v", err)
		}
	}()
}

// sameOrigin：Origin 的 host 必须等于这次请求的 Host。
// 反代/隧道会改写 Host 时，用 X-Forwarded-Host 对齐；没有 Origin（curl / 同源旧客户端）放行。
func sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := r.Host
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	return strings.EqualFold(u.Host, host)
}
