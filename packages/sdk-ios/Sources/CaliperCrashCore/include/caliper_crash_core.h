#ifndef CALIPER_CRASH_CORE_H
#define CALIPER_CRASH_CORE_H

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/// Installs async-signal-safe Unix signal handlers (with dedicated 64 KiB sigaltstack)
/// AND a Mach exception port listener thread (EXC_BAD_ACCESS, EXC_BAD_INSTRUCTION,
/// EXC_ARITHMETIC, EXC_CRASH) writing DWARF-compatible slide-relative tombstones to tombstone_dir.
/// Returns 0 on success, non-zero on error.
int caliper_crash_install(const char *tombstone_dir, bool enable_mach_handler);

/// Updates the active scene label stored in static lock-free memory for crash tombstones.
void caliper_crash_set_scene(const char *scene);

/// Cleanly restores previous signal handlers and terminates the Mach exception listener thread.
void caliper_crash_uninstall(void);

/// Returns true if a debugger (LLDB / Xcode) is currently attached to this process (P_TRACED).
bool caliper_is_debugger_attached(void);

/// Returns kernel boot timestamp in seconds (sysctl kern.boottime) for FOOM reboot exclusion.
int64_t caliper_boot_time_sec(void);

#ifdef __cplusplus
}
#endif

#endif // CALIPER_CRASH_CORE_H
