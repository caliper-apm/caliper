#include "caliper_crash_core.h"

#include <errno.h>
#include <execinfo.h>
#include <fcntl.h>
#include <mach-o/dyld.h>
#include <mach-o/loader.h>
#include <mach/mach.h>
#include <mach/mach_init.h>
#include <mach/thread_act.h>
#include <pthread.h>
#include <signal.h>
#include <stdatomic.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/sysctl.h>
#include <sys/time.h>
#include <unistd.h>

#define CALIPER_MAX_PATH 1024
#define CALIPER_MAX_SCENE 128
#define CALIPER_MAX_IMAGES 512
#define CALIPER_MAX_FRAMES 48
#define CALIPER_ALT_STACK_SIZE (64 * 1024)
#define CALIPER_SHUTDOWN_MSG_ID 0x43414C50

typedef struct {
    uintptr_t base;
    uintptr_t end;
    intptr_t  slide;
    char      name[64];
    char      uuid[40];
} caliper_image_info_t;

// Static pre-allocated storage so the crash path performs ZERO heap allocations.
static char g_tombstone_dir[CALIPER_MAX_PATH];
static char g_scene[CALIPER_MAX_SCENE] = "unknown";
static unsigned char g_alt_stack_mem[CALIPER_ALT_STACK_SIZE];

static caliper_image_info_t g_images[CALIPER_MAX_IMAGES];
static _Atomic uint32_t g_image_count = 0;
static _Atomic int g_dyld_registered = 0;
static _Atomic int g_installed = 0;
static _Atomic int g_crash_written = 0;

static const int g_handled_signals[] = {
    SIGSEGV, SIGABRT, SIGBUS, SIGFPE, SIGILL, SIGTRAP, SIGSYS
};
#define CALIPER_NUM_SIGNALS (sizeof(g_handled_signals) / sizeof(g_handled_signals[0]))
static struct sigaction g_prev_actions[CALIPER_NUM_SIGNALS];

// Mach exception listener state
static mach_port_t g_exc_port = MACH_PORT_NULL;
static pthread_t g_mach_thread;
static int g_mach_running = 0;
static _Atomic int g_mach_stop = 0;

// ===== Async-signal-safe formatting helpers (zero snprintf / zero malloc) =====

static size_t safe_strlen(const char *s) {
    if (!s) return 0;
    size_t n = 0;
    while (s[n] != '\0') n++;
    return n;
}

static void safe_write_all(int fd, const char *buf, size_t len) {
    size_t off = 0;
    while (off < len) {
        ssize_t n = write(fd, buf + off, len - off);
        if (n <= 0) {
            if (n < 0 && errno == EINTR) continue;
            break;
        }
        off += (size_t)n;
    }
}

static void safe_write_str(int fd, const char *s) {
    if (!s) return;
    safe_write_all(fd, s, safe_strlen(s));
}

static void safe_write_hex(int fd, uint64_t val) {
    static const char hex[] = "0123456789abcdef";
    char buf[20];
    buf[0] = '0';
    buf[1] = 'x';
    int i = 0;
    char rev[16];
    if (val == 0) {
        rev[i++] = '0';
    } else {
        while (val > 0 && i < 16) {
            rev[i++] = hex[val & 0xF];
            val >>= 4;
        }
    }
    int out = 2;
    while (i > 0) {
        buf[out++] = rev[--i];
    }
    safe_write_all(fd, buf, (size_t)out);
}

static void safe_write_dec(int fd, int64_t val) {
    char buf[24];
    int pos = 0;
    uint64_t u;
    if (val < 0) {
        buf[pos++] = '-';
        u = (uint64_t)(-(val + 1)) + 1;
    } else {
        u = (uint64_t)val;
    }
    char rev[20];
    int i = 0;
    if (u == 0) {
        rev[i++] = '0';
    } else {
        while (u > 0 && i < 20) {
            rev[i++] = (char)('0' + (u % 10));
            u /= 10;
        }
    }
    while (i > 0) {
        buf[pos++] = rev[--i];
    }
    safe_write_all(fd, buf, (size_t)pos);
}

