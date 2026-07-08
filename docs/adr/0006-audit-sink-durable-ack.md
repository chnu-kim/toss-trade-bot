---
id: "0006"
status: Accepted
date: 2026-07-03
deciders: [chnu-kim]
domain: [audit, persistence]
protects: []
supersedes: []
superseded_by: null
verification: []
---

# ADR-0006: 감사 sink는 발생 시점에 fsync-durable하게 기록하는 자체 내구 경로를 소유하고, 전달은 at-least-once + 멱등, prune은 "로컬 durable ack"에 게이팅한다

- **Status**: Accepted
- **Date**: 2026-07-03
- **Deciders**: chnu-kim
- **관련 이슈/PR**: (TBD)

## Context

이 봇은 24시간 무인 실행되고 주문 제출은 **비가역(돈이 움직임)**이다. ADR-0005가 영속 계층을 **라이브 상태 저장소(store)**와 **감사/관측 히스토리(감사 sink)**로 갈랐고, prune-gating(ADR-0005 point 6)의 **선행 조건으로 "감사 sink의 내구성·재시도·멱등·durable-ack 계약"을 이 ADR에 명시적으로 위임**했다: *"이 계약이 서기 전까지 terminal prune을 활성화하지 않는다."*

이 ADR은 답한다: **무엇이·어느 시점에·어떤 물리 내구성으로 감사 기록을 남기고, 실패하면 무엇을 하며, 같은 기록이 여러 번 나가도 안전하게 만드는 멱등 계약은 무엇이고, prune을 여는 "durable ack"는 정확히 무슨 신호인가.**

앞선 ADR·불변식이 고정한 힘(재-grilling 안 함, 이 결정의 전제):

- **관측성 불변식(CLAUDE.md).** "무인이므로 로그가 유일한 사후 진단 수단 — 구조화 로깅 + **모든 주문/체결/에러 영속 기록**." 감사 기록은 비가역 자금행위의 **유일한 사본**일 수 있다.
- **감사 sink는 store가 아니다(ADR-0005 point 5).** 라이브 상태 저장소를 히스토리로 부풀리지 않는다. 감사는 별도 계층(파일/외부 sink)으로 간다.
- **prune-gating(ADR-0005 point 6).** terminal 행은 감사 sink가 대응 기록을 durable하게 ack하기 전까지 prune하지 않는다. ack 부재 시 **보존 쪽으로 fail**(삭제 아님). ADR-0005는 두 구현 후보를 남겼다 — (a) durable ack를 store에 트랜잭션으로 기록하고 그 플래그가 선 terminal 행만 prune, (b) sink가 확인하기 전까지 최소 terminal 감사 레코드를 SQLite에 fallback 보존.
- **디스크 풀 = fail-closed(ADR-0005 point 6).** durable append 실패 시 안전하게 제출할 수 없다. 트리거 처리는 killswitch(ADR-0004).
- **단일 프로세스(ADR-0001), 단일 store 트랜잭션(ADR-0004/0005), 2-마커 journal-outbox(ADR-0002).**
- **기존 `runtime.NewLogger`.** slog JSON을 주입된 writer(prod=stdout)로 뽑는 운영 진단 로거. 파일/회전/외부 sink 관심사는 없고, 주석이 "future durable audit-sink issue(ADR-0005 point 6)"로 이 ADR을 지목한다.

**Toss API 실측(이 ADR을 위해 `openapi.json`을 jq로 직접 검증):**

- **per-fill(체결 건별) 식별자는 존재하지 않는다.** `OrderExecution`은 orderId당 **누적 집계 스냅샷**(`filledQuantity`, `averageFilledPrice`, `filledAmount`, `commission`, `tax`, `filledAt`, `settlementDate`)이고 `Order.execution`에 임베드된다 — 개별 체결 이벤트 목록도, 체결 id도 없다.
- `/api/v1/trades`의 `Trade`(price/volume/timestamp/currency)는 **시장 체결 틱**이지 내 계좌 체결이 아니며 id가 없다.
- 즉 "체결 이벤트"는 우리가 `GET /api/v1/orders/{orderId}` 상세를 폴링해 관측하는 **누적 execution 델타**로 **우리가 합성**한다.

핵심 긴장 네 가지가 결정을 가른다:

