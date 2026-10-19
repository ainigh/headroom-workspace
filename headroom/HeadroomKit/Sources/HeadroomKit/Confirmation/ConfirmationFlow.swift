import Foundation
import Observation

// The only path to SysactClient.execute (implementation.md §5.4,
// separation-of-concerns §6): a person sees one sheet with the exact action,
// params, effect, tier, undo note and (for the assistant) its explanation,
// and confirms after a short delay. The token proving that is constructible
// only in this file.

/// Proof that a person confirmed one specific plan. Its initializer is
/// fileprivate: nothing outside this file — no other type in HeadroomKit,
/// no app code, no test — can make one. Tests/ConfirmationTokenTests.swift
/// and scripts/check-confirmation-token.sh enforce that.
public struct ConfirmationToken: Sendable, Hashable {
    public let planID: String
    public let planHash: String
    public let confirmedAt: Date
    public let nonce: String
    public let via: String

    fileprivate init(plan: ActionPlan, via: String, at: Date) {
        planID = plan.plan_id
        planHash = plan.plan_hash
        confirmedAt = at
        self.via = via.isEmpty ? "headroom-ui" : via
        var rng = SystemRandomNumberGenerator()
        nonce = (0..<16).map { _ in String(format: "%02x", UInt8.random(in: 0...255, using: &rng)) }.joined()
    }
}

/// Who asked for the action (shown on the sheet; signed as `via`).
public enum ActionRequester: String, Sendable, Hashable {
    case user = "headroom-ui"
    case assistant = "headroom-assistant"

    public var label: String { self == .user ? "You" : "Assistant" }
}

/// What the sheet shows.
public struct ConfirmationRequest: Identifiable, Sendable, Hashable {
    public var id: String { plan.plan_id }
    public var plan: ActionPlan
    public var requestedBy: ActionRequester
    /// The model's explanation (assistant requests only).
    public var explanation: String?
    public var shownAt: Date
    public var delay: Double

    public var enablesAt: Date { shownAt.addingTimeInterval(delay) }
}

@MainActor
@Observable
public final class ConfirmationFlow {
    /// The sheet currently shown, if any. One at a time, never queued.
    public private(set) var current: ConfirmationRequest?
    /// Becomes true once the anti-reflex delay has passed.
    public private(set) var confirmEnabled = false

    /// Seconds the confirm button stays disabled (assistant.yaml
    /// confirm_delay_seconds, at least 1).
    public var delay: Double
    @ObservationIgnored let now: @Sendable () -> Date
    @ObservationIgnored private var continuation: CheckedContinuation<ConfirmationToken?, Never>?
    @ObservationIgnored private var timer: Task<Void, Never>?

    public init(delay: Double = 2, now: @escaping @Sendable () -> Date = { Date() }) {
        self.delay = max(1, delay)
        self.now = now
    }

    /// Shows the sheet and waits for the person. Returns a token only when
    /// they confirm; nil when they decline, close it, or another
    /// confirmation is already showing.
    public func request(_ plan: ActionPlan, requestedBy: ActionRequester, explanation: String? = nil) async -> ConfirmationToken? {
        if current != nil { return nil }
        let d = max(1, delay)
        let req = ConfirmationRequest(plan: plan, requestedBy: requestedBy, explanation: explanation, shownAt: now(), delay: d)
        current = req
        confirmEnabled = false
        timer = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(d * 1_000_000_000))
            guard let self, !Task.isCancelled, self.current?.id == req.id else { return }
            self.confirmEnabled = true
        }
        return await withCheckedContinuation { cont in
            continuation = cont
        }
    }

    /// The confirm button. Ignored until the delay has elapsed, whatever the
    /// view does.
    public func confirm() {
        guard let req = current, now() >= req.enablesAt else { return }
        finish(ConfirmationToken(plan: req.plan, via: req.requestedBy.rawValue, at: now()))
    }

    /// Decline, Escape, or closing the sheet.
    public func decline() {
        guard current != nil else { return }
        finish(nil)
    }

    private func finish(_ token: ConfirmationToken?) {
        timer?.cancel()
        timer = nil
        current = nil
        confirmEnabled = false
        let c = continuation
        continuation = nil
        c?.resume(returning: token)
    }
}
