import Foundation
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

/// Sends HTTP requests (a seam for tests: every client takes one).
public protocol HTTPTransport: Sendable {
    func send(_ request: URLRequest) async throws -> (Data, Int)
}

public struct ClientError: Error, LocalizedError, Sendable, Equatable {
    public enum Kind: String, Sendable { case unreachable, unauthorized, forbidden, badRequest, failed, decoding, notRunning }
    public var kind: Kind
    public var status: Int
    public var message: String
    public var errorDescription: String? { message }

    public init(_ kind: Kind, status: Int = 0, _ message: String) {
        self.kind = kind; self.status = status; self.message = message
    }
}

/// URLSession transport with a per-request timeout.
public final class URLSessionTransport: HTTPTransport, @unchecked Sendable {
    let session: URLSession

    public init(timeout: TimeInterval = 60) {
        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = timeout
        cfg.timeoutIntervalForResource = timeout * 2
        cfg.connectionProxyDictionary = [:] // loopback only; never a proxy
        session = URLSession(configuration: cfg)
    }

    public func send(_ request: URLRequest) async throws -> (Data, Int) {
        try await withCheckedThrowingContinuation { cont in
            let task = session.dataTask(with: request) { data, response, error in
                if let error {
                    cont.resume(throwing: ClientError(.unreachable, "\(request.url?.host ?? "server"): \(error.localizedDescription)"))
                    return
                }
                let status = (response as? HTTPURLResponse)?.statusCode ?? 0
                cont.resume(returning: (data ?? Data(), status))
            }
            task.resume()
        }
    }
}

/// A server-sent-events stream over URLSession (sysqd's /v1/subscribe and /v1/alerts).
public final class SSEStream: NSObject, URLSessionDataDelegate, @unchecked Sendable {
    public struct Event: Sendable {
        public var event: String
        public var data: String
    }

    private var buffer = Data()
    private var event = "message"
    private var dataLines: [String] = []
    private let continuation: AsyncThrowingStream<Event, Error>.Continuation
    private var session: URLSession?
    private var task: URLSessionDataTask?
    private var status = 0

    /// Opens a stream; cancel by dropping the stream (onTermination).
    public static func open(_ request: URLRequest) -> AsyncThrowingStream<Event, Error> {
        AsyncThrowingStream { cont in
            let s = SSEStream(cont)
            let cfg = URLSessionConfiguration.ephemeral
            cfg.timeoutIntervalForRequest = 120
            cfg.timeoutIntervalForResource = 7 * 24 * 3600
            cfg.connectionProxyDictionary = [:]
            let session = URLSession(configuration: cfg, delegate: s, delegateQueue: nil)
            s.session = session
            let t = session.dataTask(with: request)
            s.task = t
            cont.onTermination = { _ in
                t.cancel()
                session.invalidateAndCancel()
            }
            t.resume()
        }
    }

    private init(_ c: AsyncThrowingStream<Event, Error>.Continuation) { continuation = c }

    public func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive response: URLResponse, completionHandler: @escaping (URLSession.ResponseDisposition) -> Void) {
        status = (response as? HTTPURLResponse)?.statusCode ?? 0
        completionHandler(.allow)
    }

    public func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        if status >= 400 {
            buffer.append(data)
            return
        }
        buffer.append(data)
        while let i = buffer.firstIndex(of: 0x0a) {
            var line = String(decoding: buffer[buffer.startIndex..<i], as: UTF8.self)
            buffer.removeSubrange(buffer.startIndex...i)
            if line.hasSuffix("\r") { line.removeLast() }
            if line.isEmpty {
                if !dataLines.isEmpty {
                    continuation.yield(Event(event: event, data: dataLines.joined(separator: "\n")))
                }
                event = "message"
                dataLines = []
            } else if line.hasPrefix(":") {
                continue
            } else if line.hasPrefix("event:") {
                event = line.dropFirst(6).trimmingCharacters(in: .whitespaces)
            } else if line.hasPrefix("data:") {
                dataLines.append(String(line.dropFirst(5)).trimmingCharacters(in: .init(charactersIn: " ")))
            }
        }
    }

    public func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: Error?) {
        if status >= 400 {
            let msg = String(decoding: buffer, as: UTF8.self)
            continuation.finish(throwing: ClientError(status == 401 ? .unauthorized : status == 403 ? .forbidden : .failed, status: status, msg))
        } else if let error {
            continuation.finish(throwing: ClientError(.unreachable, error.localizedDescription))
        } else {
            continuation.finish()
        }
        session.finishTasksAndInvalidate()
    }
}

extension JSONDecoder {
    static let api: JSONDecoder = {
        let d = JSONDecoder()
        return d
    }()
}
