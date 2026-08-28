# syntax=docker/dockerfile:1

# ---------- Stage 1: 前端构建 ----------
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# vite outDir 固定输出到 /src/internal/webui/dist（见 web/vite.config.ts）
RUN npm run build

# ---------- Stage 2: Go 构建 ----------
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 嵌入前端产物（internal/webui/embed.go: //go:embed dist）
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/aistudio2api ./cmd/aistudio2api

# ---------- Stage 3: 运行时（含 Camoufox/Firefox 依赖） ----------
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        libgtk-3-0 libdbus-glib-1-2 libxt6 libx11-xcb1 libxcomposite1 \
        libxdamage1 libxrandr2 libxfixes3 libxkbcommon0 libxss1 \
        libasound2 libpango-1.0-0 libcairo2 libglib2.0-0 \
        libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 \
        libcups2 libdrm2 libgbm1 libxcursor1 libxi6 \
        fonts-liberation procps \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /out/aistudio2api ./aistudio2api

# 登录态与 Camoufox 运行时（首次运行自动下载）均落卷持久化
VOLUME ["/app/auth", "/app/runtime"]
EXPOSE 2048
ENTRYPOINT ["./aistudio2api"]
