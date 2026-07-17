---
id: "0005"
status: Accepted
date: 2026-07-02
deciders: [chnu-kim]
domain: [persistence]
protects: []
supersedes: []
superseded_by: null
verification: []
---

# ADR-0005: 영속 저장소는 도메인 인지형 단일 트랜잭션 store(임베디드 SQLite / 순수 Go)이고, 사건 단위 fsync commit·라이브 상태만 보존한다

- **Status**: Accepted
- **Date**: 2026-07-02
- **Deciders**: chnu-kim
- **관련 이슈/PR**: (TBD)

## Context

이 봇은 24시간 무인 실행되고 주문 제출은 **비가역(돈이 움직임)**이다. 앞선 ADR들이 영속 계층에 강한 제약을 이미 박아뒀고, 그중 **저장소 확정을 명시적으로 이 ADR에 위임**했다(ADR-0004: *"영속 저장소 결정 ADR — SQLite/Postgres 확정 및 ADR-0002의 'append-only 파일 + fsync' 표현 조정"*).

이 ADR은 답한다: **journal(outbox)·전역 halt·재구성 불가 임계 카운터를 무엇이·어떤 트랜잭션 경계로·어떤 내구성으로 영속하고, 그 저장소는 어떤 패키지 경계로 노출되며, 24/7에서 어떻게 유계로 유지되는가.**

앞선 ADR이 고정한 힘(재-grilling 안 함, 이 결정의 전제):

- **단일 트랜잭션 저장소 1개(ADR-0004).** journal + 전역 halt + 재구성 불가 임계 카운터를 **한 저장소**에 담고, "주문 실패 기록"과 "halt trip"처럼 원자적이어야 하는 상태 변경을 **한 트랜잭션**으로 묶는다. **두 번째 영속 계층을 발명하지 않는다.**
- **단일 프로세스(ADR-0001).** 토큰 1개 제약이 토큰 발급을 단일 프로세스로 확정했다 → 동시 writer·분산 조율은 범위 밖. 로컬 임베디드 저장소로 충분하다.
- **write-ahead journal + 2-마커(ADR-0002).** POST 전에 `(intentId, clientOrderId, payload)`를 durable append하고, `prepared` → `submit-attempted` → `acked` 마커로 crash window를 닫는다. journal은 SoT가 아니라 outbox다. ADR-0002는 substrate를 *"append-only 파일 + fsync"*로 추상적으로만 적었고, 그 확정을 이 ADR에 남겼다.
- **단일 reconciler·재시작 복구(ADR-0003).** 재시작 시 journal의 **모든 미해결 intent를 스캔**해 orderId 상세조회로 진실을 재구성한다. 저장소는 이 스캔이 성립하도록 미해결 intent를 잃지 않아야 한다.
- **CLAUDE.md 무인 불변식.** 죽지 않는다 · 재시작 안전 · 로컬 상태 불신(SoT는 API) · 조회는 백오프 재시도 OK, 주문 재전송은 자동 금지 · **관측성(모든 주문/체결/에러 영속 기록)**.

핵심 긴장 세 가지가 결정을 가른다:

1. **원자성 vs crash window.** 2-마커 각 전이는 그 자체로 durable해야 crash 복구가 성립하는데(마커를 뭉치면 ADR-0002가 무너짐), 동시에 "마커 기록 + halt trip"은 원자여야 어긋남(주문은 실패 기록됐는데 halt는 안 켜진)이 안 생긴다(ADR-0004). 이 둘을 동시에 만족시키는 트랜잭션 경계를 정의해야 한다.
2. **내구성의 은닉된 fail-open.** 저장소 엔진의 "빠르게 vs 확실하게(fsync)" 스위치를 미래에 누가 성능을 이유로 끄면 2-마커 crash-safety가 조용히 무효화된다.
3. **무인 24/7의 무한 성장.** journal이 무한정 커지면 디스크가 차고, 그러면 `submit-attempted`를 append하지 못해 새 ambiguous submit(비가역 위험)을 만든다. 성장을 제어하되 reconciler가 재시작에 필요한 상태는 절대 잃으면 안 된다.

## Decision

