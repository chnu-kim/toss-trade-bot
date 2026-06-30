# ADR-0003: 주문 제출 불명·재시작은 단일 reconciler가 API 진실로 해소하고, 자동 재제출은 하지 않는다

- **Status**: Accepted
- **Date**: 2026-06-30
- **Deciders**: chnu-kim
- **관련 이슈/PR**: (미정)

## Context

ADR-0002는 "내가 어떤 주문을 제출하려 했는가"의 identity(write-ahead journal + intentId + intentId→clientOrderId deterministic 도출)를 정했다. 이 ADR은 그 다음 질문에 답한다: **제출이 실패하거나 결과가 불명일 때, 그리고 프로세스가 죽었다 살아났을 때, 무엇이 진실이고 무엇을 하는가.**

여기가 실제로 중복 체결(비가역 손실)이 발생하는 지점이며, CLAUDE.md "주문 제출은 자동 재시도 금지"가 존재하는 이유다.

Toss Open API 실측 제약(OpenAPI 스펙 확인):

- **중복 `clientOrderId` 제출 시 서버 거동(replay vs 409)이 미정의.** 같은 키 재사용이 중복을 막아준다고 **신뢰할 수 없다.**
- `GET /api/v1/orders`에 **`clientOrderId` 필터가 없다.** 진실 확인은 OPEN/CLOSED 목록을 나열해 **client-side로 clientOrderId 매칭**하는 수밖에 없다.
- `status` 필터는 `OPEN`(PENDING, PARTIAL_FILLED, PENDING_CANCEL, PENDING_REPLACE)과 `CLOSED`(FILLED, CANCELED, REJECTED, …)로 갈린다. CLOSED는 `from`/`to`(KST, inclusive) 날짜 필터·커서 페이징을 받는다.

무인 안전 불변식(CLAUDE.md): 죽지 않는다 · 재시작 안전 · **조회는 백오프 재시도 OK, 주문 제출은 자동 재시도 금지** · 로컬 상태(여기선 journal)는 outbox일 뿐 SoT는 API.

핵심 함정: **"지금 목록에 없음"은 "서버에 도달 안 함"이 아니다**(eventual consistency). 직전에 POST한 intent를 성급히 "없음(ABSENT)"으로 판정해 닫으면, 전략이 같은 의도로 새 주문을 내고 → 사실 둘 다 체결 → 중복이 된다. UNKNOWN을 ABSENT로 강등하는 것이 가장 위험한 오판이다.

## Decision

1. **진실 확인·복구는 단일 reconciler가 수행한다.** ambiguous-submit(POST 타임아웃/5xx로 결과 불명)과 재시작 복구는 **같은 reconciler 코드 경로**를 쓴다. 제출 경로는 결과가 불명이면 해당 intent를 journal에서 `unresolved`로 mark하고 반환한 뒤 reconciler를 즉시 깨운다(인라인 동기 verify를 하지 않는다 — 주문 루프를 막지 않는다). 재시작 시에는 journal의 모든 `unresolved` intent에 대해 같은 reconciler가 돈다. 위험 로직(나열·매칭·판정)이 한 곳에만 있어 `go test -race`·단위 테스트로 검증할 표면이 하나다.

2. **reconciler는 verify 결과를 3-값으로 분류한다.** 각 `unresolved` intent의 clientOrderId를 API 나열 결과와 매칭해:
   - **PLACED** — OPEN 또는 CLOSED에서 매칭됨. journal을 `resolved`로 확정하고 정상 추적으로 넘긴다. **재제출하지 않는다.**
   - **ABSENT** — 나열이 성공적으로 완료되었고(에러 없음) 매칭이 없으며, **POST 시도 후 settle window가 경과**한 경우에만 확정. intent를 `failed-absent`로 닫는다. **재제출하지 않는다**(아래 3).
   - **UNKNOWN** — 위 두 조건을 만족하지 못한 모든 경우(나열 자체가 실패, 또는 settle window 미경과). intent를 **닫지 않고** `unresolved`로 유지해 다음 사이클에 재verify한다. 나열 실패는 *조회*이므로 지수 백오프로 재시도한다. **"의심스러우면 PLACED일 수 있다"**가 기본 가정이다(fail-safe). 