static const char *signal_name_str(int sig) {
    switch (sig) {
        case SIGSEGV: return "SIGSEGV";
        case SIGABRT: return "SIGABRT";
        case SIGBUS:  return "SIGBUS";
        case SIGFPE:  return "SIGFPE";
        case SIGILL:  return "SIGILL";
        case SIGTRAP: return "SIGTRAP";
        case SIGSYS:  return "SIGSYS";
        default:      return "SIGUNKNOWN";
    }
}

static const char *mach_exception_name(exception_type_t exc) {
    switch (exc) {
        case EXC_BAD_ACCESS:      return "EXC_BAD_ACCESS";
        case EXC_BAD_INSTRUCTION: return "EXC_BAD_INSTRUCTION";
        case EXC_ARITHMETIC:      return "EXC_ARITHMETIC";
        case EXC_EMULATION:       return "EXC_EMULATION";
        case EXC_SOFTWARE:        return "EXC_SOFTWARE";
        case EXC_BREAKPOINT:      return "EXC_BREAKPOINT";
        case EXC_CRASH:           return "EXC_CRASH";
        default:                  return "EXC_UNKNOWN";
    }
}

// ===== Lock-free Mach-O image cache populated via dyld callback =====

static void format_uuid_hex(const uint8_t u[16], char out[40]) {
    static const char hex[] = "0123456789ABCDEF";
    static const int dash_after[16] = {0,0,0,1, 0,1, 0,1, 0,1, 0,0,0,0,0,0};
    int pos = 0;
    for (int i = 0; i < 16; i++) {
        out[pos++] = hex[(u[i] >> 4) & 0xF];
        out[pos++] = hex[u[i] & 0xF];
        if (dash_after[i]) {
            out[pos++] = '-';
        }
    }
    out[pos] = '\0';
}

static void record_dyld_image(const struct mach_header *mh, intptr_t vmaddr_slide) {
    if (!mh) return;
    uint32_t idx = atomic_fetch_add(&g_image_count, 1);
    if (idx >= CALIPER_MAX_IMAGES) {
        atomic_store(&g_image_count, CALIPER_MAX_IMAGES);
        return;
    }

    uintptr_t base = (uintptr_t)mh;
    uint64_t text_size = 0;
    char uuid_buf[40];
    uuid_buf[0] = '\0';

    uint32_t ncmds = 0;
    const uint8_t *cmd_ptr = NULL;
    if (mh->magic == MH_MAGIC_64) {
        const struct mach_header_64 *mh64 = (const struct mach_header_64 *)mh;
        ncmds = mh64->ncmds;
        cmd_ptr = (const uint8_t *)(mh64 + 1);
    } else if (mh->magic == MH_MAGIC) {
        ncmds = mh->ncmds;
        cmd_ptr = (const uint8_t *)(mh + 1);
    }

    for (uint32_t i = 0; i < ncmds && cmd_ptr != NULL; i++) {
        const struct load_command *lc = (const struct load_command *)cmd_ptr;
        if (lc->cmdsize == 0) break;
        if (lc->cmd == LC_SEGMENT_64) {
            const struct segment_command_64 *seg = (const struct segment_command_64 *)lc;
            if (strncmp(seg->segname, "__TEXT", 6) == 0) {
                text_size = seg->vmsize;
            }
        } else if (lc->cmd == LC_SEGMENT) {
            const struct segment_command *seg = (const struct segment_command *)lc;
            if (strncmp(seg->segname, "__TEXT", 6) == 0) {
                text_size = seg->vmsize;
            }
        } else if (lc->cmd == LC_UUID && lc->cmdsize >= sizeof(struct uuid_command)) {
            const struct uuid_command *uc = (const struct uuid_command *)lc;
            format_uuid_hex(uc->uuid, uuid_buf);
        }
        cmd_ptr += lc->cmdsize;
    }

    // Look up dyld image basename while outside signal handler.
    const char *img_name = "unknown";
    uint32_t total = _dyld_image_count();
    for (uint32_t i = 0; i < total; i++) {
        if (_dyld_get_image_header(i) == mh) {
            const char *full = _dyld_get_image_name(i);
            if (full) {
                const char *slash = strrchr(full, '/');
                img_name = slash ? (slash + 1) : full;
            }
            break;
        }
    }

    caliper_image_info_t *slot = &g_images[idx];
    slot->base = base;
    slot->end = base + (uintptr_t)(text_size > 0 ? text_size : 0x10000);
    slot->slide = vmaddr_slide;
    strncpy(slot->name, img_name, sizeof(slot->name) - 1);
    slot->name[sizeof(slot->name) - 1] = '\0';
    strncpy(slot->uuid, uuid_buf, sizeof(slot->uuid) - 1);
    slot->uuid[sizeof(slot->uuid) - 1] = '\0';
}