1. **엔진 = 임베디드 SQLite를 순수 Go 드라이버(`modernc.org/sqlite`)로 쓴다. 관계형(SQL) 모델 + 스키마 마이그레이션.** 부하는 저빈도 폴링 + 아웃박스 몇 줄 + 상태 몇 줄이라 KV로도 충분하지만, **관계형 모델을 택해 미래 이식성을 산다** — store 내부를 SQL로 짜두면 Postgres/MySQL 전환이 "드라이버 + 방언 교체"이지 저장소 계층 전면 재작성이 아니다(KV였다면 재작성). **CGO를 쓰지 않는다**: `CGO_ENABLED=0` 순수 Go로 정적 바이너리·크로스컴파일·distroless/scratch 이미지가 공짜가 되어 무인 배포·OSS 재현성이 단순해진다. 이 워크로드에서 CGO SQLite의 성능·성숙도 우위는 실질 무의미하다. **서버 RDB(Postgres/MySQL)는 지금 세우지 않는다** — ADR-0001이 단일 프로세스를 확정해 스케일아웃 실익이 없고, store 인터페이스가 안정적이라 실제 필요가 생길 때 미룰 수 있다.

2. **도메인 인지형 `internal/store` leaf 패키지가 엔진·스키마·마이그레이션·영속 레코드 타입을 소유한다.** store는 `Intent`/`Marker`/`HaltState`/`Counter` 같은 **영속 레코드 타입**과 의미-드러내는 타입 메서드(`AppendMarker`, `TripHalt`, `LoadUnresolvedIntents` 등)를 노출하고, 원자적 다중 쓰기는 `Atomically(func(tx) error)` 안에서 여러 메서드를 호출해 달성한다. **의존 방향**: order/killswitch/reconciler가 store를 import하고, **store는 어떤 도메인 로직 패키지도 import하지 않는다**(순환 금지 — ADR-0004에서 killswitch도 leaf). "무엇을 어떻게 영속하나"가 한 곳에 모여, stateless 구현자·검수자가 스키마를 한 파일에서 읽는다.

   **타입 소유권**: store가 소유하는 레코드 타입은 **영속 DTO**다 — 저장 스키마의 형태일 뿐, 도메인 객체의 canonical 소유권이 아니다. **행동 소유권은 도메인에 남는다**: intent 고유성·의미론은 전략/order(ADR-0002), halt 판단·임계 로직은 killswitch(ADR-0004)가 진다. store는 그 상태를 durable하게 쓰고 읽는 substrate이지, "언제 halt할지"나 "무엇이 같은 intent인지"를 결정하지 않는다. (도메인이 자기 인메모리 개념을 store DTO로 매핑한다.)

   **테스트 seam**: 소비자는 store가 만족하는 **인터페이스에 의존**해 가짜 구현으로 단위 테스트한다. 그러나 **store 자신의 크래시·내구성·부분쓰기·원자성 테스트는 임시 디렉터리에 실제 엔진**을 띄워 돌린다 — 인메모리 가짜는 fsync·부분쓰기·트랜잭션 경계를 검증하지 못하기 때문이다(CLAUDE.md 테스트 방침).

3. **트랜잭션 경계 = "하나의 관측 가능한 사건".** 2-마커 진행은 사건이 여러 개이므로 **각 전이가 독립 durable commit**이다(`prepared`, `submit-attempted`, `acked`를 한 트랜잭션에 뭉치지 않는다 — 뭉치면 마커 사이 크래시가 복구 불가가 되어 ADR-0002의 존재 이유가 무너진다). **단, 한 논리적 사건이 journal 상태 변경과 halt/카운터 상태 변경을 동시에 유발하면(예: ambiguous submit 기록 → 종목 차단/전역 halt), 그 둘은 반드시 단일 트랜잭션으로 원자화**한다 — 같이 살거나 같이 죽는다. 이 계약이 "저장소 1개"를 강제하는 이유다(별도 저장소면 두 write가 원자일 수 없다).
   > **Amend (ADR-0012)**: **order-failure 카운터의 journal-결합에 한해** 이 point의 '단일 트랜잭션 원자화'를 ADR-0012가 count-before-resolve ordering + reconciler re-count로 대체한다 — killswitch가 카운터++를 자기 트랜잭션으로 `ResolveIntent(rejected)`보다 **먼저** durable commit하고, 카운터 persist + 재시작 재-count가 같은 어긋남('실패 기록됐는데 halt 안 켜짐')을 원자 tx 없이 닫는다(killswitch 미러의 durable-before-visible 순서와 정합하기 위해 seam을 killswitch 소유로 뒤집은 결과). ambiguous 종목차단·audit ack 등 **다른 결합엔 이 point 그대로 적용**한다. 상세·근거는 ADR-0012 Decision point 2·3·5.

