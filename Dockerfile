# GovDoc —— 单镜像多阶段构建（linux/amd64）
#
# 三个阶段的职责刻意分开：
#   1. web    用 pnpm 构建前端，产物 web/dist 会被 //go:embed 打进二进制；
#   2. build  用 CGO_ENABLED=0 编译静态二进制，因此运行镜像不需要 glibc；
#   3. runtime 只带二进制与 CA 证书，非 root 运行。
#
# 构建上下文必须是仓库根目录：go:embed 要求 web/dist 在声明包之下，
# 而 embed.go 位于模块根。

# ---------------------------------------------------------------- 前端
FROM node:26-alpine AS web

WORKDIR /src/web

# 构建阶段没有 TTY：pnpm 在清理/重装 node_modules 前会要求交互确认，
# CI=true 让它按无人值守处理，否则镜像构建会在这里直接失败。
ENV CI=true

# pnpm 版本显式固定，不用 corepack 的隐式选择：本地 lockfile 由 pnpm 11 生成，
# 构建也必须用同一个大版本，否则 --frozen-lockfile 可能因格式差异失败。
ARG PNPM_VERSION=11.23.0
RUN npm install -g pnpm@${PNPM_VERSION}

# 先只拷贝依赖清单，让 pnpm install 这一层能被 Docker 缓存复用。
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

# 源码与配置：vite.config.ts 决定产物落在 dist/ 并 embed 进二进制。
COPY web/ ./
RUN pnpm run build

# ---------------------------------------------------------------- 编译
FROM golang:1.27.4-alpine AS build

WORKDIR /src

# go.mod 只依赖 rosetta；先拷贝清单再下载模块，源码变动不会击穿这层缓存。
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# 用阶段 1 的产物覆盖仓库里可能过期的 dist：镜像里的界面必须与源码一致，
# 不能依赖提交进仓库的构建结果。
COPY --from=web /src/web/dist ./web/dist

ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# -trimpath 去掉本机绝对路径；-s -w 去掉符号表与调试信息，镜像更小。
# CGO 关闭后产物是静态链接的，运行阶段可以直接用 alpine 或 scratch。
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/govdoc ./cmd/govdoc

# ---------------------------------------------------------------- 运行
FROM alpine:3.24

# ca-certificates：调用 HTTPS 模型接口需要根证书；tzdata：日志按 Asia/Shanghai。
#
# 刻意不装中文字体：预览是浏览器渲染的，用的是**用户自己机器**上的字体；
# 导出 DOCX 只写入字体名、不嵌入字体文件。容器里装一套字体既影响不到两者，
# 又会让镜像多出上百 MB。
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 govdoc \
    && mkdir -p /data \
    && chown govdoc:govdoc /data

COPY --from=build /out/govdoc /usr/local/bin/govdoc

# 8080 而不是桌面版的 8765：容器里必须监听 0.0.0.0，端口冲突也少。
ENV GOVDOC_CONFIG_DIR=/data \
    TZ=Asia/Shanghai

USER govdoc
WORKDIR /data

# 设置文件写到挂载卷，重启后 API Key 与自定义配置仍在。
VOLUME ["/data"]
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/govdoc"]
CMD ["-addr", "0.0.0.0:8080"]
