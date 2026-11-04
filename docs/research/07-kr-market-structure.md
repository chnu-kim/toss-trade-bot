# Korean equity market structure as it affects a bot (KRX + Nextrade ATS, hours, VI, limits, ticks, settlement)

> **왜 중요한가** — Target market undecided; internal/market/calendar.go models KrSession/IntegratedHour already; CLAUDE.md requires market-time awareness via market-calendar. Nextrade (NXT) ATS launched in 2025 changed KR trading hours (pre/after-market) and VI behaviour; the order path must validate tick sizes and price limits to avoid rejects counting toward the halt threshold. Feeds the strategy ADR and internal/market extensions.

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 8건. 상태 집계: 🟡 부분 9, ❌ 미답 3

## 핵심 결론

- (완전 답변된 질문 없음 — 아래 부분 답변 참조)

## 질문별 답변

### T7A.Q1 · Current KRX regular session, pre-market (장전 시간외), after-market (장후 시간외), 시간외 단일가 windows, opening/closing single-price auctions (동시호가), and random-end rules — exact times and order types accepted in each.

🟡 부분 · 우선순위 must · 확신 low

No new external evidence this round: the source pool was empty, and no web calls were made, to keep usage low. The only evidence is structural. Toss's KR calendar returns preMarket, regularMarket and afterMarket sessions per day, each with an optional single-price-auction sub-window [repo-market-calendar-go]. The bot should read auction and 시간외 windows from the API each day rather than hard-code them. The order schema exposes only LIMIT and MARKET with timeInForce DAY, so session-specific order types (시간외 종가, 단일가) are not available to the bot [repo-order-api-go]. Exact KRX times and random-end rules are unverified.

### T7A.Q2 · Nextrade (넥스트레이드) ATS: trading hours (pre-market 08:00, main, after-market to 20:00), eligible symbols, order types (mid-price orders), SOR/best-execution obligations of brokers, and whether Toss routes to NXT and exposes it in the API/calendar.

🟡 부분 · 우선순위 must · 확신 low

The Toss calendar is labelled KRX+NXT integrated and includes pre and after sessions [repo-market-calendar-go]. The order schema has no venue selector and no mid-price order type [repo-order-api-go]. Routing therefore appears to be broker-side SOR, and NXT-specific order types are not reachable from the bot. Two search snippets (unverified, and they conflict) put NXT at roughly 08:00-08:50 pre-market and 15:30/15:40-20:00 after-market, with mid-price orders and SOR [blockmedia-nxt-hours-866421][kiwoom-nxt-guide-2025]. The eligible-symbol list and best-execution obligations are unsourced.

### T7A.Q3 · Price limits (±30%) and Volatility Interruption (dynamic/static VI) thresholds per market segment, cooling periods, reopening auctions, and how a bot detects VI state or gets its order rejected during VI.

🟡 부분 · 우선순위 must · 확신 low

Thresholds are unsourced. The Price and Orderbook types carry no VI-state, limit-price or halt fields [repo-market-go], so the bot cannot detect VI or price limits before it places an order. A server reject comes back as a generic APIError [repo-order-api-go]. Each reject counts toward a global-halt streak with a default threshold of 3 and no reason classification [repo-killswitch-reject-path]. Repeated rejects during VI can therefore halt the bot. The ±30% limit, the VI thresholds and the cooling and reopening rules are unverified.

### T7A.Q4 · Tick size table (호가가격단위) for KOSPI/KOSDAQ by price band after the 2023 revision, order quantity units, and odd-lot rules; how to round limit prices client-side.

🟡 부분 · 우선순위 must · 확신 low

The 2023 tick table is unsourced. The repo leaves tick validation to the server and does no client-side rounding [repo-order-api-go]. Rejects count toward the halt threshold of 3 [repo-killswitch-reject-path]. Quantity is a decimal string, and the server decides fractional eligibility [repo-order-api-go]. Lot and odd-lot rules are unverified.

### T7A.Q5 · Market-wide circuit breakers and sidecar rules, trading halts for disclosures (거래정지), 투자경고/위험 종목 restrictions (e.g. 위탁증거금 100%, 신용 금지), 관리종목/정리매매 — and which of these a retail API can detect from stock-info endpoints.

❌ 미답 · 우선순위 should · 확신 low

No sources on circuit breakers, sidecars, 거래정지, 투자경고/위험 or 관리종목. The repo models no stock-status fields [repo-market-go]. Whether Toss exposes these is unverified.

### T7A.Q6 · Settlement (T+2), cash availability for re-use (매도대금 재사용), 미수/반대매매 risk for a cash account, and how 예수금/주문가능금액 appear via the API.

🟡 부분 · 우선순위 must · 확신 low

