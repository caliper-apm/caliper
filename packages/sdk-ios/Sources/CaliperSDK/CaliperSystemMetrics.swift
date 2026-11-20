import Foundation
import Darwin

/// Public Darwin/Mach process counters — zero private APIs, zero thread suspension.
public enum CaliperSystemMetrics {
    /// Returns process CPU time (user + system) in milliseconds via getrusage(RUSAGE_SELF).
    public static func processCPUMilliseconds() -> Double? {
        var usage = rusage()
        guard getrusage(RUSAGE_SELF, &usage) == 0 else { return nil }
        let userMS = Double(usage.ru_utime.tv_sec) * 1000.0 + Double(usage.ru_utime.tv_usec) / 1000.0
        let sysMS = Double(usage.ru_stime.tv_sec) * 1000.0 + Double(usage.ru_stime.tv_usec) / 1000.0
        return userMS + sysMS
    }

    /// Returns Apple's canonical resident memory footprint (phys_footprint) in KiB via task_info(TASK_VM_INFO).
    public static func physicalFootprintKiB() -> Double? {
        var info = task_vm_info_data_t()
        var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<integer_t>.size)
        let kr: kern_return_t = withUnsafeMutablePointer(to: &info) { ptr in
            ptr.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { rebound in
                task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), rebound, &count)
            }
        }
        guard kr == KERN_SUCCESS else { return nil }
        return Double(info.phys_footprint) / 1024.0
    }
}
