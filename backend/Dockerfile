# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/flowboard ./cmd/flowboard \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fbctl ./cmd/fbctl

# `docker compose --profile test run --rm api-test` runs unit + integration tests.
FROM build AS test
ENV CGO_ENABLED=0
CMD ["go", "test", "-count=1", "./..."]

FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates wget
COPY --from=build /out/flowboard /out/fbctl /usr/local/bin/
USER nobody
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/flowboard"]
CMD ["serve"]
