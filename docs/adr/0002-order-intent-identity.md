# ADR-0002: 주문 intent identity는 write-ahead journal에 기록하고 clientOrderId는 intentId에서 deterministic하게 도출한다

- **Status**: Accepted
- **Date**: 2026-06-30
- **Deciders**: chnu-kim
- **관련 이슈/PR**: (미정)

## Context

이 봇은 24시간 무인 실행되며, 주문 제출은 **비가역(돈이 움직임)**이다. CLAUDE.md "무인 실행 제약"이 요구하는 핵심 불변식:

- **재시작 안전**: 프로세스가 죽었다 살아나도 같은 주문을 중복 제출하지 않아야 한다(멱등성/dedup).
- **로컬 상태 불신**: 미체결 주문/포지션의 **진실 출처(SoT)는 API**다 — 로컬 상태를 SoT로 신뢰하지 않는다.

Toss Open API 실측(OpenAPI 스펙 확인):

- `POST /api/v1/orders`는 optional `clientOrderId`를 받는다. 스펙 설명 그대로 "Idempotency key for duplicate prevention"이며, 응답(`OrderResponse`)에 echo된다.
- 단, **중복 `clientOrderId` 제출 시 서버 거동(idempotent replay vs 409)은 스펙에 미정의**다. 서버의 우아한 dedup에 기댈 수 없다.
- `GET /api/v1/orders`에는 **`clientOrderId` 필터가 없다**(필터는 status/symbol/날짜만). 따라서 재구성 시 "이 키의 주문이 존재하나?"를 직접 질의할 수 없고, OPEN/CLOSED 목록을 나열해 **client-side로 clientOrderId를 매칭**해야 한다.

그리고 결정을 가르는 가장 큰 힘: **매매 전략은 현재 미정**이다. 따라서 "전략 결정이 재시작 후 API 재구성 상태에서 deterministic하게 재현 가능하다"를 전제할 수 없다. intent의 identity를 *전략 재실행으로 재현*하는 설계는 재현 불가 전략에서 깨진다.

이 ADR은 **하나의 질문**에 답한다: 재시작을 넘어 "내가 이미 이 주문을 제출했는가"를 무엇이 알게 하는가. (제출 실패/불명 시의 복구 프로토콜은 별도 결정 — ADR-0003.)

## Decision

세 결정을 묶는다(셋이 함께 intent identity를 정의한다):

1. **Intent identity의 출처 = write-ahead journal(outbox).** 주문을 서버에 POST하기 *전에* `(intentId, clientOrderId, 주문 payload)`를 durable하게 append(append-only + fsync)한다. 재시작 시 journal을 재생해 각 entry를 API 상태와 대조한다. journal은 **SoT가 아니라 outbox**다 — "내가 어떤 intent를 제출하려 했는가"의 기록일 뿐, 주문/포지션의 진실은 여전히 API에서 가져온다. 따라서 "로컬 상태 불신" 불변식과 충돌하지 않는다(그 불변식은 포지션/미체결 주문의 SoT를 한정한다).

2. **intentId는 전략이 부여한다(계약).** strategy가 각 매매 결정에 고유한 `intentId`를 붙여 order 계층에 넘긴다. order 계층은 이를 journal의 키로 신뢰하고, "같은 intentId면 이미 제출된 주문을 재사용"한다. intent의 **고유성 보장은 전략의 책임**이다 — 같은 결정의 재발화(예: 폴링 루프가 연속 tick에서 같은 조건 감지)는 같은 intentId여야 하고, 진짜 새 결정은 새 intentId여야 한다. 의존 방향: order 계층은 `intentId`를 받는 인터페이스를 노출하고, strategy가 그 계약을 충족한다(order는 strategy를 import하지 않는다).

