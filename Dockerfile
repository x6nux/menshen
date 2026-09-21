FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X 'main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)'" \
    -o /out/menshen .

FROM alpine:3.21
# TLS 根证书：连 Telegram 与 AI 上游都要它，scratch 镜像里少了它
# 表现为「所有请求都失败」而不是一条明确的证书错误。
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/menshen /app/menshen
# 数据卷：库里有上游 api_key 明文
VOLUME /app/data
EXPOSE 8081
ENTRYPOINT ["/app/menshen", "-config", "/app/config.yaml"]