static const caliper_image_info_t *find_image_for_pc(uintptr_t pc) {
    uint32_t count = atomic_load(&g_image_count);
    if (count > CALIPER_MAX_IMAGES) count = CALIPER_MAX_IMAGES;
    for (uint32_t i = 0; i < count; i++) {
        if (pc >= g_images[i].base && pc < g_images[i].end) {
            return &g_images[i];
        }
    }
    return NULL;
}

// ===== Async-signal-safe Tombstone Writer =====

static void write_tombstone(
    const char *mechanism,
    const char *signal_or_exc,
    int64_t code,
    uint64_t fault_addr,
    const uintptr_t *frames,
    int frame_count
) {
    if (atomic_exchange(&g_crash_written, 1) != 0) {
        return; // Already written by the first winning handler (Mach vs Signal)
    }
    if (g_tombstone_dir[0] == '\0') {
        return;
    }

    struct timeval tv;
    gettimeofday(&tv, NULL);
    int64_t ts_ms = (int64_t)tv.tv_sec * 1000 + (int64_t)(tv.tv_usec / 1000);

    // Build path: <dir>/caliper-crash-<ts>.tombstone without snprintf
    char path[CALIPER_MAX_PATH + 64];
    size_t dlen = safe_strlen(g_tombstone_dir);
    if (dlen >= CALIPER_MAX_PATH - 1) return;
    memcpy(path, g_tombstone_dir, dlen);
    size_t pos = dlen;
    if (pos > 0 && path[pos - 1] != '/') {
        path[pos++] = '/';
    }
    const char *prefix = "caliper-crash-";
    size_t plen = safe_strlen(prefix);
    memcpy(path + pos, prefix, plen);
    pos += plen;

    char num_rev[20];
    int ni = 0;
    uint64_t u = (uint64_t)(ts_ms > 0 ? ts_ms : 1);
    while (u > 0 && ni < 20) {
        num_rev[ni++] = (char)('0' + (u % 10));
        u /= 10;
    }
    while (ni > 0) {
        path[pos++] = num_rev[--ni];
    }
    const char *suffix = ".tombstone";
    size_t slen = safe_strlen(suffix);
    memcpy(path + pos, suffix, slen);
    pos += slen;
    path[pos] = '\0';

    int fd = open(path, O_WRONLY | O_CREAT | O_TRUNC, 0600);
    if (fd < 0) return;

    safe_write_str(fd, "format=caliper_native_crash_v1\n");
    safe_write_str(fd, "mechanism=");
    safe_write_str(fd, mechanism);
    safe_write_str(fd, "\n");
    safe_write_str(fd, "exception=");
    safe_write_str(fd, signal_or_exc);
    safe_write_str(fd, "\n");
    safe_write_str(fd, "code=");
    safe_write_dec(fd, code);
    safe_write_str(fd, "\n");
    safe_write_str(fd, "fault_addr=");
    safe_write_hex(fd, fault_addr);
    safe_write_str(fd, "\n");
    safe_write_str(fd, "timestamp_ms=");
    safe_write_dec(fd, ts_ms);
    safe_write_str(fd, "\n");
    safe_write_str(fd, "scene=");
    safe_write_str(fd, g_scene);
    safe_write_str(fd, "\n");

    for (int i = 0; i < frame_count && i < CALIPER_MAX_FRAMES; i++) {
        uintptr_t pc = frames[i];
        if (pc == 0) continue;
        const caliper_image_info_t *img = find_image_for_pc(pc);
        safe_write_str(fd, "frame=");
        if (img) {
            uint64_t rel = (uint64_t)(pc - img->base);
            safe_write_str(fd, img->name);
            safe_write_str(fd, "|");
            safe_write_str(fd, img->uuid);
            safe_write_str(fd, "|");
            safe_write_hex(fd, rel);
            safe_write_str(fd, "|");
            safe_write_hex(fd, (uint64_t)pc);
        } else {
            safe_write_str(fd, "unknown||");
            safe_write_hex(fd, (uint64_t)pc);
            safe_write_str(fd, "|");
            safe_write_hex(fd, (uint64_t)pc);
        }
        safe_write_str(fd, "\n");
    }

    fsync(fd);
    close(fd);
}

