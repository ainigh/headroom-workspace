import Foundation

/// Where things live (CLAUDE.md: ~/Library/Application Support/{sysq,sysact,Headroom}).
public struct Paths: Sendable {
    public var home: URL
    public var headroom: URL { home.appendingPathComponent("Library/Application Support/Headroom") }
    public var config: URL { headroom.appendingPathComponent("config") }
    public var sysq: URL { home.appendingPathComponent("Library/Application Support/sysq") }
    public var sysact: URL { home.appendingPathComponent("Library/Application Support/sysact") }
    public var sysqAdminSocket: String { sysq.appendingPathComponent("admin.sock").path }
    public var sysactSocket: String { sysact.appendingPathComponent("sysact.sock").path }
    public var sysactHostKey: URL { sysact.appendingPathComponent("host.key") }
    public var logs: URL { home.appendingPathComponent("Library/Logs/Headroom") }

    public init(home: URL = FileManager.default.homeDirectoryForCurrentUser) { self.home = home }
}
