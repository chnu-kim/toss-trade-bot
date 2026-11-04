# Portfolio accounting, exposure computation, cash/settlement, and PnL for the independent 'reduce exposure' check and future risk limits

> **왜 중요한가** — ADR-0004 point 2 requires order/risk to independently verify that a 'closing' order actually reduces exposure using holdings + open intents; internal/account has holdings/overview types but no exposure logic; FX for US (orderAmount USD), settlement rules (KR T+2, US T+1), and partial fills complicate it. Also needed for loss-based kill switches (T3.Q6) and position sizing (T8.Q5).

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 6건. 상태 집계: 🟡 부분 6, ❌ 미답 2, ✅ 답변됨 1

## 핵심 결론

- **Numeric precision: integer KRW vs decimal USD, share quantities with fractional US shares, and deterministic rounding for digests (ADR-0006) and limit checks.** — Keep raw decimal strings for ADR-0006 digests [repo-internal-order-api][repo-internal-account]. Use a decimal library for limit arithmetic, comparing with Cmp or Equal [shopspring-decimal-pkgdev]. Never hash re-rendered decimals, and never mutate DivisionPrecision at runtime [shopspring-decimal-pkgdev]. Round toward the safe side: Floor for sellable quantity, Ceil for cost and exposure. Toss tick, lot and fractional precision are unverified.

## 질문별 답변

### T14.Q1 · Formal definition of net exposure per symbol and per portfolio with pending orders: should open buy intents count as exposure (worst-case) for the kill-switch check, and how do FIX/OMS systems define 'reducing' orders?

🟡 부분 · 우선순위 must · 확신 low

The internal requirement is sourced. A close tag is untrusted; reduction must be proven from holdings plus unresolved open intents, and an unproven order is new exposure [repo-adr-0004]. The algorithm is deferred to an implementation issue (ADR-0004 line 119) [repo-adr-0004]. Holdings are aggregate [repo-adr-0003] and acked-open intents live in the LIVE tracker [repo-adr-0014]. Derived design, not externally sourced: sellable_now = holdings.quantity - Σ remaining qty of open SELL intents (acked-open plus unresolved or ambiguous sells at full size). A SELL is reducing only if qty <= sellable_now. Open BUYs never help prove a SELL is reducing, but they count at full size toward worst-case exposure limits. Cancels are always allowed (point 1). orderAmount SELLs count as new exposure [repo-internal-order-api]. FIX PositionEffect and reduce-only semantics are unsourced.

### T14.Q2 · Reconciling local position view with broker holdings that lag (settlement-pending quantities, 미체결 vs 체결, 매도가능수량 vs 보유수량) — which Toss holdings fields represent sellable quantity now.

🟡 부분 · 우선순위 must · 확신 low

HoldingsItem models only 'quantity', with no sellable or settlement-pending field [repo-internal-account]. Holdings are aggregate [repo-adr-0003]. The live spec was unreachable, so whether a 매도가능수량 (sellable quantity) field exists is unverified. Interim rule: treat quantity as an upper bound, subtract open and ambiguous SELLs, and fail closed when holdings are stale or missing.

### T14.Q3 · Cash and buying power: 주문가능금액 computation with unsettled sales (KR T+2 reuse rules), US T+1, FX conversion requirement, and how to prevent orders that fail for insufficient funds (counting toward the failure threshold).

❌ 미답 · 우선순위 must · 확신 low

No external source. The repo has no buying-power type [repo-internal-account]. OrderAmount is USD and allowed only on US MARKET orders [repo-internal-order-api]. KR T+2 reuse, US T+1, FX auto-conversion and the orderable-amount endpoint are unverified. Design note: insufficient-funds rejections would feed ADR-0004 point 7's failure counter, so add a conservative pre-submit cash check or settle the counting question in an ADR.

### T14.Q4 · PnL accounting for a bot: realized vs unrealized, average-cost vs FIFO (Korean tax uses 이동평균/선입선출 for overseas stocks?), commission/tax inclusion, FX gains for USD positions, and daily mark-to-market for drawdown limits.

🟡 부분 · 우선순위 should · 확신 low

The broker reports averagePurchasePrice, profitLoss (including afterCost), dailyProfitLoss and cost fields, with a KRW/USD overview [repo-internal-account]. /trades cannot attribute fills [repo-adr-0003], so realized P&L must come from per-orderId execution data in the journal. The Korean overseas-stock cost-basis method, FX-gain treatment and Toss's afterCost methodology are unsourced.

### T14.Q5 · Corporate-action adjustments to positions and cost basis (splits, dividends, rights) and detecting discrepancies between journal-derived and broker-reported holdings after such events.

❌ 미답 · 우선순위 should · 확신 low

No source. Implication: a mismatch between holdings and the journal-derived position should fail closed per symbol, consistent with ADR-0014 [repo-adr-0014]. Do not auto-correct it.

### T14.Q6 · Handling manual trades by the human in the Toss app alongside the bot: detecting orphan orders/positions, policy options (bot-managed symbol set, ignore others), and reconciliation frequency.

