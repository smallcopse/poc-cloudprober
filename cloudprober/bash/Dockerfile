ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG CLOUDPROBER_VERSION=v0.14.3

# cloudprober の公式マルチアーキテクチャイメージからバイナリを取得
FROM --platform=$TARGETPLATFORM ghcr.io/cloudprober/cloudprober:${CLOUDPROBER_VERSION} AS cloudprober-src

FROM redhat/ubi10-minimal:latest

RUN microdnf install -y bash curl ca-certificates tzdata && microdnf clean all

COPY --from=cloudprober-src /cloudprober /usr/local/bin/cloudprober

COPY scripts/ /scripts/
RUN chmod +x /scripts/*.sh

COPY cloudprober.cfg /etc/cloudprober.cfg

EXPOSE 9313

USER 65534:65534

ENTRYPOINT ["/usr/local/bin/cloudprober", "--config_file=/etc/cloudprober.cfg"]
