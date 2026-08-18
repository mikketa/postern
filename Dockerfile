# Postern in a container: the Go binary, a real Chromium, and the Xvfb it draws
# on. Nothing here is headless — that is the whole point of the project, and a
# container changes none of it.

FROM golang:1.26-trixie AS build
WORKDIR /src

# Dependencies first, so a source-only change does not refetch the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static: the runtime stage has no Go toolchain and postern links nothing —
# it drives an external browser over a socket.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /postern ./cmd/postern

FROM debian:trixie-slim

# chromium is the browser, xvfb the screen postern starts for it.
#
# The fonts are not optional padding. A browser with no fonts renders every
# page in fallback boxes, and font enumeration is one of the oldest signals in
# fingerprinting — a machine that reports two fonts is not a machine anyone
# browses from.
RUN apt-get update && apt-get install -y --no-install-recommends \
        chromium \
        xvfb \
        ca-certificates \
        curl \
        fonts-liberation \
        fonts-dejavu-core \
        fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/*

# Not root. A captcha solver runs someone else's JavaScript in a browser all
# day, which is the definition of a process that should own as little as it can.
RUN useradd --create-home --shell /usr/sbin/nologin postern
USER postern
WORKDIR /home/postern

COPY --from=build /postern /usr/local/bin/postern

# The profile is the thing worth keeping between runs: it is what ages, and an
# identity that starts clean every restart is an identity that never matures.
VOLUME ["/home/postern/.config/postern"]

EXPOSE 8099

# /health needs no token, which is exactly what a health check should not need.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8099/health || exit 1

# 0.0.0.0 because a container that binds to loopback is unreachable — and
# postern refuses that address unless POSTERN_TOKEN is set, so running this
# image without a token fails immediately and says why. That is deliberate.
ENTRYPOINT ["postern", "serve", "-addr", "0.0.0.0:8099"]
