# Self-contained build of the PPU-enabled chaosmetad image, including the
# cgo-bound gorm sqlite driver the chaosmetad storage layer depends on.
#
# Why this exists: pkg/storage uses gorm.io/driver/sqlite (mattn/go-sqlite3,
# cgo). Building CGO_ENABLED=0 produces a DB stub that breaks inject/recover
# persistence. Cross-compiling cgo on macOS needs a musl cross-toolchain we
# don't have, so we build inside an alpine (musl) builder under emulation.
# OrbStack uses Rosetta for amd64 images on Apple Silicon, which makes this
# tolerable; a real CI/CD path would use native amd64 runners.
#
# Build (from repo root):
#   docker build --platform linux/amd64 -t \
#     goodputai-registry.cn-hangzhou.cr.aliyuncs.com/dev/chaosmetad-daemon:ppu-0.1 \
#     -f chaosmetad/build/chaosmeta-ppu.Dockerfile .

# ---- stage 1: build chaosmetad + Go exec tools with CGO (musl) ----
FROM alpine:3.21 AS builder

ENV CGO_ENABLED=1 GOOS=linux GOARCH=amd64
RUN apk add --no-cache go gcc musl-dev

WORKDIR /src
COPY chaosmetad/go.mod chaosmetad/go.sum* ./
RUN go mod download

COPY chaosmetad/cmd ./cmd
COPY chaosmetad/pkg  ./pkg
COPY chaosmetad/tools ./tools

ARG CHAOSMETAD_VERSION=0.3.9
ARG BUILD_DATE=unknown
RUN mkdir -p /out/tools

# main chaosmetad (cgo for the sqlite driver). ldflags-stamped version.
RUN go build -trimpath \
    -ldflags "-X 'github.com/traas-stack/chaosmeta/chaosmetad/pkg/version.Version=${CHAOSMETAD_VERSION}' -X 'github.com/traas-stack/chaosmeta/chaosmetad/pkg/version.BuildDate=${BUILD_DATE}'" \
    -o /out/chaosmetad ./cmd/main.go

# Go exec tools are pure Go; build them CGO_ENABLED=0 (static, smaller).
RUN CGO_ENABLED=0 go build -trimpath -o /out/tools/chaosmeta_cpuburn  ./tools/chaosmeta_cpuburn.go \
 && CGO_ENABLED=0 go build -trimpath -o /out/tools/chaosmeta_diskburn ./tools/chaosmeta_diskburn.go \
 && CGO_ENABLED=0 go build -trimpath -o /out/tools/chaosmeta_memfill  ./tools/chaosmeta_memfill.go \
 && CGO_ENABLED=0 go build -trimpath -o /out/tools/chaosmeta_occupy   ./tools/chaosmeta_occupy.go \
 && CGO_ENABLED=0 go build -trimpath -o /out/tools/chaosmeta_fd       ./tools/chaosmeta_fd.go \
 && CGO_ENABLED=0 go build -trimpath -o /out/tools/chaosmeta_nproc    ./tools/chaosmeta_nproc.go \
 && CGO_ENABLED=0 go build -trimpath -o /out/tools/chaosmeta_diskfill ./pkg/exec/disk/chaosmeta_diskfill.go \
 && CGO_ENABLED=0 go build -trimpath -o /out/tools/chaosmeta_diskio   ./pkg/exec/diskio/chaosmeta_diskio.go \
 && CGO_ENABLED=0 go build -trimpath -o /out/tools/chaosmeta_ppu      ./pkg/exec/ppu/chaosmeta_ppu.go

# ---- stage 2: runtime (alpine → tiny, runs the cgo+musl binary) ----
FROM alpine:3.21

ENV CHAOSMETAD_VERSION=0.3.9
RUN apk add --no-cache bash util-linux iproute2 procps-ng coreutils

COPY --from=builder /out/chaosmetad /opt/chaosmeta/chaosmetad-${CHAOSMETAD_VERSION}/chaosmetad
COPY --from=builder /out/tools      /opt/chaosmeta/chaosmetad-${CHAOSMETAD_VERSION}/tools

CMD while true; do if [ ! -d "/tmp/chaosmetad-${CHAOSMETAD_VERSION}" ]; then cp -r /opt/chaosmeta/chaosmetad-${CHAOSMETAD_VERSION} /tmp/chaosmetad-${CHAOSMETAD_VERSION}; fi; sleep 600; done
