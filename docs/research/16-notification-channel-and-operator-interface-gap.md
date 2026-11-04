# Cross-cutting undecided seams: notifier channel, remote audit sink, operator CLI, and strategy interface contract (explicit ADR candidates)

> **왜 중요한가** — The repo names four seams as undecided or future: notifier channel (ADR-0004 point 5 seam, CLAUDE.md '외부 알림(미정)'), remote audit shipping (ADR-0006 point 5), operator clear CLI (#80, required before any strategy attaches), and the strategy interface/intentId contract (ADR-0002 point 2, internal/strategy stub). This topic lists the decision-driving questions so the research corpus directly feeds the next /architect runs.

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 7건. 상태 집계: 🟡 부분 7, ❌ 미답 1

## 핵심 결론

- (완전 답변된 질문 없음 — 아래 부분 답변 참조)

## 질문별 답변

### T15.Q1 · What is the minimum viable notifier for the kill switch (channel, ack semantics, retry, rate-limit) and should notification failure itself trip a halt or only log (ADR-0004 point 5)?

🟡 부분 · 우선순위 must · 확신 low

This answer uses repo evidence only, because no external sources were collected.

HaltTripped is invoked under the killswitch lock and MUST be non-blocking [repo-killswitch-seam-code]. A network channel therefore needs an async adapter: a bounded queue, a supervised delivery goroutine with backoff, and coalescing or dropping with a counter when the queue overflows. Today the only implementation is an ERROR log [repo-killswitch-seam-code].

ADR-0006 already names three alert producers: trips, the audit disk threshold, and shipping failure [repo-adr-0006]. The seam should therefore be generalised beyond HaltTripped.

Notification failure should not halt. The halt is already durable and fails closed [repo-adr-0004]. Gating on an external delivery would repeat the failure mode that ADR-0006 rejected for remote acks [repo-adr-0006]. A notification failure should instead be logged, audited and surfaced in status.

The channel, the ack/escalation behaviour and the rate limits remain unsourced.

### T15.Q2 · What should the strategy interface look like for a polling bot (tick/bar callback vs scheduled rebalance; inputs: prices, holdings, calendar; outputs: intents with deterministic intentId and exposure tag), and how do existing frameworks shape this contract?

🟡 부분 · 우선순위 must · 확신 low

This covers the repo contract only. No comparison with external frameworks was collected.

The repo contract requires:
- intentId is deterministic and unique, and owned by the strategy [repo-adr-0002].
- order does not import strategy [repo-adr-0002].
- the exposure tag is untrusted and is re-verified fail-closed [repo-adr-0004].

Inference: a pure function from a snapshot (API-re-queried holdings, prices, calendar state, unresolved intents) to []Intent makes intentId derivation deterministic across restarts, for example hash(strategyID, decision epoch, symbol, side). This has not been checked against how established frameworks shape their interfaces.

### T15.Q3 · Operator CLI design (#80): authentication to the running process or direct store access, command set (status, clear-halt with reason, list unresolved intents, force-reconcile), audit emission for operator actions, and preventing automation from invoking it.

🟡 부분 · 우선순위 must · 확신 low

ClearHalt(ctx) exists, but it carries no reason or operator, returns ErrClearDeferred while trips are in flight, and no cmd/ binary calls it [repo-killswitch-seam-code]. A second safety surface is forbidden [repo-adr-0007], so the CLI must drive the same clear transition.

A separate process editing the store cannot update the running process's in-memory mirror (ADR-0013). The design must therefore use one of two paths: local IPC to the live process, or an offline edit followed by a restart.

Candidate commands:
- status
- clear-halt --reason, retrying on ErrClearDeferred
- list-unresolved-intents
- force-reconcile, which is safe because the reconciler is query-only [repo-adr-0003]

Each operator action should emit an audit event. How to prevent automation from invoking the CLI (peer credentials, TTY confirmation) is unsourced.

### T15.Q4 · Remote audit sink choice (S3-compatible object storage, Loki, append-only Postgres) with idempotent at-least-once shipping and cost for a tiny volume; verification tooling for completeness.

🟡 부분 · 우선순위 should · 확신 low

The semantics are fixed: async at-least-once delivery with backoff, deterministic idempotency keys, local files retained until confirmed, and prune not gated on remote confirmation [repo-adr-0006]. The sink and protocol are undecided. Any sink must support writes that are idempotent on the ADR-0006 key, such as a segment-keyed object or a unique constraint. A verifier can compare per-segment counts or digests between local and remote. No vendor, immutability or cost evidence was collected.

### T15.Q5 · Initial authorization procedure (ADR-0007 awaiting-initial-authorization): a runbook for first live clear — checklist of preconditions (paper results, limits configured, alerting tested, IP allowlist), and how to record the authorization durably.

🟡 부분 · 우선순위 must · 확신 low

The mechanism is fixed. First boot sets the halt with reason awaiting-initial-authorization, and only a human ClearHalt releases it [repo-adr-0007][repo-adr-0004]. ClearHalt cannot record who authorized or why [repo-killswitch-seam-code]. A durable record therefore needs either a store change (a V6 halt-history or clear-reason row) or an audit event emitted on clear.

No runbook for this exists: docs/runbooks holds only phase-b-entry and loop-pr-environment-provisioning, verified locally. The checklist items (paper results, configured limits, a tested notifier, an IP allowlist) are unsourced, and whether Toss requires an IP allowlist is unverified.

### T15.Q6 · Order cancel/modify design as a future ADR: whether cancels go through the same 2-marker journal, kill-switch treatment (cancels allowed during halt), and reconciliation of cancel outcomes.

🟡 부분 · 우선순위 should · 확신 low

Cancels are already allowed during a halt, subject to fail-closed verification of the exposure tag [repo-adr-0004]. Cancel outcomes are observable as CANCELED in order detail [repo-adr-0003]. A cancel should target a confirmed orderId only, never an unresolved ambiguous intent, consistent with ADR-0003's rejection of heuristics. Whether cancels go through the journal is undecided, and the Toss cancel/modify endpoint was not verified.

### T15.Q7 · Multi-instance/HA stance: should the project explicitly forbid multi-instance forever (token constraint) and document a cold-standby procedure instead — what do similar single-token API systems do?

🟡 부분 · 우선순위 nice · 확신 low

Because issuing a new token invalidates the old one [repo-adr-0001], a second instance invalidates the first instance's token. That makes the first instance's refresh fail, which trips a global halt [repo-adr-0004]. Multi-instance operation therefore fails safe but cannot work.

Inference: explicitly forbid it with a store-level exclusive lock and document cold standby (stop the primary, start the standby on the same store or a restored store, which boots halted per ADR-0007 point 7). No external practice was collected.

### T15.Q8 · Market choice decision inputs (KR vs US first): operational (overnight vs daytime for the operator), cost, API coverage, tax, strategy evidence — a decision matrix to seed the /architect grilling.

❌ 미답 · 우선순위 must · 확신 low

No sources were collected on KR vs US trading hours, tax, fees or Toss API coverage. The only repo input is that a single human performs manual clears [repo-adr-0004]. US regular hours fall overnight in KST, which would weigh against US-first, but this is unsourced general knowledge.

## 프로젝트 함의

- This topic has no external evidence. Every source is repo-internal. Re-run it with the search budget restored and the hosts allowed before an /architect run relies on it.
- #80: ClearHalt(ctx) in internal/killswitch/seam.go:29 and in store has no reason or operator parameter, and nothing in cmd/ calls it. The ADR must decide two things before the CLI is built: the signature change plus either a store bump beyond V5 or an audit event on clear, and live IPC vs offline edit plus restart (because of the ADR-0013 mirror).
- Notifier ADR: the non-blocking-under-lock contract (seam.go:43) forces an async queue adapter. ADR-0006 Follow-ups require a seam broader than HaltTripped. Notification failure should not trip a halt.
- Cancel ADR: ADR-0004 point 2 already allows verified cancels during a halt. The ADR still needs to decide the journal treatment and require confirmed orderIds only.
- A new docs/runbooks/initial-authorization.md would fall under instructionsurface_test and the sacred registration rule (same-PR registration in CODEOWNERS and sacredRequiredPaths). It must be written before window W0.
- Strategy ADR: it must encode ADR-0002 point 2 (intentId uniqueness) and ADR-0004 point 2 (untrusted exposure tag). A pure snapshot-to-intents function makes that contract testable.
- Single instance: add an explicit lock and a cold-standby note as an ADR-0001 follow-up.

## 출처 간 상충

- There are no external sources, so contradictions between sources cannot be assessed.
- ADR-0004 point 8 scopes the notifier to halt trips, while ADR-0006 Follow-ups route disk and shipping alerts to the same undecided channel. The single HaltTripped seam in code does not cover the ADR-0006 alerts, so the notifier ADR should resolve this.
- ADR-0004 points 5 and 7 are amended by ADR-0013 (a mutex snapshot instead of lock-free reads) and ADR-0014 (the reconciler owns ambiguous-submit escalation). Both conflicts are resolved by those amendments.

## 남은 공백

- **T15.Q1** — Notifier channel docs and the ack/dedup semantics of alerting tools (추가 검색: ntfy publish API priority; PagerDuty Events API v2 dedup_key; Prometheus Alertmanager repeat_interval)
- **T15.Q2** — Strategy interfaces in established trading frameworks (추가 검색: freqtrade strategy-customization; NautilusTrader Strategy on_bar; QuantConnect LEAN algorithm framework)
- **T15.Q3** — Patterns for authenticating local admin IPC and auditing operator actions (추가 검색: SO_PEERCRED unix socket Go; break-glass procedure audit trail)
- **T15.Q4** — Sink immutability guarantees and cost at tiny volume (추가 검색: S3 Object Lock compliance mode; Cloudflare R2 pricing; Postgres append-only audit table)
- **T15.Q5** — Go-live control references, and whether Toss requires an IP allowlist (추가 검색: SEC Rule 15c3-5; SEC Knight Capital order 34-70694; 토스증권 Open API IP 허용)
- **T15.Q6** — Toss cancel/modify endpoint spec (추가 검색: openapi.tossinvest.com openapi.json orders cancel)
- **T15.Q7** — Single-active-instance and cold-standby practice in comparable systems (추가 검색: single active instance lease cold standby trading bot)
- **T15.Q8** — KR vs US trading hours, tax, fees and API coverage (추가 검색: 해외주식 양도소득세 250만원 기본공제 국세청; 증권거래세 2026 세율; 토스증권 해외주식 수수료)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|
| `repo-adr-0004` | ADR-0004: Kill switch submit guard | toss-trade-bot maintainers | 2026 | official-doc | <docs/adr/0004-kill-switch-submit-guard.md> | 5 | 5 | Point 2: liquidation and cancel orders are not blocked. The strategy's exposure tag is untrusted and is verified fail-closed.; Point 6: only an explicit human operation clears a global halt. The clear UI is deferred.; Point 7: Trip(scope, reason) is generic. Token-refresh failure trips a global halt. |
| `repo-adr-0006` | ADR-0006: Audit sink durable ack | toss-trade-bot maintainers | 2026 | official-doc | <docs/adr/0006-audit-sink-durable-ack.md> | 5 | 5 | Point 3: deterministic idempotency keys per event class.; Point 5: shipping never blocks the order loop or gates prune. A disk threshold breach raises an operational alert whose target is undecided.; Point 6: a failed local audit write fails closed into a killswitch trip. |
| `repo-killswitch-seam-code` | internal/killswitch seam.go + clear.go and internal/runtime slogNotifier | toss-trade-bot maintainers | 2026 | code | <internal/killswitch/seam.go> | 5 | 5 | seam.go:43: HaltTripped MUST be non-blocking (it is invoked under the transition lock; reentry would deadlock).; seam.go:29: ClearHalt(ctx context.Context) error has no reason or operator parameter.; clear.go:11: ErrClearDeferred while inflightTrips > 0. On a durable error, all carriers are kept (fail-closed). |
| `repo-adr-0002` | ADR-0002: Order intent identity | toss-trade-bot maintainers | 2026 | official-doc | <docs/adr/0002-order-intent-identity.md> | 5 | 5 | Point 2: order takes intentId through an interface and does not import strategy.; Point 4: clientOrderId is not visible in order lists or details. |
| `repo-adr-0007` | ADR-0007: Dev-time autonomy boundary at live-trade authorize | toss-trade-bot maintainers | 2026 | official-doc | <docs/adr/0007-dev-time-autonomy-boundary.md> | 5 | 5 | Point 3: the first-boot default is tripped/awaiting-initial-authorization.; Point 7: ambiguous provenance resolves to halted.; A separate authorize flag was rejected. |
| `repo-adr-0003` | ADR-0003: Order reconciliation | toss-trade-bot maintainers | 2026 | official-doc | <docs/adr/0003-order-reconciliation.md> | 4 | 5 | OrderStatus includes PENDING, PARTIAL_FILLED, FILLED, CANCELED and REJECTED. Unknown codes must be tolerated.; Heuristic payload matching was rejected. |
| `repo-adr-0001` | ADR-0001: Single-flight token refresh | toss-trade-bot maintainers | 2026 | official-doc | <docs/adr/0001-single-flight-token-refresh.md> | 4 | 5 | Token lifetime is 86400s with no refresh token. Reissuing a token invalidates the previous one.; A token-refresh failure trips a global halt (ADR-0004 point 7). |

### 출처 요약

- **`repo-adr-0004`** — Internal repo ADR. CanSubmit is a synchronous fail-closed predicate on the new-exposure submit edge. A global halt is persisted and only a human can clear it. The clear UI is deferred. Point 8 defines only a notifier seam, with no concrete channel. _(접근: Repo-internal; relative path)_
- **`repo-adr-0006`** — Internal repo ADR. Prune is gated on local durability. Remote shipping is async best-effort with backoff, and local files are retained until shipping is confirmed. Follow-ups list two undecided items: the remote sink/protocol, and an operational alert channel for three triggers (audit disk threshold, long shipping failure, killswitch trips). _(접근: Repo-internal; relative path)_
- **`repo-killswitch-seam-code`** — Code, re-verified locally in this step. Notifier.HaltTripped(reason string) MUST be non-blocking because it is invoked under the killswitch transition lock. The only implementation is an ERROR-level slog notifier. ClearHalt(ctx) takes no reason or operator, and returns ErrClearDeferred while trips are in flight. No cmd/ binary calls it. _(접근: Repo-internal code)_
- **`repo-adr-0002`** — Internal repo ADR. The write-ahead journal acts as an outbox. The strategy assigns intentId and owns its uniqueness. clientOrderId is a deterministic hash of intentId and serves only as a dedup hope. The truth key is orderId. _(접근: Repo-internal; relative path)_
- **`repo-adr-0007`** — Internal repo ADR. On first boot the global halt is tripped with reason awaiting-initial-authorization, and only the ADR-0004 human clear releases it. A separate authorize flag was rejected. Ambiguous provenance resolves to halted. _(접근: Repo-internal; relative path)_
- **`repo-adr-0003`** — Internal repo ADR. The reconciler is query-only, and order detail returns every status including CANCELED. Heuristic auto-ack was rejected, partly because of the risk of cancelling the wrong order. _(접근: Repo-internal; relative path)_
- **`repo-adr-0001`** — Internal repo ADR. Each client has only one valid token, and reissuing a token invalidates the previous one. Tokens are therefore issued and cached in one place, which makes multiple instances structurally unsafe. _(접근: Repo-internal; relative path)_
