ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG CLOUDPROBER_VERSION=v0.14.3
ARG PYTHON_VERSION=3.14

# cloudprober の公式マルチアーキテクチャイメージからバイナリを取得
FROM --platform=$TARGETPLATFORM ghcr.io/cloudprober/cloudprober:${CLOUDPROBER_VERSION} AS cloudprober-src

FROM redhat/ubi10-minimal:latest

ARG PYTHON_VERSION

RUN microdnf install -y \
        python${PYTHON_VERSION} \
        python${PYTHON_VERSION}-pip \
        ca-certificates \
        tzdata && \
    microdnf clean all

COPY requirements.txt /tmp/requirements.txt
RUN python${PYTHON_VERSION} -m ensurepip --upgrade && \
    python${PYTHON_VERSION} -m pip install --no-cache-dir -r /tmp/requirements.txt && \
    rm /tmp/requirements.txt

COPY --from=cloudprober-src /cloudprober /usr/local/bin/cloudprober

COPY scripts/ /scripts/

COPY cloudprober.cfg /etc/cloudprober.cfg

EXPOSE 9313

USER 65534:65534

ENTRYPOINT ["/usr/local/bin/cloudprober", "--config_file=/etc/cloudprober.cfg"]