// ===== Unix Signal Handler (altstack, async-signal-safe) =====

static void caliper_signal_handler(int sig, siginfo_t *info, void *ucontext) {
    (void)ucontext;
    void *raw_frames[CALIPER_MAX_FRAMES];
    int n = backtrace(raw_frames, CALIPER_MAX_FRAMES);
    uintptr_t pcs[CALIPER_MAX_FRAMES];
    for (int i = 0; i < n && i < CALIPER_MAX_FRAMES; i++) {
        pcs[i] = (uintptr_t)raw_frames[i];
    }
    uint64_t fault_addr = info ? (uint64_t)(uintptr_t)info->si_addr : 0;
    int64_t code = info ? (int64_t)info->si_code : 0;

    write_tombstone("signal", signal_name_str(sig), code, fault_addr, pcs, n);

    // Restore default / chain previous handler and re-raise so OS terminates cleanly.
    for (size_t i = 0; i < CALIPER_NUM_SIGNALS; i++) {
        if (g_handled_signals[i] == sig) {
            sigaction(sig, &g_prev_actions[i], NULL);
            break;
        }
    }
    raise(sig);
}

// ===== Mach Exception Port Handler Thread =====

#pragma pack(push, 4)
typedef struct {
    mach_msg_header_t Head;
    mach_msg_body_t msgh_body;
    mach_msg_port_descriptor_t thread;
    mach_msg_port_descriptor_t task;
    NDR_record_t NDR;
    exception_type_t exception;
    mach_msg_type_number_t codeCnt;
    int64_t code[2];
    mach_msg_trailer_t trailer;
} caliper_mach_exc_msg_t;

typedef struct {
    mach_msg_header_t Head;
    NDR_record_t NDR;
    kern_return_t RetCode;
} caliper_mach_reply_msg_t;
#pragma pack(pop)

static int capture_thread_frames(thread_t thread, uintptr_t *out, int max_frames) {
    int count = 0;
#if defined(__arm64__) || defined(__aarch64__)
    arm_thread_state64_t state;
    mach_msg_type_number_t state_cnt = ARM_THREAD_STATE64_COUNT;
    if (thread_get_state(thread, ARM_THREAD_STATE64, (thread_state_t)&state, &state_cnt) == KERN_SUCCESS) {
        uintptr_t pc = (uintptr_t)arm_thread_state64_get_pc(state);
        uintptr_t lr = (uintptr_t)arm_thread_state64_get_lr(state);
        uintptr_t fp = (uintptr_t)arm_thread_state64_get_fp(state);
        if (pc != 0 && count < max_frames) out[count++] = pc;
        if (lr != 0 && lr != pc && count < max_frames) out[count++] = lr;
        // Walk arm64 frame pointer chain safely within monotonic stack growth bounds.
        for (int i = 0; i < max_frames - 2 && fp != 0 && (fp & 0x7) == 0; i++) {
            uintptr_t *frame_ptr = (uintptr_t *)fp;
            uintptr_t next_fp = frame_ptr[0];
            uintptr_t ret_pc = frame_ptr[1];
            // Strip arm64e PAC bits if present in upper byte.
            ret_pc &= 0x0000007FFFFFFFFFULL;
            if (ret_pc == 0) break;
            out[count++] = ret_pc;
            if (next_fp <= fp || (next_fp - fp) > (1024 * 1024)) break;
            fp = next_fp;
        }
    }
#elif defined(__x86_64__)
    x86_thread_state64_t state;
    mach_msg_type_number_t state_cnt = x86_THREAD_STATE64_COUNT;
    if (thread_get_state(thread, x86_THREAD_STATE64, (thread_state_t)&state, &state_cnt) == KERN_SUCCESS) {
        uintptr_t pc = (uintptr_t)state.__rip;
        uintptr_t fp = (uintptr_t)state.__rbp;
        if (pc != 0 && count < max_frames) out[count++] = pc;
        for (int i = 0; i < max_frames - 1 && fp != 0 && (fp & 0x7) == 0; i++) {
            uintptr_t *frame_ptr = (uintptr_t *)fp;
            uintptr_t next_fp = frame_ptr[0];
            uintptr_t ret_pc = frame_ptr[1];
            if (ret_pc == 0) break;
            out[count++] = ret_pc;
            if (next_fp <= fp || (next_fp - fp) > (1024 * 1024)) break;
            fp = next_fp;
        }
    }
#endif
    return count;
}

