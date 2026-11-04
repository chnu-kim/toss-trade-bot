# Regulation, compliance and tax for an individual running an algorithmic bot in Korea (KR + US stocks)

> **왜 중요한가** — The bot trades a personal Toss account unattended; CLAUDE.md and ADR-0007 assume human authorization but the repo has no compliance notes. KRX's 고속 알고리즘거래자 등록제 (KRX 업무규정 시행세칙 §131~131의3, effective 2023-01-25) may or may not apply to a low-frequency retail bot; tax treatment differs KR vs US and affects strategy turnover (T8.Q9). Feeds strategy ADR, ToS review (T1.Q11), and README disclaimers for OSS.

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 2건. 상태 집계: ❌ 미답 10, 🟡 부분 1

## 핵심 결론

- (완전 답변된 질문 없음 — 아래 부분 답변 참조)

## 질문별 답변

### T9.Q1 · Does KRX's 고속 알고리즘거래자 사전등록·계좌신고 regime apply to an individual retail account placing low-frequency API orders — what is the quantitative definition of '고속', what are broker obligations (수탁 거부), and what did the later 시행세칙 amendments change?

❌ 미답 · 우선순위 must · 확신 low

No verified source in any pool. Every collection round hit an exhausted WebSearch budget, and krx.co.kr and law.go.kr were unreachable. Unverified background, not citable: the KRX regime in force since 2023-01-25 defines 'high-speed' by order-rate and latency/co-location criteria, so a low-frequency REST-polling retail bot is probably outside it. The numeric threshold, the per-account scope, the broker's 수탁 거부 duty and any amendments remain unknown.

### T9.Q2 · 자본시장법/금융투자업규정 provisions on 시세조종·허수호가·통정매매 that an algorithm could inadvertently trigger (e.g. rapid cancel/replace, layering), and the FSS/KRX market surveillance thresholds (불공정거래 적출 기준).

❌ 미답 · 우선순위 must · 확신 low

No verified source. The texts of 자본시장법 §176 and §178의2 and the KRX/FSS 적출 기준 were not retrieved. Unverified background: 시장질서 교란행위 can be found without intent, and high cancel/replace ratios or 허수성 호가 are typical surveillance triggers. No thresholds can be stated.

### T9.Q3 · Broker-side rules: Toss 전자금융거래 약관, 주문대리/위임 금지, API 키 공유 금지, 투자일임 vs self-trading boundaries — is running an OSS bot for oneself permitted and what about sharing the bot with others (투자일임업/유사투자자문 issues)?

❌ 미답 · 우선순위 must · 확신 low

No verified source. The Toss 약관 and Open API terms could not be fetched. Unverified background: self-trading with your own credentials is presumably the intended use. Operating the bot for others' accounts risks 투자일임업, and selling signals risks 유사투자자문업 reporting duties. Publishing generic OSS code that each user runs with their own keys is plausibly lower risk. ccxt describes itself as 'software, not a service' and as non-custodial [ccxt-readme-license], which is a useful framing but not a Korean-law determination.

### T9.Q4 · Korean tax on domestic stocks for an individual after 금투세 폐지 (confirm Dec 2024 repeal): 증권거래세, 대주주 양도세 thresholds, dividend taxation, and reporting obligations.

❌ 미답 · 우선순위 must · 확신 low

No verified source. Neither the Dec 2024 금투세 repeal nor the 증권거래세 rates, 대주주 thresholds, dividend withholding or 금융소득종합과세 threshold were confirmed. Do not hard-code any figure until it is sourced.

### T9.Q5 · Korean tax on foreign (US) stock gains: 22% (20% + 지방소득세) on net gains above 2.5M KRW basic deduction, loss netting rules, FX basis determination, annual May filing, 2026 law changes if any.

❌ 미답 · 우선순위 must · 확신 low

No verified source. The figures in the question (22%, 2.5M KRW deduction, annual netting, May filing) match common understanding but were not confirmed against 소득세법 or 국세청. The FX basis and any 2026 changes are unverified.

### T9.Q6 · US-side rules for a non-resident alien trading via a Korean broker: W-8BEN via broker, 15% dividend withholding (treaty), no US capital gains tax, PDT/Reg T handled by broker — what, if anything, the bot must respect.

❌ 미답 · 우선순위 should · 확신 low

No verified source; irs.gov was unreachable. Unverified background: a 15% treaty dividend rate via W-8BEN, no US capital-gains tax for a non-resident alien, and PDT/Reg T enforced on the broker side. How these apply through a Korean broker's omnibus account is unconfirmed.

### T9.Q7 · Market data licensing: KRX 시세 정보 이용 약관 and US exchange non-professional data agreements — constraints on storing/redistributing data the bot polls, especially for an OSS repo with sample data.

❌ 미답 · 우선순위 should · 확신 low

