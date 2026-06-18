ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG TARGETARCH
ARG CLOUDPROBER_VERSION=v0.13.6

FROM --platform=$BUILDPLATFORM alpine:3.19 AS downloader

ARG CLOUDPROBER_VERSION
ARG TARGETARCH

RUN apk add --no-cache curl && \
    ARCH="${TARGETARCH:-amd64}" && \
    curl -sSfL \
      "https://github.com/cloudprober/cloudprober/releases/download/${CLOUDPROBER_VERSION}/cloudprober-${CLOUDPROBER_VERSION}-linux-${ARCH}.tar.gz" \
      -o /tmp/cloudprober.tar.gz && \
    tar -xzf /tmp/cloudprober.tar.gz -C /usr/local/bin/ cloudprober && \
    chmod +x /usr/local/bin/cloudprober

FROM redhat/ubi10-minimal:latest

RUN microdnf install -y bash curl ca-certificates tzdata && microdnf clean all

COPY --from=downloader /usr/local/bin/cloudprober /usr/local/bin/cloudprober

COPY scripts/ /scripts/
RUN chmod +x /scripts/*.sh

COPY cloudprober.cfg /etc/cloudprober.cfg

EXPOSE 9313

USER 65534:65534

ENTRYPOINT ["/usr/local/bin/cloudprober", "--config_file=/etc/cloudprober.cfg"]
