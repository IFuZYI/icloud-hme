# syntax=docker/dockerfile:1

# ── 第一阶段:构建前端 ──
FROM node:22-alpine AS web-builder
WORKDIR /src/web
# 先复制依赖清单,命中 npm ci 的层缓存;源码改动不会使依赖层失效
COPY web/package.json web/package-lock.json ./
# 复用 BuildKit 缓存挂载,避免每次构建重新下载全部依赖
RUN --mount=type=cache,target=/root/.npm \
    npm ci
COPY web/ ./
# 镜像构建只产出前端资源;lint/单测属于 CI/本地环节,不在此拖慢构建
RUN npm run build

# ── 第二阶段:编译 Go 二进制(含内嵌前端) ──
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /build
COPY go.mod go.sum ./
# 模块下载单独成层并挂载模块缓存,依赖不变时零网络开销
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
# 复制第一阶段生成的前端产物到内嵌目录
COPY --from=web-builder /src/internal/webui/dist ./internal/webui/dist
# 挂载模块与编译缓存,增量构建可复用已编译的包对象
# 只编译二进制;go test/vet 属于 CI/本地环节,不在镜像构建内执行
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o icloud-hme .

# ── 第三阶段:运行时(仅二进制,无 Node) ──
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/icloud-hme .
EXPOSE 8081
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8081/ || exit 1
ENTRYPOINT ["/app/icloud-hme"]
