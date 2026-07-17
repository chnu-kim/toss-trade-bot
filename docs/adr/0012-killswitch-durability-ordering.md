---
id: "0012"
status: Proposed
date: 2026-07-17
deciders: [chnu-kim]
domain: [killswitch, order, persistence]
protects: [live-execution-human-gate]
supersedes: []
superseded_by: null
verification: []
---

# ADR-0012: 킬 스위치 상태는 durable-before-visible이다 — 미러는 durable commit 뒤에만 halt를 노출하고, order-failure 카운트는 count-before-resolve로 크래시-세이프하다

- **Status**: Proposed
- **Date**: 2026-07-17
- **Deciders**: chnu-kim
- **관련 이슈/PR**: #32 (killswitch 구현), #57 (재설계 시도)

## Context

ADR-0004는 킬 스위치를 fail-closed 제출 가드로 확정하며 두 계층을 세웠다: **저장소 = 영속 진실, 메모리 = 값싼 읽기 미러**(point 5). `CanSubmit`은 모든 주문마다 도는 뜨거운 경로이므로 매번 저장소를 때리지 않고 in-process 미러만 읽는다 — 즉 **미러가 `CanSubmit`의 유일한 입력이자 fail-open의 유일한 표면**이다. 그러나 ADR-0004 point 5는 "trip/clear 시 저장소에 쓰고 미러를 갱신한다"고만 적고 **미러 갱신과 durable write의 순서를 명시하지 않았다.** 이 ADR은 그 빈칸을 채운다.

