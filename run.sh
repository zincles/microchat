#!/bin/sh
# 起 microchat：**不加任何参数**，直接就是终端 TUI + 对外 Web 服务。
# 就是 go 自带的那条命令 —— 没有别的构建系统。
#
# 这两行环境变量是必须的：`go -C core` 会把**进程的工作目录也切到 core/**，
# 而 data/ 与 data/config/ 是相对工作目录解析的 ⇒ 不指回去就会去 core/ 下找。
cd "$(dirname "$0")" || exit 1
MICROCHAT_DATA_DIR="$PWD/data" \
MICROCHAT_CONFIG_DIR="$PWD/data/config" \
exec go -C core run .
