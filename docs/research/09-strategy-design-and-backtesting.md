# Systematic strategy design, backtesting rigour, and paper-trading substitutes for a retail REST bot

> **왜 중요한가** — Strategy is explicitly undecided (internal/strategy/doc.go is an interface stub; CLAUDE.md '매매 전략·대상 시장은 미정'). The next /architect must choose a strategy family compatible with REST polling, 1-token/1-process limits, rate limits, no sandbox, and fail-closed halts. Also informs intentId contract (ADR-0002 point 2) and risk limits (T3).

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 2건. 상태 집계: ❌ 미답 10, 🟡 부분 3

## 핵심 결론

- (완전 답변된 질문 없음 — 아래 부분 답변 참조)

## 질문별 답변

### T8.Q1 · Which strategy families are empirically viable for retail with daily/intraday REST data and realistic costs — time-series momentum, cross-sectional momentum, mean reversion, pairs/stat-arb, volatility targeting, trend following on ETFs — and what the post-publication decay literature says (McLean & Pontiff).

❌ 미답 · 우선순위 must · 확신 low

No source collected. Every collection pass hit the exhausted shared WebSearch budget, and SSRN and arXiv do not resolve in this session. Following the user's instruction not to overuse the model, this pass made no further web calls. Nothing is asserted from memory.

### T8.Q2 · Korea-specific evidence: momentum/reversal anomalies in KOSPI/KOSDAQ, retail-flow effects, 공매도 restrictions impact, and the 2015 price-limit expansion effects on intraday strategies (KCI/KCMI papers).

❌ 미답 · 우선순위 should · 확신 low

No KCI or KCMI source collected. There is no evidence yet on Korean momentum or reversal, retail-flow effects, 공매도 bans, or the 2015 price-limit expansion.

### T8.Q3 · Backtest pitfalls and corrections: look-ahead and survivorship bias, data-snooping, probability of backtest overfitting (PBO, CSCV), deflated Sharpe ratio, walk-forward/purged k-fold CV (López de Prado), and minimum backtest length.

❌ 미답 · 우선순위 must · 확신 low

No source collected for PBO/CSCV, the deflated Sharpe ratio, purged k-fold, walk-forward testing, or minimum backtest length.

### T8.Q4 · Transaction cost and slippage models for KR and US retail orders: spread, market impact for small orders, commission/tax, fill-probability of limit orders, and how to model polling-latency slippage.

❌ 미답 · 우선순위 must · 확신 low

No source collected on KR or US costs, slippage, limit-order fill models, or Toss fees and order-type fields. The Toss hosts returned ENOTFOUND.

### T8.Q5 · Position sizing and drawdown control: Kelly and fractional Kelly, volatility targeting, max-drawdown-based de-risking, and their interaction with kill-switch thresholds and a small account.

❌ 미답 · 우선순위 must · 확신 low

No external source on Kelly sizing, volatility targeting, or drawdown control. Two repo constraints apply, both re-verified in this pass. (a) ADR-0004 point 7 and its Alternatives keep 'expected loss' only as a future trigger candidate, because holdings are aggregates and trades carry no orderId. Drawdown de-risking therefore has no data path into the kill switch yet. (b) ADR-0004 point 4 says a halt that cannot be re-derived from the journal must be persisted, so any drawdown counter that gates orders must be persisted too.

### T8.Q6 · Paper trading without a sandbox: shadow mode (compute intents, no submit), replay/simulation harness with recorded Toss responses, tiny-size live canary, and statistical tests that shadow results predict live results.

🟡 부분 · 우선순위 must · 확신 low

No external source on dry-run or shadow-mode practice. Repo fit, re-verified in ADR-0003 line 43: an intent that reached 'prepared' with no 'submit-attempted' closes as the terminal 'aborted-before-submit'. A shadow mode can therefore journal intents without ever calling POST. CLAUDE.md bans tests that hit the Toss API, so a replay harness must run on recorded httptest fixtures. A live canary needs human authorization (ADR-0007).

### T8.Q7 · Data sources for backtests: KRX/KIND daily data, Toss historical candles via API, FinanceDataReader/pykrx, US daily data (Yahoo, Tiingo, Polygon), adjustment for splits/dividends, and licensing for OSS inclusion.

🟡 부분 · 우선순위 should · 확신 medium

KR daily data can come from FinanceDataReader (MIT code; reads NAVER from 2000, KRX from 1995, plus Yahoo, FRED and ECOS) [financedatareader-github] or from pykrx, which scrapes KRX and Naver [pykrx-github]. pykrx says data copyright stays with each provider and commercial use must follow provider terms [pykrx-github]. FinanceDataReader states no upstream data terms [financedatareader-github]. So do not commit fetched datasets: commit fetch scripts or synthetic fixtures only. Still uncovered: KRX/KIND terms, Toss historical candles, US vendor licensing, and split/dividend adjustment.

