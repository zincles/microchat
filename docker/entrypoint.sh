#!/bin/sh
# microchat 容器的启动脚本：**先后端、后前端**，两个都在"现场编译"。
# 两个进程都往 stdout/stderr 打日志（非 TTY ⇒ 后端日志不落文件，`docker logs` 直接看）。
#
# 为什么先后端：vite 的代理目标读的是后端自己的配置文件（data/config/config.json），
# 首次启动那个文件还不存在 —— 等后端把文件与端口都就绪，前端再去读，才不会读个默认值。
set -eu
cd /app

# 数据/配置目录：与后端同一套环境变量（默认 /app/data —— 随 bind 持久化在宿主机仓库里）。
DATA_DIR=${MICROCHAT_DATA_DIR:-/app/data}
CONFIG_DIR=${MICROCHAT_CONFIG_DIR:-$DATA_DIR/config}

# pid 锁不用管：容器有**自己的 PID 名字空间** —— 锁文件里要么是宿主的 pid、要么是上一个容器的 pid，
# 在这个名字空间里都**不存在**（kill 探活 = ESRCH ⇒ 当成陈旧锁覆盖 ✓）。后端又是本容器的第一个子进程
# （npm/vite 都还没起），陈旧 pid 撞上无辜进程的可能只有 pid 1（= 本脚本），而锁从不由 pid 1 写 ⇒ 不会误杀。
# （所以别在这里 rm 锁文件：那会把宿主实例的锁误删，下次宿主起新实例就少了"先停旧的"这道闸。）

# ── 后端 ────────────────────────────────────────────────────────────────────
# 编译到 /tmp（不往 bind 进来的仓库里丢二进制）。⚠ **编译用子 shell，别 cd 走**：
# 后端的默认数据目录 = CWD/data —— 曾经 cd 进 core 再起 ⇒ 数据落进 /app/core/data（仓库里凭空多一份影子库 ✗，
# 实测咬过：needs_setup 说"全新库"，说的是那个影子）。二进制始终从 /app 起 ⇒ 数据在 /app/data ✓。
(cd /app/core && CGO_ENABLED=0 go build -o /tmp/microchat-server .)
/tmp/microchat-server &
BACK=$!

# 等它真的在听：端口从配置读（改过 server.port 也等得对）；文件还没生成（首次启动）⇒ 8787 缺省。
PORT=$(grep -o '"port"[[:space:]]*:[[:space:]]*[0-9]\+' "$CONFIG_DIR/config.json" 2>/dev/null | head -1 | grep -o '[0-9]\+$' || true)
PORT=${PORT:-8787}
i=0
until nc -z 127.0.0.1 "$PORT" 2>/dev/null; do
  i=$((i+1))
  [ "$i" -le 300 ] || { echo "[entrypoint] 后端 300s 没起来，放弃"; exit 1; }
  kill -0 "$BACK" 2>/dev/null || { echo "[entrypoint] 后端进程没了（看上面的日志）"; exit 1; }
  sleep 1
done
echo "[entrypoint] 后端就绪：127.0.0.1:$PORT（不对外发布，只有本容器里的前端够得着）"

# ── 前端 ────────────────────────────────────────────────────────────────────
# 源码**拷到 /build** 再装/构建，为什么不就地跑：
#   ① bind 底下挂不住命名卷（podman-compose 把 bind 排在卷之后、整个盖住嵌套卷，实测踩过）；
#   ② 就算挂得住也不该跟宿主共用 node_modules —— 宿主多半是 glibc、容器是 musl，
#      rollup 的原生包不通用（实测：直接 `MODULE_NOT_FOUND` 崩在 vite build）。
# 拷到 bind 之外后：依赖卷（/build/web/node_modules）没人盖得住 ✓ 两边彻底隔离 ✓ 宿主 dev 不受影响 ✓。
# 依赖只装一次（**改了 package.json 的话**：删掉那个卷——`microchat_microchat-node-modules`——再 up）。
mkdir -p /build/web
(cd /app/frontend/web && tar cf - --exclude=node_modules --exclude=dist .) | (cd /build/web && tar xf -)
cd /build/web
[ -d node_modules/vite ] || npm ci
npm run build
npm run preview &
FRONT=$!
echo "[entrypoint] 前端起来了：http://0.0.0.0:8788（/api 由 vite 代理给本容器后端）"

# ── 看家 ────────────────────────────────────────────────────────────────────
# 任一方退出 ⇒ 容器跟着退（restart: unless-stopped 会整个重来）。
trap 'kill "$BACK" "$FRONT" 2>/dev/null || true; exit 0' TERM INT
while kill -0 "$BACK" 2>/dev/null && kill -0 "$FRONT" 2>/dev/null; do
  sleep 2
done
echo "[entrypoint] 有进程退出了，容器跟着退（restart 策略接管）"
exit 1
