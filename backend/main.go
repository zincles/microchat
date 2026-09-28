// microchat 后端（Go 版）—— 正在从 Rust 版迁移过来。
//
// 迁移纪律（写在这里，免得忘）：
//  1. **表结构照搬** `src/store.rs` 的 MIGRATIONS（一字不改，见 internal/store/migrations.go）；
//  2. **接口照搬** AGENTS.md 那张表 —— `python3 scripts/api-audit.py` 全绿 = 接口层次转对了；
//  3. **行为照搬** 171 条测试 —— `go test ./...` 全绿 = 行为层次转对了；
//  4. Rust 版先原地留着当参照，Go 版跑绿了再谈删。
package main

import (
	"flag"
	"log"
	"net/http"

	"microchat/backend/internal/server"
	"microchat/backend/internal/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "监听地址")
	dataDir := flag.String("data", "data", "数据目录（含 microchat.db）")
	flag.Parse()

	st, err := store.Open(*dataDir + "/microchat.db")
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	version, _ := st.UserVersion()
	log.Printf("microchat 后端(Go) 起在 http://%s（db: %s/microchat.db，user_version=%d）",
		*addr, *dataDir, version)
	if err := http.ListenAndServe(*addr, server.New(st).Handler()); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