3. **ABSENT가 확정되어도 order 계층은 자동 재제출하지 않는다.** intent를 `failed-absent`로 닫을 뿐이고, 그 포지션을 여전히 원하면 **전략이 다음 결정에서 새 intentId로** 주문한다. 재시도 의사결정의 주인은 의미를 아는 전략이다(ADR-0002의 경계와 일치). 근거: 같은 clientOrderId 재사용은 check-then-act 레이스의 backstop일 뿐이고 **서버 dedup이 미정의**라 신뢰할 수 없다 — 무인에서 order가 자동 재제출하는 것보다 닫고 전략에 위임하는 편이 폭발 반경이 작다.

4. **재시작 시 CLOSED lookback 창은 journal 기반 동적 창으로 bound한다.** 재시작 reconciler는 OPEN 전체와, **가장 오래된 `unresolved` intent의 append 시각에서 settle window만큼 더 과거**부터 now까지의 CLOSED를 나열한다. journal append 시각을 하한으로 쓰므로 다운타임에 정확히 비례하고, 무한 과거 조회가 없다. journal 시각은 `from`/`to`가 KST inclusive이므로 **KST 기준으로 일치**시킨다.

## Alternatives considered

- **제출 경로 인라인 동기 verify + 재시작 별도 복구** — 기각: ambiguous를 빨리 수렴시키지만 같은 "나열·매칭·판정" 위험 로직이 제출 경로와 재시작 복구 두 곳에 중복된다. 비가역 영역에서 불변식 위반 표면을 둘로 늘린다. 단일 reconciler + "제출 직후 reconciler 깨우기"로 수렴 속도도 확보한다.
- **ABSENT 확정 시 같은 clientOrderId로 1회 자동 재제출** — 기각: 빠른 체결 회복은 매력적이나, 나열-재제출 레이스 + 서버 dedup 미정의로 중복 체결 잔여 위험을 진다. CLAUDE.md "주문 자동 재시도 금지"의 가장 안전한 해석은 "order는 재제출하지 않는다"이다.
- **"나열에 없으면 즉시 ABSENT"(settle window 없음)** — 기각: eventual consistency에서 "아직 안 보임"을 "없음"으로 오판해 전략이 중복 주문을 내게 만든다. UNKNOWN→ABSENT 강등이 가장 위험한 오판이므로, 적극적 부재 증거(나열 성공 + settle window)를 요구한다.
- **ABSENT/UNKNOWN 시 알림 후 사람 게이트** — 기각: 가장 보수적이나 24시간 무인 운영과 충돌한다(사람이 항상 부재). 단, 무한 UNKNOWN처럼 reconciler가 수렴 못 하는 상황은 킬 스위치 트리거 후보로 남긴다(별도 ADR).
- **고정 CLOSED lookback(예: 7일)** — 기각: 다운타임이 창보다 길면 체결을 놓치고, 짧으면 매번 과조회·페이징한다. 창 크기를 임의로 고를 근거가 없다. journal append 시각이 정확한 하한을 공짜로 준다.

## Consequences

- (좋음) 비가역 영역의 위험 로직이 단일 reconciler에 모여 동시성·정확성을 한 곳에서 테스트한다. ambiguous-submit과 재시작이 같은 불변식을 공유한다.
- (좋음) order 계층은 "멱등 제출 + 진실 확인"만 책임지고 재시도 정책을 전략에 위임 → 경계가 ADR-0002와 일관된다.
- (좋음) fail-safe 기본값(의심 시 PLACED)으로 **중복 체결을 구조적으로 억제**한다. 대가는 "안 들어간 주문을 늦게 포기"하는 latency인데, 무인 비가역 맥락에서 옳은 방향이다.
- (비용) settle window·lookback 창·KST 정렬 같은 시간 파라미터가 생긴다 — 단위 테스트(시계 주입)로 경계를 커버해야 한다. eventual-consistency 시나리오는 mock 나열로 재현해 검증한다.
- (제약 전파) **전략은 ABSENT로 닫힌 intent의 재시도를 새 intentId로 책임진다.** 전략 설계 시 이 계약을 ADR-0002·0003 링크로 전달한다.
- (후속) reconciler가 끝내 수렴 못 하는 상태(영속 UNKNOWN, 나열 지속 실패)에서 신규 주문을 멈추는 **킬 스위치**는 별도 ADR에서 결정한다. CLOSED 나열의 커서 페이징·레이트 리밋 처리는 구현 이슈로 분리한다.
