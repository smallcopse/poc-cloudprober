ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG TARGETARCH
ARG CLOUDPROBER_VERSION=v0.13.6
ARG PYTHON_VERSION=3.14

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

ARG PYTHON_VERSION

RUN microdnf install -y \
        python${PYTHON_VERSION} \
        python3-pip \
        ca-certificates \
        tzdata && \
    microdnf clean all

COPY requirements.txt /tmp/requirements.txt
RUN python${PYTHON_VERSION} -m pip install --no-cache-dir -r /tmp/requirements.txt && \
    rm /tmp/requirements.txt

COPY --from=downloader /usr/local/bin/cloudprober /usr/local/bin/cloudprober

COPY scripts/ /scripts/

COPY cloudprober.cfg /etc/cloudprober.cfg

EXPOSE 9313

USER 65534:65534

ENTRYPOINT ["/usr/local/bin/cloudprober", "--config_file=/etc/cloudprober.cfg"]