No verified source. Until the KRX, Toss and US exchange data terms are retrieved, assume that redistributing or committing polled quote data is restricted.

### T9.Q8 · Record-keeping and audit expectations for an individual (none statutory?) vs what helps in a dispute with the broker (zdnet 2026-10 'AI 주식주문 정상처리돼도 고객 뜻과 다르면 누가 책임질까') — how to keep evidence (ADR-0006 audit) usable.

❌ 미답 · 우선순위 should · 확신 low

No verified source; the zdnet 2026-10 article was not fetched. No statutory record-keeping duty for an individual was confirmed either way. In the repo, ADR-0006 provides fsync-durable audit records. No source addresses their evidential weight in a broker dispute.

### T9.Q9 · Personal information and 전자금융거래법 obligations when secrets/keys are stored on a self-managed server; FSS guidance on 접근매체 (API key) custody.

❌ 미답 · 우선순위 nice · 확신 low

No verified source; law.go.kr was unreachable. Unverified background: under 전자금융거래법, a user who fails to keep their 접근매체 safe may bear the resulting losses. That would make API-key custody a liability matter as well as a security one.

### T9.Q10 · Comparative reference regimes (MiFID II RTS 6 algo-trading requirements, SEC/FINRA algorithmic trading guidance FINRA RN 15-09) — as design checklists even if not legally binding on a KR retail user.

❌ 미답 · 우선순위 nice · 확신 low

No verified source. FINRA RN 15-09 and RTS 6 (EU 2017/589) could not be retrieved because finra.org and eur-lex were unreachable.

### T9.Q11 · OSS licensing and disclaimer practice for trading bots (MIT/Apache 2 with financial disclaimers, freqtrade/ccxt examples) and Korean consumer-protection implications if others run the code.

🟡 부분 · 우선순위 should · 확신 medium

Practice is verified for two precedents. freqtrade pairs GPL-3.0 with a prominent README disclaimer: educational purposes only, use at your own risk, no responsibility for trading results, and start in Dry-Run [freqtrade-readme-license]. ccxt uses MIT, and its README disclaimer frames the project as non-custodial software rather than a service, used at the user's own risk with no warranty, as-is [ccxt-readme-license]. Both copyleft and permissive licenses are used, and in both projects the disclaimer appears in the README as well as in the license's warranty clause. Korean consumer-protection effects were not researched: whether such a disclaimer holds under 약관규제법, and whether publishing code counts as 투자권유.

## 프로젝트 함의

- Coverage is still near zero: no pool brought new regulatory or tax evidence, only GitHub sources. No strategy ADR, ToS or tax decision should rely on this synthesis. A rerun needs a fresh WebSearch budget and an allowlist covering krx.co.kr, law.go.kr, nts.go.kr, fss.or.kr, tossinvest.com, irs.gov, finra.org and eur-lex.europa.eu.
- The repo root has no LICENSE or README; it holds only CLAUDE.md, cmd, configs, docs, go.mod, internal and scripts. Because CLAUDE.md requires OSS quality that can go public at any time, two prerequisites follow: a human-decided license (GPL-3.0 per freqtrade, or MIT per ccxt) and a README disclaimer combining freqtrade's 'own risk / no responsibility' wording with ccxt's 'software, not a service; non-custodial' framing [freqtrade-readme-license][ccxt-readme-license]. Fits a type:docs, risk:low issue.
- freqtrade's Dry-Run norm cannot be copied directly because the Toss Open API has no sandbox. ADR-0007's halt-by-default first boot, cleared only by a human, is the substitute, and the README should say so.
- ADR-0007's human live-trade authorization fits a self-trading posture. Any feature that operates the bot for other people's accounts touches unverified 투자일임업 and 유사투자자문업 questions (T9.Q3), so it needs its own ADR first.
- The strategy ADR should record three unverified open inputs: an order-rate ceiling (T9.Q1), a cancel/replace-ratio limit (T9.Q2) and after-tax turnover cost (T9.Q4/Q5). ADR-0004's kill-switch submit guard is a natural home for the rate and ratio guards.
- ADR-0006's durable audit records are the strongest evidence asset for a dispute (T9.Q8). Candidate amendments: a retention period, tamper-evidence such as hash chaining, and a defined timestamp clock source.
- Until T9.Q7 is resolved, do not commit polled quotes as fixtures. Keep using the synthetic httptest data the CLAUDE.md test policy already requires.

## 출처 간 상충

- freqtrade license: the ACADEMIC pool said it was unverified and the INDUSTRY pool said GPL-3.0. Resolved: GPL-3.0, confirmed by a fetch of the LICENSE file.
- ccxt disclaimer: three pools said its text was not captured. Resolved: the gap-fill round 2 fetch of the raw README captured it (non-custodial software, MIT, as-is).

## 남은 공백