Order execution includes settlementDate, commission and tax [repo-order-api-go], so the bot can read the settlement date from each fill instead of assuming T+2. The account wrapper has no 예수금 or 주문가능금액 field [repo-account-go]. T+2, 매도대금 재사용 and 미수/반대매매 are unverified.

### T7A.Q7 · Holiday calendar sources (KRX 휴장일 공시), half-days (e.g. 수능일 1시간 지연 개장, 연말 폐장일), and how far ahead KRX publishes them; cross-check against Toss market-calendar.

🟡 부분 · 우선순위 should · 확신 low

The Toss calendar returns only the previous, current and next business day, and a full holiday comes back as integrated: null [repo-market-calendar-go]. It cannot serve as a long-horizon holiday source. A delayed open or early close would show up only as shifted session times. The KRX 휴장일 notice source and its lead time are unverified.

### T7A.Q8 · Corporate actions handling: ex-dividend, rights issues, stock splits/reverse splits, 액면분할 trading suspension windows, ticker changes, delisting — data sources (KIND, DART) and how holdings quantities change in the account API.

❌ 미답 · 우선순위 should · 확신 low

No sources on corporate actions or on KIND/DART. Holdings carry no corporate-action fields [repo-account-go]. Startup reconciliation from the API should pick up quantity changes, but this is unverified.

### T7A.Q9 · Transaction costs: 증권거래세 and 농어촌특별세 rates (2025 values), Toss commission schedule for domestic stocks, and typical spread by liquidity tier — inputs to backtest cost models.

🟡 부분 · 우선순위 must · 확신 low

Commission and tax are returned per fill [repo-order-api-go], and cost is returned per holding [repo-account-go], so the cost model can be calibrated from real fills. The 2025 증권거래세 and 농특세 rates, Toss's commission schedule and spread tiers are unsourced.

### T7A.Q10 · Short selling for retail (대주/공매도 재개 2025, 업틱룰), 신용거래 — whether the Open API supports them and whether the bot should structurally exclude them.

🟡 부분 · 우선순위 nice · 확신 low

The order schema has only BUY/SELL with LIMIT/MARKET, and no credit, short or 대주 flags [repo-order-api-go]. Shorting and margin therefore appear unsupported, and the bot is structurally long-only and cash-only. This was not checked against the full spec. The details of the 2025 short-selling resumption and the uptick rule are unsourced.

### T7A.Q11 · Market data specifics: level-1 vs 10-level orderbook availability, 체결강도, VWAP fields, data latency of REST polling, and whether Toss market data redistribution terms matter.

🟡 부분 · 우선순위 should · 확신 low

The orderbook is asks/bids {price, volume} lists of unspecified depth. Price carries only lastPrice and a timestamp. There are no 체결강도 or VWAP fields, and REST polling is assumed [repo-market-go]. Depth, latency and redistribution terms are unverified.

### T7A.Q12 · Daily trading restrictions for individuals: 단일가 매매 적용 종목, 단기과열종목 rules, ELW/ETN/ETF special sessions (e.g. ETF 괴리율), and how to filter the tradable universe.

❌ 미답 · 우선순위 nice · 확신 low

No sources on 단일가 매매 적용 종목, 단기과열종목 or ETF/ETN/ELW rules. No universe-status fields are modelled [repo-market-go].

## 프로젝트 함의

- This round added no new evidence: the pool was empty, and no extra web calls were made, per the user's instruction not to overuse the model. Do not write the strategy ADR or the tick and limit validators from this corpus. Rerun only when a fresh search budget and the KRX, NXT and Toss hosts are available.
- ADR-0004/0012 halt risk, confirmed in code. drive.go sends every verdictRejected to ReportOrderFailure without classifying the reason, and the default threshold is 3. Three deterministic rejects in a row (tick, limit, VI or session) would cause a global halt that only a human can clear. There are two fixes. (a) Client-side pre-validation, which needs the Q4 tick table and the daily limit prices that Price does not model. (b) An /architect amendment to ADR-0004 that classifies deterministic rejects separately. Option (b) is sacred killswitch logic, so it falls under the W0 timing rule.
- internal/market/calendar.go IsRegularOpen ignores the NXT-integrated pre and after sessions and the auction sub-windows. Extending the order gate to cover them needs an ADR.
- The Toss calendar covers only three days, so the bot must re-poll it daily and treat a fetch failure as closed (fail-closed, consistent with ADR-0004).
- ADR-0002 line 33 conflicts with the order/api.go comment about a 10-minute replay window. Resolve this through an /architect amendment to ADR-0002 after re-checking openapi.json.
- Order.execution returns settlementDate, commission and tax. These let the backtest cost model and the cash-availability logic be calibrated from real fills.

## 출처 간 상충