### T8.Q8 · Strategy-to-order contract: intentId derivation (deterministic per signal/bar), signal-to-order latency, order type selection (limit vs market), handling of missed fills and re-quoting, and daily rebalancing vs event-driven loops under REST polling.

🟡 부분 · 우선순위 must · 확신 low

No external source. Repo contract, re-verified: ADR-0002 point 2 makes intentId the strategy's responsibility. The same decision re-emitted keeps the same intentId, and a new decision gets a new one. ADR-0002 point 4 derives clientOrderId from the intentId by hash, as a server-side dedup hope only. ADR-0003 point 4 says the order layer never auto-resubmits; the strategy retries terminal intents with a NEW intentId at the current price. Inference: a re-quote must mint a new intentId, e.g. f(strategyId, symbol, bar, side, requoteSeq). Bar-close or daily-rebalance loops make this derivation simplest. No source covers the choice between limit and market orders or latency.

### T8.Q9 · Risk of over-trading vs rate limits and tax drag: turnover budgets, per-day order caps, and tax-aware holding periods (KR 양도세 for 대주주 only vs US 22% tax).

❌ 미답 · 우선순위 should · 확신 low

No source collected on turnover budgets, order caps, KR or US capital-gains tax rules, or Toss rate limits.

### T8.Q10 · Performance evaluation: Sharpe/Sortino/Calmar, t-stat thresholds under multiple testing (Harvey, Liu & Zhu 'and the cross-section of expected returns'), bootstrap confidence intervals, and reporting conventions for OSS strategy repos.

❌ 미답 · 우선순위 should · 확신 low

No source collected for Harvey-Liu-Zhu t-stat thresholds, Sharpe-ratio statistics, bootstrap confidence intervals, or reporting conventions for OSS strategy repos.

### T8.Q11 · Open-source backtesting/trading frameworks to learn from (Backtrader, Zipline/Zipline-reloaded, vectorbt, Lean/QuantConnect, Nautilus Trader, freqtrade's dry-run) — especially how they model partial fills, slippage, and dry-run modes.

❌ 미답 · 우선순위 nice · 확신 low

No framework documentation was fetched; those hosts were unreachable.

### T8.Q12 · Regime and robustness checks: parameter sensitivity surfaces, out-of-sample across market regimes (2020, 2022), and when to pause a strategy (model-decay detection) — feeding a 'strategy health' kill signal.

❌ 미답 · 우선순위 should · 확신 low

No source collected. Repo constraint: a 'strategy health' kill signal would be a new halt trigger that cannot be re-derived from the journal, so ADR-0004 point 4 requires it to be persisted.

### T8.Q13 · ETF-based and low-frequency strategies as the safest first strategy (daily rebalance, few orders, broad liquidity) — evidence and typical implementations for KR ETFs (KODEX/TIGER) and US ETFs.

❌ 미답 · 우선순위 nice · 확신 low

No source collected on low-frequency KR or US ETF strategies.

## 프로젝트 함의

- Blocking: this corpus cannot support a strategy-family /architect decision. It holds 2 data-tooling sources, and every 'must' question is unanswered beyond its repo-internal parts. Rerun collection in a fresh turn with web budget and allowed hosts (ssrn.com, arxiv.org, freqtrade.io, tossinvest hosts, law.go.kr, kci.go.kr). Per the user's instruction against overusing the model, this pass made no further web calls.
- ADR-0002 point 2 (the strategy assigns intentId) and ADR-0003 point 4 (the strategy retries terminal intents with a new intentId at the current price) together mean a strategy ADR must specify intentId = f(strategy, symbol, bar/signal, side, requoteSeq). Bar-close or daily loops keep this simplest. This is a repo inference, not externally sourced.
- ADR-0004 point 7 defers the expected-loss trigger because holdings are aggregates and trades carry no orderId. Drawdown de-risking (Q5) and a strategy-health kill signal (Q12) therefore need a fill/P&L tracking design first.
- ADR-0004 point 4 applies to any drawdown, turnover or strategy-health counter: it must be persisted in the same store, or a restart resets it.
- Q6: ADR-0003's prepared-only path to aborted-before-submit gives a natural shadow mode. A replay harness should run on httptest fixtures, and a live canary needs human authorization (ADR-0007).
- Do not commit scraped KRX/Naver data or vendor pykrx code, under the OSS-quality rule [pykrx-github][financedatareader-github].

## 출처 간 상충

