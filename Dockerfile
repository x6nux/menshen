# 前端构建阶段：Vite 产物输出到 ../internal/panel/webdist（相对 web/），
# 也就是镜像里的 /internal/panel/webdist，供下面的 Go 阶段 go:embed。
# 前端产物与目标架构无关，固定跑构建机的原生架构，避免 QEMU 模拟。
# 依赖一律在容器里 npm ci（宿主机的 web/node_modules 已由 .dockerignore 排除）。
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build
# 半成品构建尽早失败：两个入口页缺一不可——index.html 是 Mini App，
# admin.html 是桌面端管理面板（npm run build 会依次构建两者）。
RUN test -f /internal/panel/webdist/index.html
RUN test -f /internal/panel/webdist/admin.html

# 构建阶段固定跑在构建机的原生架构上，靠 GOOS/GOARCH 交叉编译出目标架构：
# 纯 Go 交叉编译只要几秒，而让 arm64 走 QEMU 模拟整个 go build 要慢十几倍。
# CI 里 amd64 / arm64 各有原生 runner（见 .github/workflows/docker.yml），这段主要
# 是给本地 `docker buildx build --platform linux/arm64` 省掉模拟编译的开销。
# 本地 docker build / compose 构建时（BuildKit，Docker 23+ 默认）这几个值
# 即本机架构，交叉编译不生效。
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 用 web 阶段的新鲜产物覆盖构建上下文里的（宿主机 webdist 已被 .dockerignore
# 排除）。产物缺失时运行时返回前端未构建的 503 提示页，编译本身不受影响。
COPY --from=web /internal/panel/webdist ./internal/panel/webdist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags="-s -w -X 'main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)'" \
    -o /out/menshen .

FROM alpine:3.21
# TLS 根证书：连 Telegram 与 AI 上游都要它，缺少时表现为所有请求失败，
# 而非一条明确的证书错误。这里只装证书，不装 tzdata（见下）。
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/menshen /app/menshen

# 时区：Go 发行包自带 zoneinfo.zip，不装 tzdata 就靠它。
# 目标路径固定：TZ / time.Local 的初始化只认
# /usr/share/zoneinfo、/etc/localtime 和构建时 GOROOT 下的 lib/time/zoneinfo.zip
# 三处，ZONEINFO 只对显式的 LoadLocation 生效（放在二进制同目录无效）。
# GOROOT 已烤进二进制，即构建镜像里的 /usr/local/go，所以按原路径放回即可。
# 可用 `docker exec <容器> ./menshen -version` 检查时间戳时区。
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /usr/local/go/lib/time/zoneinfo.zip
# 只影响日志时间戳的显示（DB 里存的是绝对时间），要改成别的时区在 compose 里覆盖 TZ。
ENV TZ=Asia/Shanghai

# 这两项在容器内有固定取值，因此烤进镜像：
#
#   MENSHEN_DB_PATH      必须落在下面挂的卷里。默认值 data.db 会写进
#                        容器可写层，重启即全部丢失。
#   MENSHEN_LISTEN_ADDR  容器内必须监听全零地址。默认值只监听容器自身的
#                        回环，端口映射过去也连不上。
#
# ⚠️ 环境变量**优先于** config.yaml。挂了配置文件的话，这两项以这里为准；
#    要改成别的路径就在 compose 的 environment 里覆盖同名变量。
ENV MENSHEN_DB_PATH=/app/data/data.db \
    MENSHEN_LISTEN_ADDR=0.0.0.0:8081

# 数据卷：库里有上游 api_key 明文
VOLUME /app/data
EXPOSE 8081

# -config 指向的文件不存在不算错误：可以完全不挂配置、全用环境变量。
ENTRYPOINT ["/app/menshen", "-config", "/app/config.yaml"]
