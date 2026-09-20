FROM golang:1.27.1-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY apps ./apps
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/manager ./apps/manager

FROM alpine:3.22.6

RUN apk add --no-cache ca-certificates \
    && addgroup -S -g 65532 bosstransfer \
    && adduser -S -D -H -u 65532 -G bosstransfer bosstransfer \
    && mkdir -p /var/lib/bosstransfer \
    && chown -R bosstransfer:bosstransfer /var/lib/bosstransfer
COPY --from=build /out/manager /manager
USER bosstransfer:bosstransfer
EXPOSE 8085
ENTRYPOINT ["/manager"]
