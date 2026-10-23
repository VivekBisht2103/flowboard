# syntax=docker/dockerfile:1
# Single-container image for hosted demos (Render, Railway, Fly.io, any Docker host):
# one Go process serves the React app, the /v1 API and the outbox worker.
# Local development keeps using docker-compose.yml (separate services).

FROM node:22-alpine AS web
WORKDIR /web
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY frontend/ ./
RUN npx vite build

FROM golang:1.24-alpine AS api
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/flowboard ./cmd/flowboard \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fbctl ./cmd/fbctl

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=api /out/flowboard /out/fbctl /usr/local/bin/
COPY --from=web /web/dist /app/web
ENV STATIC_DIR=/app/web \
    SEED_ON_START=true \
    RUN_WORKER=true \
    PORT=8080
USER nobody
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/flowboard"]
CMD ["serve"]