static void *mach_exception_loop(void *arg) {
    (void)arg;
    while (!atomic_load(&g_mach_stop)) {
        caliper_mach_exc_msg_t req;
        memset(&req, 0, sizeof(req));
        kern_return_t kr = mach_msg(
            &req.Head,
            MACH_RCV_MSG | MACH_RCV_LARGE,
            0,
            sizeof(req),
            g_exc_port,
            MACH_MSG_TIMEOUT_NONE,
            MACH_PORT_NULL
        );
        if (kr != KERN_SUCCESS) {
            break;
        }
        if (req.Head.msgh_id == CALIPER_SHUTDOWN_MSG_ID || atomic_load(&g_mach_stop)) {
            break;
        }

        uintptr_t frames[CALIPER_MAX_FRAMES];
        int n = capture_thread_frames(req.thread.name, frames, CALIPER_MAX_FRAMES);
        int64_t code0 = req.codeCnt > 0 ? req.code[0] : 0;
        uint64_t fault_addr = req.codeCnt > 1 ? (uint64_t)req.code[1] : 0;

        write_tombstone("mach", mach_exception_name(req.exception), code0, fault_addr, frames, n);

        // Reply KERN_FAILURE so the exception cascades to Unix signal / Apple CrashReporter cleanly.
        caliper_mach_reply_msg_t reply;
        memset(&reply, 0, sizeof(reply));
        reply.Head.msgh_bits = MACH_MSGH_BITS(MACH_MSGH_BITS_REMOTE(req.Head.msgh_bits), 0);
        reply.Head.msgh_size = sizeof(reply);
        reply.Head.msgh_remote_port = req.Head.msgh_remote_port;
        reply.Head.msgh_local_port = MACH_PORT_NULL;
        reply.Head.msgh_id = req.Head.msgh_id + 100;
        reply.NDR = req.NDR;
        reply.RetCode = KERN_FAILURE;
        (void)mach_msg(&reply.Head, MACH_SEND_MSG, reply.Head.msgh_size, 0, MACH_PORT_NULL, MACH_MSG_TIMEOUT_NONE, MACH_PORT_NULL);
    }
    return NULL;
}

// ===== Public C API =====

