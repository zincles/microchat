#!/bin/sh
# 起 microchat：**不加任何参数**，就是一台 web 服务（http://127.0.0.1:8787）。
#
# 2026-11-01：原来这条脚本会连 TUI 一起起（`exec go -C core run .` 在有终端时开 TUI）。
# 用户定了「砍掉CLI对话器，只保留服务web前端的功能」⇒ TUI 整个删掉，这条脚本就只剩起服务。
# 部署走 `podman compose up -d`（见 AGENTS.md 的「Docker 部署」一节）；这条是开发机直接跑。
#
# 这两行环境变量是必须的：`go -C core` 会把**进程的工作目录也切到 core/**，
# 而 data/ 与 data/config/ 是相对工作目录解析的 ⇒ 不指回去就会去 core/ 下找。
cd "$(dirname "$0")" || exit 1
MICROCHAT_DATA_DIR="$PWD/data" \
MICROCHAT_CONFIG_DIR="$PWD/data/config" \
exec go -C core run .
