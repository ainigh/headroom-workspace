import Foundation

/// The bits of a GitHub release the updater needs (GET /repos/{owner}/{repo}/releases/latest).
public struct Release: Decodable, Equatable, Sendable {
    public struct Asset: Decodable, Equatable, Sendable {
        public let name: String
        public let browserDownloadURL: URL

        enum CodingKeys: String, CodingKey {
            case name
            case browserDownloadURL = "browser_download_url"
        }

        public init(name: String, browserDownloadURL: URL) {
            self.name = name
            self.browserDownloadURL = browserDownloadURL
        }
    }

    public let tag: String
    /// The commit (or branch) the release was made from; CI passes the commit's sha.
    public let target: String?
    public let notes: String?
    public let page: URL?
    public let assets: [Asset]

    enum CodingKeys: String, CodingKey {
        case tag = "tag_name"
        case target = "target_commitish"
        case notes = "body"
        case page = "html_url"
        case assets
    }

    public static func decode(_ data: Data) throws -> Release {
        try JSONDecoder().decode(Release.self, from: data)
    }

    public func asset(named name: String) -> Asset? { assets.first { $0.name == name } }
}

/// The newest commit on a branch (GET /repos/{owner}/{repo}/commits/{branch}).
public struct Commit: Decodable, Equatable, Sendable {
    public let sha: String
    public let message: String

    struct Details: Decodable { let message: String }

    enum CodingKeys: String, CodingKey { case sha, commit }

    public init(sha: String, message: String) {
        self.sha = sha
        self.message = message
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        sha = try c.decode(String.self, forKey: .sha)
        message = try c.decode(Details.self, forKey: .commit).message
    }

    public static func decode(_ data: Data) throws -> Commit {
        try JSONDecoder().decode(Commit.self, from: data)
    }

    public var short: String { String(sha.prefix(7)) }
    /// The first line of the message.
    public var title: String { message.components(separatedBy: "\n").first ?? message }
}
