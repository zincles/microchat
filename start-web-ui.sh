#!/bin/sh
# 起 Web 前端：只起 vite（:5173），不碰后端。
# 后端另起（./run.sh 或 ./microchat）；单实例锁：同 data/ 只许一个后端，再起会杀掉正在跑的那个。
cd "$(dirname "$0")" || exit 1
if [ ! -d frontend/web/node_modules ]; then
  echo "装依赖（首次一次）：frontend/web/npm ci"
  (cd frontend/web && npm ci) || exit 1
fi
exec npm run dev --prefix frontend/web -- --host 127.0.0.1
