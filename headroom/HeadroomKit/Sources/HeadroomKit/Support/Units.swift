import Foundation

/// Parsing and humanizing of the units Headroom shows (bytes, durations,
/// percentages). Byte units are decimal, as Finder shows them.
public enum Units {
    /// Parses "50GB", "1.5 GiB", "512MB", "50MB/s", "30", "2h", "90s" into a number
    /// (bytes, bytes per second, or seconds). Returns nil for anything else.
    public static func parse(_ s: String) -> Double? {
        var t = s.trimmingCharacters(in: .whitespaces)
        if t.hasSuffix("/s") { t = String(t.dropLast(2)) }
        let lower = t.lowercased()
        let table: [(String, Double)] = [
            ("kib", 1024), ("mib", 1_048_576), ("gib", 1_073_741_824), ("tib", 1_099_511_627_776),
            ("kb", 1e3), ("mb", 1e6), ("gb", 1e9), ("tb", 1e12),
            ("ms", 0.001), ("w", 604_800), ("d", 86_400), ("h", 3_600), ("m", 60), ("s", 1), ("b", 1), ("%", 1),
        ]
        for (suffix, mult) in table where lower.hasSuffix(suffix) {
            let num = lower.dropLast(suffix.count).trimmingCharacters(in: .whitespaces)
            if let v = Double(num) { return v * mult }
        }
        return Double(lower)
    }

    public static func bytes(_ n: Double) -> String {
        var f = abs(n)
        let units = ["B", "KB", "MB", "GB", "TB", "PB"]
        var i = 0
        while f >= 1000, i < units.count - 1 { f /= 1000; i += 1 }
        let sign = n < 0 ? "-" : ""
        if i == 0 { return "\(sign)\(Int(f)) B" }
        return f >= 100 ? "\(sign)\(Int(f.rounded())) \(units[i])" : "\(sign)\(String(format: "%.1f", f)) \(units[i])"
    }

    public static func duration(_ sec: Double) -> String {
        if sec < 0 { return "-" + duration(-sec) }
        if sec < 1 { return "\(Int(sec * 1000)) ms" }
        if sec < 60 { return "\(Int(sec)) s" }
        if sec < 3600 { return "\(Int(sec / 60)) min" }
        if sec < 48 * 3600 { let h = Int(sec / 3600); let m = Int(sec / 60) % 60; return m == 0 ? "\(h) h" : "\(h) h \(m) min" }
        return "\(Int(sec / 86_400)) d"
    }

    public static func percent(_ v: Double) -> String {
        v >= 10 || v == 0 ? "\(Int(v.rounded())) %" : String(format: "%.1f %%", v)
    }

    public static func number(_ v: Double) -> String {
        if v.rounded() == v, abs(v) < 1e12 { return String(Int64(v)) }
        return abs(v) >= 100 ? String(Int64(v.rounded())) : String(format: "%.1f", v)
    }
}