🟡 부분 · 우선순위 must · 확신 medium

Manual and bot orders are indistinguishable, and the bot-only namespace is unproven, so ambiguity fails closed [repo-adr-0003][repo-adr-0014]. A manual buy inflates aggregate holdings and could validate a bot SELL [repo-internal-account]. Candidate: a managed-symbol allowlist plus bot_attributable_qty = min(holdings.quantity, journal-derived position), reconciled on every reduce check and on each reconciler tick. Industry practice is unsourced.

### T14.Q7 · Numeric precision: integer KRW vs decimal USD, share quantities with fractional US shares, and deterministic rounding for digests (ADR-0006) and limit checks.

✅ 답변됨 · 우선순위 should · 확신 medium

Keep raw decimal strings for ADR-0006 digests [repo-internal-order-api][repo-internal-account]. Use a decimal library for limit arithmetic, comparing with Cmp or Equal [shopspring-decimal-pkgdev]. Never hash re-rendered decimals, and never mutate DivisionPrecision at runtime [shopspring-decimal-pkgdev]. Round toward the safe side: Floor for sellable quantity, Ceil for cost and exposure. Toss tick, lot and fractional precision are unverified.

### T14.Q8 · Daily loss / drawdown kill-switch design: measurement window (calendar day in exchange time), inclusion of unrealized PnL, data staleness protections, and reset semantics requiring human clear (ADR-0004 point 6).

🟡 부분 · 우선순위 should · 확신 low

A global halt clears only by a human (point 6). Non-rebuildable counters must be persisted, and the loss trigger is deferred (point 7) [repo-adr-0004]. ADR-0012 requires a durable-before-visible halt write. The dailyProfitLoss window is undocumented [repo-internal-account]. Staleness should fail closed (point 3). There are no external design references.

### T14.Q9 · Multi-account and multi-market scoping: one accountSeq per bot instance, KR and US positions in one account, and whether exposure limits are per market or global.

🟡 부분 · 우선순위 nice · 확신 low

One accountSeq can hold KR and US positions; marketCountry and currency appear per item, and the overview splits KRW and USD [repo-internal-account]. Per-market vs global limits are undecided. A global limit needs an FX rate source, which nothing collected covers.

## 프로젝트 함의

- ADR-0004 point 2 and line 119: candidate reducing check is sellable = holdings.quantity - (LIVE-tracker acked-open SELL remaining + unresolved/ambiguous SELL at full size); a SELL is reducing iff qty <= sellable. Stale or missing holdings means not proven, so the order is new exposure (point 3).
- ADR-0003/0014: the bot-only namespace is unproven, and a manual buy can validate a bot SELL. Bounding by min(holdings, journal position) or an allowlist needs an /architect ADR before /issue-drafter.
- orderAmount SELLs should be classified as new exposure.
- ADR-0004 point 7 + ADR-0012: a loss trigger must be a durable killswitch write that a human clears; the broker's dailyProfitLoss window is undocumented.
- ADR-0004 point 7: insufficient-funds rejections would count toward the consecutive-failure halt, so either add a pre-submit cash check or decide the counting rule in an ADR.
- ADR-0006: hash raw decimal strings and use a decimal library only for comparisons.
- CLAUDE.md requires checking the spec first, but openapi.tossinvest.com is blocked in the cloud session. Fetching the spec is a precondition for exposure/risk issues.

## 출처 간 상충

- No conflicting external sources (only one, off-core). Internal tension: ADR-0004 point 2 requires a holdings-based proof, while ADR-0003 calls holdings a weak signal and ADR-0014 says the bot-only namespace is unproven. The proof is sound only if non-bot activity is bounded.

## 남은 공백

