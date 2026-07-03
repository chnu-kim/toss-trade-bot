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
   - **에러**: 자연키가 없다 → `(intentId|orderId|"global") + 연산(operation) + 에러클래스 + 발생시퀀스`로 합성하되, **발생시퀀스는 별도 카운터가 아니라 로컬 durable append 위치(감사 로그 offset/record id)에서 파생**한다 — seq 할당과 레코드 append를 **하나의 크래시-복구 가능한 연산**으로 묶어, 카운터 전진과 레코드 durability 사이 크래시가 seq를 건너뛰거나 같은 에러를 다른 키로 재방출하는 것을 원천 차단한다. **재구성 불가하므로 발생 시점 durable write가 필수**이고(1의 근거), fsync 미완(torn tail)은 재시작 시 폐기(ack 안 됨)되어 downstream 미관측이므로 새 위치로 재방출해도 정합하며, durable append 자체 실패는 point 6 fail-closed로 수렴한다.

4. **prune을 여는 "durable ack" = 로컬 fsync 완료. 원격 수집기 확인이 아니다.** terminal intent의 감사 레코드(최종 execution 스냅샷 포함)가 **로컬 durable 파일에 fsync된 순간** ack가 성립하고, 그 ack를 받아야 prune이 열린다. 원격 확인에 게이팅하지 않는다 — 수집기 다운이 journal 무한 성장 → 디스크 풀(ADR-0005가 봉쇄한 실패)로 되돌아가기 때문이다. 로컬 fsync는 디스크 풀이 아닌 한 항상 달성 가능하고, **디스크 풀 자체는 fail-closed(point 6)**라 안전 방향으로 수렴한다. 원격 배송은 5에서 분리한다.

   **prune-gate 구현 = ADR-0005 후보 (a).** terminal intent 행에 **ack 플래그/타임스탬프**를 store 트랜잭션으로 기록하고(감사 *내용*이 아니라 boolean/timestamp만 — store를 히스토리로 부풀리지 않는다, point 5), 그 플래그가 선 terminal 행만 prune 대상이 된다. 후보 (b)(terminal 감사 내용을 SQLite에 fallback 보존)는 기각 — 감사 히스토리를 트랜잭션 저장소로 밀어넣어 point 5를 위반한다. **복구 루프**: terminal화 후 감사 fsync 전에 크래시하면 재시작 시 reconciler가 **ack 미표시 terminal intent를 감사 sink로 re-emit**한다(멱등이라 소비 측에서 병합). ack가 서면 그때만 prune-eligible. 즉 store journal이 **주문 수명주기 감사의 durable outbox** 역할을 하고, ack 플래그가 그 위의 prune-gate 층이다. (체결·에러는 3의 자체 durable 경로로 보장되며 개별적으로 journal prune을 게이팅하지 않는다 — terminal 감사 레코드가 최종 체결 스냅샷을 포함해 그 시점의 자금 결과를 봉인한다.)

5. **원격 배송(shipping)은 로컬 durable 스테이지에서 분리된 async best-effort이고, 자체 보존·재시도를 가지며 prune을 게이팅하지 않는다.** 로컬 durable 파일을 원격 sink로 밀어보내는 일은 별도 파이프라인이다 — 실패해도 **주문 루프를 막지 않고**, 지수 백오프로 재시도하며, 배송 확인 전까지 로컬 파일을 보존한다(회전 시 미배송분 유실 금지). 이 보존은 **prune-gating과 독립**이다(prune은 로컬 fsync ack에만 게이팅, 4). 원격이 오래 다운되면 로컬 파일이 커지므로, 로컬 감사 디스크 사용도 **모니터링 대상**이며 임계 초과는 운영 알림(대상 미정)으로 승격한다.

6. **감사 로컬 durable write 실패(디스크 풀 포함) = fail-closed, killswitch로 승격. 단 halt 트립은 감사와 다른 내구 경로를 쓴다.** 발생 시점 감사를 durable하게 남기지 못하면 **신규 주문을 안전하게 제출할 수 없다**(관측성 불변식: 자금행위를 기록 없이 실행 금지). 이 조건을 killswitch(ADR-0004) 트리거로 배선한다. **순환 방지**: halt 트립은 store 트랜잭션(라이브 상태, ADR-0004/0005)에 쓰이고 — **감사 sink write에 의존하지 않는다** — 따라서 "감사 불가 → halt"가 다시 감사 write를 요구해 데드락에 빠지지 않는다. halt 트립 사건 자체의 감사 기록은 best-effort(실패해도 halt 상태는 store에 durable).

## Alternatives considered

