import Foundation
import Observation

// Storage Explorer (headroom-spec §9): volumes, a folder/file/type listing
// under a root with filters, and Optimize-mode suggestions. Recipes come
// from groups.yaml's `explorer` section; scans are cancellable.

public enum ExplorerView: String, Sendable, CaseIterable, Identifiable {
    case folders, files, types
    public var id: String { rawValue }
    public var label: String { switch self { case .folders: return "Folders"; case .files: return "Files"; case .types: return "By type" } }
}

/// One row of a listing, with the columns the explorer needs found by name.
public struct ExplorerItem: Sendable, Hashable, Identifiable {
    public var id: String
    public var name: String
    public var path: String?
    public var size: Double
    public var sizeText: String
    public var detail: String?
}

public struct ExplorerSuggestion: Sendable, Identifiable {
    public var id: String { recipe }
    public var recipe: String
    public var doc: String?
    public var table: SysqTable?
    public var error: String?
    public var total: Double
}

@MainActor
@Observable
public final class ExplorerStore {
    public private(set) var volumes: SysqTable?
    public private(set) var purgeable: SysqTable?
    public private(set) var listing: SysqTable?
    public private(set) var items: [ExplorerItem] = []
    public private(set) var suggestions: [ExplorerSuggestion] = []
    public private(set) var scanning = false
    public private(set) var error: String?
    /// The zoom path in the blocks view (breadcrumb).
    public private(set) var breadcrumb: [String] = []

    public var root = "~"
    public var view: ExplorerView = .folders
    public var minBytes: Double = 0
    public var ageDays: Double = 0

    @ObservationIgnored var client: SysqClientProtocol?
    @ObservationIgnored var config: ExplorerConfig?
    @ObservationIgnored var scan: Task<Void, Never>?

    public init() {}

    public func attach(_ c: SysqClientProtocol?, config: ExplorerConfig?) {
        client = c
        self.config = config
    }

    public var roots: [String] { config?.roots ?? ["~"] }
    public var available: Bool { config != nil }

    func binding(_ v: ExplorerView) -> ExplorerBinding? {
        switch v {
        case .folders: return config?.folders
        case .files: return config?.files
        case .types: return config?.by_type
        }
    }

    public func loadVolumes() async {
        guard let client, let c = config else { return }
        volumes = try? await client.runRecipe(c.volumes.recipe, params: c.volumes.params ?? [:]).table
        if let p = c.purgeable { purgeable = try? await client.runRecipe(p.recipe, params: p.params ?? [:]).table }
    }

    /// Rescans with the current root, view and filters (cancels a running scan).
    public func refresh() {
        scan?.cancel()
        guard let client, let b = binding(view) else { error = "This view isn't configured in groups.yaml."; return }
        let params = b.params(root: root, minBytes: minBytes, ageSeconds: ageDays * 86400)
        scanning = true
        error = nil
        breadcrumb = [root]
        scan = Task { [weak self] in
            do {
                let r = try await client.runRecipe(b.recipe, params: params)
                guard !Task.isCancelled, let self else { return }
                self.listing = r.table
                self.items = Self.items(r.table)
                self.error = r.error ?? r.table.meta?.unavailable
            } catch {
                guard !Task.isCancelled, let self else { return }
                self.error = error.localizedDescription
            }
            self?.scanning = false
        }
    }

    public func cancel() {
        scan?.cancel()
        scanning = false
    }

    /// Zooms into a folder (blocks view click).
    public func zoom(into path: String) {
        root = path
        refresh()
    }

    public func loadSuggestions() async {
        guard let client, let list = config?.suggestions else { return }
        var out: [ExplorerSuggestion] = []
        for b in list {
            do {
                let r = try await client.runRecipe(b.recipe, params: b.params(root: root, minBytes: nil, ageSeconds: nil))
                let t = r.table
                let col = Self.sizeColumn(t)
                let total = col.map { c in (0..<t.count).compactMap { t.double($0, c) }.reduce(0, +) } ?? 0
                out.append(ExplorerSuggestion(recipe: b.recipe, table: t, error: r.error, total: total))
            } catch {
                out.append(ExplorerSuggestion(recipe: b.recipe, error: error.localizedDescription, total: 0))
            }
        }
        suggestions = out
    }

    nonisolated static func sizeColumn(_ t: SysqTable) -> String? {
        for n in ["reclaimable", "size", "bytes", "total"] where t.index(of: n) != nil { return n }
        return t.schema.first { $0.type == "bytes" }?.name
    }

    /// Rows → items: name from name/path/type, size from the first size column.
    public nonisolated static func items(_ t: SysqTable) -> [ExplorerItem] {
        guard let sizeCol = sizeColumn(t) else { return [] }
        let pathCol = ["path", "mount"].first { t.index(of: $0) != nil }
        let nameCol = ["name", "type", "model"].first { t.index(of: $0) != nil } ?? pathCol
        let detailCol = ["modified", "last_opened", "kind", "files", "count"].first { t.index(of: $0) != nil }
        return (0..<t.count).map { i in
            let path = pathCol.flatMap { t.string(i, $0) }
            var name = nameCol.map { t.text(i, $0) } ?? "—"
            if nameCol == pathCol, let p = path { name = (p as NSString).lastPathComponent }
            return ExplorerItem(id: "\(i):\(path ?? name)", name: name, path: path, size: t.double(i, sizeCol) ?? 0,
                                sizeText: t.text(i, sizeCol), detail: detailCol.map { t.text(i, $0) })
        }
    }
}

/// Squarified treemap layout (Bruls et al.): rectangles in a w×h box with
/// areas proportional to the values, kept close to square.
public enum Treemap {
    public struct Rect: Sendable, Hashable {
        public var x, y, w, h: Double
        public var index: Int
    }

    public static func layout(_ values: [Double], width: Double, height: Double) -> [Rect] {
        let items = values.enumerated().filter { $0.element > 0 }.sorted { $0.element > $1.element }
        let total = items.reduce(0) { $0 + $1.element }
        guard total > 0, width > 0, height > 0 else { return [] }
        let scale = width * height / total
        var out: [Rect] = []
        var x = 0.0, y = 0.0, w = width, h = height
        var row: [(Int, Double)] = []

        func worst(_ r: [(Int, Double)], _ side: Double) -> Double {
            let areas = r.map { $0.1 * scale }
            let s = areas.reduce(0, +)
            guard let mx = areas.max(), let mn = areas.min(), s > 0, mn > 0 else { return .infinity }
            return max(side * side * mx / (s * s), (s * s) / (side * side * mn))
        }
        func place(_ r: [(Int, Double)]) {
            let area = r.reduce(0) { $0 + $1.1 * scale }
            if w >= h {
                let colW = area / h
                var cy = y
                for (i, v) in r { let rh = v * scale / colW; out.append(Rect(x: x, y: cy, w: colW, h: rh, index: i)); cy += rh }
                x += colW; w -= colW
            } else {
                let rowH = area / w
                var cx = x
                for (i, v) in r { let rw = v * scale / rowH; out.append(Rect(x: cx, y: y, w: rw, h: rowH, index: i)); cx += rw }
                y += rowH; h -= rowH
            }
        }
        for (i, v) in items {
            let side = min(w, h)
            if row.isEmpty || worst(row + [(i, v)], side) <= worst(row, side) {
                row.append((i, v))
            } else {
                place(row)
                row = [(i, v)]
            }
        }
        if !row.isEmpty { place(row) }
        return out
    }
}
