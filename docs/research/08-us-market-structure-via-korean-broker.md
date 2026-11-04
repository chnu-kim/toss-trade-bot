# US equity market structure for a KR-based retail bot (hours, extended sessions, LULD, ticks, settlement, FX)

> **왜 중요한가** — Target market undecided; internal/market/calendar.go has UsSession/UsMarketDay; order API supports orderAmount (USD, US only). Running a US bot from Korea adds FX, DST, overnight operation (bot active at KST night), and broker-specific session coverage. Feeds strategy ADR and calendar logic.

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 2건. 상태 집계: 🟡 부분 4, ❌ 미답 8

## 핵심 결론

- (완전 답변된 질문 없음 — 아래 부분 답변 참조)

## 질문별 답변

### T7B.Q1 · US regular hours (09:30-16:00 ET), pre-market (04:00-09:30) and after-hours (16:00-20:00), and emerging 24/5 or overnight sessions (Blue Ocean ATS, Nasdaq/NYSE 24-hour proposals 2025-2026) — which sessions Toss supports for US orders and how the calendar endpoint represents them.

🟡 부분 · 우선순위 must · 확신 low

No external source has been verified; all collection pools are empty. Repo evidence only: internal/market/calendar.go models the US calendar day (UsMarketDay) with four nullable sessions: DayMarket (json "dayMarket", line 69), preMarket, regularMarket and afterMarket. IsTradingDay (line 84) and IsRegularOpen (line 90) look only at RegularMarket. Not verified: Toss's hours for each session, which sessions accept orders, and the 2025-2026 status of Blue Ocean ATS and the Nasdaq/NYSE 24-hour proposals.

### T7B.Q2 · US holidays and early closes (NYSE calendar), DST transitions (March/November) and the KST mapping; how to avoid off-by-one-hour errors in Go time zones.

🟡 부분 · 우선순위 must · 확신 medium

Go side (verified): resolve America/New_York with time.LoadLocation, and embed time/tzdata in cmd/bot main or build with -tags timetzdata [go-time-tzdata-pkgdoc]. Wall-clock times that DST skips or repeats are ambiguous, and AddDate steps can be 23 or 25 hours long, so build session gates from offset-bearing instants and never from a fixed KST offset [go-time-loadlocation-dst]. Repo: withinSession (calendar.go:120) parses RFC3339 bounds and compares absolute instants, so it is DST-safe if Toss returns offsets. The US-local date argument must not be derived from KST time.Now(). Not verified: the NYSE 2026-2027 holiday and early-close lists, and the DST dates (general knowledge, unsourced: 2nd Sunday of March to 1st Sunday of November).

### T7B.Q3 · Limit Up-Limit Down (LULD) bands, market-wide circuit breakers (7/13/20%), trading halts (news pending, SEC suspensions), and how a bot sees them via a REST broker API.

❌ 미답 · 우선순위 should · 확신 low

No source collected. LULD bands, circuit-breaker levels, halt types and how Toss REST shows a halt are all unverified.

### T7B.Q4 · Tick size and order constraints: Rule 612 sub-penny, SEC 2024 tick-size/access-fee amendments (effective dates, half-penny ticks), round lots and odd lots, fractional shares via Toss (orderAmount).

🟡 부분 · 우선순위 should · 확신 low

No external source. Repo: internal/order/api.go supports OrderAmount (USD notional, US MARKET only) as an XOR alternative to Quantity. Not verified: Rule 612, the SEC 2024 tick-size and access-fee amendments, and odd-lot and round-lot definitions.

### T7B.Q5 · Settlement T+1 (since May 2024), cash account 'good faith violation' / free-riding rules, and how buying power with unsettled funds appears in Toss accounts.

❌ 미답 · 우선순위 must · 확신 low

No source collected. T+1, good-faith-violation rules and Toss's orderable-amount semantics are all unverified. internal/ has no buying-power field.

### T7B.Q6 · Pattern Day Trader rule (FINRA 4210) applicability to a Korean retail investor trading via a Korean broker's omnibus account — does Toss impose PDT-like limits; any 2025 FINRA PDT amendments.

