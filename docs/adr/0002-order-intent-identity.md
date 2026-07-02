# ADR-0002: 주문 intent는 write-ahead journal(2-마커)에 기록하고, 사후 검증 1차 키는 orderId다 (clientOrderId는 서버측 중복방지용)

- **Status**: Accepted
- **Date**: 2026-06-30
- **Deciders**: chnu-kim
- **관련 이슈/PR**: PR #8

## Context

이 봇은 24시간 무인 실행되며, 주문 제출은 **비가역(돈이 움직임)**이다. CLAUDE.md "무인 실행 제약"의 핵심 불변식:

- **재시작 안전**: 죽었다 살아나도 같은 주문을 중복 제출하지 않아야 한다.
- **로컬 상태 불신**: 미체결 주문/포지션의 **진실 출처(SoT)는 API**다.

Toss Open API 실측 — **OpenAPI 스펙(`openapi.json`)을 jq로 직접 검증**했다(이전 요약은 틀렸다):

- `POST /api/v1/orders` → `OrderResponse {orderId, clientOrderId}`. 요청의 `clientOrderId`(optional, "Idempotency key for duplicate prevention")를 echo한다.
- **`clientOrderId`는 `OrderResponse`에서만 노출된다.** `GET /api/v1/orders`(목록, `Order` 스키마)와 `GET /api/v1/orders/{orderId}`(상세, `Order` 스키마) 어디에도 `clientOrderId`가 없다 — jq로 확인: `clientOrderId` 속성을 가진 스키마는 `OrderResponse` 단 하나.
- **`GET /api/v1/orders/{orderId}` 상세는 모든 상태(체결/취소/거부 등)의 주문을 orderId로 조회할 수 있다**(스펙 설명 명시). 즉 orderId만 있으면 열림/닫힘 무관하게 진실을 얻는다.
- `GET /api/v1/orders`는 `status=OPEN`만 동작하고 **`status=CLOSED`는 `400 closed-not-supported`**(미지원). OPEN은 페이징 없음(`nextCursor` 항상 null).
- 중복 `clientOrderId` 제출 시 서버 거동(replay vs 409)은 스펙에 **미정의**.

결정을 가르는 가장 큰 힘: 매매 전략이 **미정**이라 "결정이 재시작 후 deterministic하게 재현 가능하다"를 전제할 수 없다.

이 ADR은 답한다: **재시작을 넘어 "내가 이미 이 주문을 제출했는가"와 "그 주문이 실제로 어떻게 됐는가"를 무엇이 알게 하는가.** (제출 불명·복구 프로토콜은 ADR-0003.)

## Decision

1. **Intent identity의 출처 = write-ahead journal(outbox).** POST 전에 `(intentId, clientOrderId, payload)`를 durable append(append-only + fsync)한다. journal은 **SoT가 아니라 outbox**다 — "내가 어떤 intent를 제출하려 했는가"의 기록일 뿐, 주문/포지션의 진실은 API에서 가져온다("로컬 상태 불신" 불변식은 포지션/미체결 주문의 SoT를 한정하므로 충돌하지 않는다).

   **journal entry는 명시적 2-마커 상태 모델을 가진다.** write-ahead 패턴에는 "append는 됐지만 POST는 아직"인 crash window가 내재한다:
   - **`prepared`** — payload를 durable append한 직후. **아직 POST를 호출하지 않았다.**
   - **`submit-attempted`** — `POST /api/v1/orders` **호출 직전에** durable append하는 두 번째 마커(시각 포함). 있으면 "POST가 일어났을 수 있다", 없으면 "확실히 일어나지 않았다".
   - **`acked`** — POST가 응답을 돌려주면 받은 **`orderId`를 durable 기록**한다. 이 시점부터 orderId가 진실의 핸들이 된다.
   - 이후 reconciler가 `resolved` 또는 terminal로 닫는다(ADR-0003).

2. **intentId는 전략이 부여한다(계약).** strategy가 각 매매 결정에 고유한 `intentId`를 붙여 order 계층에 넘긴다. order는 이를 journal 키로 신뢰하고 "같은 intentId면 이미 제출된 주문 재사용". intent **고유성은 전략 책임**이다(같은 결정의 재발화는 같은 intentId, 새 결정은 새 intentId). 의존 방향: order는 `intentId`를 받는 인터페이스를 노출, strategy가 충족(order는 strategy를 import하지 않는다).