1. **재구성 가능성의 비대칭.** 주문 수명주기 이벤트는 store journal(2-마커 + orderId)이 durable하게 들고 있어 재시작 시 re-emit으로 복구된다. 그러나 **에러(일시 500, 토큰 갱신 실패, 폴링 오류)는 journal intent가 아니고 API가 SoT로 되짚어주지도 않는다 — 발생 시점에 durable하게 안 남기면 크래시로 영구 소실**된다. 체결도 per-fill id·이벤트 스트림이 없어 재구성이 관측 시점에 묶인다. 따라서 journal-as-outbox는 **주문 수명주기에만** 통하고, 전 스트림 내구성은 sink 자신이 발생 시점에 져야 한다.
2. **"durable ack"의 은닉된 fail-open 두 방향.** ack를 **원격 수집기 확인**으로 정의하면 수집기 다운 시 terminal 행이 prune되지 않아 journal이 무한 성장 → 디스크 풀(ADR-0005가 막으려던 바로 그 실패)로 되돌아간다. ack를 너무 약하게(메모리 버퍼에 넣음) 정의하면 크래시로 자금행위 기록이 소실된다(관측성 불변식 위반). 둘 사이에 물리 내구성의 경계를 그어야 한다.
3. **멱등의 자연키 부재.** at-least-once 재전송·재시작 re-emit이 같은 기록을 중복 방출한다. 주문 이벤트는 orderId로 키를 얻지만, **체결은 per-fill id가 없고 에러는 자연키가 아예 없다.** 멱등키를 이벤트 부류별로 합성해야 한다.
4. **감사 실패 → 안전 정지의 순환 위험.** "감사 못 하면 신규 주문 정지"(fail-closed)를 걸면, 그 정지 자체가 감사 write에 의존하면 데드락이다. halt 트립 경로는 감사 sink와 **다른 내구 경로**를 써야 한다.

## Decision

1. **감사 sink는 발생 시점에 로컬 append-only 파일에 fsync-durable하게 기록하는 자체 내구 경로를 소유한다. store journal은 이 경로가 아니다.** "모든 주문/체결/에러 영속 기록" 불변식을 sink가 직접 진다 — 특히 **재구성 불가능한 에러·관측-시점에 묶인 체결**은 발생 시점에 sink가 durable하게 쓰지 않으면 소실된다. store journal의 durability는 **주문 수명주기 이벤트에 한해** 보조적으로 겹칠 뿐(point 5의 re-emit), sink의 1차 내구성을 대체하지 않는다. 이 로컬 durable 스테이지가 **감사의 진실 매체(system of record for history)**다.

2. **`internal/audit` leaf 패키지가 감사 레코드 타입·직렬화·로컬 durable writer·멱등키 도출·ack 신호를 소유한다.** 의존 방향: order/reconciler/killswitch/토큰매니저가 audit를 import해 이벤트를 방출하고, **audit는 어떤 도메인 로직 패키지도 import하지 않는다**(순환 금지 — store·killswitch와 동일한 leaf 규율). 소비자는 audit가 만족하는 **인터페이스에 의존**해 가짜로 단위 테스트하지만, **sink 자신의 fsync·부분쓰기·회전(rotation)·재시작 재개 테스트는 임시 디렉터리에 실제 writer**를 띄워 돌린다(ADR-0005 테스트 방침과 동형).

   **`runtime.NewLogger`와의 관계 = 역할 분리, 대체 아님.** slog JSON 로거는 **운영 진단**(best-effort, stdout, greppable)이고, 감사 sink는 **비가역 자금행위의 durable 히스토리**(fsync·ack·재시도·멱등)다. 둘은 내구성 계약이 다르다. 감사 sink는 가시성을 위해 slog로도 흘릴 수 있으나, **durability는 slog에 의존하지 않는다**(stdout은 durable ack를 줄 수 없다).

