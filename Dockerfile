FROM --platform=$BUILDPLATFORM node:20-alpine AS builder

ARG VERSION=1.0.0
WORKDIR /build
COPY ./web/package*.json ./
RUN npm ci
COPY ./web .
RUN VITE_VERSION=${VERSION} npm run build


FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS builder2

ARG VERSION=1.0.0
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
ENTRYPOINT ["/app/autogateway"]
