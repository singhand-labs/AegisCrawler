# Build the Admin UI inside the image so every deployable server artifact has
# the exact frontend that corresponds to its source revision.
FROM node:20-alpine AS admin-builder

WORKDIR /src/admin-ui

COPY admin-ui/package.json admin-ui/package-lock.json ./
RUN npm ci

COPY admin-ui/ ./
RUN npm run build

# Build stage
FROM golang:1.24.2-alpine AS server-builder

WORKDIR /src/server

COPY server/go.mod server/go.sum ./
RUN go mod download

COPY server/ ./
COPY --from=admin-builder /src/admin-ui/dist ./web/admin
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o server ./cmd/server

# Runtime stage
FROM alpine:3.19

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata
RUN adduser -D -u 1000 appuser

COPY --from=server-builder /src/server/server .

ENV LISTEN_ADDR=:8080
ENV DATABASE_PATH=/data/opencrawler.db

EXPOSE 8080

RUN mkdir -p /data && chown appuser:appuser /data

USER appuser

HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -q -O - 'http://127.0.0.1:8080/health?ready=1' | grep -q '"status":"ok"' || exit 1

CMD ["/app/server"]
