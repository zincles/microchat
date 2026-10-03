#!/bin/sh
# 起 Godot 前端（编辑器模式）：与后端同机调试用。
# 后端另起（./run.sh 或 ./microchat）；Godot 读的是同一套 HTTP 契约（默认 127.0.0.1:8787）。
cd "$(dirname "$0")" || exit 1
exec godot -e ./frontend/godotui/
