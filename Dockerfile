# 构建阶段固定跑在构建机的原生架构上，靠 GOOS/GOARCH 交叉编译出目标架构：
# 纯 Go 交叉编译只要几秒，而让 arm64 走 QEMU 模拟整个 go build 要慢十几倍。
# CI 里 amd64 / arm64 各有原生 runner（见 .github/workflows/docker.yml），这段主要
# 是给本地 `docker buildx build --platform linux/arm64` 省掉模拟编译的开销。
# 本地 docker build / compose 构建时（BuildKit，Docker 23+ 默认）这几个值
# 就是本机架构，产物与原先一致。
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags="-s -w -X 'main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)'" \
    -o /out/menshen .

FROM alpine:3.21
# TLS 根证书：连 Telegram 与 AI 上游都要它，scratch 镜像里少了它
# 表现为「所有请求都失败」而不是一条明确的证书错误。
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/menshen /app/menshen

# 这两项在容器里只有一个正确答案，所以烤进镜像，免得每个部署各踩一次：
#
#   MENSHEN_DB_PATH      必须落在下面挂的卷里。默认值 data.db 会写进
#                        容器可写层，重启即全部丢失。
#   MENSHEN_LISTEN_ADDR  容器内必须监听全零地址。默认值只听容器自己的
#                        回环，端口映射过去也连不上。
#
# ⚠️ 环境变量**压过** config.yaml。挂了配置文件的话，这两项以这里为准；
#    要改成别的路径就在 compose 的 environment 里覆盖同名变量。
ENV MENSHEN_DB_PATH=/app/data/data.db \
    MENSHEN_LISTEN_ADDR=0.0.0.0:8081

# 数据卷：库里有上游 api_key 明文
VOLUME /app/data
EXPOSE 8081

# -config 指向的文件不存在不算错误：可以完全不挂配置、全用环境变量。
ENTRYPOINT ["/app/menshen", "-config", "/app/config.yaml"]
