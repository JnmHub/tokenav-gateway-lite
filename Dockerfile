FROM node:24.13.0-alpine AS frontend
WORKDIR /build/frontend-win12
RUN npm install --global pnpm@9.15.9
COPY frontend-win12/package.json frontend-win12/pnpm-lock.yaml frontend-win12/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY frontend-win12/ ./
RUN pnpm run build

FROM golang:1.27.0-alpine AS builder
WORKDIR /app
RUN apk add --no-cache ca-certificates git
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY ent/ ./ent/
COPY internal/ ./internal/
COPY migrations/ ./migrations/
COPY pkg/ ./pkg/
COPY resources/ ./resources/
COPY --from=frontend /build/internal/web/dist/ ./internal/web/dist/
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -tags "embed timetzdata" -trimpath \
    -ldflags="-s -w -X main.Commit=${COMMIT} -X main.Date=${BUILD_DATE} -X main.BuildType=release" \
    -o /out/main ./cmd/server/

FROM scratch AS runtime
WORKDIR /app
LABEL org.opencontainers.image.title="Tokenav Gateway Lite" \
      org.opencontainers.image.source="https://github.com/JnmHub/tokenav-gateway-lite" \
      org.opencontainers.image.licenses="LGPL-3.0"
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/main /app/main
# 离线或远程价格源故障时仍使用完整的内置价格目录。
COPY --from=builder /app/resources /app/resources
ENV RUN_MODE=gateway-lite DATA_DIR=/app/data SERVER_HOST=0.0.0.0 SERVER_PORT=8080 SERVER_MODE=release
EXPOSE 8080
CMD ["/app/main"]
