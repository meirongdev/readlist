# syntax=docker/dockerfile:1
# readlist 单 Go 二进制镜像。SPA 已作为纯静态文件嵌入 internal/api/dist,无需前端构建步骤。
# modernc.org/sqlite 是纯 Go(CGO 关闭)→ 交叉编译到 TARGETARCH,无需 QEMU。

# ---- Go 构建 ----
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/readlist ./cmd/readlist

# ---- 运行时(静态 + nonroot)----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /data
COPY --from=build /out/readlist /usr/local/bin/readlist
ENV DB_PATH=/data/readlist.db \
    API_LISTEN_ADDR=:8080 \
    TZ=UTC
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/readlist"]
