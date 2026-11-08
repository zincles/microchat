#!/bin/sh
# microchat 容器的启动脚本：**先后端、后前端**，默认现场编译；
# 源码哈希没变时复用上次的 Go 二进制 / 前端 dist（2026-11-09）。
# 两个进程都往 stdout/stderr 打日志（非 TTY ⇒ 后端日志不落文件，`docker logs` 直接看）。
#
# 为什么先后端：vite 的代理目标读的是后端自己的配置文件（data/config/config.json），
# 首次启动那个文件还不存在 —— 等后端把文件与端口都就绪，前端再去读，才不会读个默认值。
set -eu
cd /app

# 旧镜像兼容（2026-11-09）：Dockerfile 已经装 git，但**重启不会重建镜像** ——
# 早于这条改动做出来的 Alpine 镜像里没有 git，更新按钮会直接失败。
# entrypoint 脚本是 bind 进来的，所以先在这里现场补一次；装不上不阻塞启动
#（服务照跑，更新页会报错让它去看日志）。
if ! command -v git >/dev/null 2>&1; then
  echo "[entrypoint] 镜像里没有 git，尝试安装（旧镜像兼容路径）"
  # 不吞输出：装不上时要能看见是网络、apk 源还是只读层的问题。
  apk add --no-cache git || echo "[entrypoint] git 安装失败：更新功能暂时不可用（服务本身照常启动）"
fi

# 数据/配置目录：与后端同一套环境变量（默认 /app/data —— 随 bind 持久化在宿主机仓库里）。
DATA_DIR=${MICROCHAT_DATA_DIR:-/app/data}
CONFIG_DIR=${MICROCHAT_CONFIG_DIR:-$DATA_DIR/config}
# 后端「更新」按钮写这个标记；entrypoint 看见就原地重编译、重启两个进程。
# 不用重启整个容器：端口发布、卷、PID 名字空间都保持不变。
RESTART_FILE=${MICROCHAT_RESTART_FILE:-$DATA_DIR/.restart-request}
rm -f "$RESTART_FILE"

# ── 构建缓存（2026-11-09）──────────────────────────────────────────────────
# 容器重启/更新 re-exec 时，源码经常一点没变；别每次都把 Go 和 Vite 重新跑一遍。
# 两份缓存分开：只改后端就只编后端，只改前端就只 build 前端；test/ 与 dist/ 不进哈希。
# 缓存住 /build（不是 bind）；同一容器 restart / update re-exec 都还在，up -d 重建就自然失效。
mkdir -p /build
GO_HASH_FILE=/build/.microchat-go-hash
WEB_HASH_FILE=/build/.microchat-web-hash
go_source_hash() {
  (find core -type f -print | sort | xargs sha256sum) | sha256sum | cut -d' ' -f1
}
web_source_hash() {
  (find frontend/web \
    \( -type d \( -name node_modules -o -name dist -o -name .vite -o -name test \) -prune \) -o \
    -type f -print | sort | xargs sha256sum) | sha256sum | cut -d' ' -f1
}
GO_HASH=$(go_source_hash)
WEB_HASH=$(web_source_hash)
CACHED_GO_HASH=$(cat "$GO_HASH_FILE" 2>/dev/null || true)
CACHED_WEB_HASH=$(cat "$WEB_HASH_FILE" 2>/dev/null || true)

# pid 锁不用管：容器有**自己的 PID 名字空间** —— 锁文件里要么是宿主的 pid、要么是上一个容器的 pid，
# 在这个名字空间里都**不存在**（kill 探活 = ESRCH ⇒ 当成陈旧锁覆盖 ✓）。后端又是本容器的第一个子进程
# （npm/vite 都还没起），陈旧 pid 撞上无辜进程的可能只有 pid 1（= 本脚本），而锁从不由 pid 1 写 ⇒ 不会误杀。
# （所以别在这里 rm 锁文件：那会把宿主实例的锁误删，下次宿主起新实例就少了"先停旧的"这道闸。）

# ── 后端 ────────────────────────────────────────────────────────────────────
# 编译到 /tmp（不往 bind 进来的仓库里丢二进制）。⚠ **编译用子 shell，别 cd 走**：
# 后端的默认数据目录 = CWD/data —— 曾经 cd 进 core 再起 ⇒ 数据落进 /app/core/data（仓库里凭空多一份影子库 ✗，
# 实测咬过：needs_setup 说"全新库"，说的是那个影子）。二进制始终从 /app 起 ⇒ 数据在 /app/data ✓。
# `-buildvcs=false`：容器里没有 git / 仓库属主不对时，Go 默认会尝试做 VCS
# stamping，git 一失败整条 build 就 128（用户 2026-11-09 实踩）；我们不需要把
# commit 焊进二进制（版本由更新面板现读 git），关掉它。
if [ "$GO_HASH" = "$CACHED_GO_HASH" ] && [ -x /tmp/microchat-server ]; then
  echo "[entrypoint] 后端源码未变，复用已有二进制"
else
  echo "[entrypoint] 后端源码有变化，重新编译"
  rm -f "$GO_HASH_FILE"
  (cd /app/core && CGO_ENABLED=0 go build -buildvcs=false -o /tmp/microchat-server .)
  printf '%s\n' "$GO_HASH" > "$GO_HASH_FILE"
