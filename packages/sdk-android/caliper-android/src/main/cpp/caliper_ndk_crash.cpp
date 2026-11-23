#include <jni.h>
#include <unwind.h>

#include <atomic>
#include <cerrno>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <sys/time.h>
#include <ucontext.h>
#include <unistd.h>

namespace {

constexpr size_t kMaxPath = 1024;
constexpr size_t kMaxScene = 128;
constexpr size_t kMaxMaps = 512;
constexpr size_t kMaxFrames = 32;
constexpr size_t kAltStackSize = 64 * 1024;

struct ExecMapEntry {
    uintptr_t start;
    uintptr_t end;
    uint64_t  file_offset;
    char      module[64];
};

// Pre-allocated static state: ZERO heap allocations inside signal handlers.
char g_tombstone_dir[kMaxPath] = {};
char g_scene[kMaxScene] = "unknown";
unsigned char g_alt_stack_mem[kAltStackSize];

ExecMapEntry g_maps[kMaxMaps];
std::atomic<uint32_t> g_map_count{0};
std::atomic<int> g_installed{0};
std::atomic<int> g_crash_written{0};

constexpr int kCrashSignals[] = {SIGSEGV, SIGABRT, SIGBUS, SIGFPE, SIGILL, SIGTRAP};
constexpr size_t kNumCrashSignals = sizeof(kCrashSignals) / sizeof(kCrashSignals[0]);
struct sigaction g_prev_crash_actions[kNumCrashSignals];

// SIGQUIT ANR interception pipe + monitor thread (forwards to ART Signal Catcher!)
struct sigaction g_prev_sigquit_action;
int g_sigquit_pipe[2] = {-1, -1};
pthread_t g_sigquit_thread;
bool g_sigquit_thread_running = false;
std::atomic<int> g_sigquit_stop{0};

JavaVM *g_jvm = nullptr;
jclass g_bridge_class = nullptr;
jmethodID g_on_sigquit_method = nullptr;

// ===== Async-signal-safe POSIX formatting helpers =====

size_t safe_strlen(const char *s) {
    if (!s) return 0;
    size_t n = 0;
    while (s[n] != '\0') n++;
    return n;
}

void safe_write_all(int fd, const char *buf, size_t len) {
    size_t off = 0;
    while (off < len) {
        ssize_t n = write(fd, buf + off, len - off);
        if (n <= 0) {
            if (n < 0 && errno == EINTR) continue;
            break;
        }
        off += static_cast<size_t>(n);
    }
}

void safe_write_str(int fd, const char *s) {
    if (!s) return;
    safe_write_all(fd, s, safe_strlen(s));
}

void safe_write_hex(int fd, uint64_t val) {
    static constexpr char kHex[] = "0123456789abcdef";
    char buf[20];
    buf[0] = '0';
    buf[1] = 'x';
    char rev[16];
    int i = 0;
    if (val == 0) {
        rev[i++] = '0';
    } else {
        while (val > 0 && i < 16) {
            rev[i++] = kHex[val & 0xF];
            val >>= 4;
        }
    }
    int out = 2;
    while (i > 0) buf[out++] = rev[--i];
    safe_write_all(fd, buf, static_cast<size_t>(out));
}

void safe_write_dec(int fd, int64_t val) {
    char buf[24];
    int pos = 0;
    uint64_t u;
    if (val < 0) {
        buf[pos++] = '-';
        u = static_cast<uint64_t>(-(val + 1)) + 1;
    } else {
        u = static_cast<uint64_t>(val);
    }
    char rev[20];
    int i = 0;
    if (u == 0) {
        rev[i++] = '0';
    } else {
        while (u > 0 && i < 20) {
            rev[i++] = static_cast<char>('0' + (u % 10));
            u /= 10;
        }
    }
    while (i > 0) buf[pos++] = rev[--i];
    safe_write_all(fd, buf, static_cast<size_t>(pos));
}

const char *signal_name(int sig) {
    switch (sig) {
        case SIGSEGV: return "SIGSEGV";
        case SIGABRT: return "SIGABRT";
        case SIGBUS:  return "SIGBUS";
        case SIGFPE:  return "SIGFPE";
        case SIGILL:  return "SIGILL";
        case SIGTRAP: return "SIGTRAP";
        case SIGQUIT: return "SIGQUIT";
        default:      return "SIGUNKNOWN";
    }
}

// ===== Async-signal-safe /proc/self/maps parser (ZERO malloc, ZERO fopen/sscanf) =====

uint64_t parse_hex_span(const char *s, size_t len, size_t *consumed) {
    uint64_t val = 0;
    size_t i = 0;
    while (i < len) {
        char c = s[i];
        uint64_t d;
        if (c >= '0' && c <= '9') d = static_cast<uint64_t>(c - '0');
        else if (c >= 'a' && c <= 'f') d = static_cast<uint64_t>(c - 'a' + 10);
        else if (c >= 'A' && c <= 'F') d = static_cast<uint64_t>(c - 'A' + 10);
        else break;
        val = (val << 4) | d;
        i++;
    }
    if (consumed) *consumed = i;
    return val;
}

void parse_maps_line(const char *line, size_t len, uint32_t &count) {
    if (count >= kMaxMaps || len < 16) return;
    size_t pos = 0, n = 0;
    uint64_t start = parse_hex_span(line, len, &n);
    if (n == 0 || pos + n >= len || line[pos + n] != '-') return;
    pos += n + 1;
    uint64_t end = parse_hex_span(line + pos, len - pos, &n);
    if (n == 0 || pos + n >= len || line[pos + n] != ' ') return;
    pos += n + 1;
    // Perms: e.g. "r-xp" -> check executable bit [2] == 'x'
    if (pos + 4 >= len || line[pos + 2] != 'x') return;
    pos += 4;
    while (pos < len && line[pos] == ' ') pos++;
    uint64_t file_off = parse_hex_span(line + pos, len - pos, &n);
    if (n == 0) return;
    pos += n;

    // Skip device (xx:xx) and inode fields to reach optional pathname.
    for (int field = 0; field < 2; field++) {
        while (pos < len && line[pos] == ' ') pos++;
        while (pos < len && line[pos] != ' ') pos++;
    }
    while (pos < len && line[pos] == ' ') pos++;

    ExecMapEntry &e = g_maps[count++];
    e.start = static_cast<uintptr_t>(start);
    e.end = static_cast<uintptr_t>(end);
    e.file_offset = file_off;

    const char *base = "anonymous";
    size_t base_len = 9;
    if (pos < len) {
        // Trim trailing CR/LF/spaces
        size_t end_pos = len;
        while (end_pos > pos && (line[end_pos - 1] == '\r' || line[end_pos - 1] == '\n' || line[end_pos - 1] == ' ')) {
            end_pos--;
        }
        size_t slash = pos;
        bool found_slash = false;
        for (size_t k = pos; k < end_pos; k++) {
            if (line[k] == '/') {
                slash = k;
                found_slash = true;
            }
        }
        size_t name_start = found_slash ? (slash + 1) : pos;
        if (end_pos > name_start) {
            base = line + name_start;
            base_len = end_pos - name_start;
        }
    }
    if (base_len >= sizeof(e.module)) base_len = sizeof(e.module) - 1;
    memcpy(e.module, base, base_len);
    e.module[base_len] = '\0';
}

// Fully async-signal-safe: uses raw POSIX open/read/close and static buffers only.
// Re-runs safely inside signal handler so dynamically dlopen'd .so libraries are always mapped!
void refresh_proc_maps_async_safe() {
    static char read_buf[4096];
    static char line_buf[512];
    int fd = open("/proc/self/maps", O_RDONLY);
    if (fd < 0) return;

    uint32_t count = 0;
    size_t line_len = 0;
    for (;;) {
        ssize_t r = read(fd, read_buf, sizeof(read_buf));
        if (r < 0) {
            if (errno == EINTR) continue;
            break;
        }
        if (r == 0) break;
        for (ssize_t i = 0; i < r; i++) {
            char c = read_buf[i];
            if (c == '\n') {
                parse_maps_line(line_buf, line_len, count);
                line_len = 0;
                if (count >= kMaxMaps) break;
            } else if (line_len < sizeof(line_buf) - 1) {
                line_buf[line_len++] = c;
            }
        }
        if (count >= kMaxMaps) break;
    }
    if (line_len > 0 && count < kMaxMaps) {
        parse_maps_line(line_buf, line_len, count);
    }
    close(fd);
    g_map_count.store(count, std::memory_order_release);
}

void refresh_proc_maps() {
    refresh_proc_maps_async_safe();
}

const ExecMapEntry *find_map_for_pc(uintptr_t pc) {
    uint32_t n = g_map_count.load(std::memory_order_acquire);
    if (n > kMaxMaps) n = kMaxMaps;
    for (uint32_t i = 0; i < n; i++) {
        if (pc >= g_maps[i].start && pc < g_maps[i].end) {
            return &g_maps[i];
        }
    }
    return nullptr;
}

// ===== _Unwind_Backtrace collector =====

struct UnwindState {
    uintptr_t *frames;
    size_t     count;
    size_t     max_frames;
};

_Unwind_Reason_Code unwind_cb(struct _Unwind_Context *ctx, void *arg) {
    auto *st = static_cast<UnwindState *>(arg);
    uintptr_t pc = _Unwind_GetIP(ctx);
    if (pc != 0) {
        if (st->count >= st->max_frames) {
            return _URC_END_OF_STACK;
        }
        st->frames[st->count++] = pc;
    }
    return _URC_NO_REASON;
}

// ===== Async-signal-safe Tombstone Writer =====

void write_tombstone_file(
    const char *prefix,
    const char *mechanism,
    const char *exception_name,
    int64_t code,
    uint64_t fault_addr,
    const uintptr_t *frames,
    size_t frame_count
) {
    if (g_tombstone_dir[0] == '\0') return;
    struct timeval tv;
    gettimeofday(&tv, nullptr);
    int64_t ts_ms = static_cast<int64_t>(tv.tv_sec) * 1000 + static_cast<int64_t>(tv.tv_usec / 1000);

    char path[kMaxPath + 64];
    size_t dlen = safe_strlen(g_tombstone_dir);
    if (dlen >= kMaxPath - 1) return;
    memcpy(path, g_tombstone_dir, dlen);
    size_t pos = dlen;
    if (pos > 0 && path[pos - 1] != '/') path[pos++] = '/';

    size_t plen = safe_strlen(prefix);
    memcpy(path + pos, prefix, plen);
    pos += plen;

    char rev[20];
    int ni = 0;
    uint64_t u = static_cast<uint64_t>(ts_ms > 0 ? ts_ms : 1);
    while (u > 0 && ni < 20) {
        rev[ni++] = static_cast<char>('0' + (u % 10));
        u /= 10;
    }
    while (ni > 0) path[pos++] = rev[--ni];

    static constexpr char kSuffix[] = ".tombstone";
    memcpy(path + pos, kSuffix, sizeof(kSuffix)); // includes '\0'

    int fd = open(path, O_WRONLY | O_CREAT | O_TRUNC, 0600);
    if (fd < 0) return;

    safe_write_str(fd, "format=caliper_native_crash_v1\n");
    safe_write_str(fd, "mechanism=");
    safe_write_str(fd, mechanism);
    safe_write_str(fd, "\n");
    safe_write_str(fd, "exception=");
    safe_write_str(fd, exception_name);
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

    for (size_t i = 0; i < frame_count && i < kMaxFrames; i++) {
        uintptr_t pc = frames[i];
        if (pc == 0) continue;
        const ExecMapEntry *m = find_map_for_pc(pc);
        safe_write_str(fd, "frame=");
        if (m) {
            uint64_t rel = static_cast<uint64_t>(pc - m->start) + m->file_offset;
            safe_write_str(fd, m->module);
            safe_write_str(fd, "||");
            safe_write_hex(fd, rel);
            safe_write_str(fd, "|");
            safe_write_hex(fd, static_cast<uint64_t>(pc));
        } else {
            safe_write_str(fd, "unknown||");
            safe_write_hex(fd, static_cast<uint64_t>(pc));
            safe_write_str(fd, "|");
            safe_write_hex(fd, static_cast<uint64_t>(pc));
        }
        safe_write_str(fd, "\n");
    }

    fsync(fd);
    close(fd);
}

// ===== Fatal NDK Signal Handler =====

void caliper_ndk_signal_handler(int sig, siginfo_t *info, void *ucontext) {
    if (g_crash_written.exchange(1, std::memory_order_acq_rel) == 0) {
        // Refresh /proc/self/maps async-signal-safely so late-loaded feature .so libraries resolve!
        refresh_proc_maps_async_safe();
        uintptr_t pcs[kMaxFrames];
        UnwindState st{pcs, 0, kMaxFrames};
#if defined(__aarch64__)
        if (ucontext) {
            auto *uc = static_cast<ucontext_t *>(ucontext);
            uintptr_t crash_pc = static_cast<uintptr_t>(uc->uc_mcontext.pc);
            uintptr_t crash_lr = static_cast<uintptr_t>(uc->uc_mcontext.regs[30]);
            if (crash_pc != 0 && st.count < kMaxFrames) pcs[st.count++] = crash_pc;
            if (crash_lr != 0 && crash_lr != crash_pc && st.count < kMaxFrames) pcs[st.count++] = crash_lr;
        }
#elif defined(__arm__)
        if (ucontext) {
            auto *uc = static_cast<ucontext_t *>(ucontext);
            uintptr_t crash_pc = static_cast<uintptr_t>(uc->uc_mcontext.arm_pc);
            uintptr_t crash_lr = static_cast<uintptr_t>(uc->uc_mcontext.arm_lr);
            if (crash_pc != 0 && st.count < kMaxFrames) pcs[st.count++] = crash_pc;
            if (crash_lr != 0 && crash_lr != crash_pc && st.count < kMaxFrames) pcs[st.count++] = crash_lr;
        }
#elif defined(__x86_64__)
        if (ucontext) {
            auto *uc = static_cast<ucontext_t *>(ucontext);
            uintptr_t crash_pc = static_cast<uintptr_t>(uc->uc_mcontext.gregs[REG_RIP]);
            if (crash_pc != 0 && st.count < kMaxFrames) pcs[st.count++] = crash_pc;
        }
#else
        (void)ucontext;
#endif
        _Unwind_Backtrace(unwind_cb, &st);
        uint64_t fault_addr = info ? static_cast<uint64_t>(reinterpret_cast<uintptr_t>(info->si_addr)) : 0;
        int64_t code = info ? static_cast<int64_t>(info->si_code) : 0;
        write_tombstone_file("ndk-crash-", "signal", signal_name(sig), code, fault_addr, pcs, st.count);
    }

    for (size_t i = 0; i < kNumCrashSignals; i++) {
        if (kCrashSignals[i] == sig) {
            sigaction(sig, &g_prev_crash_actions[i], nullptr);
            break;
        }
    }
    raise(sig);
}

// ===== SIGQUIT ANR Interceptor + Non-Blocking Monitor Thread =====

void caliper_sigquit_handler(int sig, siginfo_t *info, void *ucontext) {
    if (g_sigquit_pipe[1] >= 0) {
        char b = 1;
        (void)write(g_sigquit_pipe[1], &b, 1);
    }
    // Always forward SIGQUIT to ART's Signal Catcher so /data/anr/traces.txt remains intact!
    if ((g_prev_sigquit_action.sa_flags & SA_SIGINFO) && g_prev_sigquit_action.sa_sigaction != nullptr) {
        g_prev_sigquit_action.sa_sigaction(sig, info, ucontext);
    } else if (g_prev_sigquit_action.sa_handler != SIG_DFL && g_prev_sigquit_action.sa_handler != SIG_IGN && g_prev_sigquit_action.sa_handler != nullptr) {
        g_prev_sigquit_action.sa_handler(sig);
    }
}

void *sigquit_monitor_loop(void *) {
    while (!g_sigquit_stop.load(std::memory_order_acquire)) {
        char b = 0;
        ssize_t n = read(g_sigquit_pipe[0], &b, 1);
        if (n <= 0) {
            if (n < 0 && errno == EINTR) continue;
            break;
        }
        if (g_sigquit_stop.load(std::memory_order_acquire) || b == 0) {
            break;
        }

        // Write lightweight SIGQUIT ANR tombstone in case the process is killed before JS/Java upload.
        write_tombstone_file("ndk-anr-", "sigquit", "SIGQUIT", 0, 0, nullptr, 0);

        // Notify Kotlin bridge to capture live main-thread stack + Looper MessageQueue history.
        if (g_jvm && g_bridge_class && g_on_sigquit_method) {
            JNIEnv *env = nullptr;
            bool attached = false;
            if (g_jvm->GetEnv(reinterpret_cast<void **>(&env), JNI_VERSION_1_6) != JNI_OK) {
                if (g_jvm->AttachCurrentThread(&env, nullptr) == JNI_OK) {
                    attached = true;
                }
            }
            if (env) {
                struct timeval tv;
                gettimeofday(&tv, nullptr);
                jlong ts_ms = static_cast<jlong>(tv.tv_sec) * 1000LL + static_cast<jlong>(tv.tv_usec / 1000LL);
                env->CallStaticVoidMethod(g_bridge_class, g_on_sigquit_method, ts_ms);
                if (env->ExceptionCheck()) env->ExceptionClear();
            }
            if (attached) {
                g_jvm->DetachCurrentThread();
            }
        }
    }
    return nullptr;
}

} // namespace

