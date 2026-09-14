# Reproducible OB-USP-Agent build for ACS interoperability tests.
#
# The upstream ci/Dockerfile clones libwebsockets' floating master branch at
# image-build time. That makes a pinned OB-USP-Agent commit non-reproducible and
# in September 2026 began failing inside libwebsockets' transitive wolfMQTT
# build before OB-USP-Agent itself was compiled. The ACS gate cares about
# interoperability with the pinned Broadband Forum agent, not testing the
# latest libwebsockets development branch, so use Debian's packaged transport
# libraries instead.
#
# This intentionally stays single-stage. It is a CI-only image and keeping the
# build/runtime packages together avoids coupling the gate to Debian's changing
# runtime-package names while still pinning the actual reference-agent source.

FROM debian:stable

RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    build-essential autoconf automake libtool pkg-config \
    libssl-dev libcurl4-openssl-dev libsqlite3-dev zlib1g-dev \
    libmosquitto-dev libwebsockets-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /usr/local/src/obuspa
COPY . .
RUN autoreconf --force --install \
    && ./configure \
    && make -j"$(nproc)" \
    && make install

COPY ci/configs /etc/obuspa/configs
COPY ci/certs /etc/obuspa/certs

ENTRYPOINT ["/usr/local/bin/obuspa"]
CMD ["-p", "-v4", "-r", "/etc/obuspa/configs/STOMP.txt", "-t", "/etc/obuspa/certs", "-f", "/tmp/usp.db"]
