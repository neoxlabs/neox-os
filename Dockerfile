# Neox OS 镜像.
#
#   不改内核. 基础镜像提供 userland, Linux 内核提供 namespace/cgroup2/
#   seccomp/LSM 机制, neox-init 作为 PID 1 提供策略.
#
#   系统层是 Go: 静态编译, 无运行时依赖, 直接当 PID 1.
#   **不再需要 tini** —— neox-init 自己 wait4 收僵尸.

# ── 层 1 · 编译 ────────────────────────────────────────────
FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go/go.mod ./go/
COPY go ./go
WORKDIR /src/go
# 静态编译: 最终镜像里不需要 libc, 也就不受基础镜像版本牵制
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/neox-init ./cmd/neox-init

# ── 层 2 · 运行时 ──────────────────────────────────────────
FROM ubuntu:24.04

RUN sed -i 's|archive.ubuntu.com|mirrors.aliyun.com|g; s|security.ubuntu.com|mirrors.aliyun.com|g' \
        /etc/apt/sources.list.d/ubuntu.sources 2>/dev/null || true

# 约束层依赖: unshare 来自 util-linux (base 已含), 其余按需
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates util-linux \
    && apt-get clean && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/neox-init /usr/local/bin/neox-init

RUN mkdir -p /work /run/neox-os && chmod 755 /work

ENV NEOX_OS_MODE=confined \
    NEOX_OS_VOLUME=/work \
    LANG=C.UTF-8

# neox-init 就是 PID 1 —— 它自己收僵尸、自己转发信号、自己启动自检
ENTRYPOINT ["/usr/local/bin/neox-init"]