int caliper_crash_install(const char *tombstone_dir, bool enable_mach_handler) {
    if (!tombstone_dir || tombstone_dir[0] == '\0') {
        return -1;
    }
    if (atomic_exchange(&g_installed, 1) != 0) {
        return 0;
    }
    atomic_store(&g_crash_written, 0);
    strncpy(g_tombstone_dir, tombstone_dir, CALIPER_MAX_PATH - 1);
    g_tombstone_dir[CALIPER_MAX_PATH - 1] = '\0';

    // Pre-warm backtrace() once so libunwind dyld stubs are bound outside signal context.
    void *warm[4];
    (void)backtrace(warm, 4);

    // Register dyld image listener once (invoked immediately for all loaded images).
    if (atomic_exchange(&g_dyld_registered, 1) == 0) {
        _dyld_register_func_for_add_image(record_dyld_image);
    }

    // Install dedicated 64 KiB alternate signal stack for stack-overflow safety.
    stack_t alt;
    alt.ss_sp = g_alt_stack_mem;
    alt.ss_size = CALIPER_ALT_STACK_SIZE;
    alt.ss_flags = 0;
    (void)sigaltstack(&alt, NULL);

    struct sigaction sa;
    memset(&sa, 0, sizeof(sa));
    sa.sa_sigaction = caliper_signal_handler;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = SA_SIGINFO | SA_ONSTACK;
    for (size_t i = 0; i < CALIPER_NUM_SIGNALS; i++) {
        sigaction(g_handled_signals[i], &sa, &g_prev_actions[i]);
    }

    if (enable_mach_handler) {
        mach_port_t self_task = mach_task_self();
        kern_return_t kr = mach_port_allocate(self_task, MACH_PORT_RIGHT_RECEIVE, &g_exc_port);
        if (kr == KERN_SUCCESS) {
            kr = mach_port_insert_right(self_task, g_exc_port, g_exc_port, MACH_MSG_TYPE_MAKE_SEND);
        }
        if (kr == KERN_SUCCESS) {
            exception_mask_t mask = EXC_MASK_BAD_ACCESS | EXC_MASK_BAD_INSTRUCTION |
                                    EXC_MASK_ARITHMETIC | EXC_MASK_CRASH;
            kr = task_set_exception_ports(
                self_task,
                mask,
                g_exc_port,
                (exception_behavior_t)(EXCEPTION_DEFAULT | MACH_EXCEPTION_CODES),
                THREAD_STATE_NONE
            );
        }
        if (kr == KERN_SUCCESS) {
            atomic_store(&g_mach_stop, 0);
            if (pthread_create(&g_mach_thread, NULL, mach_exception_loop, NULL) == 0) {
                g_mach_running = 1;
            }
        }
    }
    return 0;
}

void caliper_crash_set_scene(const char *scene) {
    if (!scene || scene[0] == '\0') return;
    size_t n = safe_strlen(scene);
    if (n >= CALIPER_MAX_SCENE) n = CALIPER_MAX_SCENE - 1;
    memcpy(g_scene, scene, n);
    g_scene[n] = '\0';
}

void caliper_crash_uninstall(void) {
    if (atomic_exchange(&g_installed, 0) == 0) {
        return;
    }
    for (size_t i = 0; i < CALIPER_NUM_SIGNALS; i++) {
        sigaction(g_handled_signals[i], &g_prev_actions[i], NULL);
    }
    if (g_mach_running) {
        atomic_store(&g_mach_stop, 1);
        mach_msg_header_t stop_msg;
        memset(&stop_msg, 0, sizeof(stop_msg));
        stop_msg.msgh_bits = MACH_MSGH_BITS(MACH_MSG_TYPE_COPY_SEND, 0);
        stop_msg.msgh_size = sizeof(stop_msg);
        stop_msg.msgh_remote_port = g_exc_port;
        stop_msg.msgh_id = CALIPER_SHUTDOWN_MSG_ID;
        (void)mach_msg(&stop_msg, MACH_SEND_MSG, sizeof(stop_msg), 0, MACH_PORT_NULL, 500, MACH_PORT_NULL);
        pthread_join(g_mach_thread, NULL);
        g_mach_running = 0;
    }
    if (g_exc_port != MACH_PORT_NULL) {
        mach_port_deallocate(mach_task_self(), g_exc_port);
        g_exc_port = MACH_PORT_NULL;
    }
}

bool caliper_is_debugger_attached(void) {
    struct kinfo_proc info;
    memset(&info, 0, sizeof(info));
    int mib[4] = { CTL_KERN, KERN_PROC, KERN_PROC_PID, getpid() };
    size_t sz = sizeof(info);
    if (sysctl(mib, 4, &info, &sz, NULL, 0) != 0) {
        return false;
    }
    return (info.kp_proc.p_flag & P_TRACED) != 0;
}

int64_t caliper_boot_time_sec(void) {
    struct timeval boottime;
    size_t sz = sizeof(boottime);
    int mib[2] = { CTL_KERN, KERN_BOOTTIME };
    if (sysctl(mib, 2, &boottime, &sz, NULL, 0) != 0) {
        return 0;
    }
    return (int64_t)boottime.tv_sec;
}
