# Builds a chaosmetad daemonset image that includes the PPU fault-injection
# kernel tool (chaosmeta_ppu) alongside the other Go exec tools and the
# C-based chaosmeta_execns (needed only for container-namespace injectors;
# harmless to PPU which is host-only).
#
# All Go binaries are pre-cross-compiled on the host (CGO_ENABLED=0, linux/amd64,
# static) into chaosmetad/build/staging/. execns is a tiny C program — we compile
# it statically under a thin alpine builder (musl). This keeps the runtime stage
# free of emulator-heavy Go compilation under QEMU on Apple Silicon.
#
# Build (from repo root, after staging host-built Go binaries):
#   ~/wattlen/scripts or manually:
#     cd chaosmetad && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
#       go build -o build/staging/chaosmetad ./cmd/main.go  (and tools...)
#   then:
#   docker build --platform linux/amd64 -t \
#     goodputai-registry.cn-hangzhou.cr.aliyuncs.com/dev/chaosmetad-daemon:ppu-0.1 \
#     -f chaosmetad/build/chaosmeta-ppu.runtime.Dockerfile .

# ---- stage 2: runtime (alpine → tiny, runs static binaries) ----
#
# NOTE: chaosmeta_execns (the C container-namespace helper used only by
# container-runtime injection paths) is intentionally NOT included in this
# image. It requires a C toolchain to build and PPU injection is host-only
# (never uses nsenter-via-execns). Add it back later if container-namespace
# faults on this daemon are needed.
FROM alpine:3.21

ENV CHAOSMETAD_VERSION=0.3.9
# bash because chaosmetad and exec tools assume /bin/bash; util-linux for
# nsenter (container-namespace paths); procps for pgrep/ps used by burn
# recover; iproute2/coreutils for completeness.
RUN apk add --no-cache bash util-linux iproute2 procps-ng coreutils

COPY chaosmetad/build/staging/chaosmetad /opt/chaosmeta/chaosmetad-${CHAOSMETAD_VERSION}/chaosmetad
COPY chaosmetad/build/staging/tools      /opt/chaosmeta/chaosmetad-${CHAOSMETAD_VERSION}/tools

# daemonset supplies its own command; keep a harmless init loop so the image
# does not exit when run standalone (mirrors the original daemonset Dockerfile).
CMD while true; do if [ ! -d "/tmp/chaosmetad-${CHAOSMETAD_VERSION}" ]; then cp -r /opt/chaosmeta/chaosmetad-${CHAOSMETAD_VERSION} /tmp/chaosmetad-${CHAOSMETAD_VERSION}; fi; sleep 600; done
