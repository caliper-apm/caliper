#ifndef CALIPER_CRASH_CORE_H
#define CALIPER_CRASH_CORE_H

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/// Slide-relative native frame captured from a suspended thread (e.g. hung main thread).
typedef struct {
    char module[64];
    char uuid[40];
    char symbol[128];
    uint64_t relative_addr;
    uint64_t absolute_addr;
} caliper_captured_frame_t;

/// Installs async-signal-safe Unix signal handlers (with dedicated 64 KiB sigaltstack)
/// AND a Mach exception port listener thread (EXC_BAD_ACCESS, EXC_BAD_INSTRUCTION,
/// EXC_ARITHMETIC, EXC_CRASH) writing DWARF-compatible slide-relative tombstones to tombstone_dir.
/// Returns 0 on success, non-zero on error.
int caliper_crash_install(const char *tombstone_dir, bool enable_mach_handler);

/// Updates the active scene label stored in static lock-free memory for crash tombstones.
void caliper_crash_set_scene(const char *scene);

/// Cleanly restores previous signal handlers and terminates the Mach exception listener thread.
void caliper_crash_uninstall(void);

/// Writes a DWARF-compatible crash tombstone for a high-level fatal exception (such as
/// Objective-C NSException) and atomically claims the crash write slot so any subsequent
/// abort()/SIGABRT does not emit a duplicate tombstone.
/// Returns 0 if written, 1 if another crash was already recorded, or <0 on invalid input.
int caliper_crash_write_exception_tombstone(
    const char *exception_name,
    const char *reason,
    const uintptr_t *frames,
    int frame_count
);

/// Registers the calling thread as the process main thread (if pthread_main_np() != 0)
/// and ensures the lock-free dyld Mach-O image cache is initialized.
void caliper_register_main_thread(void);

/// Safely suspends the registered main thread (never suspending the calling thread itself),
/// captures its Mach register + vm_read_overwrite-protected FP call stack, immediately resumes
/// the main thread, and resolves slide-relative Mach-O offsets + UUIDs + optional dladdr symbols.
/// Returns the number of frames written into out_frames (0 if unavailable or called on main thread).
int caliper_capture_main_thread_frames(caliper_captured_frame_t *out_frames, int max_frames);

/// Safely captures frames for an explicit Mach thread port (never suspending the calling thread).
/// Primarily useful for deterministic testing and cross-thread stall attribution.
int caliper_capture_thread_frames_safe(
    uint32_t mach_thread_port,
    caliper_captured_frame_t *out_frames,
    int max_frames
);

/// Ultra-low-overhead Mach sampler for CPU flamegraphs: suspends the registered main thread,
/// captures raw PC addresses via thread_get_state + vm_read_overwrite, and resumes immediately
/// with ZERO symbol lookup / ZERO dladdr inside the sampling loop.
int caliper_capture_main_thread_pcs(uintptr_t *out_pcs, int max_frames);

/// Resolves a single captured PC address into slide-relative Mach-O module/UUID/offset and
/// optional dladdr symbol name (designed to run once per unique PC after profiling completes).
void caliper_resolve_pc_frame(
    uintptr_t pc,
    bool resolve_dladdr,
    caliper_captured_frame_t *out_frame
);

/// Returns true if a debugger (LLDB / Xcode) is currently attached to this process (P_TRACED).
bool caliper_is_debugger_attached(void);

/// Returns kernel boot timestamp in seconds (sysctl kern.boottime) for FOOM reboot exclusion.
int64_t caliper_boot_time_sec(void);

/// Returns process creation timestamp in epoch milliseconds (sysctl KERN_PROC_PID kp_proc.p_starttime)
/// for single-anchor cold-start stage conservation. Returns 0 if unavailable.
int64_t caliper_process_start_time_ms(void);

#ifdef __cplusplus
}
#endif

#endif // CALIPER_CRASH_CORE_H