3. **clientOrderId는 order 계층이 intentId에서 deterministic하게 도출한다.** Toss가 허용하는 charset/길이로 intentId를 deterministic 변환(해시)해 clientOrderId를 만든다. 이 값을 journal에도 기록하되, **journal이 손상/유실되어도 intentId만 있으면 같은 clientOrderId를 재현**할 수 있다. clientOrderId 값의 진실이 journal에만 갇히지 않는다.

journal과 deterministic 도출은 직교한다: **journal**은 "어떤 intentId들을 제출했는가"(전략 재현 불가 대비)를, **deterministic 도출**은 "그 intentId의 clientOrderId 값"(journal 무결성 대비)을 책임진다.

## Alternatives considered

- **Deterministic key, journal 없음** — 기각: intent를 재현해 clientOrderId를 recompute하면 로컬 영속이 아예 없어 "상태 불신" 불변식에 가장 깔끔히 맞아 보인다. 그러나 "어떤 intent들을 제출했는가"를 알려면 전략을 재실행해 결정을 재현해야 하는데, **전략이 미정 + 재현 불가일 수 있다**. 재현 불가 전략에서 "내가 무엇을 제출했는지" 자체를 모르게 된다. 무인 안전 1순위에서 받아들일 수 없는 가정.
- **order 계층이 intent 내용 + time-bucket으로 dedup 키 합성** — 기각: 전략을 멱등 의식 없이 둘 수 있어 매력적이나, **의미를 모르는 계층(order)이 dedup 경계를 정하게** 된다. time-bucket 크기를 order가 임의로 골라야 하고, "같은 의도"를 오판(서로 다른 결정을 한 주문으로 합치거나, 같은 결정을 둘로 쪼갬)할 수 있다. 경계는 의미를 아는 전략에 둔다.
- **dedup 안 함, 제출만 멱등** — 기각: order는 POST 재시도 멱등만 보장하고 intent 간 중복은 전략 책임으로 미룬다. 가장 단순하나 전략 버그가 곧바로 중복 체결(비가역 손실)로 이어진다 — 무인 안전 1순위와 정면 충돌.
- **clientOrderId를 append 시 새로 생성해 journal에 매핑 영속** — 기각: 단순하고 충돌 없으나 clientOrderId 값의 진실이 전적으로 journal에만 존재한다. journal 손상 시 재제출(ADR-0003)에서 같은 키를 재현할 수 없어 backstop이 무너진다. deterministic 도출이면 intentId만으로 값을 복원해 이중 안전.

## Consequences

- (좋음) "내가 제출한 intent" 목록이 전략 재실행 없이 journal에서 복원되고, 각 clientOrderId 값은 intentId에서 재현된다 → 재시작 후 reconciliation이 전략 종류와 무관하게 성립한다.
- (좋음) 패키지 경계가 깨끗하다: order는 `intentId`를 받는 인터페이스만 노출, dedup 의미론은 전략에 머문다. order → strategy 역방향 의존이 없다.
- (비용) **디스크 영속 계층이 다시 들어온다**(append-only journal + fsync). 단 SoT가 아닌 outbox이며, crash 도중 부분 쓰기·동시 append 안전성을 설계·테스트해야 한다(별도 이슈 후보).
- (계약 전파) **전략은 "고유하고 재시작에 안정적인 intentId 부여"를 계약으로 진다.** 같은 결정의 재발화는 같은 intentId여야 한다 — 전략 설계 시 이 계약을 ADR로 링크해 전달한다.
- (제약 전파) clientOrderId 도출 함수는 Toss `clientOrderId` 형식 제약(charset/최대 길이)을 만족해야 한다 — 정확한 한계는 구현 시 OpenAPI 스키마에서 확정한다.
- (후속) 제출 실패/응답 불명 시의 복구, 재시작 시 OPEN/CLOSED 나열·clientOrderId 매칭으로 진실을 재구성하는 프로토콜은 **ADR-0003(ambiguous-submit & 재시작 reconciliation)**에서 결정한다. 이 ADR은 "무엇이 identity인가"만 정한다.