- The two NXT search snippets disagree. Block Media gives the main session as 09:00-15:30 and the after-market from 15:30. The Kiwoom guide gives the main session as 09:00:30-15:20 and the after-market from 15:40. Both are unverified. Resolve against nextrade.co.kr, or against the per-day session times in the Toss calendar.
- Within the repo, ADR-0002 line 33 says duplicate-clientOrderId behaviour is undefined in the spec. The order/api.go comment claims a 10-minute replay window. Re-check the live spec.
- Design tension within the repo: ADR-0004 treats rejects as a halt signal, while order/api.go leaves tick and hours validation to the server.

## 남은 공백

- **T7A.Q1** — Exact KRX session, auction, 시간외 and random-end times, and the order types accepted in each window. (추가 검색: KRX 유가증권시장 업무규정 시행세칙 매매거래시간 시간외 단일가; KRX 동시호가 랜덤엔드)
- **T7A.Q2** — NXT hours (to reconcile the conflicting snippets), eligible symbols, mid-price orders, SOR rules, and whether Toss routes to NXT. (추가 검색: site:nextrade.co.kr 거래시간; 넥스트레이드 중간가호가 거래대상종목; 토스증권 넥스트레이드 SOR)
- **T7A.Q3** — VI thresholds and cooling periods, and any VI or limit fields in the Toss spec. (추가 검색: KRX 변동성완화장치 동적 정적 VI 발동기준; openapi.tossinvest.com openapi.json upperLimitPrice)
- **T7A.Q4** — The 2023 tick table, lot rules and odd-lot rules. (추가 검색: 2023 호가가격단위 개편 유가증권 코스닥)
- **T7A.Q5** — Circuit-breaker and sidecar thresholds, 시장경보 restrictions, and stock-status fields. (추가 검색: KRX 서킷브레이커 사이드카 기준; 투자경고종목 위탁증거금 100%)
- **T7A.Q6** — T+2 and cash-reuse rules, 미수/반대매매, and a buying-power endpoint. (추가 검색: 매도대금 재사용 D+2 미수 반대매매; 토스증권 주문가능금액 API)
- **T7A.Q7** — The KRX holiday notice and its lead time, and the 수능일 and year-end rules. (추가 검색: KRX 휴장일 공시; 수능일 증시 개장 지연)
- **T7A.Q8** — Corporate-action data sources and how account quantities change. (추가 검색: KIND 권리락 액면분할 매매거래정지; DART OpenAPI 주요사항보고서)
- **T7A.Q9** — Tax rates, the Toss commission schedule and spread tiers. (추가 검색: 2025 증권거래세율 농어촌특별세; 토스증권 국내주식 수수료)
- **T7A.Q10** — Details of the short-selling resumption and the uptick rule. (추가 검색: 공매도 재개 2025 개인 대주; 업틱룰 KRX)
- **T7A.Q11** — Orderbook depth, latency and redistribution terms. (추가 검색: Toss Open API orderbook depth rate limit; 토스증권 Open API 이용약관 시세)
- **T7A.Q12** — 단일가 and 단기과열 rules, ETF/ETN/ELW rules, and universe filtering. (추가 검색: 단기과열종목 단일가매매 지정요건; ETF 괴리율 LP 호가 의무)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|
| `repo-killswitch-reject-path` | internal/reconciler/drive.go verdictRejected branch + internal/killswitch/report.go ReportOrderFailure + internal/config DefaultOrderFailureThreshold | toss-trade-bot maintainers | 2026 | code | <../../internal/reconciler/drive.go> | 5 | 4 | Every rejected order increments a persisted streak counter, with no classification of the reject reason.; The default threshold of 3 consecutive failures trips a global halt that only a human can clear.; Cancels neither increment nor reset the streak. |
| `repo-adr-0002` | ADR-0002: Order intent identity (write-ahead journal; orderId as the post-hoc key; clientOrderId for server-side dedup only) | toss-trade-bot maintainers | 2026 | official-doc | <../../docs/adr/0002-order-intent-identity.md> | 3 | 4 | clientOrderId exists only in OrderResponse.; Duplicate-clientOrderId behaviour is undefined in the spec, per line 33.; Status CLOSED listing is unsupported (400). |
| `repo-market-calendar-go` | internal/market/calendar.go (KR KRX+NXT integrated calendar types: KrSession, IntegratedHour, KrMarketDay) | toss-trade-bot maintainers | 2026 | code | <../../internal/market/calendar.go> | 5 | 3 | The Toss KR calendar is KRX+NXT integrated, with preMarket, regularMarket and afterMarket sessions.; Each session may carry an optional singlePriceAuctionStartTime/EndTime pair.; A full holiday is returned as integrated: null. |
| `repo-order-api-go` | internal/order/api.go (OrderRequest/Order schema wrapper for POST/GET /api/v1/orders) | toss-trade-bot maintainers | 2026 | code | <../../internal/order/api.go> | 4 | 3 | Only LIMIT and MARKET order types exist, and timeInForce defaults to DAY.; There is no KRX/NXT venue selector and no mid-price order type.; Tick-size and market-hours validation happen on the server, and violations surface as APIError. |
| `repo-market-go` | internal/market/market.go (Price and Orderbook types) | toss-trade-bot maintainers | 2026 | code | <../../internal/market/market.go> | 4 | 3 | The orderbook is asks/bids {price, volume} lists of unspecified depth.; There are no VI, limit-price, halt or VWAP fields.; REST polling is assumed because WebSocket availability is unconfirmed. |
| `repo-account-go` | internal/account/account.go (accounts and holdings overview) | toss-trade-bot maintainers | 2026 | code | <../../internal/account/account.go> | 3 | 3 | Each holding carries Cost {commission, tax?}.; No buying-power or settlement-cash field is modelled. |
| `blockmedia-nxt-hours-866421` | 출근 전, 퇴근 후에도 주식투자 가능해진다 (Block Media) | Block Media | 2025 | blog | <https://www.blockmedia.co.kr/archives/866421> | 3 | 2 | Snippet only: NXT pre-market 08:00-08:50, main 09:00-15:30, after-market 15:30-20:00 (unverified).; Snippet only: brokers route through a best-execution SOR (unverified). |
| `kiwoom-nxt-guide-2025` | Kiwoom Securities Alternative Exchange (Nextrade) Guide Booklet | Kiwoom Securities | 2025 | industry-report | <https://ocrw.kiwoom.com/pdf/guide/Kiwoom_Securities_Alternative_Exchange_Guide_Booklet_Single-Sided.pdf> | 3 | 2 | Snippet only: main market 09:00:30-15:20, after-market 15:40-20:00 (unverified).; Snippet only: NXT offers mid-price and stop-limit orders (unverified). |