3. **사후 검증의 1차 키는 `orderId`다.** POST가 응답을 주면 `OrderResponse.orderId`를 journal(`acked`)에 기록하고, 이후 진실 확인은 **`GET /api/v1/orders/{orderId}` 상세**로 한다 — 열림/닫힘 무관하게 실제 상태를 준다. 재시작 복구의 대부분이 여기서 해결된다. (orderId를 끝내 못 받은 ambiguous submit만 이 키가 없다 → ADR-0003의 유일한 blind spot.)

4. **`clientOrderId`는 intentId에서 deterministic 도출하되, 역할은 서버측 중복방지 키일 뿐이다.** order 계층이 intentId를 Toss 허용 charset/길이로 deterministic 변환(해시)해 만든다. 같은 키로 재제출 시 서버가 중복을 막아주길 *희망*하는 용도다(거동 미정의라 보장 아님). **사후 조회·매칭 키로는 쓸 수 없다** — 목록·상세에 노출되지 않기 때문이다. deterministic 도출이라 journal이 손상돼도 intentId만 있으면 같은 값을 재현한다.

## Alternatives considered

- **clientOrderId를 사후 매칭 키로 사용(OPEN/CLOSED 나열 후 clientOrderId 매칭)** — 기각: **원천 불가능**. jq 검증 결과 `clientOrderId`는 `OrderResponse`(POST 응답)에만 있고 목록·상세 `Order`에는 없다. 게다가 `status=CLOSED` 나열 자체가 미지원(400)이다. (이 ADR의 이전 버전이 잘못된 API 요약 위에 이 메커니즘을 세웠고, codex 리뷰가 PR #8에서 잡았다.)
- **Deterministic key, journal 없음** — 기각: "어떤 intent를 제출했는가"를 알려면 전략을 재실행해야 하는데 전략이 미정·재현 불가일 수 있다. 무인 안전 1순위에서 받아들일 수 없다.
- **order가 intent 내용 + time-bucket으로 dedup 키 합성** — 기각: 의미를 모르는 계층(order)이 dedup 경계를 정하게 되어 "같은 의도"를 오판한다. 경계는 의미를 아는 전략에 둔다.
- **dedup 안 함, 제출만 멱등** — 기각: 전략 버그가 곧바로 중복 체결로 이어진다.
- **clientOrderId를 append 시 새로 생성(랜덤)** — 기각: 값의 진실이 journal에만 갇혀, 손상 시 재제출에서 같은 키 재현 불가. deterministic 도출이 이중 안전.
- **단일 마커(POST 시도 여부 구분 없음)** — 기각: "prepare 도중 크래시"와 "POST 도중 크래시"를 구분 못 해, 일어난 적 없는 POST를 "보냈을 수 있음"으로 다뤄 영속 UNKNOWN에 갇힌다(ADR-0003 settle 기준 시각 부재). 2-마커가 "확실히 안 보냄"과 "보냈을 수 있음"을 가른다.

## Consequences

- (좋음) orderId를 1차 키로 삼아 `GET /orders/{orderId}` 상세로 진실을 직접 얻으므로, 재시작 reconciliation이 전략 종류와 무관하게 성립한다. clientOrderId 노출 부재·CLOSED 나열 부재에 의존하지 않는다.
- (좋음) 패키지 경계가 깨끗하다: order는 `intentId`를 받는 인터페이스만 노출, dedup 의미론은 전략에 머문다.
- (제약) **clientOrderId로는 ambiguous submit(orderId 미확보)을 사후 식별할 수 없다.** 그 한 경우의 처리가 ADR-0003의 핵심 난제다.
- (비용) **디스크 영속 계층**(append-only journal + fsync), POST 경로에 마커 append가 다회(`prepared`/`submit-attempted`/`acked`) 들어간다. 비가역 영역에서 crash window를 닫는 대가로 수용한다. crash 도중 부분 쓰기·동시 append 안전성을 테스트한다(별도 이슈).
- (계약 전파) 전략은 "고유하고 재시작에 안정적인 intentId 부여"를 계약으로 진다.
- (후속) 제출 불명·재시작 복구, ambiguous submit의 fail-safe는 **ADR-0003**에서 결정한다.