fi

# 端口：从配置读（改过 server.port 也等得对）；文件还没生成（首次启动）⇒ 8787 缺省。
# ⚠ 必须**起后端之前**读出来 —— 下面要用它拼 `-addr`。
PORT=$(grep -o '"port"[[:space:]]*:[[:space:]]*[0-9]\+' "$CONFIG_DIR/config.json" 2>/dev/null | head -1 | grep -o '[0-9]\+$' || true)
PORT=${PORT:-8787}

# **容器里绑 `0.0.0.0`**（2026-11-02 修）：后端默认绑 `127.0.0.1`（那是宿主机的"唯一闸门"），
# 但在容器里绑回环 ⇒ 宿主的端口发布**转发到容器的 eth0，而那里没人听**
# ⇒ `127.0.0.1:8787:8787` 那条发布能连上却**永远没有应答**（实测过；注释里"给本机 dev/curl 用"是假的）。
#
# 为什么这样不放松安全性：容器的 `0.0.0.0` 只在本容器的网络名字空间里可见，
# 外界够不够得着**完全由 compose 的端口绑定决定**（那儿两条都是 `127.0.0.1:` 开头）。
# 换句话说：闸门没有从端口绑定挪走，只是让"容器内"这一层别再自己挡自己。
/tmp/microchat-server -addr "0.0.0.0:$PORT" &
BACK=$!
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
# 依赖卷跨容器持久；用 package.json + package-lock.json 的哈希当安装标记，
# lock/依赖变了或安装不完整就自动 npm ci（2026-11-08 VPS 缺 vite-plugin-vuetify 实踩）。
mkdir -p /build/web
(cd /app/frontend/web && tar cf - --exclude=node_modules --exclude=dist .) | (cd /build/web && tar xf -)
cd /build/web
# 依赖卷是**跨容器持久**的：只判断 `node_modules/vite` 在不在会漏掉新加的插件
# （2026-11-08 VPS 实踩：vite 在、vite-plugin-vuetify 不在 ⇒ 每次 build 都崩）。
# 用「package.json + package-lock.json 的哈希」当安装标记：lock/依赖变了或安装不完整就重装。
LOCK_HASH=$(sha256sum package.json package-lock.json | sha256sum | cut -d' ' -f1)
if [ ! -d node_modules/vite ] || [ "$(cat node_modules/.microchat-lock 2>/dev/null || true)" != "$LOCK_HASH" ]; then
  echo "[entrypoint] 安装前端依赖（node_modules 与 lock 不一致或未完成）"
  npm ci
  printf '%s\n' "$LOCK_HASH" > node_modules/.microchat-lock
fi
if [ "$WEB_HASH" = "$CACHED_WEB_HASH" ] && [ -f /build/web/dist/index.html ]; then
  echo "[entrypoint] 前端源码未变，复用已有 dist"
else
  echo "[entrypoint] 前端源码有变化，重新构建"
  rm -f "$WEB_HASH_FILE"
  npm run build
  printf '%s\n' "$WEB_HASH" > "$WEB_HASH_FILE"
fi
npm run preview &
FRONT=$!
echo "[entrypoint] 前端起来了：http://0.0.0.0:8788（/api 由 vite 代理给本容器后端）"

# ── 看家 ────────────────────────────────────────────────────────────────────
# 收掉一棵进程树：npm 会再起 node（vite），只 kill npm 会留下占着 8788 的孤儿。
stop_tree() {
  pid=$1
  [ -n "$pid" ] || return 0
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    stop_tree "$child"
  done
  kill "$pid" 2>/dev/null || true
}
stop_tree_force() {
  pid=$1
  [ -n "$pid" ] || return 0
  for child in $(pgrep -P "$pid" 2>/dev/null || true); do
    stop_tree_force "$child"
  done
  kill -KILL "$pid" 2>/dev/null || true
}

# 任一方退出 ⇒ 容器跟着退（restart: unless-stopped 会整个重来）。
trap 'stop_tree "$BACK"; stop_tree "$FRONT"; exit 0' TERM INT
while kill -0 "$BACK" 2>/dev/null && kill -0 "$FRONT" 2>/dev/null; do
  # 更新按钮的标记：收掉旧进程，exec 自己 ⇒ 重新读源码 / 依赖锁 / 重编译。
  if [ -f "$RESTART_FILE" ]; then
    echo "[entrypoint] 收到更新重启请求，重新编译前后端"
    rm -f "$RESTART_FILE"
    stop_tree "$BACK"
    stop_tree "$FRONT"
    # npm 会吞掉 TERM（实测：只发 SIGTERM 时 wait "$FRONT" 永远不回，
    # 重启就卡死在这一行）⇒ 一秒后补 SIGKILL；Go 后端也被一并保证收干净。
    sleep 1
    stop_tree_force "$BACK"
    stop_tree_force "$FRONT"
    wait "$BACK" 2>/dev/null || true
    wait "$FRONT" 2>/dev/null || true
    exec sh /app/docker/entrypoint.sh
  fi
  sleep 2
done
echo "[entrypoint] 有进程退出了，容器跟着退（restart 策略接管）"
exit 1
