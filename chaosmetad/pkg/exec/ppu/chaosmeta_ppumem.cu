/*
 * chaosmeta_ppumem — PPU 显存占用内核工具 (CUDA)。
 *
 * 用 cudaMalloc 在指定 PPU 上占住指定 MB 的显存，常驻直到被信号杀（recover 用）。
 * 占用是真实的设备显存分配，可通过 ppu-smi 的 memory.used 可观测。
 *
 * 用法：
 *   chaosmeta_ppumem <ppu_index> <mb_to_occupy>
 * 占住后进程挂起等信号；SIGTERM/SIGINT/SIGKILL 退出时由 CUDA runtime 释放内存。
 *
 * 编译（宿主机 /opt/pg1 CUDA SDK）：
 *   PATH=/opt/pg1/CUDA_SDK/bin:$PATH \
 *     nvcc -O2 chaosmeta_ppumem.cu -o chaosmeta_ppumem -lcudart \
 *       -L/opt/pg1/CUDA_SDK/lib64 -Wl,-rpath,/opt/pg1/CUDA_SDK/lib64
 *
 * 说明：PPU 是阿里 alixpu 推理加速卡，但驱动/运行时走 CUDA 兼容层
 * （/opt/pg1/CUDA_SDK，nvcc 12.3），故用标准 cudaMalloc 即可占设备显存。
 */

#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <csignal>
#include <unistd.h>
#include <cuda_runtime.h>

static volatile sig_atomic_t g_stop = 0;
static void on_sig(int s) { (void)s; g_stop = 1; }

static void fail(const char *msg, cudaError_t ce = cudaSuccess) {
    if (ce != cudaSuccess) {
        fprintf(stderr, "[ppumem] %s: %s\n", msg, cudaGetErrorString(ce));
    } else {
        fprintf(stderr, "[ppumem] %s\n", msg);
    }
    exit(1);
}

int main(int argc, char **argv) {
    if (argc != 3) {
        fprintf(stderr, "usage: %s <ppu_index> <mb_to_occupy>\n", argv[0]);
        return 2;
    }
    int dev = atoi(argv[1]);
    long mb = atol(argv[2]);
    if (mb <= 0) fail("mb_to_occupy must be positive");

    // 选卡
    cudaError_t ce = cudaSetDevice(dev);
    if (ce != cudaSuccess) fail("cudaSetDevice failed", ce);

    // 看可用显存（诊断信息，非阻断）
    size_t freeB = 0, totalB = 0;
    cudaMemGetInfo(&freeB, &totalB);
    fprintf(stderr, "[ppumem] device %d: free=%zu MiB total=%zu MiB, requesting %ld MiB\n",
            dev, freeB / (1024 * 1024), totalB / (1024 * 1024), mb);

    // 分配 mb MB 的设备显存（每块 256MB，避免一次大块失败）。
    const size_t BLOCK = 256ULL * 1024 * 1024;
    size_t want = (size_t)mb * 1024 * 1024;
    size_t allocated = 0;
    int blocks = 0;
    void *ptrs[4096];
    while (allocated < want && blocks < 4096) {
        size_t remaining = want - allocated;
        size_t chunk = remaining < BLOCK ? remaining : BLOCK;
        void *p = nullptr;
        cudaError_t e = cudaMalloc(&p, chunk);
        if (e != cudaSuccess) {
            fprintf(stderr, "[ppumem] allocated %zu MiB across %d blocks, cudaMalloc failed for next %zu MiB: %s\n",
                    allocated / (1024 * 1024), blocks, chunk / (1024 * 1024), cudaGetErrorString(e));
            break;
        }
        ptrs[blocks++] = p;
        allocated += chunk;
    }
    if (allocated == 0) fail("could not allocate any device memory (card may be full or device unavailable)");
    // 机器可读的最后状态行：OCCUPY_RESULT ok|partial requested=<mb> allocated=<mb>
    // （Go 端若要检测不足可解析此行；当前保留进程占住已分配显存。partial 仍按"占位成功"处理，
    // 因故障注入语义是"尽量占满"，少占不算失败。）
    const char *st = (allocated >= want) ? "ok" : "partial";
    fprintf(stderr, "[ppumem] OCCUPY_RESULT %s requested=%ld allocated=%zu on device %d (%d blocks); holding until signal\n",
            st, mb, allocated / (1024 * 1024), dev, blocks);

    // 信号处理：退出时 runtime 释放所有 cudaMalloc 内存。
    signal(SIGTERM, on_sig);
    signal(SIGINT, on_sig);
    while (!g_stop) {
        sleep(1);
    }

    // 清理
    for (int i = 0; i < blocks; i++) {
        cudaFree(ptrs[i]);
    }
    fprintf(stderr, "[ppumem] released %zu MiB on device %d\n", allocated / (1024 * 1024), dev);
    cudaDeviceReset();
    return 0;
}