3. **전달 보증 = at-least-once + 멱등 소비. 순서 = per-intent/per-order 부분순서만.** 크래시·재전송으로 같은 레코드가 여러 번 나갈 수 있음을 전제하고, **멱등키로 중복을 병합**한다. exactly-once는 분산 커밋을 요구하나 단일 프로세스·로컬 파일에는 과하고 달성 불가에 가깝다. 전역 total order는 불필요하다 — 재구성은 intent/order 단위로 성립한다(ADR-0002/0003).

   **멱등키는 이벤트 부류별로 결정적으로 합성한다:**
   - **주문 수명주기**: orderId 확보 후 = `orderId + marker/status`. 확보 전(prepared/submit-attempted) = `intentId + marker`(ADR-0002의 결정적 키 재사용).
   - **체결**: per-fill id가 없으므로(**실측**) **스냅샷 버전**으로 다룬다 — `orderId + 재무필드 digest`(filledQuantity·averageFilledPrice·filledAmount·commission·tax·settlementDate에 대한 결정적 다이제스트)로 합성한다. **누적 filledQuantity 증가만이 아니라 재무필드 중 하나라도 바뀌면 감사 레코드를 방출**한다. 같은 스냅샷 재폴링은 digest 일치로 자연 dedup되지만, **동일 수량에서의 수수료·세금·결제일(settlementDate) 정정은 새 digest → 새(정정) 레코드로 감사에 남는다**(재구성·수수료·세금·결제 진단에 필요 — 누적량만으로 dedup하면 이 정정이 중복으로 억제돼 소실된다).
   - **에러**: 자연키가 없다 → `(intentId|orderId|"global") + 연산(operation) + 에러클래스 + 발생시퀀스`로 합성하되, **발생시퀀스는 별도 카운터가 아니라 커밋된 레코드의 durable append 위치(감사 로그 offset/record id)에서 파생**한다 — 별도 카운터의 전진과 레코드 durability 사이 desync를 원천 차단한다(같은 committed 레코드는 같은 위치-키로 재배송돼 멱등). **에러는 재구성 불가(journal intent 아님·API SoT 없음)라 재시작 re-emit로 복구할 수 없다** — 그래서 에러 감사 emit은 **동기 durable**이다: 레코드가 durable 세그먼트에 **완전히 커밋(point 4 세그먼트 durability 프로토콜)되기 전에는 호출자에게 성공을 반환하지 않는다.** 복구는 committed와 uncommitted(torn tail)를 명확히 구분하고(write 포맷이 완결 마커/체크섬으로 판별), **uncommitted 레코드는 재구성 불가라 '유실'로 취급**한다 — 이때 emit 실패/미완은 **point 6 fail-closed(감사 못 하면 신규 주문 금지)**로 이어진다. **잔여 위험 명시**: 에러 관측 직후 그 durable write *도중* 크래시하면 정확히 그 한 레코드가 유실된다 — 비재구성 이벤트에 내재한 유계 잔여 위험으로 수용하며(주문 수명주기는 journal 재구성으로 복구되는 것과 대조), 동기 durable로 그 창을 최소화한다. **re-emit로 에러를 복구한다고 주장하지 않는다.**

