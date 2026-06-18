ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG TARGETARCH
ARG TARGETOS
ARG CLOUDPROBER_VERSION=v0.14.3
ARG GO_VERSION=1.26

# cloudprober の公式マルチアーキテクチャイメージからバイナリを取得
FROM --platform=$TARGETPLATFORM ghcr.io/cloudprober/cloudprober:${CLOUDPROBER_VERSION} AS cloudprober-src

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS go-builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /build
COPY scripts/probe.go .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -ldflags="-s -w" -o probe probe.go

FROM redhat/ubi10-minimal:latest

RUN microdnf install -y ca-certificates tzdata && microdnf clean all

COPY --from=cloudprober-src /cloudprober /usr/local/bin/cloudprober
COPY --from=go-builder /build/probe /scripts/probe

COPY cloudprober.cfg /etc/cloudprober.cfg

EXPOSE 9313

USER 65534:65534

ENTRYPOINT ["/usr/local/bin/cloudprober", "--config_file=/etc/cloudprober.cfg"]