4. **내구성은 마커 commit마다 물리적 보장(fsync-on-commit)이고, 이를 끄는 것을 금지한다.** SQLite `synchronous=FULL` + WAL(또는 이와 동등한 commit 시 물리 내구성) 설정을 고정한다. **성능을 위해 내구성을 낮추는 설정(`synchronous=OFF`/`NORMAL`로의 완화, 비동기 flush 등)은 금지** — 명시적으로 이 ADR에 적어 미래의 "최적화"가 2-마커 crash-safety를 조용히 무효화하지 못하게 한다. 이 봇은 초당 수백 주문이 아니라 폴링 기반이라 commit 지연은 수용 가능하다.

5. **저장소는 "라이브 행동 가능 상태"만 담는다. 관측성/감사 로그는 이 저장소가 아니다.** CLAUDE.md "모든 주문/체결/에러 영속 기록"(사후 진단용 히스토리)은 **구조화 로깅(stdout/파일/외부 sink)**으로 별도로 간다. 역할 분담: **저장소** = "재시작 때 뭘 해야 하나 / 지금 halt인가"에 답하는 라이브 상태(미해결 intent + 현재 halt + 영속 카운터). **감사 로그** = "무슨 일이 있었나"에 답하는 사람용 히스토리. 둘을 섞으면 트랜잭션 저장소가 히스토리로 부풀고, 감사 볼륨이 commit 내구성 비용에 묶이며, 라이브 상태와 지나간 기록이 뒤섞인다.

6. **보존(retention): terminal만, 보존창 경과 후 prune. reconciler가 필요한 상태는 절대 prune하지 않는다.** 저장소 크기를 **동시 in-flight 주문 수 + 상태 몇 줄**로 유계로 유지한다.
   - **절대 prune 안 함**: 비-terminal intent(`prepared`, 미해결 `submit-attempted`, `unresolved-ambiguous`), 현재 halt 상태, 영속 카운터.
   - **prune 대상**: terminal로 닫힌 intent(`aborted-before-submit`, 종결 주문)만, **보존창 경과 후 + 감사 내구성 ack 확인 후**(아래 prune-gating).
   - **prune-gating(감사 내구성 계약)**: terminal 행은 **감사 sink가 대응 주문/체결/에러 기록을 durable하게 ack하기 전까지 prune하지 않는다.** "결과가 감사 로그로 나갔다"는 단정만으로 지우지 않는다 — 감사 sink는 point 5에서 저장소 밖 별도 계층이라, 로그 write 실패·회전 유실·수집기 다운·terminal화 직후 durable 영속 전 크래시 시 그 기록이 **비가역 자금행위의 유일한 사본**일 수 있다(CLAUDE.md "모든 주문/체결/에러 영속 기록" 불변식). 따라서 **ack를 못 받으면 보존 쪽으로 fail한다(삭제 아님)** — fail-safe 방향이 "잃음"이 아니라 "남김"이다. ack 추적은 둘 중 하나로 구현한다: (a) 감사 sink의 durable ack를 store에 트랜잭션으로 기록하고 그 플래그가 선 행만 prune 대상, 또는 (b) sink가 멱등·재시도 가능하게 ingest를 확인하기 전까지 **최소 terminal 감사 레코드를 SQLite에 fallback 보존**. 감사 sink의 내구성·재시도·멱등 계약 자체는 **prune을 활성화하기 전에** 정의한다(감사 sink 설계 후속 이슈의 선행 조건).
   - 보존창 길이·prune 주기·ack 추적 방식은 구현 이슈(파라미터, 단위 테스트로 경계 커버). 이 ADR은 규칙("terminal만, 창 경과 후, **감사 ack 후**, reconciler 필요분 불가, ack 부재 시 보존")만 고정한다.
   - **durable append 실패(디스크 풀 포함)는 fail-closed 취급** — 기록하지 못하면 안전하게 제출할 수 없다. 트리거로서의 처리(전역 halt 에스컬레이션)는 killswitch(ADR-0004) 영역이며 이 ADR은 조건만 링크한다.

7. **ADR-0002와의 관계 = substrate-only supersede.** 이 ADR은 ADR-0002가 추상적으로 남긴 substrate(*"append-only 파일 + fsync"*)를 **구체화**한다. ADR-0002의 실제 결정(2-마커 모델, orderId 1차 키, clientOrderId deterministic 도출)은 바뀌지 않는다 — 그것이 얹히는 저장 매체만 SQLite 트랜잭션 store로 확정된다. 따라서 ADR-0002를 전체 `Superseded`로 표시하지 않는다. ADR-0002의 "append-only 파일 + fsync" 표현은 **substrate 한정으로만** 이 ADR에 의해 대체된다(내구성 계약 point 4가 그 fsync 의도를 그대로 이어받는다).

