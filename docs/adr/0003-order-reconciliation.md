# ADR-0003: 주문 검증·복구는 orderId 상세조회로 하고, orderId 없는 ambiguous submit은 fail-closed한다

- **Status**: Accepted
- **Date**: 2026-06-30
- **Deciders**: chnu-kim
- **관련 이슈/PR**: PR #8

## Context

ADR-0002는 intent identity(write-ahead journal + 2-마커, 사후 검증 1차 키 = orderId, clientOrderId는 서버측 중복방지용)를 정했다. 이 ADR은 답한다: **제출이 실패·불명일 때, 그리고 재시작했을 때, 무엇이 진실이고 무엇을 하는가.**

여기가 실제로 중복 체결(비가역 손실)이 나는 지점이며, CLAUDE.md "주문 제출은 자동 재시도 금지"가 존재하는 이유다.

Toss Open API 실측(jq로 `openapi.json` 직접 검증):

- **`GET /api/v1/orders/{orderId}` 상세는 모든 상태의 주문을 orderId로 조회**한다(열림/닫힘 무관). `OrderStatus` enum: PENDING, PARTIAL_FILLED, FILLED, CANCELED, REJECTED, … (클라이언트는 unknown code 허용 구현 필요).
- **`clientOrderId`는 POST 응답에만 있다.** 목록·상세 어디에도 없다 → clientOrderId로 사후 매칭 **불가능**.
- **`GET /api/v1/orders`는 `status=OPEN`만 동작**, `status=CLOSED`는 `400 closed-not-supported`(미지원). OPEN 목록(`Order`)은 orderId는 주지만 clientOrderId는 없다.
- `/api/v1/trades`는 `{currency, price, timestamp, volume}`뿐(orderId/symbol 없음) → 주문 correlation에 **무용**.
- `/api/v1/holdings`는 symbol+quantity **집계 포지션**(orderId 없음) → 개별 주문 추적 불가, "포지션이 변했나"의 약한 간접 신호.

무인 안전 불변식: 죽지 않는다 · 재시작 안전 · **조회는 백오프 재시도 OK, 주문 제출/재전송은 자동 금지** · journal은 outbox, SoT는 API.

검증 키를 orderId로 잡으면 blind spot이 **정확히 하나로 붕괴**한다: **ambiguous submit** — POST가 응답(orderId)을 돌려주기 전에 타임아웃/단절/크래시로 끝난 경우. 이때만 orderId가 없어 상세조회가 불가능하고, 그 주문이 "안 들어감"인지 "이미 체결됨"인지 **구분할 수단이 없다**(CLOSED 나열 불가, clientOrderId 매칭 불가, trades 무용, holdings는 집계). 즉 **ABSENT를 확정할 방법이 사라졌다** — 성급히 ABSENT로 닫고 전략이 재발행하면 체결된 주문을 중복 체결한다.

## Decision

1. **진실 확인·복구는 단일 reconciler가 수행한다.** ambiguous-submit과 재시작 복구는 **같은 reconciler 코드 경로**를 쓴다. 제출 경로는 결과가 불명이면 intent를 `unresolved`로 두고 반환한 뒤 reconciler를 즉시 깨운다(인라인 동기 verify 안 함 — 주문 루프를 막지 않는다). 재시작 시 journal의 모든 미종결 intent에 같은 reconciler가 돈다. 위험 로직이 한 곳에만 있어 `go test -race`·단위 테스트로 검증할 표면이 하나다.

2. **reconciler는 journal 마커/orderId로 분기한다:**
   - **`submit-attempted` 없음(prepared-only)** → POST가 일어난 적 없음이 확실. terminal **`aborted-before-submit`**로 닫는다. order는 스스로 제출하지 않고(소유권은 전략, 재시작은 stale 가격이라 decision-safe 아님) 전략이 새 intentId로 현재 가격에 맞춰 재결정한다.
   - **`orderId` 있음(`acked`)** → **`GET /api/v1/orders/{orderId}` 상세로 진실 확인.** 열림 상태면 추적, 닫힘(FILLED/CANCELED/REJECTED 등)이면 결과를 기록하고 종결. 상세조회는 *조회*이므로 실패 시 지수 백오프 재시도. 이 경로가 재시작 복구의 대부분을 해결한다.
   - **`submit-attempted` 있고 `orderId` 없음 = ambiguous submit** → 아래 3.

3. **orderId 없는 ambiguous submit은 곧장 fail-closed한다 — 추측 binding을 하지 않는다.** orderId가 없으면 진실을 확정할 수단이 없다: "OPEN에 없음"은 "안 들어감"과 "이미 체결됨"을 구분 못 하고(CLOSED 조회 불가), OPEN 목록을 payload(symbol/side/quantity/price)로 대조하는 것은 **API 기반 identity가 아니라 추측**이다 — 단일 매칭 OPEN 주문이 사실은 다른 봇 intent·동일 파라미터의 동시 전략 결정·수동/API 주문일 수 있어, 잘못된 orderId를 `acked`로 기록하면 무관한 주문을 이 intent의 진실로 취급해 중복 노출을 은폐하고 journal을 오염시킨다. 따라서:
   - (a) intent를 **`unresolved-ambiguous`**로 두고 **국소 fail-closed**: 해당 **symbol에 한해** 신규 주문을 보류하고 외부 알림을 낸다. 나머지 종목은 정상 운영한다. **ABSENT로 강등하지도, payload 매칭으로 auto-ack하지도 않는다** — 해결은 사람 개입(또는 아래 불변식이 확립된 미래의 자동 복구)에 맡긴다.
   - (b) **빈발 시 전역 킬 스위치로 에스컬레이션.** ambiguous submit이 임계(연속/단위시간당 횟수)를 넘으면 네트워크·API 장애 같은 시스템 문제 신호이므로 봇 전체 신규 주문을 멈춘다. (킬 스위치 메커니즘 자체는 별도 ADR, 이 ADR은 트리거 조건을 정의·링크한다.)