extern "C" {

JNIEXPORT jint JNICALL JNI_OnLoad(JavaVM *vm, void *) {
    g_jvm = vm;
    return JNI_VERSION_1_6;
}

JNIEXPORT jint JNICALL
Java_io_caliper_apm_android_CaliperNativeBridge_nativeInstall(
    JNIEnv *env,
    jclass clazz,
    jstring tombstoneDir,
    jboolean enableSigQuitAnr
) {
    if (!tombstoneDir) return -1;
    if (g_installed.exchange(1, std::memory_order_acq_rel) != 0) {
        return 0;
    }
    g_crash_written.store(0, std::memory_order_release);

    const char *dir = env->GetStringUTFChars(tombstoneDir, nullptr);
    if (!dir) {
        g_installed.store(0, std::memory_order_release);
        return -2;
    }
    strncpy(g_tombstone_dir, dir, kMaxPath - 1);
    g_tombstone_dir[kMaxPath - 1] = '\0';
    env->ReleaseStringUTFChars(tombstoneDir, dir);

    if (!g_bridge_class) {
        g_bridge_class = static_cast<jclass>(env->NewGlobalRef(clazz));
        g_on_sigquit_method = env->GetStaticMethodID(g_bridge_class, "onSigQuitAnrDetected", "(J)V");
        if (env->ExceptionCheck()) {
            env->ExceptionClear();
            g_on_sigquit_method = nullptr;
        }
    }

    refresh_proc_maps();

    // Install dedicated 64 KiB alternate stack for stack-overflow signal handling.
    stack_t alt{};
    alt.ss_sp = g_alt_stack_mem;
    alt.ss_size = kAltStackSize;
    alt.ss_flags = 0;
    (void)sigaltstack(&alt, nullptr);

    struct sigaction sa{};
    sa.sa_sigaction = caliper_ndk_signal_handler;
    sigemptyset(&sa.sa_mask);
    sa.sa_flags = SA_SIGINFO | SA_ONSTACK;
    for (size_t i = 0; i < kNumCrashSignals; i++) {
        sigaction(kCrashSignals[i], &sa, &g_prev_crash_actions[i]);
    }

    if (enableSigQuitAnr && pipe(g_sigquit_pipe) == 0) {
        int flags = fcntl(g_sigquit_pipe[1], F_GETFL, 0);
        if (flags >= 0) {
            fcntl(g_sigquit_pipe[1], F_SETFL, flags | O_NONBLOCK);
        }
        g_sigquit_stop.store(0, std::memory_order_release);
        if (pthread_create(&g_sigquit_thread, nullptr, sigquit_monitor_loop, nullptr) == 0) {
            g_sigquit_thread_running = true;
            struct sigaction qsa{};
            qsa.sa_sigaction = caliper_sigquit_handler;
            sigemptyset(&qsa.sa_mask);
            qsa.sa_flags = SA_SIGINFO | SA_ONSTACK;
            sigaction(SIGQUIT, &qsa, &g_prev_sigquit_action);
        }
    }
    return 0;
}

JNIEXPORT void JNICALL
Java_io_caliper_apm_android_CaliperNativeBridge_nativeSetScene(
    JNIEnv *env,
    jclass,
    jstring scene
) {
    if (!scene) return;
    const char *s = env->GetStringUTFChars(scene, nullptr);
    if (!s) return;
    size_t n = safe_strlen(s);
    if (n >= kMaxScene) n = kMaxScene - 1;
    memcpy(g_scene, s, n);
    g_scene[n] = '\0';
    env->ReleaseStringUTFChars(scene, s);
}

JNIEXPORT void JNICALL
Java_io_caliper_apm_android_CaliperNativeBridge_nativeRefreshMaps(JNIEnv *, jclass) {
    refresh_proc_maps();
}

JNIEXPORT void JNICALL
Java_io_caliper_apm_android_CaliperNativeBridge_nativeUninstall(JNIEnv *, jclass) {
    if (g_installed.exchange(0, std::memory_order_acq_rel) == 0) return;
    for (size_t i = 0; i < kNumCrashSignals; i++) {
        sigaction(kCrashSignals[i], &g_prev_crash_actions[i], nullptr);
    }
    if (g_sigquit_thread_running) {
        sigaction(SIGQUIT, &g_prev_sigquit_action, nullptr);
        g_sigquit_stop.store(1, std::memory_order_release);
        if (g_sigquit_pipe[1] >= 0) {
            char zero = 0;
            (void)write(g_sigquit_pipe[1], &zero, 1);
        }
        pthread_join(g_sigquit_thread, nullptr);
        g_sigquit_thread_running = false;
    }
    if (g_sigquit_pipe[0] >= 0) { close(g_sigquit_pipe[0]); g_sigquit_pipe[0] = -1; }
    if (g_sigquit_pipe[1] >= 0) { close(g_sigquit_pipe[1]); g_sigquit_pipe[1] = -1; }
}

} // extern "C"