## Alternatives considered

- **생짜 append-only 파일 journal + 별도 상태 파일** — 기각: "주문 실패 기록 + halt trip"을 한 트랜잭션으로 묶을 수 없다(ADR-0004의 원자성 요구 위반). 두 파일 사이 크래시가 곧 어긋남이다.
- **halt/카운터용 두 번째 영속 계층 신설** — 기각(ADR-0004 재확인): 단일 저장소를 재사용해 journal 기록과 halt 기록을 한 트랜잭션 경계에 묶는다. 중복 상태는 어긋남만 만든다.
- **bbolt 등 임베디드 KV 엔진** — 기각: 순수 Go·ACID·단일 writer로 이 워크로드에 잘 맞지만, **KV → Postgres/MySQL 전환이 저장소 계층 전면 재작성**이 된다. 사용자가 성숙한 SQL 생태계·트랜잭션 모델·미래 이식성을 우선했고, 관계형 모델이 그 이식성을 산다. (도메인 인지형 store가 엔진을 숨기므로 소비자 코드는 어느 쪽이든 불변이지만, store *내부* 재작성 비용이 SQL 선택으로 최소화된다.)
- **지금부터 서버 RDB(Postgres/MySQL)** — 기각: ADR-0001이 토큰 1개 제약으로 단일 프로세스를 확정해 스케일아웃 실익이 없다. 무인 단일 프로세스 봇에 서버 RDB의 운영 부담(별도 프로세스·네트워크·백업 인프라)은 과설계다. store 인터페이스가 안정적이라 실제 필요(운영 툴링·프로세스 밖 저장소)가 생길 때로 미룬다.
- **CGO SQLite(`mattn/go-sqlite3`)** — 기각: 크로스컴파일·정적 링크·C 툴체인 결합으로 무인 배포·OSS 재현성이 복잡해진다. Docker 빌드=런타임이면 실현 가능하나, 이 워크로드는 CGO의 성능·충실도 우위가 무의미해 순수 Go의 배포 단순성이 순이익이다.
- **제네릭 트랜잭션 엔진 store(도메인 모름, 생짜 Put/Get/Range)** — 기각: 직렬화·키 규약이 도메인마다 흩어지고, 원자적 다중 쓰기가 "생짜 tx를 두 도메인이 만짐"이라는 규율에 의존한다. 도메인 인지형이 스키마를 한 곳에 모으고 원자성을 의도-드러내는 메서드로 표현한다.
- **2-마커를 한 트랜잭션에 뭉치기** — 기각: 마커 사이 크래시가 복구 불가가 되어 "POST 도중 크래시"를 구분하는 ADR-0002가 무너진다. 각 마커는 독립 commit이어야 한다.
- **성능을 위해 내구성 완화(`synchronous=OFF`, 비동기 flush)** — 기각: 도장을 찍었다고 믿는데 안 남아 2-마커 crash-safety가 조용히 무효화된다(fail-open). 폴링 기반이라 완화할 이유도 없다.
- **감사 로그를 트랜잭션 저장소에 함께 적재** — 기각: 라이브 상태 저장소를 히스토리로 부풀리고, 감사 볼륨을 commit 내구성 비용에 묶는다. 관측성은 구조화 로깅으로 분리한다.
- **journal을 영원히 보존(prune 안 함)** — 기각: 무인 24/7에서 무한 성장 → 디스크 풀 → `submit-attempted` append 불가 → 새 ambiguous submit. 성장은 안전 문제다.
- **비-terminal까지 prune(단순 시간 기준 일괄 삭제)** — 기각: reconciler가 재시작에 필요한 미해결 intent·halt·카운터를 지우면 복구가 깨진다. prune은 terminal에만, 보존창 뒤에만.
- **"결과가 감사 로그로 나갔다" 단정만으로 terminal prune(감사 ack 게이팅 없음)** — 기각(codex adversarial-review 지적): 감사 sink는 저장소 밖 별도 계층(point 5)이라, 로그 write 실패·회전 유실·수집기 다운·terminal화 직후 크래시 시 그 기록이 비가역 자금행위의 유일한 사본일 수 있다. reconciler가 더는 필요로 하지 않는 terminal 행을 prune이 지우면 감사 이력이 영구 소실된다(CLAUDE.md 관측성 불변식 위반). prune을 감사 durable ack에 게이팅하고, ack 부재 시 보존 쪽으로 fail한다.

