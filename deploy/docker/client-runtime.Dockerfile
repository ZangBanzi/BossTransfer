FROM alpine:3.22.6 AS certificates

RUN apk add --no-cache ca-certificates

FROM scratch

LABEL org.opencontainers.image.title="BossTransfer Client" \
      org.opencontainers.image.version="2.1.0" \
      org.opencontainers.image.description="BossTransfer customer NAS 115 download client"
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 bin/client /client

USER 65532:65532
EXPOSE 18085
ENTRYPOINT ["/client"]
