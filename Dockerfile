# microchat 单容器镜像：Go 与 Node 工具链都在（**现场编译、现场跑**，见 docker/entrypoint.sh）。
# 部署成本 = git clone + docker compose up -d（见 docker-compose.yml）。
#
# 一切版本钉死（用户定）：两个基础镜像按 **digest** 锁死 —— 想升级 = 显式改这一行。
#   golang 1.26.9：≥ go.mod 要求的 1.26.0（工具链不用现拉，GOTOOLCHAIN 也不会触发下载）
#   node   24.19.0：与开发机同版本；同一个 alpine 3.24 / musl，两边的二进制互相认
FROM golang:1.26.9-alpine3.24@sha256:3082400e369fa24d5fc60bca20edab3f6d604e0c5a690ec66b295eff4dd87ade

# Node（node / npm / npx 都在 /usr/local 里）：直接从官方 node 镜像搬过来。
COPY --from=node:24.19.0-alpine3.24@sha256:d32cdf619f63fe0471182d08996dd516c6275bb5fd31ae06e55a570bd9e1ad43 /usr/local /usr/local

# 两样都补上：
#   libstdc++ —— 官方 node 的 alpine 构建链着 C++ 标准库（golang 镜像里没有；少了就是
#                `Error relocating /usr/local/bin/node: _ZSt4cerr: symbol not found`，实测踩过）
#   ca-certificates —— 后端要连上游 HTTPS（golang 镜像自带，显式补一行防换底后翻车）
RUN apk add --no-cache libstdc++ ca-certificates

# 构建期自检：Node 搬进来必须当场能跑（上面两样缺任何一样，这一行就炸在 build 时，不留到运行时）
RUN node --version && npm --version

WORKDIR /app
# 源码**不** COPY 进镜像：compose 把仓库 bind 进 /app（改代码不用重建镜像；编译发生在容器启动时）。
# 镜像里只有工具链 + 启动脚本（docker/entrypoint.sh 也来自 bind 的仓库本体）。
