# ---- 构建阶段 ----
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
WORKDIR /src

# 先只拷依赖描述，利用层缓存
COPY go.mod ./
RUN go mod download 2>/dev/null || true

COPY . .

# 版本号由 CI 从这个 build-arg 注入（-X 只能改 var，改不了 const）
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# CGO 关掉 → 静态二进制，能放进 scratch
# -s -w 去掉符号表和调试信息
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
      -ldflags="-s -w -X justnav/internal/api.Version=${VERSION}" \
      -o /out/justnav .

# ---- 运行阶段 ----
FROM scratch

# 壁纸走 HTTPS，scratch 里没有根证书
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/justnav /justnav

ENV JUSTNAV_PORT=8522 \
    JUSTNAV_DATA_DIR=/data \
    TZ=Asia/Shanghai

VOLUME ["/data"]
EXPOSE 8522

ENTRYPOINT ["/justnav"]