구현(#32)과 재설계 시도(#57)는 그 빈칸을 **mirror-first optimistic**으로 채웠다: `Trip`이 미러 halt(`applyTrip`으로 `g.halted`/`haltGen`)를 먼저 세우고 durable `TripHalt`+halt-epoch를 나중 트랜잭션으로 쓴다. 이 순서는 구조적 크래시 창을 만든다 — 두 커밋 사이에 프로세스가 죽으면 미러는 halted였는데 durable은 롤백되어, 이미 authorize된 저장소에서 재기동 시 `halt=0`을 읽고 **unhalted로 부팅**한다(ADR-0004 point 4가 막으려던 "재시작 = 안전장치 우회"의 재발). `recoveryFailed` 같은 in-memory 보정 플래그는 프로세스가 살아 있을 때의 persist 실패만 잡고 크래시는 구조적으로 못 잡는다.

이 결함 계열은 우연이 아니다. 이 코어에 대한 적대적 리뷰가 **6라운드 연속** 동형의 durability critical/high를 냈다(mirror-first crash-window, TripTx 롤백 skew, persist 실패 재시작 fail-open, reset crash-window, recoveryFailed 무시, Trip mirror-first crash-window). 동종 결함의 반복은 개별 창을 하나씩 막는 방식이 수렴하지 않는다는 신호다 — 근본 원인은 "optimistic 미러 상태가 durable과 갈라지는 경로가 여러 곳"이라는 설계 선택 자체다.

이 결정을 가르는 힘:

- **미러가 fail-open의 유일 표면(ADR-0004 point 5).** 재시작 안전("미러가 halted면 재시작도 halted")은 미러가 durable보다 **앞서지 않을** 때만 크래시 타이밍과 무관하게 성립한다.
- **원자 결합 계약(ADR-0005 point 3).** 한 논리적 사건이 journal 상태 변경과 halt/카운터 상태 변경을 동시에 유발하면 그 둘은 단일 트랜잭션으로 원자화한다("같이 살거나 같이 죽는다"). ADR-0004는 이 어긋남("주문은 실패 기록됐는데 halt는 안 켜진")을 명시적으로 기각했다.
- **재시작 비대칭(ADR-0004 point 7).** journal에서 재구성 가능한 신호(ambiguous 빈도)는 기동 시 재계산하고, 재구성 불가한 신호(토큰 갱신 실패)는 카운터를 persist하며, 복구 실패는 fail-closed다.
- **신호별 성질의 비대칭(적대적 검토에서 드러남).** 세 에스컬레이션 신호를 (persist × journal-결합) 두 축으로 분해하면 **order 연속 실패만** 두 성질을 동시에 가진다 — persist(재구성 불가, journal 재계산 경로 없음)이면서 journal-결합(주문 실패는 intent를 `resolved-failed`로 마감하는 journal write를 동반). ambiguous는 journal 재계산으로, 토큰 갱신 실패는 journal 대응물 부재로 각각 원자 결합이 불필요하지만, order-failure는 그렇지 않다.
- **store 무수정 원칙(#32 제약).** killswitch는 기존 `store.Atomically`/`Tx`/`Counter` seam만 쓰고 store 패키지를 수정하지 않는다.
- **단일 프로세스·단일 write 커넥션(ADR-0001·0005).** `store.Atomically`는 전용 write 커넥션 하나로 직렬화되므로, 한 시점에 진행 중인 write 트랜잭션은 하나뿐이다.

## Decision

**1. 킬 스위치 상태는 durable-before-visible이다 — 미러는 durable commit이 성공한 뒤에만 halt를 노출한다.**

`CanSubmit`이 읽는 미러(`g.halted`/`haltGen`)의 halt 노출을, 그 halt를 뒷받침하는 durable write(`TripHalt`+halt-epoch bump, 또는 clear의 `ClearHalt`)의 commit **성공 이후로** 미룬다. 이로써 "미러가 halted면 durable도 halted"가 크래시 타이밍과 무관하게 성립한다 — durable이 항상 미러보다 앞서므로, 두 상태가 갈라지는 크래시 창이 구조적으로 존재하지 않는다. reset도 대칭이다: `ClearHalt` commit 성공 후에만 미러를 unhalt하므로, clear commit 전 크래시는 halt를 유지한다(fail-closed).

durable commit이 **진행 중인 창**(전역 trip이 in-flight)에서는 미러가 아직 halt를 노출하지 않지만, 이 창에서 `CanSubmit`을 **fail-closed(pending)**로 둔다 — 전역 trip이 시작되면 그 즉시 신규 노출을 막고, durable commit 성공 시 pending을 정식 halt로 전환한다. 이는 ADR-0004 point 3("상태 전이 중/불명 → blocked")의 자연스러운 확장이며, "보호가 durable commit까지 늦어진다"는 우려를 뒤집는다 — durable 확정 전에는 오히려 보수적으로 blocked다.

**durable write가 크래시가 아니라 에러로 실패하는 arm(SQLite 락·디스크 등)도 fail-closed다.** durable commit이 에러를 반환하면 미러를 unhalted로 되돌리지 않고 pending/halted(blocked)로 유지한다 — 저장소가 다운된 순간에 미러가 unhalted로 남아 fail-open되는 것을 막는다. durable write 실패는 "증거 없음"이 아니라 blocked로 취급한다(ADR-0004 point 3·7의 fail-closed 철학).

**단 이 arm의 재시작 안전은 별도로 보장해야 한다.** durable write 에러 자체는 store 손상/다운을 뜻하므로 halt/카운터가 아무 durable 표면에도 안 써진다. 그 뒤 프로세스가 죽으면 in-memory pending은 휘발하고, 재구성 불가 trigger(토큰 갱신 실패)는 journal에서 재계산되지 않으므로 — 미러 pending만으로는 재시작 unhalted 부팅이 가능하다(ADR-0004 point 7이 "persist 또는 fail-closed"라 한 클래스의 재시작 fail-open). 이 재시작 안전은 store 손상 구간의 물리적 성질에서 세 겹으로 나온다:

- **(a) store-down은 신규 노출 제출도 스스로 fail-closed시킨다.** durable write가 실패하는 store 상태에서는 신규 노출의 write-ahead journal 기록(ADR-0002/0003의 `prepared` 마커) 역시 실패한다. 제출은 `prepared` 마커 durable 기록에 성공해야 진행되므로(ADR-0002), store-down 구간 동안 신규 노출 제출은 killswitch halt와 무관하게 durable journal write 실패로 막힌다. 즉 durable 보장이 원리적으로 불가능한 바로 그 구간이 제출 경로가 스스로 fail-closed되는 구간과 일치한다.
- **(b) store 복구 후 재시작 시 재구성 가능 신호는 복구된다.** ambiguous 빈도는 journal 재계산으로, order 연속 실패는 reconciler re-count로 재-trip된다(Decision point 3). 토큰 갱신 실패는 조건이 지속되면 재기동 후 토큰 재발급 재시도에서 재발해 재-trip된다(조건 재발생이 journal 재계산을 대신한다).
- **(c) 유일한 잔여 창은 위험 소멸 창이다.** store 복구 AND 그 trigger 조건 해소(예: 토큰 갱신이 다시 성공) 사이에만 non-reconstructable halt 증거가 유실될 수 있는데, 이는 halt 사유가 이미 사라진 창이므로 fail-open이 실질 위험(위험이 살아 있는데 halt 못 함)을 노출하지 않는다.

이 세 겹이 없는 해석 — 예컨대 durable write 에러를 in-memory pending으로만 처리하고 store-down↔제출 fail-closed의 결합을 근거로 삼지 않는 구현 — 은 point 7의 재시작 fail-open을 남기므로 금지다.

**2. `TripTx`(발신처 트랜잭션 참여 seam)를 제거하고, 킬 스위치가 모든 durable halt write에 자기 `store.Atomically`를 소유한다.** 발신처(order·reconciler·토큰 매니저)는 위험 신호를 `report`할 뿐, halt durable write를 자기 트랜잭션에 원자 결합하지 않는다. killswitch가 commit 시점을 소유하므로 point 1(durable commit 후 미러 노출)이 자명해지고 — post-commit hook이나 2-phase confirm 계약이 불필요하며 — store seam을 손대지 않는다. 원자 결합(ADR-0005 point 3)이 요구하던 "halt ↔ journal" 정합성은 아래 신호별로 다른 메커니즘이 대체한다:

- **ambiguous 빈발 → 전역 halt**: 종목 trip은 memory-only(persist 안 함, ADR-0004 point 4)이고, 재시작 시 reconciler가 `unresolved-ambiguous` intent에서 빈도를 재계산해 재-trip한다(point 7). report와 journal write가 별도 트랜잭션이어도 기동 재계산이 skew를 치유한다.
- **토큰 갱신 실패 → 전역 halt**: 토큰 매니저는 journal(intent)을 쓰지 않으므로 결합할 대상이 없다. killswitch 자기 카운터 persist로 충분하다.
- **order 연속 실패 → 전역 halt**: point 3이 다룬다.

**3. order 연속 실패 카운트는 count-before-resolve ordering으로 크래시-세이프하며, reconciler re-count가 overcount(과-halt = 안전 방향)를 흡수한다.**

order 연속 실패 신호는 유일하게 persist(재구성 불가)이면서 journal-결합(실패가 intent를 `resolved-failed`로 마감)이다. `TripTx` 제거로 카운터 증가와 journal의 실패-resolution이 별도 트랜잭션이 되면, **순서**가 안전성을 가른다:

- **금지 — resolve-first**: 발신처가 `ResolveIntent(rejected)`를 먼저 commit하고 killswitch 카운터++를 나중에 하면, 그 사이 크래시가 "journal엔 실패 기록됐는데 카운터엔 없는" **permanent undercount**를 만든다(카운터는 persist라 재계산 안 되고, reconciler는 unresolved intent만 스캔하므로 이미 resolved된 intent를 re-count하지 않는다). crash-loop에서 매 실패가 이 창에 먹혀 최후 에스컬레이션이 영구 우회된다 = money-guard의 fail-open.
- **채택 — count-before-resolve**: killswitch가 카운터++(임계 도달 시 같은 killswitch tx에서 `TripHalt`+epoch)를 **먼저** durable commit하고, 발신처가 `ResolveIntent(rejected)`를 나중에 commit한다. 모든 크래시 위치를 전개하면 "resolved-but-not-counted"가 나오는 위치가 없다: (a) count 전 크래시 → intent가 unresolved로 남아 reconciler가 re-drive하며 다시 count, (b) count 후·resolve 전 크래시 → count됨 + intent unresolved → 재시작 re-drive가 다시 count = **overcount**, (c) 둘 다 후 → 정확히 한 번. 유일한 발산은 (b)의 overcount인데, 이는 **같은 실패를 중복으로 세어 임계에 더 빨리 닿는 것**(과-halt)이라 ADR-0004의 fail-closed 철학과 정렬된다 — crash-loop에서 오히려 halt를 앞당긴다.
- **reconciler 계약**: reconciler가 unresolved intent를 `rejected`로 확정할 때 killswitch에 order 실패를 report한다(re-count). 이 계약이 (a)·(b)의 재시작 복구를 완성한다.

이로써 ADR-0004 Alternatives가 원자 트랜잭션으로 닫으려던 "주문은 실패 기록됐는데 halt는 안 켜진" 어긋남을, order-failure에 한해 **count-first ordering + reconciler re-count**가 대체한다 — 카운터는 여전히 persist되고(ADR-0004 point 7·ADR-0005 "카운터는 prune 안 함" 준수), pruned journal에서 재구성하는 열등한 탈출로를 쓰지 않는다.

**4. reset(order 성공에 의한 카운터 리셋)은 원자 결합이 불필요하다.** 성공-journal과 카운터 reset이 어긋나면 방향이 overcount(리셋 안 됨 = 과-halt = 보수적/안전)이므로, order 성공 report는 count ordering 계약에서 제외한다.

**5. ADR-0005 point 3·ADR-0004와의 관계 — order-failure 원자결합의 부분 amend.** ADR-0005 point 3은 "한 논리적 사건이 journal과 halt/카운터를 동시에 바꾸면 단일 트랜잭션으로 원자화"를 요구하고, ADR-0004 Alternatives는 "주문은 실패 기록됐는데 halt는 안 켜진" 어긋남을 원자 트랜잭션으로 닫기로 기각했다. 이 ADR은 **order-failure 경로에 한해** 그 원자 결합을 count-before-resolve ordering + reconciler re-count로 대체한다. 두 ADR의 안전 목표(어긋남 차단)는 유지된다 — 카운터는 여전히 persist되고 재시작 복구가 어긋남을 닫는다 — 그러나 그 목표를 달성하는 **메커니즘**이 "단일 원자 tx"에서 "순서 규율 + 재시작 재계산"으로 바뀐다. frontmatter `supersedes`는 두 ADR을 통째로 대체하지 않으므로 비워두되(ADR-0010: 부분 대체를 표현하는 필드가 스키마에 없고 필드 즉석 발명은 금지 — ADR-0011 선례), **같은 PR에서 ADR-0005 point 3과 ADR-0004 Alternatives(원자결합 기각 대안)에 이 ADR로의 amend 포인터를 추가한다.** 그러지 않으면 stateless 구현자가 ADR-0005/0004만 읽고 order-failure에 원자 seam을 요구해 이 ADR과 모순된 구현 계약을 만든다(ADR-0011이 확립한 amend-포인터 규범 — 새 파일에서 조용히 모순 선언 금지).

## Alternatives considered

- **mirror-first 유지 + 크래시 창을 개별적으로 막기** — 기각: 적대적 리뷰 6라운드가 증명하듯 optimistic 미러가 durable과 갈라지는 경로(Trip·TripTx·reset·escalation)마다 새 창이 생겨 수렴하지 않는다. 근본 순서를 durable-first로 뒤집는 것이 창을 구조적으로 없앤다.
- **`TripTx` 유지 + store `Atomically`에 post-commit hook seam 추가** — 기각: store 무수정 원칙을 깨고, 결합 실행(after-commit 콜백) 개념을 순수 write seam에 들인다. durable-first는 killswitch가 자기 tx를 소유하면 hook 없이 성립한다.
- **`TripTx` 유지 + 2-phase(durable write in-tx, caller가 commit 후 `ConfirmTrip`)** — 기각: `ConfirmTrip` 누락이라는 새 caller 실수 표면을 만든다(누락 시 현 프로세스 전면 blocked — 안전 방향이나 운영 혼란). count-first가 이 seam 자체를 없앤다.
- **order-failure에 원자 seam(`ReportOrderFailureTx`) 유지 — exactly-once anchor** — 기각(count-first 채택): 카운터++를 발신처 tx에 원자 참여시키면 undercount는 exactly-once로 닫히지만, 발신처가 commit을 소유하므로 point 1(durable-first)의 미러 노출을 commit 뒤로 미루는 post-commit 문제가 order-failure에 재등장한다. count-first는 killswitch가 자기 tx를 소유해 durable-first와 깔끔히 정합하고, overcount는 crash-loop에서 안전 방향이다. (적대적 검토가 count-first의 crash-safety를 모든 크래시 위치로 확증했다.)
- **order-failure를 resolve-first ordering으로** — 기각: permanent undercount fail-open을 만든다(Decision point 3). 카운터가 persist·재구성 불가이고 reconciler가 resolved intent를 re-count하지 않으므로, resolve 후·count 전 크래시가 재시작에 영구히 남는다.
- **order-failure를 "재구성 가능"으로 재분류해 journal에서 재계산** — 기각: ADR-0005 point 6의 terminal prune(성공·실패 intent 모두 보존창 뒤 prune)이 "streak since last success" 재계산을 prune 경계 밖에서 undercount시켜 그 자체가 fail-open이 된다. order-failure는 persist가 강제되고, persist는 크래시-세이프를 위해 count-first ordering을 강제한다.
- **Q1을 문자 그대로 두 상태(in-flight→pending, commit→halted)만 규정** — 기각(적대적 검토 지적): durable write가 크래시가 아니라 에러로 실패하는 arm이 미정의면, 문자대로는 미러가 unhalted로 남아 저장소 다운 순간 fail-open된다. 실패 arm을 명시적으로 fail-closed(blocked 유지)로 규정한다(Decision point 1).
- **CanSubmit이 매번 durable을 읽어 미러를 없앤다** — 기각: ADR-0004 point 5가 뜨거운 경로 이유로 이미 기각. durable-before-visible은 미러를 유지하되 그 노출 순서만 durable 뒤로 정한다.
- **재시작 시 미해결 안전 신호가 있으면 무조건 fail-closed + 사람 검토** — 기각: crash-loop에서 재시작마다 사람 개입을 요구해 무인성을 과도하게 희생한다(ADR-0004가 같은 이유로 기각한 대안과 동형). count-first + reconciler re-count로 자동 복구가 가능하면 그쪽이 비례적이다.

## Consequences

- (좋음) "미러가 halted면 재시작도 halted"가 크래시 타이밍과 무관하게 성립한다 — mirror-first가 만들던 crash-window(Trip·persist 실패·reset·escalation)가 한 순서 규칙(durable-before-visible)에서 통째로 사라진다.
- (좋음) `TripTx`/`ReportOrderFailureTx`/`recoveryFailed` 같은 보정 seam·상태가 대부분 불필요해진다 — killswitch가 자기 tx를 소유하고 durable이 진실이므로, 프로세스 생존 시 persist 실패도 크래시도 같은 fail-closed 경로(durable 미확정 → blocked)로 수렴한다.
- (좋음) store seam을 손대지 않는다(무수정 원칙 유지).
- (비용) 전역 trip이 durable commit을 기다리는 in-flight 창 동안 `CanSubmit`이 fail-closed(pending)로 신규 노출을 막는다 — 정상(halt 아닌) 경로는 미러 읽기 그대로이고, pending은 오직 trip in-flight 중에만이다. durable commit이 느리면 그 짧은 창의 신규 노출이 지연되지만, 지연되는 쪽이 안전(blocked)이다.
- (비용) order 연속 실패 신호에 **count-before-resolve ordering 계약**이 발신처(order·reconciler)에 부과된다 — killswitch 카운터++가 journal `ResolveIntent(rejected)`보다 **먼저** durable commit돼야 한다. 이 계약과 reconciler re-count(unresolved rejection을 재시작 시 다시 report)는 order·reconciler 구현 이슈(#34/#35)의 수용 기준에 들어간다.
- (비용) count-first는 crash-loop에서 order-failure를 overcount할 수 있다(같은 실패 중복 카운트). 이는 임계에 더 빨리 닿아 halt를 앞당기는 과-halt이므로 안전 방향이지만, "정확히 N번 실패해야 halt"라는 정밀한 임계 의미는 크래시 상황에서 "N번 이하에서도 halt 가능"으로 느슨해진다 — 무인 안전에서 수용 가능한 보수적 오차다.
- (계약 전파) reconciler(ADR-0003, 구현 이슈 #35)는 unresolved intent를 `rejected`로 확정할 때 killswitch에 order 실패를 report해야 한다(count-first 재시작 복구의 완성). ambiguous 빈도 재계산 계약(ADR-0004 point 7)과 같은 표면이다.
- (후속) 현 #57 재설계(mirror-first + epoch fence)는 이 ADR의 durable-before-visible로 **재구현**된다 — 조건부 단일-tx clear·durable epoch fence는 참고하되, `recoveryFailed`·self-repersist·`ReportOrderFailureTx` 같은 mirror-first 보정 장치는 걷어낸다. 구현 이슈로 분해한다(`/issue-drafter`).
- (후속) durable-first 미러의 pending 상태 표현(sync primitive)과 Reconfirm 술어 배선(제출 임계구역이 pending도 blocked로 보게)은 구현 이슈에서 `-race`로 검증한다.