4. **prune을 여는 "durable ack" = 로컬 세그먼트 durability 프로토콜 완료. 단순 record fsync도, 원격 수집기 확인도 아니다.** intent의 **모든 lifecycle 감사 레코드**(각 마커 `prepared`·`submit-attempted`·`acked` 전이 + 최종 execution 스냅샷을 담은 terminal)가 **로컬 durable 세그먼트에 완전히 커밋된 순간**(아래 프로토콜) 그 intent의 ack가 성립하고, 그 ack를 받아야 prune이 열린다. **terminal 레코드 하나만으로 게이팅하지 않는다** — 마커가 store journal에 커밋된 뒤 그 마커의 감사 write가 커밋되기 전에 크래시했는데 terminal만 ack돼 prune이 journal(그 중간 마커 감사의 **유일한 durable outbox**)을 지우면, 재구성 불가한 중간 lifecycle 감사가 영구 소실되어 "모든 주문 감사" 불변식이 깨진다. 원격 확인에 게이팅하지 않는다 — 수집기 다운이 journal 무한 성장 → 디스크 풀(ADR-0005가 봉쇄한 실패)로 되돌아가기 때문이다. 로컬 커밋은 디스크 풀이 아닌 한 항상 달성 가능하고, **디스크 풀 자체는 fail-closed(point 6)**라 안전 방향으로 수렴한다. 원격 배송은 5에서 분리한다.

   **세그먼트 durability 프로토콜(단순 내용 fsync ≠ durable)**: 레코드 append는 (i) 세그먼트 파일 내용 fsync로 끝나지 않는다. 새 세그먼트 생성·회전은 (ii) 임시명으로 write 후 내용 fsync → **atomic rename**으로 최종명 확정 → (iii) create/rename/unlink **직후 부모 디렉터리를 fsync**해 디렉터리 엔트리까지 durable화한다. 내용만 fsync하고 디렉터리 엔트리를 fsync하지 않으면 새로/회전 생성된 세그먼트가 크래시로 **통째 유실**될 수 있어, store가 fully-audited 플래그를 세우고 prune을 허용한 뒤 감사가 사라지는 **fail-open**이 된다(POSIX 함정). "durable ack"·point 3의 "완전 커밋"·point 5 회전의 "미배송분 유실 금지"는 모두 **이 프로토콜 완료**를 뜻하며, 단순 record fsync가 아니다.

   **prune-gate 구현 = ADR-0005 후보 (a).** intent 행에 **"모든 lifecycle 감사 레코드 durable ack 완료" 플래그/타임스탬프**를 store 트랜잭션으로 기록하고(감사 *내용*이 아니라 boolean/timestamp만 — store를 히스토리로 부풀리지 않는다, point 5), 그 플래그가 선 intent만 prune 대상이 된다. 후보 (b)(감사 내용을 SQLite에 fallback 보존)는 기각 — 감사 히스토리를 트랜잭션 저장소로 밀어넣어 point 5를 위반한다. **복구 루프**: 마커 커밋 후 그 감사 fsync 전에 크래시하면, 재시작 시 reconciler가 **아직 fully-audited로 ack되지 않은 intent의 모든 lifecycle 감사 레코드를 journal 상태(마커·orderId·상태·시각)에서 결정적으로 재구성해 re-emit**한다(중간 마커 전이 감사를 포함, 멱등키로 소비 측 병합). journal이 마커 히스토리를 prune 전까지 들고 있으므로 재구성이 성립한다 — 모든 lifecycle 레코드가 durable ack된 뒤에만 그 intent가 prune-eligible. 즉 store journal이 **주문 수명주기 감사(중간 마커 포함)의 durable outbox** 역할을 하고, per-intent fully-audited 플래그가 그 위의 prune-gate 층이다. (체결·에러는 3의 자체 durable 경로로 보장되며 개별적으로 journal prune을 게이팅하지 않는다.)

5. **원격 배송(shipping)은 로컬 durable 스테이지에서 분리된 async best-effort이고, 자체 보존·재시도를 가지며 prune을 게이팅하지 않는다.** 로컬 durable 파일을 원격 sink로 밀어보내는 일은 별도 파이프라인이다 — 실패해도 **주문 루프를 막지 않고**, 지수 백오프로 재시도하며, 배송 확인 전까지 로컬 파일을 보존한다(회전은 point 4 세그먼트 durability 프로토콜을 지켜 미배송분 유실 금지). 이 보존은 **prune-gating과 독립**이다(prune은 로컬 세그먼트 durability ack에만 게이팅, 4). 원격이 오래 다운되면 로컬 파일이 커지므로, 로컬 감사 디스크 사용도 **모니터링 대상**이며 임계 초과는 운영 알림(대상 미정)으로 승격한다.

6. **감사 로컬 durable write 실패(디스크 풀 포함) = fail-closed, killswitch로 승격. 단 halt 트립은 감사와 다른 내구 경로를 쓴다.** 발생 시점 감사를 durable하게 남기지 못하면 **신규 주문을 안전하게 제출할 수 없다**(관측성 불변식: 자금행위를 기록 없이 실행 금지). 이 조건을 killswitch(ADR-0004) 트리거로 배선한다. **순환 방지**: halt 트립은 store 트랜잭션(라이브 상태, ADR-0004/0005)에 쓰이고 — **감사 sink write에 의존하지 않는다** — 따라서 "감사 불가 → halt"가 다시 감사 write를 요구해 데드락에 빠지지 않는다. halt 트립 사건 자체의 감사 기록은 best-effort(실패해도 halt 상태는 store에 durable).

## Alternatives considered

