#!/bin/sh
# 起 Web 前端：vite dev（:5173）+ 后端（:8787，同 run.sh 那一套）。
# 用法：./start-web-ui.sh（Ctrl+C 一起停；后端日志落 data/microchat.log）。
cd "$(dirname "$0")" || exit 1
if [ ! -d frontend/web/node_modules ]; then
  echo "装依赖（首次一次）：frontend/web/npm ci"
  (cd frontend/web && npm ci) || exit 1
fi
MICROCHAT_DATA_DIR="$PWD/data" \
MICROCHAT_CONFIG_DIR="$PWD/data/config" \
go -C core run . > data/microchat.log 2>&1 &
BACKEND_PID=$!
trap 'kill $BACKEND_PID 2>/dev/null' INT TERM EXIT
(cd frontend/web && npm run dev -- --host 127.0.0.1) &
wait