- **T9.Q1** — The KRX 시행세칙 §131–131의3 text, the numeric 'high-speed' definition, the broker's 수탁 거부 duty, and post-2023 amendments. (추가 검색: 한국거래소 업무규정 시행세칙 고속 알고리즘거래자 정의; 고속 알고리즘거래자 사전등록 2023년 1월 25일 시행)
- **T9.Q2** — The texts of 자본시장법 §176 and §178의2, and the KRX/FSS detection criteria. (추가 검색: 자본시장법 제178조의2 허수성 호가; 한국거래소 시장감시 이상거래 적출 기준)
- **T9.Q3** — The Toss Open API terms, and the 투자일임업 and 유사투자자문업 boundaries for distributing a bot. (추가 검색: 토스증권 Open API 이용약관; 자동매매 프로그램 배포 유사투자자문업 해당 여부)
- **T9.Q4** — Confirmation of the 금투세 repeal, and the 2026 증권거래세, 대주주 and dividend tax figures. (추가 검색: 금융투자소득세 폐지 소득세법 개정 2024년 12월; 2026년 증권거래세율 대주주 기준)
- **T9.Q5** — NTS guidance on overseas-stock capital gains tax: rate, deduction, netting, FX basis and filing. (추가 검색: 국세청 해외주식 양도소득세 기본공제 250만원; 해외주식 양도소득 환율 적용 기준)
- **T9.Q6** — The IRS W-8BEN instructions and the KR-US treaty dividend article. (추가 검색: IRS Instructions for Form W-8BEN; IRS Publication 515 nonresident alien capital gains)
- **T9.Q7** — The KRX, Toss and Nasdaq data-redistribution terms. (추가 검색: 한국거래소 시세정보 이용 약관 재배포; Nasdaq non-professional subscriber agreement)
- **T9.Q8** — The zdnet 2026-10 article, and guidance on the evidential weight of logs in FSS 분쟁조정. (추가 검색: zdnet AI 주식주문 고객 뜻과 다르면 누가 책임; 금융분쟁조정 주문 로그 증거)
- **T9.Q9** — The texts of 전자금융거래법 §6 and §9. (추가 검색: 전자금융거래법 제6조 접근매체; 전자금융거래법 제9조 이용자 중과실)
- **T9.Q10** — FINRA RN 15-09 and RTS 6. The finra.org and eur-lex hosts must be allowed first. (추가 검색: FINRA Regulatory Notice 15-09; Commission Delegated Regulation (EU) 2017/589)
- **T9.Q11** — A Korean analysis of 약관규제법 effects and of 투자권유 for published code. (추가 검색: 오픈소스 면책조항 약관규제법 효력; 자동매매 소스코드 공개 투자권유 해당)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|
| `freqtrade-readme-license` | freqtrade/freqtrade README (Disclaimer section) and LICENSE (GNU GPL v3) | freqtrade contributors | 2026 | code | <https://github.com/freqtrade/freqtrade> | 4 | 4 | 'This software is for educational purposes only. Do not risk money which you are afraid to lose.'; 'USE THE SOFTWARE AT YOUR OWN RISK. THE AUTHORS AND ALL AFFILIATES ASSUME NO RESPONSIBILITY FOR YOUR TRADING RESULTS.'; 'Always start by running a trading bot in Dry-Run and do not engage money before you understand how it works' |
| `ccxt-readme-license` | ccxt README (Disclaimer and License sections) and LICENSE.txt (MIT) | ccxt contributors | 2026 | code | <https://raw.githubusercontent.com/ccxt/ccxt/master/README.md> | 4 | 4 | 'a free open source non-custodian API broker software under MIT license'; 'not a service nor a server. CCXT is a software.'; Traders run it locally and connect to exchanges directly; the project never holds user funds |

### 출처 요약

- **`freqtrade-readme-license`** — Merged from three pool entries (freqtrade-github-readme x2, freqtrade-readme-disclaimer). The README Disclaimer says the software is for educational purposes only, tells users not to risk money they are afraid to lose, says they use it at their own risk, and says the authors and affiliates take no responsibility for trading results. It tells users to start in Dry-Run and to read the source. An earlier pass fetched the LICENSE and confirmed GPL-3.0. This is the best verified template for the repo's missing README disclaimer. It does not analyse Korean law. _(접근: open access)_
- **`ccxt-readme-license`** — Merged from four pool entries. The gap-filling round 2 fetch of the raw README captured the Disclaimer body. It describes CCXT as software rather than a service or server, and as a free, open-source, non-custodian API broker under the MIT license. Traders run it locally and connect to exchanges directly, so the project never holds funds. The License section allows commercial and open-source use at the user's own risk, with no warranties, as-is. This resolves the earlier gap where the disclaimer text was missing. It is a permissive-license counterpart to freqtrade's GPL-3.0. _(접근: open access; repo page https://github.com/ccxt/ccxt)_
