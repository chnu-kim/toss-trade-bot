---
id: "0007"
status: Accepted
date: 2026-07-08
deciders: [chnu-kim]
domain: [killswitch, runtime, loop-governance]
protects: [live-execution-human-gate]
supersedes: []
superseded_by: null
verification:
  - reviewer: advisor
    date: 2026-07-08
    verdict: converged (2 rounds)
  - reviewer: codex:adversarial-review
    date: 2026-07-08
    verdict: approve (converged after 4 rounds)
  - reviewer: chnu-kim
    date: 2026-07-08
    verdict: approved
---

# ADR-0007: 개발 자율성 경계는 "배포 시점"이 아니라 "실거래 authorize"에 있다 — 킬 스위치 halt 기본값으로 흡수

- **Status**: Accepted
- **Date**: 2026-07-08
- **Deciders**: chnu-kim
- **관련 이슈/PR**: #39

## Context

사용자가 "loop engineering" — 뚜렷한 목표만 주고 나머지(설계·구현·리뷰·머지·검증)를 전부 AI에 위임하는 개발 방식 — 을 이 레포의 개발 파이프라인 전체에 적용하길 원한다. 지금까지 파이프라인(CLAUDE.md §개발 파이프라인)은 매 고도(architect/issue-drafter/dispatch-issue)마다 사람 게이트가 있었다.

이 결정을 가르는 힘:

- **아직 실거래 이력이 없다.** 매매 전략·대상 시장이 미정이라(CLAUDE.md "프로젝트 현황") 지금 이 시점엔 어떤 코드를 자율로 짜도 실행되지 않는 한 위험이 0이다.
- **Toss Open API에는 모의투자/샌드박스가 없다**(developers.tossinvest.com/llms.txt 직접 확인). `POST /api/v1/orders`를 태우는 순간 100% 실주문·실자금이며, "테스트 모드"로 완충할 방법이 없다.
- **ADR-0004가 이미 킬 스위치 primitive를 확정했다**: 전역 halt는 트랜잭션 저장소에 persist되고, 재시작 시 halted로 기동하며, **사람 수동 clear만이 해제 수단**이다(ADR-0004 point 6). 자동 재개는 이미 기각됐다("시스템 이상은 사람 확인이 안전 조건이다").
- **CLAUDE.md 무인 안전 불변식**: 죽지 않는다·재시작 안전·킬 스위치.
- ADR-0004는 전역 halt가 **트립된 이후**의 재개 절차만 규정했다 — **최초 배포 시 기본값**(아직 한 번도 트립된 적 없는 상태에서 시작 값)은 미정으로 남아 있었다.

## Decision

1. **개발 파이프라인(설계·구현·리뷰·머지·CI/CD 배포)은 `risk:critical` 코드(주문/킬스위치/정합 로직 포함)를 예외 없이 자율화 대상으로 삼는다.** "위험도"가 아니라 "개발 시점 vs 실행 시점"이 진짜 경계다.
2. **유일한 예외는 "신규 노출 주문 제출의 최초 시작"이며, 이는 새 메커니즘을 만들지 않고 ADR-0004의 기존 전역 halt/사람 수동 clear로 흡수한다.**
3. **최초 배포 시(halt 상태가 저장소에 한 번도 기록된 적 없을 때) 전역 halt 기본값은 tripped다**, 사유는 `awaiting-initial-authorization`. 이는 ADR-0004 point 4("전역 halt = persist")가 비워둔 "최초 값"을 명시적으로 정의한다.
4. 사람이 `awaiting-initial-authorization`을 ADR-0004 point 6의 기존 수동 clear 절차로 명시적으로 해제해야만 최초 실거래가 시작된다. 이후 재트립(연속 실패·ambiguous 빈발·토큰 갱신 불가 등 ADR-0004 point 7의 기존 트리거)이 걸리면 다시 사람 clear가 필요하다 — 이 재개 절차 자체는 바뀌지 않는다.
5. 이 경계 밖의 모든 것 — 코드 작성·테스트·CI·프로덕션 인프라 배포 그 자체 — 은 자율화 가능하다. 배포된 프로세스가 항상 halted로 기동하는 한, 배포 자체는 무해하다.
6. 시크릿(`client_id`/`client_secret`) 최초 발급은 Toss 개발자 계정 가입이 전제이므로 이미 물리적으로 사람 전용 행위다 — 별도 게이트를 설계할 필요가 없다(사실의 확인일 뿐, 결정 아님).
7. **halt 상태의 provenance가 모호하면 항상 halted로 귀결된다.** "한 번도 트립된 적 없는 최초 기동"과 "저장소가 비워지거나 이관되어 과거 clear 기록이 유실된 기동"을 항상 구분할 수 있는 건 아니다(ADR-0006의 prune 정책과 교차). 이 둘을 판별하지 못하는 모든 경우 — 저장소 조회 실패, 예상 스키마와 불일치, 기록 유실 의심 — 는 **일률적으로 tripped(halted)로 취급한다.** "증거 없음"을 "안전함"으로 해석하지 않는다(ADR-0004 point 3의 fail-closed 원칙을 최초-기동 판별에도 그대로 적용).

## Alternatives considered

- **별도의 "authorize" 플래그/설정 파일을 새로 설계** — 기각: 이미 ADR-0004에서 하드닝된 halt/clear primitive가 있는데 두 번째 안전 표면을 만들면, 두 메커니즘 간 불일치(하나는 clear됐는데 하나는 안 된 상태)라는 새 위험만 생긴다.
- **prod 배포 행위 자체를 사람 게이트로 묶기**(배포 PR은 항상 사람 승인) — 기각: halt-기본값 설계로 배포는 이미 무해해진다(항상 halted로만 기동). 배포에 게이트를 걸면 자율성만 깎이고 안전 이득이 없다.
- **`risk:critical` 라벨 기준으로 사람 게이트를 유지** — 기각: "위험도"가 아니라 "개발 시점 vs 실행 시점"이 진짜 경계라는 게 이 논의의 핵심 통찰이다. `risk:critical` 코드도 아직 실거래를 안 하는 한 개발 시점엔 위험이 없다.

## Consequences

- (좋음) 새 안전 primitive를 발명하지 않고 이미 하드닝된 킬 스위치 계약(ADR-0004)을 재사용한다 — 안전 표면을 최소화한다.
- (좋음) 개발 파이프라인 전체(`risk:critical` 포함)가 완전 자율화 가능해진다 — loop engineering 목표 달성.
- (좋음) "배포 = 실행 시작"이라는 위험한 암묵적 동일시가 사라진다 — 배포와 실거래 시작이 명시적으로 분리된 별개 이벤트가 된다.
- (비용) `awaiting-initial-authorization`이라는 새 halt reason 값이 킬스위치 구현에 필요하다(구현 이슈).
- (비용) 재배포/이관 시나리오(예: 새 환경으로 마이그레이션, 저장소를 옮겨서 기동)에서 point 7의 fail-closed 판별 규칙 때문에, 저장소 이관이 조금이라도 불완전하면 재-authorize(사람 수동 clear)가 다시 필요할 수 있다 — 편의보다 안전을 택한 의도적 비용이다.
- (후속) 킬 스위치 구현 이슈에 `awaiting-initial-authorization` reason과 그 clear 절차를 반영한다.
- (후속) ADR-0008(검증 게이트)·ADR-0009(ADR 자율화)가 이 ADR이 정의한 "실거래 authorize" 불변식을 참조·보호한다.
