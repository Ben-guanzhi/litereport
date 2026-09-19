# 多阶段构建：最终镜像不含 Go 工具链（modernc/sqlite 等驱动均为纯 Go，无需 CGO）
FROM golang:1.26-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /litereport .

FROM alpine:3.20
RUN adduser -D -u 10001 litereport
WORKDIR /app
COPY --from=builder /litereport /app/litereport
COPY web /app/web
COPY config.yaml /app/config.yaml
RUN mkdir -p /app/data && chown -R litereport:litereport /app
USER litereport
EXPOSE 8085
HEALTHCHECK --interval=30s --timeout=3s --retries=3 CMD wget -qO- http://127.0.0.1:8085/healthz || exit 1
ENTRYPOINT ["/app/litereport"]