### 출처 요약

- **`repo-killswitch-reject-path`** — Verified in an earlier round. Every order the reconciler classifies as verdictRejected calls guard.ReportOrderFailure(reasonOrderRejected), which increments a durable order_failure_streak counter. The global halt trips at OrderFailureThreshold, which defaults to 3 and can be overridden with TOSS_BOT_ORDER_FAILURE_THRESHOLD. The reject reason is not classified, so deterministic business-rule rejects (tick, limit, VI, session) count the same as any other failure. _(접근: Local repo files.)_
- **`repo-market-calendar-go`** — Models the Toss KR market-calendar response, which is labelled KRX+NXT integrated. It has optional preMarket, regularMarket and afterMarket sessions, each with start and end times and an optional single-price-auction sub-window. A full holiday comes back as integrated: null. Only the previous, current and next business days are returned. IsRegularOpen checks only the regular [start,end) window. _(접근: Local repo file, a secondary reading of the Toss spec. Not re-verified against the live openapi.json.)_
- **`repo-order-api-go`** — Wraps the Toss order schema. The order types are LIMIT and MARKET, and timeInForce defaults to DAY. quantity and orderAmount are oneOf, with orderAmount allowed only for US MARKET orders. Execution fields include commission, tax and settlementDate. Tick size, market hours and fractional eligibility are enforced on the server and come back as *APIError. There is no venue field, no mid-price order type and no after-hours order type. _(접근: Local repo file.)_
- **`repo-market-go`** — Price has symbol, timestamp, lastPrice and currency. Orderbook holds asks and bids as {price, volume} lists with no fixed depth. Nothing models VI state, upper or lower limit prices, halts, 체결강도 or VWAP. REST polling is assumed. _(접근: Local repo file.)_
- **`repo-account-go`** — Models per-currency totals, market value, P/L and per-holding cost (commission, with tax nullable). There is no 예수금, 주문가능금액 or D+2 cash field, and there are no corporate-action fields. _(접근: Local repo file.)_
- **`repo-adr-0002`** — clientOrderId is exposed only in OrderResponse and is absent from the Order list and detail schemas. The spec does not define server behaviour on a duplicate clientOrderId (replay vs 409); see line 33. clientOrderId is derived deterministically from intentId and is not a matching key. _(접근: Local repo ADR.)_
- **`blockmedia-nxt-hours-866421`** — News article on Nextrade extended hours, seen only as a search snippet. The snippet gives NXT pre-market 08:00-08:50, main 09:00-15:30 and after-market 15:30-20:00, with broker SOR across KRX and NXT. Secondary and unverified. _(접근: Korean-only. Not fetched.)_
- **`kiwoom-nxt-guide-2025`** — A broker's investor guide to NXT, seen only as a search snippet. The snippet gives the main market as 09:00:30-15:20 and the after-market from 15:40, and it mentions mid-price and stop-limit orders. Unverified, and it conflicts with the Block Media snippet. _(접근: Not fetched. Snippet only.)_