- **journal이 전 스트림의 durable outbox(감사 sink는 발생 시점 durability 불필요)** — 기각(advisor 지적): journal은 주문 수명주기 intent만 durable하게 든다. **에러는 journal intent가 아니고 API가 SoT로 되짚어주지 않아 재구성 불가** — 발생 시점에 안 남기면 크래시로 영구 소실된다. 체결도 per-fill id가 없어 관측 시점에 묶인다. 관측성 불변식(모든 주문/**체결/에러**)이 sink 자신의 발생-시점 durable 경로를 강제한다.
- **"durable ack" = 원격 수집기 확인** — 기각: 수집기 다운 시 terminal 행이 prune되지 않아 journal 무한 성장 → 디스크 풀(ADR-0005 point 6이 봉쇄한 바로 그 fail 모드)로 되돌아간다. 더 강한 보증처럼 보이나, 무인 24/7에서는 오히려 안전사고를 재도입한다. 로컬 fsync를 ack로 삼고 원격 배송을 분리(재시도·모니터링)하는 편이 유계와 무손실을 양립시킨다.
- **"durable ack" = 메모리 버퍼 적재(async flush)** — 기각: 크래시로 자금행위 기록이 소실된다(관측성 불변식 위반). ADR-0005 point 4의 "내구성 완화 금지"와 동일 정신 — 도장을 찍었다고 믿는데 안 남는 fail-open.
- **prune-gate 후보 (b): terminal 감사 내용을 SQLite에 fallback 보존** — 기각: 감사 히스토리를 트랜잭션 저장소로 밀어넣어 ADR-0005 point 5(라이브 상태 store를 히스토리로 부풀리지 않음)를 위반한다. (a)는 boolean/timestamp 플래그만 store에 두어 내용은 sink에 남긴다.
- **exactly-once 전달** — 기각: 분산 원자 커밋(감사 write ↔ 소비 확인)을 요구하는데 로컬 파일·단일 프로세스에서 실질 불가·과설계다. at-least-once + 결정적 멱등키가 같은 안전성을 훨씬 싸게 준다.
- **체결 멱등키 = per-fill 식별자** — 기각(**원천 불가능, 실측**): `openapi.json` jq 검증 결과 `OrderExecution`은 orderId당 누적 집계 스냅샷일 뿐 per-fill id·이벤트 목록이 없고, `/api/v1/trades`는 시장 틱이지 계좌 체결이 아니다. `orderId + 재무필드 digest`로 스냅샷 버전화한다.
- **체결 멱등키 = `orderId + 누적 filledQuantity`만(수량 증가 시에만 방출)** — 기각(codex adversarial-review 지적): 동일 수량에서 averageFilledPrice·filledAmount·commission·tax·settlementDate가 정정되면 그 스냅샷이 중복으로 억제돼 감사에서 **재무 변화가 소실**된다(재구성·수수료·세금·결제 진단 누락). 재무필드 digest로 키를 잡고 필드 변화 시마다 방출해 정정을 보존한다.
- **에러 발생시퀀스를 별도 durable 카운터로 할당** — 기각(codex adversarial-review 지적): 카운터 전진과 레코드 durability가 분리되면 그 사이 크래시가 seq를 건너뛰거나 같은 에러를 **다른 키로 재방출**한다 — 에러는 재구성 불가라 중복·유실이 가장 치명적인 부류다. seq를 durable append 위치에서 파생해 **할당=append를 원자화**하고, torn tail은 재시작 시 폐기해 정합을 지킨다.
- **에러를 재시작 시 재구성(re-emit)으로 복구** — 기각: 에러는 journal intent가 아니고 API SoT에도 없어 **재구성 자체가 불가능**하다. at-least-once re-emit 모델을 태울 수 없으므로 발생 시점 durable write가 유일한 보증이다.
- **감사 sink가 `runtime.NewLogger`(slog/stdout)를 durability 경로로 재사용** — 기각: stdout은 durable ack를 줄 수 없고 회전·유실 통제가 없다. slog는 운영 진단(best-effort)으로 유지하고, 감사는 fsync·ack·재시도·멱등을 지는 별도 경로로 둔다(역할 분리).
- **감사 실패를 무시하고 주문 계속(fail-open)** — 기각: 기록 없이 비가역 자금행위를 실행하게 되어 관측성 불변식·무인 안전 1순위를 정면으로 위반한다. fail-closed로 killswitch 승격.
- **fail-closed halt를 감사 sink에 기록해 트립** — 기각: "감사 불가 → halt"가 다시 감사 write를 요구하면 데드락이다. halt 트립은 store 라이브 상태(ADR-0004/0005)에 써서 감사 경로와 분리한다.
- **주문 제출 경로에 원격 배송을 동기 결합** — 기각: 원격 sink 지연·다운이 주문 루프를 막아 "죽지 않는다" 불변식과 충돌한다. 배송은 async best-effort로 분리하고 로컬 fsync만 주문 경로에 동기로 둔다.

## Consequences

- (좋음) "모든 주문/체결/에러" 관측성 불변식이 sink의 발생-시점 fsync 경로로 **전 스트림에서** 성립한다 — 재구성 불가한 에러·관측-시점에 묶인 체결까지 크래시를 견딘다.
- (좋음) prune이 **로컬 fsync ack**에 게이팅되어 유계(디스크 풀 방지)와 무손실(자금행위 감사 보존)이 양립한다. 원격 수집기 상태가 journal 성장·주문 루프에 영향을 주지 않는다.
- (좋음) ADR-0005 point 6의 선행 조건(감사 내구성·재시도·멱등·durable-ack 계약)이 확정되어, **terminal prune을 안전하게 활성화**할 수 있다.
- (좋음) prune-gate가 store에 boolean/timestamp 플래그만 두어(내용은 sink) ADR-0005 point 5의 "store를 히스토리로 부풀리지 않음"을 지킨다. store journal이 주문 수명주기 감사의 durable outbox, ack 플래그가 그 위 prune-gate 층으로 깔끔히 층화된다.
- (좋음) 결정적 멱등키(부류별 합성)로 at-least-once 재전송·재시작 re-emit이 안전하게 병합된다 — 체결의 per-fill id 부재를 **재무필드 digest로 스냅샷 버전화**(동일 수량 정정도 보존)하고, 에러 seq를 **durable append 위치에서 파생**해 할당=append를 원자화(카운터 desync·다른-키 재방출 봉쇄)한다.
- (비용) POST/폴링 경로에 감사 fsync가 동기로 들어간다(주문 이벤트는 store 마커 fsync에 더해). 폴링 기반이라 수용하나, "하나의 사건이 store 마커 + 감사 + (필요 시) halt를 건드릴 때"의 경계를 구현이 정확히 지켜야 한다.
- (비용) 이중 관리: 라이브 상태(store) · 감사 히스토리(로컬 durable + 원격 배송) · 운영 진단(slog). 세 경로의 역할 경계를 구현·리뷰가 유지해야 한다.
- (비용) 로컬 감사 파일이 원격 배송 지연 시 성장한다 → 로컬 감사 디스크도 모니터링·알림 대상(별도 라이브 상태 store 유계와 무관하게).
- (제약 전파)
  - **reconciler(ADR-0003)**는 재시작 시 ack 미표시 terminal intent를 감사 sink로 re-emit하고, 감사 ack가 선 뒤에만 prune-eligible로 넘긴다.
  - **killswitch(ADR-0004)**는 감사 durable write 실패를 트립 조건으로 받되, 트립 경로가 감사 write에 의존하지 않도록(store 라이브 상태에 트립) 배선한다.
  - **store(ADR-0005)**는 terminal intent 행에 감사 ack 플래그/타임스탬프 컬럼을 갖고, prune 쿼리가 그 플래그를 조건에 포함한다.
  - **order/전략(ADR-0002)**은 감사 이벤트 방출 지점(marker 전이·orderId 확보·에러)에서 audit 인터페이스를 호출한다.
- (후속)
  - **`internal/audit` 구현 이슈** — 레코드 스키마·직렬화·로컬 durable writer(fsync·회전)·멱등키 도출·ack 신호·인터페이스 형태. **에러 seq는 durable append 위치에서 파생하고 부분쓰기(torn tail)는 재시작 시 폐기, 체결은 재무필드 digest로 스냅샷 버전화** — 이 두 크래시-복구 프로토콜을 반드시 단위 테스트로 커버(회전이 append 위치 파생 seq의 단조성을 깨지 않는지 포함). 실제 writer 크래시/부분쓰기/재개 테스트(temp dir, `go test -race`).
  - **감사 ack ↔ store 플래그 배선 이슈** — terminal화 → 감사 fsync → store 트랜잭션 ack 기록 → prune-eligible의 원자성·복구(re-emit) 경계. "ack 없으면 보존" 단위 테스트.
  - **원격 배송 파이프라인 이슈** — async 백오프 재시도·미배송분 보존·로컬 파일 회전 안전·로컬 감사 디스크 모니터링. (대상 sink·프로토콜 미정.)
  - **감사 실패 → killswitch 트리거 배선 이슈** — 순환 방지(store 경로 트립) 포함. ADR-0004·0005 point 6 디스크 풀 트리거와 통합.
  - **운영 알림 채널 결정** — 로컬 감사 디스크 임계·원격 배송 장기 실패·killswitch 트립의 외부 알림 대상(CLAUDE.md "치명적 상황은 외부 알림(미정)").
