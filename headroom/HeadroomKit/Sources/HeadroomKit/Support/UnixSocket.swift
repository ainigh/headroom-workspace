import Foundation
#if canImport(Darwin)
import Darwin
#elseif canImport(Glibc)
import Glibc
#endif

/// A blocking unix-domain stream socket (sysact's JSON lines; sysqd's admin
/// HTTP). URLSession can't connect to unix sockets.
public final class UnixSocket {
    public enum Failure: Error, LocalizedError {
        case path(String), connect(String, Int32), io(Int32), closed
        public var errorDescription: String? {
            switch self {
            case .path(let p): return "socket path too long: \(p)"
            case .connect(let p, let e): return "can't connect to \(p): \(String(cString: strerror(e)))"
            case .io(let e): return "socket error: \(String(cString: strerror(e)))"
            case .closed: return "connection closed"
            }
        }
    }

    private let fd: Int32
    private var buffer = [UInt8]()

    public init(path: String, timeout: TimeInterval = 30) throws {
        #if canImport(Darwin)
        fd = socket(AF_UNIX, SOCK_STREAM, 0)
        #else
        fd = socket(AF_UNIX, Int32(SOCK_STREAM.rawValue), 0)
        #endif
        guard fd >= 0 else { throw Failure.io(errno) }
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8)
        let cap = MemoryLayout.size(ofValue: addr.sun_path)
        guard bytes.count < cap else { close(fd); throw Failure.path(path) }
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: bytes)
            raw[bytes.count] = 0
        }
        var tv = timeval(tv_sec: Int(timeout), tv_usec: 0)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard rc == 0 else {
            let e = errno
            close(fd)
            throw Failure.connect(path, e)
        }
    }

    deinit { close(fd) }

    public func write(_ data: Data) throws {
        var sent = 0
        let bytes = [UInt8](data)
        while sent < bytes.count {
            let n = bytes[sent...].withUnsafeBytes { ptr in
                #if canImport(Darwin)
                Darwin.write(fd, ptr.baseAddress, ptr.count)
                #else
                Glibc.write(fd, ptr.baseAddress, ptr.count)
                #endif
            }
            if n <= 0 { throw Failure.io(errno) }
            sent += n
        }
    }

    private func fill() throws {
        var chunk = [UInt8](repeating: 0, count: 65536)
        let n = chunk.withUnsafeMutableBytes { ptr in
            #if canImport(Darwin)
            Darwin.read(fd, ptr.baseAddress, ptr.count)
            #else
            Glibc.read(fd, ptr.baseAddress, ptr.count)
            #endif
        }
        if n < 0 { throw Failure.io(errno) }
        if n == 0 { throw Failure.closed }
        buffer.append(contentsOf: chunk[0..<n])
    }

    /// Reads up to and excluding the next newline.
    public func readLine(limit: Int = 4 << 20) throws -> Data {
        while true {
            if let i = buffer.firstIndex(of: 0x0a) {
                let line = Data(buffer[0..<i])
                buffer.removeSubrange(0...i)
                return line
            }
            if buffer.count > limit { throw Failure.io(EMSGSIZE) }
            try fill()
        }
    }

    /// Reads exactly n bytes.
    public func read(count n: Int) throws -> Data {
        while buffer.count < n { try fill() }
        let out = Data(buffer[0..<n])
        buffer.removeSubrange(0..<n)
        return out
    }

    /// Reads until the peer closes.
    public func readToEnd() throws -> Data {
        while true {
            do { try fill() } catch Failure.closed { break }
        }
        defer { buffer.removeAll() }
        return Data(buffer)
    }
}

/// Minimal HTTP/1.1 over a unix socket, for sysqd's admin endpoints.
public enum UnixHTTP {
    public struct Response: Sendable {
        public let status: Int
        public let body: Data
    }

    public static func request(socket path: String, method: String, path urlPath: String, body: Data? = nil, timeout: TimeInterval = 15) throws -> Response {
        let s = try UnixSocket(path: path, timeout: timeout)
        var head = "\(method) \(urlPath) HTTP/1.1\r\nHost: admin\r\nConnection: close\r\nAccept: application/json\r\n"
        if let body { head += "Content-Type: application/json\r\nContent-Length: \(body.count)\r\n" }
        head += "\r\n"
        var out = Data(head.utf8)
        if let body { out.append(body) }
        try s.write(out)
        let raw = try s.readToEnd()
        guard let sep = raw.range(of: Data("\r\n\r\n".utf8)) else { throw UnixSocket.Failure.closed }
        let header = String(decoding: raw[..<sep.lowerBound], as: UTF8.self)
        let status = Int(header.split(separator: " ").dropFirst().first ?? "") ?? 0
        var payload = Data(raw[sep.upperBound...])
        if header.lowercased().contains("transfer-encoding: chunked") { payload = dechunk(payload) }
        return Response(status: status, body: payload)
    }

    static func dechunk(_ d: Data) -> Data {
        var out = Data()
        var rest = d[...]
        while let lineEnd = rest.range(of: Data("\r\n".utf8)) {
            let sizeText = String(decoding: rest[..<lineEnd.lowerBound], as: UTF8.self)
            guard let size = Int(sizeText.split(separator: ";").first ?? "", radix: 16), size > 0 else { break }
            let start = lineEnd.upperBound
            let end = rest.index(start, offsetBy: size, limitedBy: rest.endIndex) ?? rest.endIndex
            out.append(rest[start..<end])
            rest = rest[min(rest.index(end, offsetBy: 2, limitedBy: rest.endIndex) ?? rest.endIndex, rest.endIndex)...]
        }
        return out
    }
}
