# 연구 코퍼스 (docs/research)

이 프로젝트(Toss Open API 무인 자동매매 봇 + loop 엔지니어링 파이프라인)가 의존하는 지식 영역을 주제별로 수집·종합한 자료 모음이다. `/architect`는 설계 결정 전에 해당 주제 파일의 "프로젝트 함의"·"남은 공백"을, `/issue-drafter`는 이슈 명세 시 "질문별 답변"을 참조한다. 출처는 모두 검색 결과·실제 페이지에서 확인된 URL만 수록했으며, 링크 유효성은 전수 재검증하지 않았다(아래 "한계").

수집일 2026-11-04. 주제 16개, 질문 186개(답변됨 23 · 부분 110 · 미답 53), 출처 261건.

## 수집 방법

1. **스코프**: CLAUDE.md·ADR 17건·회고·런북·열린 이슈에서 지식 공백 지도를 도출(주제·질문·우선순위·검색 각도).
2. **수집**: 주제마다 학술 / 공식·표준 / 업계·포스트모템 / 한국어 4경로를 병렬 검색·열람(sonnet).
3. **종합**: 주제별 중복 제거·질문별 답변·공백 판정(opus). must/should 공백이 남으면 표적 재수집 최대 2라운드.
4. 교차 주제 심층 종합·완결성 비평·링크 전수 검증·서술형 정리 단계는 **비용 상한으로 생략**했다 — 이 파일들은 종합 결과를 기계적으로 정리한 것이다.

## 한계

- "부분"·"미답" 질문이 많다. 특히 Toss Open API 비공개 사항(레이트 리밋 수치 등)·국내 규제의 개인 적용은 1차 출처가 부족하다 — 각 파일 "남은 공백"의 추가 검색어로 이어서 수집한다.
- 출처 요약·핵심 주장은 모델 요약이므로 설계 결정 근거로 쓰기 전에 원문을 확인한다.

## 색인

| 파일 | 주제 | 출처 | 답변/부분/미답 |
|---|---|---|---|
| [01-toss-open-api-primary-facts.md](01-toss-open-api-primary-facts.md) | Toss Open API primary facts (auth, limits, order semantics, calendar, streaming, errors, ToS) | 17 | 3/12/0 |
| [02-order-execution-safety.md](02-order-execution-safety.md) | Order management and execution safety: idempotency, write-ahead intent, reconciliation, exactly-once limits | 29 | 4/8/0 |
| [03-kill-switch-risk-controls-and-failures.md](03-kill-switch-risk-controls-and-failures.md) | Kill switches, circuit breakers, pre-trade risk controls, and canonical failures | 44 | 3/8/1 |
| [04-durable-state-and-crash-safety.md](04-durable-state-and-crash-safety.md) | Durable state and crash safety: SQLite durability, outbox/WAL, sentinels, retention | 38 | 2/10/0 |
| [05-resilience-patterns-go.md](05-resilience-patterns-go.md) | Resilience patterns for a 24/7 Go process: backoff, rate limiting, single-flight, supervision, shutdown | 41 | 4/8/0 |
| [06-observability-and-alerting-unattended.md](06-observability-and-alerting-unattended.md) | Observability for unattended systems: structured logs, durable audit, alerting, dead-man switches, SRE practice | 22 | 0/10/1 |
| [07-kr-market-structure.md](07-kr-market-structure.md) | Korean equity market structure as it affects a bot (KRX + Nextrade ATS, hours, VI, limits, ticks, settlement) | 8 | 0/9/3 |
| [08-us-market-structure-via-korean-broker.md](08-us-market-structure-via-korean-broker.md) | US equity market structure for a KR-based retail bot (hours, extended sessions, LULD, ticks, settlement, FX) | 2 | 0/4/8 |
| [09-strategy-design-and-backtesting.md](09-strategy-design-and-backtesting.md) | Systematic strategy design, backtesting rigour, and paper-trading substitutes for a retail REST bot | 2 | 0/3/10 |
| [10-regulation-compliance-tax-kr-us.md](10-regulation-compliance-tax-kr-us.md) | Regulation, compliance and tax for an individual running an algorithmic bot in Korea (KR + US stocks) | 2 | 0/1/10 |
| [11-security-secrets-supply-chain.md](11-security-secrets-supply-chain.md) | Security: OAuth2 client-credentials custody, secret management, and supply-chain hardening | 23 | 2/9/0 |
| [12-llm-agent-engineering-governance.md](12-llm-agent-engineering-governance.md) | Governance of LLM-agent software engineering: verification limits, N-of-2 review, LLM-as-judge, prompt injection in CI, self-protection, ADR/TDD evidence | 0 | 0/0/13 |
| [13-go-engineering-references.md](13-go-engineering-references.md) | Go engineering references: layout, httptest/race testing, modernc sqlite, context/cancellation, clock injection, toolchain | 20 | 4/7/1 |
| [14-deployment-operations-and-time.md](14-deployment-operations-and-time.md) | Deployment, operations, and time handling for a single-process unattended bot (host, restarts, single-instance, NTP, IP allowlist) | 7 | 0/8/3 |
| [15-portfolio-accounting-and-exposure.md](15-portfolio-accounting-and-exposure.md) | Portfolio accounting, exposure computation, cash/settlement, and PnL for the independent 'reduce exposure' check and future risk limits | 6 | 1/6/2 |
| [16-notification-channel-and-operator-interface-gap.md](16-notification-channel-and-operator-interface-gap.md) | Cross-cutting undecided seams: notifier channel, remote audit sink, operator CLI, and strategy interface contract (explicit ADR candidates) | 7 | 0/7/1 |
| [bibliography.md](bibliography.md) | 전 주제 통합 참고문헌 | 261 | — |

