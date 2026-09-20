FROM alpine:3.22.6 AS certificates

RUN apk add --no-cache ca-certificates

FROM scratch

LABEL org.opencontainers.image.title="BossTransfer Manager" \
      org.opencontainers.image.version="2.1.0" \
      org.opencontainers.image.description="BossTransfer central 115 catalog and authorization manager"
COPY --from=certificates /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --chmod=755 bin/manager /manager

USER 65532:65532
EXPOSE 8085
ENTRYPOINT ["/manager"]
