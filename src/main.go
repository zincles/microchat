// microchat 后端（Go 版）—— 主体代码。老的 Rust 版在 deprecated/ 里当参照，别再加功能。
//
// 迁移纪律（写在这里，免得忘）：
//  1. **表结构照搬** `deprecated/src/store.rs` 的 MIGRATIONS（一字不改，见 internal/store/migrations.go）；
//  2. **接口照搬** AGENTS.md 那张表 —— `python3 scripts/api-audit.py` 全绿 = 接口层次转对了；
//  3. **行为照搬** 171 条测试 —— `go test ./...` 全绿 = 行为层次转对了；
//  4. **只许对账**：`python3 scripts/go-parity.py` 把两版的响应逐字节比 —— 说法不同就是没转对；
//  5. Rust 版先原地留着当参照，Go 版跑绿了再谈删。
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"microchat/internal/config"
	"microchat/internal/server"
	"microchat/internal/store"
)

func main() {
	// 与 Rust 版同名：目录可用环境变量覆盖（MICROCHAT_CONFIG_DIR / MICROCHAT_DATA_DIR）
	paths := config.LoadPaths()
	flag.StringVar(&paths.ConfigDir, "config", paths.ConfigDir, "配置目录（含 agents.json 等）")
	flag.StringVar(&paths.DataDir, "data", paths.DataDir, "数据目录（含 microchat.db）")
	addr := flag.String("addr", "", "监听地址（留空 = 用 config.json 的 server.host:port）")
	flag.Parse()

	cfg, err := config.LoadConfig(paths)
	if err != nil {
		log.Fatalf("读配置失败: %v", err)
	}
	if *addr == "" {
		*addr = fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	}

	st, err := store.Open(paths.DataDir + "/microchat.db")
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	version, _ := st.UserVersion()
	log.Printf("microchat 后端(Go) 起在 http://%s（数据 %s，配置 %s，user_version=%d）",
		*addr, paths.DataDir, paths.ConfigDir, version)
	if err := http.ListenAndServe(*addr, server.New(st, cfg, paths).Handler()); err != nil {
		log.Fatalf("服务退出: %v", err)
	}
}
