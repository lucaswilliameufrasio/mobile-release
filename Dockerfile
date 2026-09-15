FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mobile-release ./cmd/mobile-release

FROM alpine:3.22
RUN adduser -D -u 10001 app
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mobile-release /usr/local/bin/mobile-release
USER app
ENTRYPOINT ["mobile-release"]