❌ 미답 · 우선순위 should · 확신 low

No source collected on FINRA 4210 PDT, its amendments, or whether an omnibus account applies it.

### T7B.Q7 · FX handling: how Toss converts KRW↔USD (통합증거금 vs 환전), exchange-rate endpoint cadence, FX spread cost, and whether orders fail when KRW is not pre-converted — inputs to position sizing.

❌ 미답 · 우선순위 must · 확신 low

No source collected on 통합증거금 (integrated margin) vs. 환전 (currency exchange), the exchange-rate endpoint, the FX spread, or what happens to an order when KRW is not converted. internal/ has no FX client.

### T7B.Q8 · US market data via a Korean broker: delayed vs real-time, consolidated vs single exchange, data licensing terms (non-professional subscriber status) and redistribution limits.

❌ 미답 · 우선순위 should · 확신 low

No source collected on US quote latency, data source or licensing via Toss.

### T7B.Q9 · US corporate actions (splits, dividends with 15% withholding under KR-US treaty, spin-offs, symbol changes, delistings to OTC) and their representation in holdings.

❌ 미답 · 우선순위 should · 확신 low

No source collected on corporate actions, 15% treaty withholding, or how they appear in holdings.

### T7B.Q10 · Transaction costs for US stocks via Toss: commission, SEC Section 31 fee, FINRA TAF, FX spread; typical spreads for large-cap vs small-cap — for backtest cost models.

❌ 미답 · 우선순위 must · 확신 low

No source collected. The Toss commission, Section 31 fee, FINRA TAF, FX spread and bid-ask spreads are all unverified, so a backtest cost model has no verified inputs.

### T7B.Q11 · Overnight operation implications for a KST operator: bot trades while the operator sleeps — alerting latency expectations, and whether to restrict strategy to specific US sessions.

🟡 부분 · 우선순위 should · 확신 medium

Reasoning from the repo, with no external source. The US regular session falls during KST night. External alerting is undecided (CLAUDE.md), and only a human can release a global halt (ADR-0004/0012/0013/0014). A halt during US hours therefore stops trading until the operator wakes. That argues for limiting a first US strategy to the regular session (IsRegularOpen) and making alerting a prerequisite.

### T7B.Q12 · Regulatory/order-handling details that affect fills: Reg NMS order protection, broker routing/payment for order flow for retail orders from Korean brokers, and market-order fill quality in pre/after-hours (why limit-only is common in extended hours).

❌ 미답 · 우선순위 nice · 확신 low

No source collected on Reg NMS, routing or PFOF for Korean-broker flow, or extended-hours fill quality.

## 프로젝트 함의

- internal/market/calendar.go withinSession (line 120) compares RFC3339 instants, so it is DST-safe if Toss timestamps carry offsets. Add fixtures that straddle the March and November US transitions [go-time-loadlocation-dst].
- Callers that compute the US-local date for UsCalendar must use time.LoadLocation("America/New_York"), and cmd/bot/main.go should blank-import time/tzdata [go-time-tzdata-pkgdoc]. cmd/bot/main.go is a shared sacred touchpoint, so this edit must land before W0 (ADR-0016 point 5).
- UsMarketDay.IsTradingDay (line 84) and IsRegularOpen (line 90) key only on RegularMarket; DayMarket (line 69) and pre/after are modeled but unused. That is fail-closed, but the strategy ADR must say explicitly whether these sessions are eligible.
- The OrderAmount (USD notional) path means ADR-0002 order-intent identity and ADR-0003 reconciliation must stay stable when the fill quantity is fractional.
- No FX client or buying-power field exists, and Q5, Q7 and Q10 are unresearched. A US-market strategy ADR is blocked until they are.
- Overnight operation, a human-only halt release (ADR-0004/0012/0013/0014) and undecided alerting together make external alerting a prerequisite ADR for any US-market choice.

## 출처 간 상충

- None. Only two complementary Go documentation sources were verified.

## 남은 공백

