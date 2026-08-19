# Postern in a container: the Go binary, a real Chromium, and the Xvfb it draws
# on. Nothing here is headless — that is the whole point of the project, and a
# container changes none of it.

FROM golang:1.26-trixie AS build
WORKDIR /src

# Dependencies first, so a source-only change does not refetch the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# The version the image reports on /health and in postern_build_info. Without
# it every container calls itself "devel" and nothing on the outside can tell
# which build is running — pass --build-arg VERSION=v1.2.3 when tagging one.
ARG VERSION=devel

# Static: the runtime stage has no Go toolchain and postern links nothing —
# it drives an external browser over a socket.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
        -o /postern ./cmd/postern

FROM debian:trixie-slim

# Google Chrome rather than Debian's chromium, for two reasons that point the
# same way. The packaged chromium in trixie does not start at all here: its
# crash handler is spawned without a database and the browser traps on the spot
# (measured 2026-08-22, chromium 151.0.7922.169-1~deb13u1, every combination of
# --disable-crashpad, --crash-dumps-dir, --no-sandbox and --headless=new).
# And even working, chromium is the wrong browser for this job: it reports
# "Chromium" in its user agent and ships without the proprietary codecs, both
# of which are fingerprinting signals on their own. The browser postern drives
# should be the browser everyone else runs.
#
# xvfb is the screen postern starts for it. Nothing here is headless — that is
# the whole point of the project, and a container changes none of it.
#
# The fonts are not optional padding. A browser with no fonts renders every
# page in fallback boxes, and font enumeration is one of the oldest signals in
# fingerprinting — a machine that reports two fonts is not a machine anyone
# browses from.
ADD https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb /tmp/chrome.deb
RUN apt-get update && apt-get install -y --no-install-recommends \
        /tmp/chrome.deb \
        xvfb \
        ca-certificates \
        curl \
        fonts-liberation \
        fonts-dejavu-core \
        fonts-noto-color-emoji \
    && rm -rf /var/lib/apt/lists/* /tmp/chrome.deb

# Chrome reads /etc/machine-id at startup and logs an error for every process
# when it is missing or empty, which a debian-slim image leaves it. A stable id
# also keeps the browser from looking like a different machine each restart.
RUN dbus-uuidgen > /etc/machine-id 2>/dev/null || \
    head -c 16 /dev/urandom | od -An -tx1 | tr -d " \n" > /etc/machine-id

# Not root. A captcha solver runs someone else's JavaScript in a browser all
# day, which is the definition of a process that should own as little as it can.
RUN useradd --create-home --shell /usr/sbin/nologin postern

# The whole home, and the profile directory in it, have to exist and belong to
# postern before the volume is declared over the profile. A path Docker creates
# itself at mount time is created root-owned, and postern is not root.
#
# Both halves matter, and the second one is not obvious. Chrome derives its
# crash-dumps location from ~/.config, never from --user-data-dir: with a
# root-owned ~/.config it cannot create ~/.config/google-chrome, the path comes
# back empty, and it launches its crash handler with no --database. The handler
# refuses, and the browser dies on the spot — reporting a missing argument,
# which says nothing about the permission that actually caused it. Measured
# 2026-08-22: root-owned ~/.config fails every time, postern-owned starts.
RUN install -d -o postern -g postern /home/postern/.config /home/postern/.config/postern \
    && chown -R postern:postern /home/postern

USER postern
WORKDIR /home/postern

COPY --from=build /postern /usr/local/bin/postern

# The profile is the thing worth keeping between runs: it is what ages, and an
# identity that starts clean every restart is an identity that never matures.
VOLUME ["/home/postern/.config/postern"]

EXPOSE 8099

# Chrome puts its renderer shared memory in /dev/shm, and Docker's default 64M
# is small enough that pages die under it. Run with --shm-size=1g (or
# --ipc=host); this line is the reminder, since a Dockerfile cannot set it.

# /health needs no token, which is exactly what a health check should not need.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8099/health || exit 1

# 0.0.0.0 because a container that binds to loopback is unreachable by anything
# outside it. Postern then refuses to start unless it is given both a token and
# a way to protect it, so this image needs one more decision from whoever runs
# it — and failing at startup with the reason is the point:
#
#   -e POSTERN_TOKEN=...  ... postern:tag -behind-tls-proxy
#   -e POSTERN_TOKEN=...  ... postern:tag -tls-cert /certs/c.pem -tls-key /certs/k.pem
#
# Arguments given to `docker run` after the image name land here, because the
# entrypoint is exec-form.
ENTRYPOINT ["postern", "serve", "-addr", "0.0.0.0:8099"]