4. **order 계층은 어떤 경우에도 자동 재제출/재전송하지 않는다.** terminal로 닫힌 intent(`aborted-before-submit`, 닫힌 주문)의 재시도 결정 주인은 의미를 아는 전략이다 — 새 intentId로, 현재 가격 기준으로 재발행한다. 근거: 같은 clientOrderId 재사용 backstop은 서버 dedup이 미정의라 신뢰 불가하고, 무인에서 order가 자동 전송하면 중복 체결·stale 결정을 유발한다.

## Alternatives considered

- **OPEN+CLOSED 나열 후 clientOrderId로 매칭(이전 버전의 토대)** — 기각: **원천 불가능**. clientOrderId는 목록·상세에 없고 CLOSED 나열은 400. orderId 상세조회로 대체한다.
- **CLOSED lookback 동적 창** — 기각: `status=CLOSED`가 미지원(400)이라 나열 자체가 불가. 닫힌 주문은 orderId 상세조회로만 닿는다.
- **ambiguous submit에서 "OPEN에 없으면 즉시 ABSENT"** — 기각: CLOSED 조회가 없어 "체결됨"을 "없음"으로 오판 → 전략 재발행 → 중복 체결. ABSENT를 확정할 수단이 없으므로 fail-closed가 정답이다.
- **ambiguous submit을 OPEN payload 휴리스틱 매칭으로 auto-ack** — 기각(codex adversarial-review 지적): payload(symbol/side/quantity/price/orderedAt) 유일성은 API·암호학 기반 identity가 아니라 **결과 집합 내 추측**이다. 단일 매칭 OPEN 주문이 다른 봇 intent·동시 동일 파라미터 결정·이미 알던 다른 주문·수동/API 주문일 수 있어, 잘못된 orderId를 `acked`로 박으면 무관한 주문을 진실로 취급해 중복 노출 은폐·잘못된 취소/상태 처리·journal 오염을 부른다. 안전한 자동 복구는 enforceable 불변식(① known journal orderId 전부 제외 ② 봇 전용 계정/namespace 가정 ③ 매칭 창 내 동시 same-symbol·side·price·quantity 주문 불가)이 **증명될 때만** 가능한데, 현재 이 불변식을 보장할 수 없으므로 fail-closed가 정답이다.
- **ambiguous submit 시 같은 clientOrderId로 1회 자동 재제출** — 기각: 서버 dedup 미정의 + 나열-재제출 레이스로 중복 체결 잔여 위험. CLAUDE.md "주문 자동 재시도 금지"의 안전한 해석은 "order는 재전송하지 않는다".
- **전역 fail-closed(ambiguous 1건에 봇 전체 정지)** — 기각: blind spot이 "응답 유실" 하나로 줄어 빈도가 극히 낮은데, 1건마다 전체를 멈추면 무인성을 과하게 희생한다. 국소(symbol) 차단 + 빈발 시 전역 에스컬레이션이 비례적이다.
- **holdings 집계로 추정 후 진행(중복 감수)** — 기각: 집계 포지션은 동시 다른 체결과 섞여 disambiguate가 약하고, 비가역 자금에서 "추정 후 진행"은 중복 체결을 사후 교정에 맡긴다 — 폭발 반경이 가장 크다.
- **인라인 동기 verify + 재시작 별도 복구** — 기각: 같은 위험 로직이 두 경로에 중복된다. 단일 reconciler + 제출 직후 깨우기로 수렴 속도도 확보한다.

## Consequences

- (좋음) 비가역 위험 로직이 단일 reconciler에 모인다. ambiguous-submit과 재시작이 같은 불변식을 공유한다.
- (좋음) orderId 상세조회로 "응답을 받은 주문"은 항상 진실에 닿는다 → 재시작 복구 대부분이 결정적으로 해결된다.
- (좋음) prepare 도중 크래시가 영속 UNKNOWN→킬 스위치로 번지지 않는다(`submit-attempted` 분기 → `aborted-before-submit`).
- (좋음) fail-safe 방향이 명확하다: 확정 못 하는 ambiguous는 국소 차단, 빈발 시 전역 정지. 중복 체결을 구조적으로 억제하고 무인성 손실은 해당 symbol·장애 상황으로 국한한다.
- (비용) **orderId 없는 ambiguous submit은 자동 복구하지 않고 국소 차단한다** — OPEN payload 추측 binding으로 journal을 오염시키느니 사람 개입을 택한다. 국소 차단 빈도(=실제 응답 유실률)가 운영 지표가 되고, 이 값이 낮다는 전제가 fail-closed의 무인성 비용을 수용 가능하게 만든다. settle window·빈발 임계 파라미터는 단위 테스트(시계·mock 주입)로 경계를 커버한다.
- (제약 전파) 전략은 terminal로 닫힌 intent(`aborted-before-submit`, 닫힌 주문)의 재시도를 새 intentId·현재 가격으로 책임진다.
- (후속) **킬 스위치 메커니즘**(국소·전역 차단 상태, 빈발 임계, 해제 절차, 알림 채널)은 별도 ADR. **ambiguous submit 자동 복구**를 미래에 원한다면, 위 Alternatives의 세 불변식(known orderId 제외·봇 전용 계정 namespace·매칭 창 내 동시 동일주문 불가)을 먼저 확립하는 별도 ADR이 선행해야 한다 — 그 전까지 기본은 국소 fail-closed다.