- **ALL** — Every pool is empty. The WebSearch budget is exhausted, and the network allowlist lacks developers.tossinvest.com, openapi.tossinvest.com, sec.gov, finra.org and nyse.com. This round did no new searching or fetching, in line with the user's request not to overuse the model; it only corrected repo line references. (추가 검색: Re-run in a fresh turn with search budget available and those domains allowed)
- **T7B.Q1** — Toss US session hours and the calendar timestamp format; 24-hour trading status (추가 검색: openapi.tossinvest.com openapi.json market-calendar US dayMarket; 토스증권 미국주식 데이마켓 프리마켓 애프터마켓 거래시간; Blue Ocean ATS overnight Korean brokers 2026)
- **T7B.Q2** — NYSE 2026-2027 holidays and early closes (추가 검색: NYSE holidays and trading hours 2026 2027 early close)
- **T7B.Q3** — Primary sources for LULD and circuit breakers; how Toss represents a halt (추가 검색: LULD plan Tier 1 Tier 2 bands; NYSE Rule 7.12 circuit breaker; 토스증권 미국주식 거래정지 주문 거부)
- **T7B.Q4** — SEC tick-size and access-fee release and compliance dates (추가 검색: SEC Release 34-101070 compliance date; 토스증권 소수점 주문 최소 금액)
- **T7B.Q5** — T+1 primary source; Toss orderable-amount semantics (추가 검색: SEC Release 34-96930 T+1; openapi.tossinvest.com orderable amount)
- **T7B.Q6** — Status of the FINRA 4210 PDT amendment (추가 검색: FINRA Rule 4210 pattern day trader amendment SEC approval)
- **T7B.Q7** — Toss FX mechanics and endpoint (추가 검색: 토스증권 통합증거금 환전; openapi.tossinvest.com exchange-rate)
- **T7B.Q8** — Toss US quote source and licensing (추가 검색: 토스증권 미국주식 실시간 시세)
- **T7B.Q9** — Withholding and corporate-action handling (추가 검색: 한미조세조약 배당 원천징수 15%)
- **T7B.Q10** — Fee figures (추가 검색: SEC Section 31 fee rate fiscal 2026; FINRA TAF rate 2026; 토스증권 미국주식 수수료)
- **T7B.Q11** — No external source on alerting latency for overnight operation (추가 검색: unattended trading bot alerting on-call latency practice)
- **T7B.Q12** — Reg NMS and extended-hours risks (추가 검색: SEC Regulation NMS Rule 611; FINRA extended hours trading limit orders)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|
| `go-time-tzdata-pkgdoc` | Package tzdata (Go standard library time/tzdata) | The Go Authors | 2026 | official-doc | <https://pkg.go.dev/time/tzdata> | 4 | 5 | Embeds a copy of the timezone database, used only when system tzdata is not found; Adds roughly 450 KB to the binary; Building with -tags timetzdata imports it automatically |
| `go-time-loadlocation-dst` | time package: LoadLocation, Date and AddDate semantics | The Go Authors | 2026 | official-doc | <https://pkg.go.dev/time#LoadLocation> | 4 | 5 | LoadLocation search order: ZONEINFO env, system dirs, $GOROOT zoneinfo.zip, then time/tzdata if imported; Date() with a skipped or repeated local time is ambiguous (US examples: 2011-03-13 02:15, 2011-11-06 01:15); AddDate(0,0,1) keeps the wall-clock time, but the elapsed time can be 23 or 25 hours |

### 출처 요약

- **`go-time-tzdata-pkgdoc`** — Official Go documentation for time/tzdata. The package embeds a copy of the IANA timezone database in the binary, and the time package falls back to it only when system zoneinfo files are missing. A 24/7 bot running in a minimal container needs it so that LoadLocation("America/New_York") cannot fail. The docs say to import it from the main package (here cmd/bot), not from libraries. _(접근: open access; fetched in prior rounds)_
- **`go-time-loadlocation-dst`** — Go time package documentation on the LoadLocation search order and on how Date and AddDate behave at DST transitions. A local time that DST skips or repeats resolves to one of two zones without saying which. Session boundaries are therefore safer built from offset-bearing instants or from civil dates in the exchange's zone than from fixed offsets. _(접근: open access; fetched in a prior round)_