## Consequences

- (좋음) "저장소 1개 + 사건 단위 트랜잭션" 계약이 확정되어, journal 마커와 halt/카운터의 원자성 요구(ADR-0004)와 마커별 crash-safety(ADR-0002)가 한 모델에서 동시에 성립한다.
- (좋음) 도메인 인지형 store에 스키마·마이그레이션·직렬화가 한 곳에 모여, stateless 구현자·검수자가 "무엇을 어떻게 영속하나"를 한 파일에서 읽는다. 소비자는 인터페이스에 의존해 가짜로 테스트하고, 내구성·크래시 검증은 실제 엔진(temp dir)으로 돌린다.
- (좋음) 순수 Go SQLite로 `CGO_ENABLED=0` 정적 바이너리·크로스컴파일·distroless 이미지가 공짜다 → 무인 배포·OSS 재현성이 단순하다.
- (좋음) SQL 모델·안정적 store 인터페이스 덕에 미래 Postgres/MySQL 전환이 드라이버+방언 교체로 축소된다(전면 재작성 아님).
- (좋음) 저장소가 라이브 상태만 담고 terminal을 보존창 뒤 prune하므로 크기가 in-flight 주문 수로 유계다 → 디스크 풀발 안전사고를 구조적으로 억제한다. **prune이 감사 durable ack에 게이팅되어(ack 부재 시 보존) 자금행위 감사 이력을 잃지 않는다** — 유계와 무손실이 양립한다.
- (좋음) 내구성 완화 금지를 ADR에 명시해, 미래 "최적화"가 2-마커 crash-safety를 조용히 무효화하는 fail-open을 봉쇄한다.
- (비용) POST 경로에 마커별 fsync commit이 다회(`prepared`/`submit-attempted`/`acked`) 들어간다. 폴링 기반이라 수용하지만, "하나의 사건이 journal + halt를 동시에 건드릴 때만 원자 결합"이라는 경계를 구현이 정확히 지켜야 한다.
- (비용) 도메인 인지형 store는 영속 레코드 타입을 아는 "살짝 뚱뚱한 leaf"다. 스키마 변경은 마이그레이션을 동반한다.
- (비용) 관측성 로그가 저장소 밖 별도 sink이므로, "감사 히스토리"와 "라이브 상태"의 이중 관리가 생긴다(단, 결합보다 싸다고 판단).
- (제약 전파) reconciler·killswitch·order는 store 인터페이스를 통해서만 영속에 접근하고, 원자적 다중 쓰기는 `Atomically` 트랜잭션 경계를 공유한다.
- (후속)
  - **`internal/store` 구현 이슈** — 스키마·마이그레이션 도구·인터페이스 형태(`Tx` 추상·타입 메서드 시그니처)·실제 엔진 크래시/원자성 테스트(temp dir, `go test -race`).
  - **단일 writer 직렬화** — SQLite는 단일 writer인데 ADR-0001은 다수 goroutine이 돈다. `Atomically`는 쓰기를 직렬화(예: 전용 write 커넥션 1개)해야 order 경로에서 `SQLITE_BUSY`발 허위 fail-closed가 나지 않는다. 이 성질을 구현이 보장하고 `-race`로 검증한다.
  - **보존창 길이·prune 주기 파라미터** — 단위 테스트로 경계 커버. "미해결 intent는 절대 prune 안 됨"을 반드시 테스트.
  - **감사/관측성 로그 sink 설계** — 구조화 로깅 포맷·대상·회전(rotation). 저장소와의 경계 유지. **내구성·재시도·멱등·ack 계약을 정의하고, 이 계약이 서기 전까지 terminal prune을 활성화하지 않는다**(point 6 prune-gating의 선행 조건).
  - **디스크 풀 → 전역 halt 트리거 배선** — durable append 실패를 killswitch(ADR-0004) 트리거로 연결.
  - **이벤트 + 아웃박스 패턴 도입 검토** — journal이 이미 outbox이므로, 미래에 이벤트 발행/외부 소비를 붙일 때 이 저장소 위에서 확장 검토.
  - **미래 Postgres/MySQL 전환 ADR** — 프로세스 밖 저장소가 실제 필요해질 때(운영 툴링·백업 인프라 요구) 드라이버+방언 전환을 별도 ADR로.
