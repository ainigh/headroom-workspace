import Foundation
import Observation

/// What sysqd needs from the Mac that only the user can grant: a NOPASSWD
/// sudoers rule per sudo primitive (powermetrics), and network primitives
/// enabled in sysq's policy.yaml. Read from sysqd's admin status; nothing
/// here changes the system — the permissions view shows what to add.
@MainActor
@Observable
public final class SetupStore {
    public struct SudoRule: Sendable, Hashable, Identifiable {
        public var id: String { primitive }
        public var primitive: String
        /// The exact command the rule must allow, e.g. "/usr/bin/powermetrics --samplers thermal -n 1 -i 1000".
        public var command: String
        public var allowed: Bool
    }

    public private(set) var sudo: [SudoRule] = []
    public private(set) var networkPrimitives: [String] = []
    public private(set) var fixtures = false
    public private(set) var loaded = false
    public private(set) var error: String?
    @ObservationIgnored let admin: SysqAdminProtocol

    public init(admin: SysqAdminProtocol) { self.admin = admin }

    /// Every sudo rule is in place.
    public var sudoReady: Bool { sudo.allSatisfy(\.allowed) }

    public func refresh(recheck: Bool = false) async {
        do {
            apply(try await admin.status(recheck: recheck))
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
        loaded = true
    }

    func apply(_ s: JSONValue) {
        let allowed = s["sudo"]?.object ?? [:]
        let rules = s["sudo_rules"]?.object ?? [:]
        let names = Set(allowed.keys).union(rules.keys).sorted()
        sudo = names.map { n in
            SudoRule(primitive: n, command: rules[n]?.string ?? "", allowed: allowed[n]?.bool ?? false)
        }
        networkPrimitives = s["network_primitives"]?.array?.compactMap(\.string) ?? []
        fixtures = s["fixtures"]?.bool == true
    }

    /// The sudoers line covering every rule still missing (all of them when
    /// `all`), for `sudo visudo -f /etc/sudoers.d/sysq`. Commas inside a
    /// command are escaped, as sudoers requires.
    public func sudoersLine(user: String, all: Bool = false) -> String? {
        let cmds = sudo.filter { (all || !$0.allowed) && !$0.command.isEmpty }.map { $0.command.replacingOccurrences(of: ",", with: "\\,") }
        guard !cmds.isEmpty else { return nil }
        return "\(user) ALL=(root) NOPASSWD: " + cmds.joined(separator: ", ")
    }
}