- None. Both tool READMEs agree that upstream data terms are separate from the code license.

## 남은 공백

- **T8.Q1** — Peer-reviewed evidence on viable strategy families and on post-publication decay (추가 검색: McLean Pontiff 2016 Does academic research destroy stock return predictability; Moskowitz Ooi Pedersen 2012 time series momentum; Hurst Ooi Pedersen century of evidence trend following)
- **T8.Q2** — Korean-market anomaly literature (KCI/KCMI) (추가 검색: KOSPI 모멘텀 반전 효과 실증 KCI; 가격제한폭 30% 확대 2015 효과 자본시장연구원; 공매도 금지 효과 실증 연구)
- **T8.Q3** — Primary papers on PBO, DSR, purged CV and MinBTL (추가 검색: probability of backtest overfitting SSRN 2326253; deflated Sharpe ratio SSRN 2460551; pseudo-mathematics and financial charlatanism minimum backtest length)
- **T8.Q4** — KR/US cost schedules, fill models, and Toss order fields (추가 검색: 증권거래세 세율 2026 law.go.kr; 토스증권 수수료; openapi.tossinvest.com openapi.json orders)
- **T8.Q5** — Sources on Kelly sizing and drawdown control (추가 검색: Thorp Kelly criterion stock market; MacLean Thorp Ziemba fractional Kelly; Grossman Zhou controlling drawdowns)
- **T8.Q6** — Dry-run practice and validation of paper vs live results (추가 검색: freqtrade dry-run documentation; Nautilus Trader sandbox)
- **T8.Q7** — KRX terms, Toss candles, US vendor licensing, and pykrx's code license (추가 검색: KRX 정보데이터시스템 이용약관 재배포; Tiingo terms redistribution; pykrx pyproject.toml license)
- **T8.Q8** — External practice on deterministic client order IDs and re-quoting (추가 검색: deterministic client order id idempotency trading; Lean limit order fill model)
- **T8.Q9** — Tax rules and Toss rate limits (추가 검색: 양도소득세 대주주 기준 2026; 해외주식 양도소득세 22% 250만원; Toss Open API rate limit)
- **T8.Q10** — Multiple-testing thresholds and reporting conventions (추가 검색: Harvey Liu Zhu and the cross-section of expected returns; Lo 2002 statistics of Sharpe ratios)
- **T8.Q11** — Framework docs on partial fills and slippage (추가 검색: backtrader slippage docs; zipline-reloaded VolumeShareSlippage; Lean reality modeling)
- **T8.Q12** — Methods for detecting strategy decay (추가 검색: strategy decay detection CUSUM live trading)
- **T8.Q13** — Evidence on low-frequency ETF strategies (추가 검색: Faber tactical asset allocation; Antonacci dual momentum; KODEX 200 추세추종 백테스트)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|
| `pykrx-github` | sharebook-kr/pykrx (KRX/Naver 주식·채권 데이터 스크래퍼) | sharebook-kr contributors | 2026 | code | <https://github.com/sharebook-kr/pykrx> | 3 | 3 | Data is scraped from KRX and Naver; README: 데이터의 저작권은 각 제공처에 있습니다; README: 상업적 용도 사용 시 데이터 제공처의 약관을 준수해야 합니다 |
| `financedatareader-github` | FinanceData/FinanceDataReader (금융 데이터 수집 라이브러리) | FinanceData.KR | 2026 | code | <https://github.com/FinanceData/FinanceDataReader> | 3 | 3 | Code license MIT, LICENSE.txt present, copyright 2018-2026 FinanceData.KR; NAVER:000100 history from 2000; KRX:000100 history from 1995-05-02 |

### 출처 요약

- **`pykrx-github`** — pykrx is a Python library that scrapes Korean stock and bond data from KRX and Naver. Its README says data copyright stays with each provider ('데이터의 저작권은 각 제공처에 있습니다') and that commercial use must follow the providers' terms. It also asks users to avoid excessive calls and says the data is for reference only. The repository page showed no code license. For this project it is a possible KR backtest data source, and it shows that redistributing fetched data is a licensing question separate from the code license. _(접근: open access; code license not visible on the repo page, so check pyproject.toml before vendoring)_
- **`financedatareader-github`** — FinanceDataReader is an MIT-licensed Python library (LICENSE.txt, copyright 2018-2026 FinanceData.KR). It reads KR and global market data by crawling NAVER Finance (the default KR source, from 2000), KRX (from 1995-05-02 for the example symbol), Yahoo Finance, FRED, ICE and ECOS. The README gives no terms of use for the upstream data. The MIT license covers the code only, so committing fetched datasets needs a separate check against each provider's terms. _(접근: open access)_
