# ── 第一阶段:构建前端 ──
FROM node:22-alpine AS web-builder
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ── 第二阶段:编译 Go 二进制(含内嵌前端) ──
FROM golang:1.26-alpine AS builder
RUN apk add --no-cache git ca-certificates
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 复制第一阶段生成的前端产物到内嵌目录
COPY --from=web-builder /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o icloud-hme .

# ── 第三阶段:运行时(仅二进制,无 Node) ──
FROM alpine:latest
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/icloud-hme .
EXPOSE 8081
ENTRYPOINT ["/app/icloud-hme"]
