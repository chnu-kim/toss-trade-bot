# Order management and execution safety: idempotency, write-ahead intent, reconciliation, exactly-once limits

> **왜 중요한가** — ADR-0002 (2-marker write-ahead journal, intentId->clientOrderId), ADR-0003 (orderId truth, fail-closed ambiguous), ADR-0014 (backlog-count escalation, bounded re-count), store V4 schema constraints (#29), submit path (#34), single reconciler (#35). Open gaps: cancel/modify lifecycle is unmodelled; partial fills and the 'reduce exposure' check (ADR-0004 point 2) need settled definitions; the literature on at-most-once/exactly-once for financial writes should be mined to confirm the ADR-0003 'no guessed binding' stance and the TOSS_BOT_SETTLE_WINDOW default (30s).

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 29건. 상태 집계: ✅ 답변됨 4, 🟡 부분 8

## 핵심 결론

- **What is the canonical treatment of 'ambiguous write' (request sent, response lost) in payments/trading systems — Stripe idempotency keys, IETF Idempotency-Key draft, FIX session recovery — and how do they decide the dedup window and key retention?** — Standard practice: the client generates the key. The server stores the first outcome under that key and replays it on retry. It rejects the same key with a different payload, and it returns an in-progress error for a duplicate that arrives while the original is still running.
- Stripe: replays the stored outcome, does not save validation failures or concurrent conflicts, and prunes keys after at least 24h [stripe-idempotent-requests].
- IETF draft: 400 for a missing key, 422 for a payload mismatch, 409 for an in-progress duplicate; expiry is a policy the server publishes [ietf-idempotency-key-07].
- AWS: record the token and the mutation atomically [aws-builders-idempotent-apis].
- Toast: query the order state before retrying [toast-order-timeouts].
- FIX: PossDupFlag plus ClOrdID dedup [fixt11-message-recovery].
- IBKR: monotonic, persistent order IDs [ibkr-open-orders-api].

Toss follows the same model: a 10-minute replay window, 409 request-in-progress and 422 idempotency-key-conflict [toss-openapi-spec-1.2.14].

In every source the key window is a vendor policy, not something derived from latency. The client journal therefore has to outlive the server's window.
- **How do FIX protocol semantics (ClOrdID uniqueness, OrigClOrdID chains, ExecType/OrdStatus state machine, 35=8 execution reports, cancel/replace races, 'unsolicited cancel') map onto a REST broker API, as a reference state machine for internal/order?** — FIX semantics [fix-order-state-matrices][fix-cancel-reject-semantics][fix-forum-clordid-origclordid]:
- ExecType names the event; OrdStatus gives the current state.
- Cancel and replace each carry a new ClOrdID, and OrigClOrdID points to the previous one.
- A fill can still win while a cancel or replace is pending, and a reject does not prove the order was cancelled.

Toss OrderStatus mirrors FIX OrdStatus [toss-openapi-spec-1.2.14], with four differences:
1. Cancel and modify take no client id, and the server-issued orderId has no link back to the original. The OrigClOrdID chain can only exist in the bot's journal.
2. There are no execution reports (35=8). Events must be inferred by diffing polled snapshots.
3. The bot must define how overlapping in-flight requests behave. Toss's 409 already-processing implies one pending operation per order.
4. There is no unsolicited-cancel code. Expiry or a broker-side cancel appears only as CANCELED.

The reconciler's classify.go already treats only FILLED, CANCELED and REJECTED as terminal, and handles REPLACED and unknown codes fail-closed.
- **Reconciliation algorithms: startup reconciliation of local intents vs broker open orders and positions (three-way: journal, orders, holdings), detecting orphan orders not in journal (manual trades in app), and policy for orphans (ignore/track/cancel).** — Peer practice: reconcile at startup and refuse to start on failure, run periodic checks with bounded retries, and tag unmatched orders EXTERNAL [nautilustrader-reconciliation]. IBKR rebuilds from open orders, positions and executions, and shows manual orders with id 0 [ibkr-open-orders-api].

For Toss the three-way check is journal vs GET /orders?status=OPEN (which returns no clientOrderId) vs holdings. The bot's own cancel and modify child orderIds will look like orphans unless they are journaled [toss-openapi-spec-1.2.14].

Orphan policy (inferred):
- Track orphans read-only as EXTERNAL and count their leaves in exposure.
- Never auto-cancel an orphan.
- Never bind an orphan to an ambiguous intent (ADR-0003).
- If an orphan coexists with an unresolved ambiguous intent on the same symbol, keep that symbol blocked.
- **Cancel/modify safety: should cancel be auto-retried (it is idempotent-ish but can race with fills); how to journal cancel intents with the same 2-marker scheme; what is the state after 'cancel rejected because already filled'?** — Toss cancel takes an empty body and no key, and returns a new orderId. It can fail with 409 already-* (already-processing carries retryAfterSeconds=1) or 422. A rejected cancel creates a separate CANCEL_REJECTED record, and the original order returns to its prior state [toss-openapi-spec-1.2.14].

A cancel races with fills [fix-order-state-matrices][cme-ilink-cancel-on-disconnect], a reject proves nothing [fix-cancel-reject-semantics], and a replace race can duplicate exposure [alpaca-replace-duplicate-forum].

Recommended:
- Retries: bounded, and only after a GET shows PENDING or PARTIAL_FILLED.
- Journal: use the two-marker scheme keyed by the parent orderId, and record the child id on ack.
- An ambiguous cancel can be resolved from the parent's status.
- After already-filled, record the final snapshot and close the cancel as too late [algolab-toss-cancel-modify].
- Modify follows the submit rules: no auto-retry, and a quantity increase goes through the kill-switch guard.

## 질문별 답변

### T2.Q1 · What is the canonical treatment of 'ambiguous write' (request sent, response lost) in payments/trading systems — Stripe idempotency keys, IETF Idempotency-Key draft, FIX session recovery — and how do they decide the dedup window and key retention?

✅ 답변됨 · 우선순위 must · 확신 high

Standard practice: the client generates the key. The server stores the first outcome under that key and replays it on retry. It rejects the same key with a different payload, and it returns an in-progress error for a duplicate that arrives while the original is still running.
- Stripe: replays the stored outcome, does not save validation failures or concurrent conflicts, and prunes keys after at least 24h [stripe-idempotent-requests].
- IETF draft: 400 for a missing key, 422 for a payload mismatch, 409 for an in-progress duplicate; expiry is a policy the server publishes [ietf-idempotency-key-07].
- AWS: record the token and the mutation atomically [aws-builders-idempotent-apis].
- Toast: query the order state before retrying [toast-order-timeouts].
- FIX: PossDupFlag plus ClOrdID dedup [fixt11-message-recovery].
- IBKR: monotonic, persistent order IDs [ibkr-open-orders-api].

Toss follows the same model: a 10-minute replay window, 409 request-in-progress and 422 idempotency-key-conflict [toss-openapi-spec-1.2.14].

In every source the key window is a vendor policy, not something derived from latency. The client journal therefore has to outlive the server's window.

### T2.Q2 · Is 'exactly-once' order submission provably impossible without server-side idempotency (two generals / FLP framing), and what are the standard at-most-once + reconcile patterns (outbox, saga, transactional inbox) and their failure modes?

🟡 부분 · 우선순위 must · 확신 medium

No primary formal proof (Two Generals or FLP) was retrieved. The secondary literature agrees on three points [brave-new-geek-exactly-once][kleppmann-ddia][helland-idempotence-acmqueue-2012]:
- exactly-once delivery is impossible;
- only at-most-once and at-least-once are achievable;
- exactly-once effects need an idempotent receiver.

Exactly-once claims depend on server-side dedup within one system boundary [kafka-kip-98], and dedup belongs at the endpoints [saltzer-end-to-end-1984]. Without server idempotency, the only safe choice is at-most-once plus reconciliation.

Patterns and their failure modes:
- Outbox: the relay can publish twice [microservices-io-outbox]. ADR-0002 is an outbox whose relay is a single POST with no retry.
- Saga: the compensating action, a cancel, can lose a race with a fill [sagas-garcia-molina-1987].
- Durability depends on synchronous=FULL [sqlite-wal-synchronous-forum], which the repo already pins.

### T2.Q3 · How do FIX protocol semantics (ClOrdID uniqueness, OrigClOrdID chains, ExecType/OrdStatus state machine, 35=8 execution reports, cancel/replace races, 'unsolicited cancel') map onto a REST broker API, as a reference state machine for internal/order?

✅ 답변됨 · 우선순위 should · 확신 medium

FIX semantics [fix-order-state-matrices][fix-cancel-reject-semantics][fix-forum-clordid-origclordid]:
- ExecType names the event; OrdStatus gives the current state.
- Cancel and replace each carry a new ClOrdID, and OrigClOrdID points to the previous one.
- A fill can still win while a cancel or replace is pending, and a reject does not prove the order was cancelled.

Toss OrderStatus mirrors FIX OrdStatus [toss-openapi-spec-1.2.14], with four differences:
1. Cancel and modify take no client id, and the server-issued orderId has no link back to the original. The OrigClOrdID chain can only exist in the bot's journal.
2. There are no execution reports (35=8). Events must be inferred by diffing polled snapshots.
3. The bot must define how overlapping in-flight requests behave. Toss's 409 already-processing implies one pending operation per order.
4. There is no unsolicited-cancel code. Expiry or a broker-side cancel appears only as CANCELED.

The reconciler's classify.go already treats only FILLED, CANCELED and REJECTED as terminal, and handles REPLACED and unknown codes fail-closed.

### T2.Q4 · Best practice for partial fills: tracking leaves quantity, average price recomputation, handling fills that arrive after cancel, and representing cumulative snapshots when no per-fill id exists (ADR-0006 digest approach) — any prior art on snapshot-versioned fills?

🟡 부분 · 우선순위 must · 확신 medium

Toss exposes only a cumulative OrderExecution per order, with no per-fill id [toss-openapi-spec-1.2.14].
- Leaves quantity = quantity − filledQuantity. Amount-based US orders have no meaningful leaves until they reach a terminal status.
- Take the average price from the broker; never recompute it locally.
- CANCELED, REJECTED and REPLACED can carry fills. FIX likewise allows Pending Cancel to become Filled [fix-order-state-matrices].
- Cumulative values can lag and are final only at a terminal status [alpaca-partial-fill-forum].

No prior art on snapshot-versioned fills was found. FIX CumQty/AvgPx is the closest analogue, and the ADR-0006 orderId+digest key fits it.

Inferred rules:
- Require filledQuantity to be monotonic. A decrease is an anomaly that should halt the bot.
- A per-fill delta is the difference between consecutive snapshots.

### T2.Q5 · What settle-window / in-flight timeout values do practitioners use before declaring a submit ambiguous, and how should the window relate to API p99 latency and to clientOrderId window (10 min)?

🟡 부분 · 우선순위 should · 확신 low

No practitioner numbers were found. The guidance is to derive timeouts from latency percentiles [aws-builders-timeouts-retries] and to query state on timeout [toast-order-timeouts]. A broker can process an order about an hour late [meritz-overseas-outage-2025], so no window justifies inferring the order is ABSENT. The window should only declare ambiguity, which fails closed.

Repo values, verified:
- HTTP timeout 10s (internal/toss/client.go:80)
- durablePersistTimeout 30s (internal/order/submit.go:219)
- DefaultSettleWindow 30s (internal/config/config.go:46)

A healthy but slow acknowledgement (10s POST plus 30s persist) can exceed the 30s settle window and be wrongly declared ambiguous. The rule should be settle ≥ HTTP timeout + ack budget + margin, roughly 45-60s, which stays well below the 10-minute clientOrderId window [toss-openapi-spec-1.2.14]. Stripe's 24h shows key windows are a policy choice [stripe-idempotent-requests].

### T2.Q6 · Reconciliation algorithms: startup reconciliation of local intents vs broker open orders and positions (three-way: journal, orders, holdings), detecting orphan orders not in journal (manual trades in app), and policy for orphans (ignore/track/cancel).

✅ 답변됨 · 우선순위 must · 확신 medium

Peer practice: reconcile at startup and refuse to start on failure, run periodic checks with bounded retries, and tag unmatched orders EXTERNAL [nautilustrader-reconciliation]. IBKR rebuilds from open orders, positions and executions, and shows manual orders with id 0 [ibkr-open-orders-api].

For Toss the three-way check is journal vs GET /orders?status=OPEN (which returns no clientOrderId) vs holdings. The bot's own cancel and modify child orderIds will look like orphans unless they are journaled [toss-openapi-spec-1.2.14].

Orphan policy (inferred):
- Track orphans read-only as EXTERNAL and count their leaves in exposure.
- Never auto-cancel an orphan.
- Never bind an orphan to an ambiguous intent (ADR-0003).
- If an orphan coexists with an unresolved ambiguous intent on the same symbol, keep that symbol blocked.

### T2.Q7 · Position/exposure arithmetic for the ADR-0004 'closing vs new exposure' independent check: definition with pending open intents, partial fills, short-sale absence (KR retail), and US fractional shares.

🟡 부분 · 우선순위 must · 확신 low

Inferred from the spec and peer practice [toss-openapi-spec-1.2.14][nautilustrader-reconciliation][fsc-short-selling-2025]. Per symbol:
- A SELL of q reduces exposure only if q ≤ min(sellableQuantity, H − S_open − U_sell), where H is holdings, S_open is open sell leaves and U_sell is the unresolved ambiguous sell quantity. Fail closed if the inputs are unavailable.
- A BUY is always new exposure, because there are no retail shorts.
- A cancel always reduces exposure.
- A KR modify reduces exposure only if it is a SELL and the new quantity ≤ leaves.
- Read holdings and leaves from one consistent snapshot.
- US fractional quantities need exact decimal arithmetic at scale 6.
- An amount-based US BUY counts as new exposure at its worst-case quantity.

### T2.Q8 · Cancel/modify safety: should cancel be auto-retried (it is idempotent-ish but can race with fills); how to journal cancel intents with the same 2-marker scheme; what is the state after 'cancel rejected because already filled'?

✅ 답변됨 · 우선순위 should · 확신 medium

Toss cancel takes an empty body and no key, and returns a new orderId. It can fail with 409 already-* (already-processing carries retryAfterSeconds=1) or 422. A rejected cancel creates a separate CANCEL_REJECTED record, and the original order returns to its prior state [toss-openapi-spec-1.2.14].

A cancel races with fills [fix-order-state-matrices][cme-ilink-cancel-on-disconnect], a reject proves nothing [fix-cancel-reject-semantics], and a replace race can duplicate exposure [alpaca-replace-duplicate-forum].

Recommended:
- Retries: bounded, and only after a GET shows PENDING or PARTIAL_FILLED.
- Journal: use the two-marker scheme keyed by the parent orderId, and record the child id on ack.
- An ambiguous cancel can be resolved from the parent's status.
- After already-filled, record the final snapshot and close the cancel as too late [algolab-toss-cancel-modify].
- Modify follows the submit rules: no auto-retry, and a quantity increase goes through the kill-switch guard.

### T2.Q9 · Duplicate-order prevention beyond keys: notional/quantity caps per intent, per-symbol open-order caps, 'one open order per symbol' invariants, and self-trade (wash) prevention when both buy and sell intents exist.

🟡 부분 · 우선순위 should · 확신 medium

KRX guidance calls for pre-set exposure and per-order limits, input-error prevention and a bulk-cancel kill switch [krx-algo-risk-guideline-2014]. Knight Capital is the failure case [sec-knight-capital-2013]. Toss enforces several guards server-side, each of which counts as a reject: max-order-amount-exceeded, confirm-high-value-required, insufficient-buying-power and opposite-pending-order-exists [toss-openapi-spec-1.2.14].

Recommended client-side checks (inferred):
- a per-intent notional and quantity cap;
- one open bot order per symbol;
- a local reject when any opposite-side order is open, including EXTERNAL orders;
- a circuit breaker on order count per time window.

No academic source on self-trade prevention for retail traders was found.

### T2.Q10 · Price validation before submit: tick-size rounding, price-limit (±30% KR) and VI-band checks, order-size limits, marketable-limit vs market orders for retail APIs — which checks belong client-side to avoid rejects counting toward the order-failure threshold (TOSS_BOT_ORDER_FAILURE_THRESHOLD=3)?

🟡 부분 · 우선순위 should · 확신 medium

The spec provides the reject taxonomy: tick-size errors (400, with nearestPrices), price-out-of-range plus GET /price-limits, order-hours, fractional, amount and opposite-pending rejects, and high-value confirmation [toss-openapi-spec-1.2.14]. The ±30% limit and VI rules come only from secondary sources [krx-price-limit-vi].

Recommendation: before writing the prepared marker, run every deterministic check locally:
- tick size (the KRX tick table has not been sourced yet);
- the price-limit band;
- order hours;
- notional caps and high-value confirmation;
- quantity integrality and scale;
- opposite-side pending orders;
- sellable quantity.

This keeps those rejects from counting toward TOSS_BOT_ORDER_FAILURE_THRESHOLD=3. Leave VI to the broker. Use marketable limit orders for KR.

### T2.Q11 · Academic/industry treatments of 'stuck orders' and stale-state detection in algo trading (heartbeat on order feed, max-age for OPEN orders, auto-cancel-on-disconnect analogues when the exchange has no CoD facility).

🟡 부분 · 우선순위 nice · 확신 low

Exchanges use cancel-on-disconnect and require clients to verify order status after reconnecting [cme-ilink-cancel-on-disconnect]. KRX prescribes a kill switch [krx-algo-risk-guideline-2014], and peers run periodic open-order checks [nautilustrader-reconciliation]. Postmortems show the cost of stale state [sec-knight-capital-2013][meritz-overseas-outage-2025]. No academic treatment of max-age policies was found.

Inferred measures:
- re-GET every acked, non-terminal order on each re-evaluation;
- a TTL on open orders, or cancellation before the close;
- a poll-freshness heartbeat that blocks new exposure when polling goes stale;
- an operator command to cancel all bot orders, which the kill switch permits because it only reduces exposure (ADR-0004 p2).

### T2.Q12 · Decimal handling: Go decimal libraries vs string passthrough (current design), rounding modes for KRW (integer) vs USD (2-4 dp), and audit-digest determinism of numeric formatting.

🟡 부분 · 우선순위 nice · 확신 medium

Every Toss numeric field is a decimal string with maxLength 30 [toss-openapi-spec-1.2.14]:
- KR prices and quantities are integers.
- US prices match ^\d+(\.\d+)?$.
- US fractional quantities have scale ≤ 6.

This validates the string passthrough in ADR-0006. No sources on Go decimal libraries were retrieved.

Inferred:
- Keep the wire strings for digests.
- Do arithmetic in an exact decimal type, never float.
- Round KRW prices to the tick, round USD prices to cents, and truncate US quantities at scale 6.
- Risk: if the broker re-formats a value ("70000" vs "70000.0"), the digest will record a spurious fill. Canonicalising strings before hashing avoids this.

## 프로젝트 함의

- ADR-0002 treats duplicate-clientOrderId behaviour as undefined and uses the key only in the hope that the server deduplicates. Spec v1.2.14 now defines this: replay within 10 minutes, a 409 or 422 otherwise, consistent with Stripe and IETF practice. Re-verify against the live spec, then amend the ADR via /architect.
- ADR-0003 rejected same-key resubmission on the premise that it was undefined. A replay probe within 10 minutes could now recover an orderId, but it is still a real submit. It would need a new ADR covering an age margin, the CanSubmit guard and confirmation that the replay returns the original id. The 'no guessed binding' rule stands, because an API-backed replay is not a guess.
- DefaultSettleWindow=30s (config.go:46) is shorter than a healthy worst case of 10s HTTP timeout (client.go:80) plus 30s durablePersistTimeout (submit.go:219). A false 'ambiguous' then feeds the ADR-0014 backlog count. Raise the default to 45-60s, or enforce a floor of settle ≥ HTTP timeout + ack budget in a test, the same way minPreparedAbandonWindow=60s is enforced (reconciler.go:137).
- The server key expires after 10 minutes, so journal intentId dedup is the only long-horizon guard. State a contract and test that Pruner retention exceeds the horizon over which a strategy can re-emit an intent.
- The cancel/modify lifecycle needs a new ADR: two-marker journal keyed by the parent orderId, child id recorded on ack. Without it, the bot's own child orders show up as orphans. An ADR-0004 CanSubmit check is also needed for KR modifies that increase quantity.
- The ADR-0003 status list is incomplete: the spec defines 10 values. The code in classify.go is already correct, so align the ADR text with it. REPLACED will need a resolution path.
- The open ADR-0004 item on reducing-order verification can be defined in terms of sellable-quantity, open-sell leaves and worst-case ambiguous quantity, failing closed when any of these is unavailable.
- ADR-0006 needs a monotonic-filledQuantity assertion and a decision on canonicalising decimals before hashing. Note that CANCELED, REJECTED and REPLACED orders can carry fills.
- Add a pre-submit validator that runs before the prepared marker, so that rejects which are deterministic locally do not trip TOSS_BOT_ORDER_FAILURE_THRESHOLD=3.
- Mirror Toss's opposite-pending-order-exists check on the client, including EXTERNAL orders, and add a per-symbol open-order cap and an order-count circuit breaker (Knight Capital, KRX guideline).
- Orphan policy for the reconciler (#35): track orphans read-only as EXTERNAL, never cancel or bind them, count them in exposure, and keep the symbol blocked if an orphan coexists with an ambiguous intent.
- Store durability (synchronous=FULL, ADR-0005) is already satisfied.
- The KRX finding that 3 of 4 incidents happened on a program's first live day supports ADR-0007's halt-on-first-boot.

## 출처 간 상충

- ADR-0002/0003 say clientOrderId behaviour is undefined, but spec v1.2.14 defines it. The spec has evolved; re-verify live and amend the ADRs.
- Key retention is 24h at Stripe, 10min at Toss, and left to server policy in the IETF draft. Stripe-based intuitions do not carry past 10 minutes.
- Stripe v1 replays errors while v2 completes the work. The Toss spec is silent on error replay.
- Korean sources disagree on dynamic VI thresholds; use the KRX 시행세칙 or /price-limits instead.
- FIX implies LeavesQty=0 when an order is Canceled, but BitGo and Toss report cancelled orders that still carry quantities. Read the quantity fields rather than inferring them from status.
- The Knight loss is reported as about $440M by Knight and over $460M by the SEC; cite the SEC figure.
- IETF draft -07 expired 2026-04-18; whether a later revision exists is unverified.

## 남은 공백

- **T2.Q2** — Primary formal sources for the impossibility claims (Akkoyunlu 1975, Gray 1978, FLP 1985) and for at-most-once RPC (Birrell and Nelson 1984). Round 2 ran out of search budget before reaching them. (추가 검색: Akkoyunlu Ekanadham Huber 1975 constraints and tradeoffs network communications; Fischer Lynch Paterson 1985 doi 10.1145/3149.214121; Birrell Nelson 1984 implementing remote procedure calls at-most-once)
- **T2.Q1** — Whether a Toss same-key replay also replays error responses and returns the original orderId. (추가 검색: site:developers.tossinvest.com clientOrderId 멱등성 10분; tossinvest clientOrderId 재요청 이전 주문 결과)
- **T2.Q5** — Submit-timeout values used in practice, and Toss ORDER rate-limit numbers. (추가 검색: tossinvest open api rate limits ORDER group; FIX order entry pending new ack timeout seconds)
- **T2.Q4** — Prior art on snapshot-versioned fills, and whether Toss revises fill averages after settlement. (추가 검색: cumulative fill snapshot reconciliation CumQty AvgPx without ExecID; site:developers.tossinvest.com filledQuantity)
- **T2.Q7** — Reduce-only definitions under 15c3-5, and whether Toss sellableQuantity already nets out pending sells. (추가 검색: SEC 15c3-5 pre-trade open orders aggregate; 토스증권 판매가능수량 미체결 매도 차감)
- **T2.Q10** — The KRX tick table after the 2023 reform, the VI thresholds in the 시행세칙, and SEC Rule 612. (추가 검색: 한국거래소 호가가격단위 2023 개편; 유가증권시장 업무규정 시행세칙 변동성완화장치; SEC Rule 612 minimum price increment)
- **T2.Q12** — How Go decimal libraries compare on canonical string form and rounding modes. (추가 검색: shopspring decimal vs cockroachdb apd; govalues decimal canonical string trailing zeros)
- **T2.Q11** — Max-age or heartbeat policies for open orders on venues without cancel-on-disconnect. (추가 검색: stale open order max age policy algorithmic trading; emulate cancel on disconnect client side broker API)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|
| `toss-openapi-spec-1.2.14` | 토스증권 Open API OpenAPI specification (info.version 1.2.14) | Toss Securities | 2026 | api-spec | <https://openapi.tossinvest.com/openapi-docs/latest/openapi.json> | 5 | 5 | clientOrderId is an idempotency key: the same value returns the previous result; max 36 chars [A-Za-z0-9-_]; client-generated; valid 10 minutes, then the same value creates a new order; POST /api/v1/orders: 409 request-in-progress; 422 idempotency-key-conflict; OrderStatus has 10 values: PENDING, PENDING_CANCEL, PENDING_REPLACE, PARTIAL_FILLED, FILLED, CANCELED, REJECTED, CANCEL_REJECTED, REPLACE_REJECTED, REPLACED; clients must tolerate unknown codes |
| `stripe-idempotent-requests` | Idempotent requests (Stripe API reference) | Stripe | 2026 | official-doc | <https://docs.stripe.com/api/idempotent_requests> | 5 | 5 | The first status and body are replayed, including 500s (v1); Keys can be pruned after >=24h; A parameter mismatch returns an error |
| `fix-order-state-matrices` | FIX Appendix D: Order State Change Matrices | FIX Trading Community (hosted by OnixS) | 2024 | standard | <https://onixs.biz/fix-dictionary/5.0/app_d.html> | 5 | 5 | ExecType is the event; OrdStatus is the current state, highest precedence first; OrigClOrdID points to the previous link in the chain; Pending Cancel can still become Filled |
| `fix-cancel-reject-semantics` | Cancel Reject (35=9): Cboe Titanium FIX specification | Cboe Global Markets | 2025 | official-doc | <https://www.cboe.com/document/tech-spec/content/technical-specifications/cboe-titanium-u.s.-options-fix-specification/fix-messages/order-protocol---cboe-to-member/cancel-reject> | 4 | 5 | 102=0 Too late to cancel; A rejected replace leaves the original order alive |
| `saltzer-end-to-end-1984` | End-to-End Arguments in System Design | J. H. Saltzer, D. P. Reed, D. D. Clark | 1984 | paper | <https://web.mit.edu/saltzer/www/publications/endtoend/endtoend.pdf> | 4 | 5 | The application must dedupe its own retries |
| `helland-idempotence-acmqueue-2012` | Idempotence Is Not a Medical Condition | Pat Helland | 2012 | paper | <https://dl.acm.org/doi/10.1145/2181796.2187821> | 4 | 5 | Retries are inevitable; idempotence makes them safe |
| `ibkr-open-orders-api` | Requesting currently active orders (IBKR TWS API) | Interactive Brokers | 2025 | official-doc | <https://www.interactivebrokers.com/docs/tws-api/doc/order-management/requesting-currently-active-orders/api-clients-orders> | 4 | 5 | Manual orders have ID 0; Rebuild state after reconnect; Reusing an ID on a new order is an error |
| `sec-knight-capital-2013` | SEC Press Release 2013-222: Knight Capital | U.S. SEC | 2013 | regulation | <https://www.sec.gov/news/press-release/2013-222> | 4 | 5 | 4M+ orders in about 45 minutes; Loss over $460M; No pre-set thresholds |
| `sagas-garcia-molina-1987` | Sagas | Hector Garcia-Molina, Kenneth Salem | 1987 | paper | <https://dl.acm.org/doi/10.1145/38713.38742> | 3 | 5 | Each step has a compensating transaction |
| `ietf-idempotency-key-07` | The Idempotency-Key HTTP Header Field (draft-07) | J. Jena, S. Dalal | 2025 | standard | <https://datatracker.ietf.org/doc/html/draft-ietf-httpapi-idempotency-key-header> | 5 | 4 | Reusing a key with a different payload returns 422; A missing key returns 400; an in-progress duplicate returns 409; Servers MAY expire keys and SHOULD publish the policy |
| `nautilustrader-reconciliation` | Execution Reconciliation (NautilusTrader docs) | Nautech Systems | 2026 | official-doc | <https://nautilustrader.io/docs/latest/concepts/reconciliation/> | 5 | 4 | Does not start if reconciliation fails; Unclaimed orders are tagged EXTERNAL; Bounded retries for orders missing at the venue |
| `fixt11-message-recovery` | FIXT 1.1 Session Protocol: Message Recovery | FIX Trading Community (hosted by OnixS) | 2024 | standard | <https://www.onixs.biz/fix-dictionary/fixt1.1/section_message_recovery.html> | 4 | 4 | Replayed messages carry PossDupFlag=Y; PossResend requires dedup at the application level |
| `aws-builders-idempotent-apis` | Making retries safe with idempotent APIs | Malcolm Featonby | 2019 | blog | <https://aws.amazon.com/builders-library/making-retries-safe-with-idempotent-APIs/> | 4 | 4 | Parameter-equality dedup fails; Record the token and the mutation atomically |
| `kleppmann-ddia` | Designing Data-Intensive Applications | Martin Kleppmann | 2017 | book | <https://www.goodreads.com/notes/34626431-designing-data-intensive-applications/146561796-charles-fonseca?page=11> | 4 | 4 | End-to-end request IDs are needed |
| `krx-algo-risk-guideline-2014` | 알고리즘거래 위험관리 가이드라인 (한국거래소, 2014.11) | 한국거래소 시장감시위원회 | 2014 | official-doc | <https://www.smallake.kr/wp-content/uploads/2016/01/20160124_111425.pdf> | 4 | 4 | Pre-set exposure and per-order limits; Kill switch that bulk-cancels and blocks new orders; 3 of 4 incidents on the first live day |
| `aws-builders-timeouts-retries` | Timeouts, retries, and backoff with jitter | Marc Brooker | 2019 | blog | <https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter> | 3 | 4 | Set timeouts from latency percentiles; Retry at one layer only; Side-effecting calls need idempotency before retry |
| `kafka-kip-98` | KIP-98 - Exactly Once Delivery and Transactional Messaging | Jason Gustafson et al. | 2017 | official-doc | <https://cwiki.apache.org/confluence/display/KAFKA/KIP-98+-+Exactly+Once+Delivery+and+Transactional+Messaging> | 3 | 4 | Server-side dedup of retries; Holds only within one cluster |
| `cme-ilink-cancel-on-disconnect` | Cancel on Disconnect (CME iLink) | CME Group | 2025 | official-doc | <https://cmegroupclientsite.atlassian.net/wiki/spaces/EPICSANDBOX/pages/457216487/Cancel+on+Disconnect> | 3 | 4 | Triggered by an involuntary disconnect; Verify order status after reconnect; A cancel sent after a fill is rejected as Too Late |
| `fsc-short-selling-2025` | 금융위원회: 무차입공매도 방지조치 개정 (2025) | 금융위원회 | 2025 | regulation | <https://www.fsc.go.kr/po010107/84025?curPage=54> | 2 | 4 | Decree 208-7 requires controls to prevent naked shorting |
| `fix-forum-clordid-origclordid` | Order Cancel/Replace - ClOrdID and OrigClOrdID (FIX forum) | FIX Trading Community forum | 2024 | blog | <https://forum.fixtrading.org/t/order-cancel-replace-request-clordid-and-origclordid/456> | 4 | 3 | A replace uses a new ClOrdID with OrigClOrdID set to the prior one; No scenario covers two pending requests |
| `toast-order-timeouts` | Handling request timeouts for the orders API | Toast | 2025 | official-doc | <https://doc.toasttab.com/doc/devguide/apiHandlingRequestTimeouts.html> | 4 | 3 | A request that timed out may have succeeded; Determine the state before retrying |
| `brave-new-geek-exactly-once` | You Cannot Have Exactly-Once Delivery | Tyler Treat | 2015 | blog | <https://bravenewgeek.com/you-cannot-have-exactly-once-delivery/> | 4 | 3 | Only at-most-once and at-least-once delivery exist |
| `microservices-io-outbox` | Pattern: Transactional outbox | Chris Richardson | 2025 | blog | <https://microservices.io/patterns/data/transactional-outbox> | 4 | 3 | The relay can publish more than once; Consumers must dedupe |
| `meritz-overseas-outage-2025` | 메리츠증권 해외주식 주문 장애 (BizWatch, 2025.5) | BizWatch | 2025 | postmortem | <https://news.bizwatch.co.kr/article/market/2025/05/07/0006> | 3 | 3 | Orders processed late after a ~1h outage; Compensation required an order record |
| `sqlite-wal-synchronous-forum` | Switching pragma synchronous (SQLite forum) | SQLite forum | 2024 | blog | <https://sqlite.org/forum/forumpost/1046c87e75> | 3 | 3 | WAL+NORMAL can lose recent commits |
| `alpaca-replace-duplicate-forum` | Replace order creates a new order but the old order is filled later | Alpaca Community Forum | 2023 | blog | <https://forum.alpaca.markets/t/replace-order-creates-a-new-order-successfully-but-the-old-order-is-filled-later-thus-creating-duplicate-order/5794> | 4 | 2 | The original order filled after the replace was accepted |
| `alpaca-partial-fill-forum` | Getting quantities on partial fills (Alpaca forum) | Alpaca staff and users | 2019 | blog | <https://forum.alpaca.markets/t/getting-quantities-on-partial-fills/667> | 3 | 2 | filled_qty was observed lagging (136/137) |
| `krx-price-limit-vi` | KRX ±30% price limit and VI (etoday 2015; KB Think) | etoday; KB Think | 2015 | industry-report | <https://www.etoday.co.kr/news/view/1127429> | 3 | 2 | ±30% price limit since 2015-06-15; Static VI: 10% move, 2-minute cooling |
| `algolab-toss-cancel-modify` | AlgoLab: 토스증권 Open API 주문 취소·정정 (2026) | AlgoLab | 2026 | blog | <https://algolab.co.kr/blog/toss-open-api-order-cancel-modify-2026> | 3 | 2 | Track fills on the original orderId |

### 출처 요약

- **`toss-openapi-spec-1.2.14`** — The broker's own contract and the authoritative source for this topic, read from a local copy in an earlier round. It defines: clientOrderId idempotency (replay within 10 minutes, 409 request-in-progress, 422 idempotency-key-conflict); a 10-value OrderStatus enum; cancel and modify endpoints that issue new orderIds; a cumulative OrderExecution snapshot with no per-fill id; and reject codes for tick size, price band, amount caps, order hours and opposite-side pending orders. Re-verify against the live spec before amending any ADR. _(접근: Local copy; live host blocked in-session; re-verify)_
- **`stripe-idempotent-requests`** — The de-facto reference for ambiguous writes. Stripe saves and replays the first outcome, rejects a request whose parameters differ, and prunes keys after at least 24 hours. _(접근: open access; search excerpts only)_
- **`ietf-idempotency-key-07`** — IETF draft that defines the idempotency-key status codes: 400 for a missing key, 422 for a payload mismatch and 409 for a request still in progress. Toss uses the same vocabulary. The draft expired 2026-04-18. _(접근: not fetched)_
- **`fix-order-state-matrices`** — The normative reference for order lifecycles: ExecType/OrdStatus, ClOrdID chains, and fills that arrive during a pending cancel or replace. _(접근: search excerpts)_
- **`fix-cancel-reject-semantics`** — Defines 'Too late to cancel'. A cancel reject is not proof that the order was cancelled. _(접근: search excerpts)_
- **`fixt11-message-recovery`** — FIX recovers lost messages with sequence numbers, ResendRequest and PossDupFlag, and the application dedupes by ClOrdID. REST has no equivalent, so reconciliation has to take its place. _(접근: search excerpts)_
- **`fix-forum-clordid-origclordid`** — Explains the ClOrdID chain. A related thread notes that the matrices do not cover overlapping requests that are both still in flight. Not normative. _(접근: not normative)_
- **`aws-builders-idempotent-apis`** — Recommends a caller-supplied token over parameter-equality dedup, and recording the token and the mutation atomically. _(접근: search excerpts)_
- **`aws-builders-timeouts-retries`** — Derive timeouts from latency percentiles, retry at a single layer only, and require idempotency before retrying a side-effecting call. _(접근: summaries)_
- **`toast-order-timeouts`** — A timeout means there was no response, not that the request failed. Query the state first and cap the number of attempts. _(접근: open access)_
- **`saltzer-end-to-end-1984`** — Duplicate suppression can only be correct when it is done at the application endpoints. _(접근: open access)_
- **`helland-idempotence-acmqueue-2012`** — The standard citation for the claim that retries are inevitable and idempotence is what makes them safe. Includes the CIDR 2007 and 2019 companion papers. _(접근: full text not read)_
- **`brave-new-geek-exactly-once`** — A practitioner essay using the Two Generals framing. It is not a formal proof. _(접근: open access)_
- **`kleppmann-ddia`** — Covers 'effectively once' and the need for end-to-end request IDs. Cite it by ISBN. _(접근: URL is a reader-notes page)_
- **`microservices-io-outbox`** — Write the state change and the outbox row atomically. The relay can publish twice. ADR-0002 is an outbox whose relay is a single POST with no retry. _(접근: open access)_
- **`sagas-garcia-molina-1987`** — The compensation model. A cancel is the compensating action, and it can lose a race with a fill. _(접근: abstract only)_
- **`kafka-kip-98`** — Exactly-once here depends on server-side idempotency and holds only within one system boundary. _(접근: open access)_
- **`nautilustrader-reconciliation`** — The closest open-source peer. It reconciles at startup and refuses to start if that fails, runs periodic checks with bounded retries, and tags unmatched orders EXTERNAL. _(접근: search excerpts)_
- **`ibkr-open-orders-api`** — Read-only open-order queries surface manual orders with id 0. After a reconnect, rebuild state from open orders, positions and executions. _(접근: open access)_
- **`alpaca-replace-duplicate-forum`** — A real case where a replace raced a fill and the position ended up with duplicate exposure. _(접근: anecdote)_
- **`alpaca-partial-fill-forum`** — Fill data is a cumulative REST snapshot and can lag the true value. _(접근: anecdote)_
- **`cme-ilink-cancel-on-disconnect`** — Exchange-side cancel on disconnect. Toss has no equivalent, so the bot has to emulate it. _(접근: sandbox wiki)_
- **`sec-knight-capital-2013`** — More than 4M orders were sent in about 45 minutes with no threshold controls. The canonical case for caps and a kill switch. _(접근: open access)_
- **`krx-algo-risk-guideline-2014`** — Non-binding KRX guideline covering order limits, a kill switch and change control. In 2012-13, 3 of 4 domestic algo-trading incidents happened on the program's first live day. _(접근: Korean-only; third-party copy)_
- **`krx-price-limit-vi`** — Secondary coverage of the ±30% price limit and the VI rules. Sources disagree on the dynamic-VI thresholds. _(접근: Korean-only; secondary)_
- **`algolab-toss-cancel-modify`** — Unofficial guide whose details match the spec. Keep the original orderId as the key for fill truth. _(접근: Korean-only; unofficial)_
- **`meritz-overseas-outage-2025`** — Orders were processed about an hour late, so an order cannot be inferred ABSENT just because a time window has passed. _(접근: Korean-only)_
- **`sqlite-wal-synchronous-forum`** — In WAL mode, synchronous=FULL syncs every commit. The repo already pins FULL. _(접근: forum)_
- **`fsc-short-selling-2025`** — Weak support for the premise that KR retail accounts cannot short. The sellable-quantity check is what enforces it in practice. _(접근: Korean-only)_