- **T14.Q1** — FIX PositionEffect, reduce-only semantics, pre-trade worst-case exposure practice. (추가 검색: FIX 4.4 PositionEffect tag 77; reduce-only order semantics open orders; SEC Rule 15c3-5 pre-trade controls)
- **T14.Q2** — Live Toss holdings schema; openapi.tossinvest.com is blocked. (추가 검색: fetch https://openapi.tossinvest.com/openapi-docs/latest/openapi.json holdings; 토스증권 매도가능수량 보유수량)
- **T14.Q3** — Orderable-amount API, settlement reuse rules, FX conversion, insufficient-funds error code. (추가 검색: 토스증권 주문가능금액 D+2 재매수; SEC T+1 Rule 15c6-1; 토스증권 해외주식 자동환전)
- **T14.Q4** — Korean overseas cost basis method, FX gains, afterCost methodology. (추가 검색: 해외주식 양도소득세 취득가액 선입선출 이동평균; 소득세법 시행령 제162조)
- **T14.Q5** — How corporate actions show up in Toss holdings. (추가 검색: 액면분할 보유수량 반영 시점 예탁결제원; corporate action position reconciliation)
- **T14.Q6** — Industry practice for bot plus manual trading in one account; Toss multi-account support. (추가 검색: trading bot manual trades same account reconciliation; 토스증권 Open API accountSeq 여러 계좌)
- **T14.Q7** — Toss tick/lot and fractional precision; orderAmount minimum. (추가 검색: 토스증권 소수점 주문 최소 금액; Toss Open API orderAmount precision)
- **T14.Q8** — Daily-loss breaker design references; dailyProfitLoss window. (추가 검색: FIA automated trading risk controls daily loss limit)
- **T14.Q9** — Per-market vs global limit practice; FX rate source. (추가 검색: multi-currency portfolio exposure limit FX)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|
| `repo-adr-0004` | ADR-0004: kill switch submit guard (points 1-3, 6, 7) | toss-trade-bot maintainers | 2026 | official-doc | <../../docs/adr/0004-kill-switch-submit-guard.md> | 5 | 5 | Point 2: a close tag is verified against holdings plus unresolved open intents; if reduction is unproven, the order counts as new exposure.; Line 119: the reducing-verification algorithm and the order/risk-to-account boundary are left to an implementation issue.; Point 1: queries, reconciler and cancels keep running during a halt. |
| `repo-adr-0003` | ADR-0003: order reconciliation via orderId detail; ambiguous submit fails closed | toss-trade-bot maintainers | 2026 | official-doc | <../../docs/adr/0003-order-reconciliation.md> | 5 | 5 | GET /api/v1/orders supports only status=OPEN.; /api/v1/trades has no orderId or symbol.; /api/v1/holdings is aggregate and only a weak indirect signal. |
| `repo-adr-0014` | ADR-0014: reconciler escalation and bounded recount | toss-trade-bot maintainers | 2026 | official-doc | <../../docs/adr/0014-reconciler-escalation-bounded-recount.md> | 4 | 5 | The LIVE tracker holds acked-open orders without creating symbol blocks.; The auto-recovery invariants include a bot-only namespace.; ClearSymbol clears a symbol only when its count of unresolved-ambiguous intents is 0. |
| `repo-internal-account` | internal/account/account.go - Holdings/Overview wire types | toss-trade-bot maintainers | 2026 | code | <../../internal/account/account.go> | 5 | 4 | The only quantity field is a single 'quantity'.; Overview USD is null when there are no US holdings.; Broker P&L fields exist, but their methodology is undocumented. |
| `repo-internal-order-api` | internal/order/api.go + doc.go - OrderRequest encoding | toss-trade-bot maintainers | 2026 | code | <../../internal/order/api.go> | 4 | 4 | OrderAmount is USD, US MARKET only, and mutually exclusive with Quantity.; Decimal strings are passed through so ADR-0006 digests stay exact.; The server rejects fractional-quantity violations with *APIError. |
| `shopspring-decimal-pkgdev` | shopspring/decimal - Go package documentation | shopspring contributors | 2026 | official-doc | <https://pkg.go.dev/github.com/shopspring/decimal> | 3 | 4 | Add, Sub and Mul are exact; Div rounds to DivisionPrecision (global; mutating it concurrently is a race).; Explicit Round, RoundBank, RoundFloor, RoundCeil and Truncate.; Compare with Cmp or Equal, not ==. |

### 출처 요약

- **`repo-adr-0004`** — Internal primary source. The kill switch is a synchronous CanSubmit predicate on the new-exposure submit edge, re-checked fail-closed before the submit-attempted marker and blocked until the boot replay finishes. Point 2: the strategy's close tag is untrusted, and order/risk must prove reduction from holdings plus unresolved open intents. The verification algorithm and the order/risk-to-account boundary are deferred to an implementation issue (Open questions, line 119, re-confirmed this session). Point 7 defers the loss trigger. _(접근: private repo, local file)_
- **`repo-adr-0003`** — Internal primary source on the API facts that limit portfolio accounting: GET /orders lists only OPEN orders, /trades has no orderId or symbol, and /holdings is aggregate. Payload-matching recovery is rejected because manual orders cannot be told apart from bot orders. _(접근: private repo, local file)_
- **`repo-adr-0014`** — Internal primary source. Acked-open orders go to a non-blocking LIVE tracker, which is the natural source of open SELL intents for the reduce check. Auto-recovery requires three invariants, including a bot-only namespace, and that invariant does not hold today. _(접근: private repo, local file)_
- **`repo-internal-account`** — HoldingsItem has symbol, marketCountry, currency, quantity, lastPrice, averagePurchasePrice, marketValue, profitLoss (including afterCost), dailyProfitLoss and cost{commission, tax?}. The overview CurrencyAmount is {krw, usd nullable}. There is no sellable, orderable or settlement-pending field. _(접근: private repo. May be a subset of the live spec, which was unreachable.)_
- **`repo-internal-order-api`** — The order request is a oneOf of Quantity or OrderAmount (a USD notional, US MARKET orders only). All numeric fields are raw decimal strings. The server decides fractional eligibility and tick size. _(접근: private repo, local file)_
- **`shopspring-decimal-pkgdev`** — Docs for an arbitrary-precision decimal library: exact add, subtract and multiply, rounded division, explicit rounding modes, comparison semantics and float hazards. Bears only on T14.Q7. Not re-fetched. _(접근: open access)_