- **journal이 전 스트림의 durable outbox(감사 sink는 발생 시점 durability 불필요)** — 기각(advisor 지적): journal은 주문 수명주기 intent만 durable하게 든다. **에러는 journal intent가 아니고 API가 SoT로 되짚어주지 않아 재구성 불가** — 발생 시점에 안 남기면 크래시로 영구 소실된다. 체결도 per-fill id가 없어 관측 시점에 묶인다. 관측성 불변식(모든 주문/**체결/에러**)이 sink 자신의 발생-시점 durable 경로를 강제한다.
- **"durable ack" = 원격 수집기 확인** — 기각: 수집기 다운 시 terminal 행이 prune되지 않아 journal 무한 성장 → 디스크 풀(ADR-0005 point 6이 봉쇄한 바로 그 fail 모드)로 되돌아간다. 더 강한 보증처럼 보이나, 무인 24/7에서는 오히려 안전사고를 재도입한다. 로컬 fsync를 ack로 삼고 원격 배송을 분리(재시도·모니터링)하는 편이 유계와 무손실을 양립시킨다.
- **"durable ack" = 메모리 버퍼 적재(async flush)** — 기각: 크래시로 자금행위 기록이 소실된다(관측성 불변식 위반). ADR-0005 point 4의 "내구성 완화 금지"와 동일 정신 — 도장을 찍었다고 믿는데 안 남는 fail-open.
- **prune-gate 후보 (b): terminal 감사 내용을 SQLite에 fallback 보존** — 기각: 감사 히스토리를 트랜잭션 저장소로 밀어넣어 ADR-0005 point 5(라이브 상태 store를 히스토리로 부풀리지 않음)를 위반한다. (a)는 boolean/timestamp 플래그만 store에 두어 내용은 sink에 남긴다.
- **prune을 terminal 감사 레코드 하나의 ack에만 게이팅** — 기각(codex GitHub 리뷰 P2 지적): 마커(`prepared`/`submit-attempted`/`acked`)가 journal에 커밋된 뒤 그 마커 감사가 fsync 전 크래시로 유실됐는데 terminal만 ack되면, prune이 journal(그 중간 마커 감사의 유일한 durable outbox)을 지워 **재구성 불가한 중간 lifecycle 감사가 영구 소실**된다(모든 주문 감사 불변식 위반). ack를 intent의 **모든 lifecycle 레코드**로 확장하고, 재시작 시 미-ack lifecycle 레코드를 journal에서 결정적으로 재구성해 re-emit한 뒤에만 prune-eligible로 넘긴다.
- **exactly-once 전달** — 기각: 분산 원자 커밋(감사 write ↔ 소비 확인)을 요구하는데 로컬 파일·단일 프로세스에서 실질 불가·과설계다. at-least-once + 결정적 멱등키가 같은 안전성을 훨씬 싸게 준다.
- **체결 멱등키 = per-fill 식별자** — 기각(**원천 불가능, 실측**): `openapi.json` jq 검증 결과 `OrderExecution`은 orderId당 누적 집계 스냅샷일 뿐 per-fill id·이벤트 목록이 없고, `/api/v1/trades`는 시장 틱이지 계좌 체결이 아니다. `orderId + 재무필드 digest`로 스냅샷 버전화한다.
- **체결 멱등키 = `orderId + 누적 filledQuantity`만(수량 증가 시에만 방출)** — 기각(codex adversarial-review 지적): 동일 수량에서 averageFilledPrice·filledAmount·commission·tax·settlementDate가 정정되면 그 스냅샷이 중복으로 억제돼 감사에서 **재무 변화가 소실**된다(재구성·수수료·세금·결제 진단 누락). 재무필드 digest로 키를 잡고 필드 변화 시마다 방출해 정정을 보존한다.
- **에러 발생시퀀스를 별도 durable 카운터로 할당** — 기각(codex adversarial-review 지적): 카운터 전진과 레코드 durability가 분리되면 그 사이 크래시가 seq를 건너뛰거나 같은 에러를 **다른 키로 재방출**한다 — 에러는 재구성 불가라 중복·유실이 가장 치명적인 부류다. seq를 durable append 위치에서 파생해 **할당=append를 원자화**하고, torn tail은 재시작 시 폐기해 정합을 지킨다.
- **에러를 재시작 시 재구성(re-emit)으로 복구** — 기각: 에러는 journal intent가 아니고 API SoT에도 없어 **재구성 자체가 불가능**하다. at-least-once re-emit 모델을 태울 수 없으므로 발생 시점 durable write가 유일한 보증이다.
- **에러 torn-tail을 재시작 re-emit로 복구(비재구성인데 재방출 주장)** — 기각(codex adversarial 재검토 지적): 유실된 torn-tail을 재방출할 durable 소스가 에러엔 없다(재구성 불가) — "새 위치로 재방출해도 정합"은 재구성 가능한 주문 수명주기에만 통하는 논리를 잘못 빌려온 자기모순이었다. 에러 emit을 **동기 durable**(완전 커밋 전 성공 반환 금지)로 하고 committed/uncommitted를 구분, uncommitted는 '유실'로 취급해 fail-closed로 잇는다 — re-emit 복구를 주장하지 않고 잔여 위험을 명시 수용한다.
- **durable ack = 단순 record fsync(세그먼트/디렉터리 durability 무시)** — 기각(codex adversarial 재검토 지적): 새로/회전 생성된 세그먼트는 내용 fsync만으로는 디렉터리 엔트리가 durable하지 않아(부모 디렉터리 fsync 필요) 크래시로 통째 유실될 수 있다 — store가 fully-audited로 표시하고 prune한 뒤 감사가 사라지는 fail-open(POSIX 함정). durable ack를 세그먼트 durability 프로토콜(내용 fsync + atomic naming + create/rename/unlink 후 부모 디렉터리 fsync)로 정의한다.
- **감사 sink가 `runtime.NewLogger`(slog/stdout)를 durability 경로로 재사용** — 기각: stdout은 durable ack를 줄 수 없고 회전·유실 통제가 없다. slog는 운영 진단(best-effort)으로 유지하고, 감사는 fsync·ack·재시도·멱등을 지는 별도 경로로 둔다(역할 분리).
- **감사 실패를 무시하고 주문 계속(fail-open)** — 기각: 기록 없이 비가역 자금행위를 실행하게 되어 관측성 불변식·무인 안전 1순위를 정면으로 위반한다. fail-closed로 killswitch 승격.
- **fail-closed halt를 감사 sink에 기록해 트립** — 기각: "감사 불가 → halt"가 다시 감사 write를 요구하면 데드락이다. halt 트립은 store 라이브 상태(ADR-0004/0005)에 써서 감사 경로와 분리한다.
- **주문 제출 경로에 원격 배송을 동기 결합** — 기각: 원격 sink 지연·다운이 주문 루프를 막아 "죽지 않는다" 불변식과 충돌한다. 배송은 async best-effort로 분리하고 로컬 fsync만 주문 경로에 동기로 둔다.

## Consequences

- (좋음) "모든 주문/체결/에러" 관측성 불변식이 sink의 발생-시점 fsync 경로로 **전 스트림에서** 성립한다 — 재구성 불가한 에러·관측-시점에 묶인 체결까지 크래시를 견딘다.
- (좋음) prune이 **로컬 fsync ack**에 게이팅되어 유계(디스크 풀 방지)와 무손실(자금행위 감사 보존)이 양립한다. 원격 수집기 상태가 journal 성장·주문 루프에 영향을 주지 않는다.
- (좋음) ADR-0005 point 6의 선행 조건(감사 내구성·재시도·멱등·durable-ack 계약)이 확정되어, **terminal prune을 안전하게 활성화**할 수 있다.
- (좋음) prune-gate가 store에 boolean/timestamp 플래그만 두어(내용은 sink) ADR-0005 point 5의 "store를 히스토리로 부풀리지 않음"을 지킨다. store journal이 주문 수명주기 감사(**중간 마커 포함**)의 durable outbox, per-intent fully-audited 플래그가 그 위 prune-gate 층으로 깔끔히 층화된다 — 모든 lifecycle 레코드가 durable ack되기 전에는 prune이 outbox를 지우지 못한다.
- (좋음) 결정적 멱등키(부류별 합성)로 at-least-once 재전송·재시작 re-emit이 안전하게 병합된다 — 체결의 per-fill id 부재를 **재무필드 digest로 스냅샷 버전화**(동일 수량 정정도 보존)하고, 에러 seq를 **durable append 위치에서 파생**해 할당=append를 원자화(카운터 desync·다른-키 재방출 봉쇄)한다.
- (비용) POST/폴링 경로에 감사 커밋이 동기로 들어간다(주문 이벤트는 store 마커 fsync에 더해). 세그먼트 durability는 내용 fsync만이 아니라 **create/rename/unlink 후 부모 디렉터리 fsync**까지 요구하므로 회전·세그먼트 생성 시 fsync가 추가된다. 폴링 기반이라 수용하나, "하나의 사건이 store 마커 + 감사 + (필요 시) halt를 건드릴 때"의 경계를 구현이 정확히 지켜야 한다.
- (제약, 잔여 위험) **에러는 재구성 불가하므로 완전 보증이 불가능하다** — 에러 관측 직후 그 동기 durable write *도중* 크래시하면 정확히 그 한 레코드가 유실된다. 동기 durable로 창을 최소화하고 uncommitted를 fail-closed로 잇지만, 이 유계 잔여 위험은 물리적으로 제거 불가하며 명시 수용한다(주문 수명주기는 journal 재구성으로 복구되는 것과 대조).
- (비용) 이중 관리: 라이브 상태(store) · 감사 히스토리(로컬 durable + 원격 배송) · 운영 진단(slog). 세 경로의 역할 경계를 구현·리뷰가 유지해야 한다.
- (비용) 로컬 감사 파일이 원격 배송 지연 시 성장한다 → 로컬 감사 디스크도 모니터링·알림 대상(별도 라이브 상태 store 유계와 무관하게).
- (제약 전파)
  - **reconciler(ADR-0003)**는 재시작 시 fully-audited로 ack되지 않은 intent의 **모든 lifecycle 감사 레코드**(중간 마커 포함)를 journal에서 재구성해 감사 sink로 re-emit하고, 전부 ack가 선 뒤에만 prune-eligible로 넘긴다.
  - **killswitch(ADR-0004)**는 감사 durable write 실패를 트립 조건으로 받되, 트립 경로가 감사 write에 의존하지 않도록(store 라이브 상태에 트립) 배선한다.
  - **store(ADR-0005)**는 intent 행에 "모든 lifecycle 감사 레코드 durable ack 완료" 플래그/타임스탬프 컬럼을 갖고, prune 쿼리가 그 플래그를 조건에 포함한다.
  - **order/전략(ADR-0002)**은 감사 이벤트 방출 지점(marker 전이·orderId 확보·에러)에서 audit 인터페이스를 호출한다.
- (후속)
  - **`internal/audit` 구현 이슈** — 레코드 스키마·직렬화·로컬 durable writer·멱등키 도출·ack 신호·인터페이스 형태. **필수 크래시-복구 프로토콜과 테스트**: (1) **세그먼트 durability** — 내용 fsync + atomic naming + create/rename/unlink 후 부모 디렉터리 fsync, *세그먼트 생성/회전 직후 디렉터리 fsync 전 크래시*에도 세그먼트가 살아남는지; (2) **torn-tail 판별** — 완결 마커/체크섬으로 committed/uncommitted 구분, uncommitted 폐기; (3) **에러 동기 durable** — 완전 커밋 전 성공 미반환, 미완 시 fail-closed; (4) **에러 seq는 committed append 위치에서 파생**(회전이 단조성을 깨지 않는지); (5) **체결 재무필드 digest** 스냅샷 버전화. 실제 writer로 temp dir·`go test -race`로 돌린다.
  - **감사 ack ↔ store 플래그 배선 이슈** — 각 lifecycle 마커 감사 fsync → store 트랜잭션 ack 기록 → 전부 ack 시 prune-eligible의 원자성·복구(재시작 시 미-ack lifecycle 레코드 journal 재구성 re-emit) 경계. "중간 마커 감사 유실 후 terminal만 ack돼도 prune 안 됨"·"ack 없으면 보존" 단위 테스트.
  - **원격 배송 파이프라인 이슈** — async 백오프 재시도·미배송분 보존·로컬 파일 회전 안전·로컬 감사 디스크 모니터링. (대상 sink·프로토콜 미정.)
  - **감사 실패 → killswitch 트리거 배선 이슈** — 순환 방지(store 경로 트립) 포함. ADR-0004·0005 point 6 디스크 풀 트리거와 통합.
  - **운영 알림 채널 결정** — 로컬 감사 디스크 임계·원격 배송 장기 실패·killswitch 트립의 외부 알림 대상(CLAUDE.md "치명적 상황은 외부 알림(미정)").
