FROM --platform=$BUILDPLATFORM node:20-alpine AS builder

ARG VERSION=v2.5.7
WORKDIR /build
COPY ./web/package*.json ./
RUN npm ci
COPY ./web .
RUN VITE_VERSION=${VERSION} npm run build


FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder2

ARG VERSION=v2.5.7
ARG TARGETOS
ARG TARGETARCH
ENV GO111MODULE=on \
    CGO_ENABLED=0

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=builder /build/dist ./web/dist
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags "-s -w -X autogateway/internal/version.Version=${VERSION}" -o autogateway


FROM alpine

WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata \
    && update-ca-certificates

COPY --from=builder2 /build/autogateway .
EXPOSE 3001
# 让 docker run (绕过 compose) 起的容器也有 healthcheck.
# 30s 间隔, 10s 超时, 3 次失败标记 unhealthy, 启动后给 40s 宽限期.
HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=40s \
  CMD wget -q --spider -T 5 -O /dev/null http://localhost:3001/health || exit 1
ENTRYPOINT ["/app/autogateway"]
