---
id: "0013"
status: Accepted
date: 2026-07-17
deciders: [chnu-kim]
domain: [killswitch]
protects: [live-execution-human-gate]
supersedes: []
superseded_by: null
verification:
  - reviewer: adversarial-panel (7 lens — interleaving-pairs / pending-lifecycle / boot-shutdown / toctou-reconfirm / counter-liveness / historical-recheck / hotpath-atomicity)
    date: 2026-07-17
    verdict: 순수 disjoint 모델 unsound 확증(상태-동치 붕괴 {trip-durable-failed}={no-trip}={none,0}) → sticky 미영속-pending 래치 필수, 카운터는 adjunct로만. 15 window 확증, 전부 "disjoint 재작성이 현 구현이 닫은 창을 재개방". clobber/torn은 disjoint+단일스냅샷으로 구조 봉쇄, lost-halt는 래치로 봉쇄.
  - reviewer: adversarial-panel (3 skeptic — interleaving-liveness / fork-resolutions / constraint-and-historical) — written ADR 최종 하드닝
    date: 2026-07-17
    verdict: W1–W7 전부 closed-structurally 확인. 그러나 written ADR이 래치를 '트리거 라벨'로 스코프한 탓에 lost-halt 2건 재개방 확증 — W-A(ambiguous 에스컬레이션 MarkHaltPending 실패 시 live fail-open+재시작 유실), W-B(panic 경계 bootHalt 재량). 래치 활성 조건을 decision-locus로 재규정 + I7(발행-선행-감소) 명문화로 봉쇄. 모델 재설계 불요 — refinement 반영 후 생존.
  - reviewer: chnu-kim
    date: 2026-07-17
    verdict: approved (grilling 수렴 — open forks 1·2·4 해소; 최종 하드닝 W-A/W-B refinement 반영)
  - reviewer: codex:github-bot (PR #63)
    date: 2026-07-17
    verdict: 3건 지적 → 전부 반영. P2(수동 ClearHalt이 bootHalt를 clear 안 해 stuck-block) · P1(clean-shutdown 자격이 bootHalt를 빠뜨려 bootHalt-only halt 재시작 유실) · P1(token 카운터 임계미달 persist 실패가 non-reconstructable인데 래치 안 걸려 영구 undercount → 에스컬레이션 우회) → 래치 스코프 기준을 decision-locus에서 '잃은 상태의 재구성 가능성'(ADR-0004 point 7)으로 정정. 결정 불변, 배선·스코프 완결성 수정.
---

# ADR-0013: 킬 스위치 미러 정합성은 disjoint block-carrier로 확보한다 — 3값 durable 미러 + sticky 미영속-pending 래치 + 단조 in-flight 카운터를 단일 잠금 스냅샷으로 읽는다

- **Status**: Accepted
- **Date**: 2026-07-17
- **Deciders**: chnu-kim
- **관련 이슈/PR**: #32 (killswitch 구현), PR #62

## Context

ADR-0004는 킬 스위치를 fail-closed 제출 가드로 확정하며 **저장소 = 영속 진실, 메모리 = 값싼 읽기 미러**(point 5)를 세웠지만, sync primitive와 **동시 전이 사이의 미러 정합성 모델은 구현에 위임**했다. ADR-0012는 미러 노출의 **durable-before-visible 순서**를 못박았지만, `CanSubmit`이 읽는 미러를 갱신하는 **여러 전이 경로(manual Trip · ambiguous 에스컬레이션 · count-first `ReportOrderFailure`/`ReportTokenRefreshFailure` · `ReportOrderSuccess` · `ClearHalt` · `BootHalt` · `FinalizePendingHalt`)가 동시 실행될 때** 미러가 어떻게 정합을 유지하는지는 비워뒀다. 이 ADR이 그 빈칸을 채운다.

구현(#32, PR #62)은 그 빈칸을 **단일 phase 미러(none/pending/halted) + generation 소유권 재조정**으로 채웠다: 하나의 phase 필드가 durable 상태와 in-flight 트립 block을 겹쳐 싣고, 어느 pending이 누구 것인지를 gen으로 재조정했다. 이 재조정은 **6 리뷰 라운드에 걸쳐 7개 동형 fail-open window(W1–W7)**를 냈고, 매 수정이 다음 window를 드러냈으며, 오케스트레이터와 구현자가 "검증했다"고 한 코드에서 적대 리뷰가 다음 window를 찾았다. 이는 ADR-0012가 durability 순서를 **설계층**에서 겪은 "개별 창을 하나씩 막는 방식은 수렴하지 않는다"의 **구현-동시성층 반복**이다 — 근본 원인은 "단일 phase에 두 종류의 block(durable 상태 + in-flight 트립)을 겹쳐 싣고 재조정한다"는 구조 선택 자체다.

inspection이 반복적으로 window를 놓쳤으므로, 이 ADR을 쓰기 **전에** 후보 모델을 **적대 패널(7 렌즈)**로 하드닝했다. 그 결과가 이 결정을 가른다:

- **순수 disjoint 모델은 unsound(증명됨).** "phase 미러를 없애고 상태를 {durableHalt(store 미러) + 균형 inflightTrips 카운터} 둘로만 둔다"는 후보는 **lost-halt를 표현할 수 없다**: liveness를 위해 카운터를 무조건 감소시키면, "durable `MarkHaltPending`까지 실패한 트립"과 "트립 없음"이 둘 다 관측 상태 `{durableHalt=none, inflightTrips=0}`으로 **붕괴**한다(상태-동치). 어떤 술어도 둘을 구별 못 하므로 위험 신호가 유실된다. 따라서 카운터 밖의 **sticky·사유-운반 슬롯(= pending 미러)이 상태공간에 반드시 있어야** 한다.
- **미러+래치는 필수, 카운터는 그것을 대체하지 못한다.** 패널이 확증한 15개 window는 전부 "disjoint 재작성이 현 구현이 이미 닫은 창(durable-error/종료/재시작 축)을 재개방"하는 것이었다 — 순수 재작성은 순 regression이다.
- **정합 읽기는 단일 잠금 스냅샷을 요구한다.** `durableHalt`와 `inflightTrips`를 서로 다른 시점에 읽으면 "durableHalt는 방금 내려갔는데 카운터는 아직 안 올라온"(또는 그 반대) torn-read 창이 생긴다.

이 결정을 가르는 힘(전제, 재-grilling 안 함):

- **미러가 fail-open의 유일 표면(ADR-0004 point 5).** `CanSubmit`은 매 주문마다 도는 뜨거운 경로라 store를 안 때리고 미러만 읽는다 — 미러 정합성이 곧 안전성이다.
- **durable-before-visible(ADR-0012 Decision 1).** halted 노출은 durable commit 뒤에만. pending in-flight 창·durable-error arm은 fail-closed.
- **재시작 비대칭(ADR-0004 point 7 · ADR-0012 Decision 1(c)/3).** 재구성 가능 신호(order 연속 실패=카운터 persist+reconciler re-count, ambiguous 빈도=journal 재계산)와 재구성 불가 신호(토큰 갱신 실패·수동 트립)의 재시작 복구 경로가 다르다.
- **단일 write 커넥션 직렬화(ADR-0005).** `store.Atomically`는 전용 write 커넥션 하나로 직렬화되므로 count-first의 임계 판정을 tx 안에서 정확히 내릴 수 있다.
- **무인 안전(CLAUDE.md).** panic이 주문 루프를 멈추면 안 되고(goroutine recover 경계), 안전장치는 애매하면 blocked(fail-closed)다.

## Decision

킬 스위치 미러 정합성을 **세 개의 서로소(disjoint) block-carrier**로 확보한다. 각 조각은 **소유자 하나·역할 하나**를 가지며, 이전 구현의 generation 재조정을 **제거**한다. 모든 상태는 `mu`(값싼 in-process 뮤텍스)로 보호하고, 뜨거운 경로는 이들을 **하나의 일관 스냅샷**으로 읽는다.

### 상태 (전부 `mu` 보호)

1. **`durableHalt` ∈ {none, pending, halted}** — store `HaltState.Phase`의 in-process 미러. `halted`로 올림은 durable `TripHalt` commit **성공 뒤에만**(durable-before-visible, ADR-0012). `none`으로 내림은 `ClearHalt`이 durable clear commit 성공 뒤에만(수동 전용, ADR-0004 point 6). 기동 시 `store.Halt()`에서 로드하며 `pending` 또는 `halted`면 halted로 기동(persistence-wins).
2. **`unpersistedPending` 래치 (bool + `haltReason`, sticky)** — durable-error로 잃은 상태가 **재시작에 재도출 불가**한 트립(store 완전 다운 순간의 halt 결정 또는 non-reconstructable 카운터 증분 — count-first order-failure만 예외, 아래 "재구성 가능성" 절 참조)을 담는다. `store.Halt().Phase==none`이라 store read로는 안 보이는 "메모리에만 있는 pending"이다. **오직 `FinalizePendingHalt` 성공 또는 수동 `ClearHalt`만 내린다 — 카운터 감소로는 절대 안 내려간다.** 이것이 상태-동치 증명이 요구하는 필수 sticky 슬롯이다(= 이전 구현의 `mirrorPhase==pending && durablePhase==none`).
3. **`inflightTrips` — 단조 atomic 카운터** — in-flight 트립 block을 **disjoint하게** 나른다. 각 트립 유발 경로가 자기 `+1`/`-1` 하나씩만 소유한다(소유권 모호성 0). **같은 `mu` 스냅샷 안에서 읽는다**(lock-free 아님).
4. **`scanComplete`(재생성 게이트) · `bootHalt`(보수적 부팅-halt, #36) · `perSymbolBlocked`(메모리 전용).**

### 뜨거운 경로 술어 (단일 `mu` 스냅샷 — torn-read 구조적 불가)

```
CanSubmit(sym) blocked iff:
  durableHalt != none || inflightTrips > 0 || unpersistedPending
  || !scanComplete || bootHalt || perSymbolBlocked(sym)
```

`Reserve`는 아무것도 캡처하지 않는다(level 의미론). `Reconfirm`은 `mu` 하에서 이 술어를 **재평가**한다(ADR-0004 point 1). **generation을 쓰지 않는다** — 카운터가 no-clobber를 담당하므로 gen의 소유권 역할이 사라졌고, Reserve~Reconfirm 창 안의 trip-then-clear는 "operator가 clear했으니 진행"이 옳다(level 의미론).

### 전이

- **트립 유발 경로**(manual Trip global · ambiguous 에스컬레이션 · `ReportOrderFailure` · `ReportTokenRefreshFailure`):
  1. `mu` 하에서 `inflightTrips++` — 어떤 slow 대기(`haltMu`·store)보다 **먼저**(I1 즉시성).
  2. `haltMu` 획득.
  3. `durableHalt==halted`면 idempotent no-op. 아니면 `MarkHaltPending`→`TripHalt` commit; 성공 시 `durableHalt=halted`(I3). durable **에러**면 아래 "durable-error 래치는 '잃은 상태의 재구성 가능성'으로 스코프한다"대로 처리(재구성 불가 신호는 sticky 래치 set — count-first order-failure만 래치 없이 re-count로 복구). count-first(`ReportOrderFailure`/`ReportTokenRefreshFailure`)는 카운터 read+증가를 **같은 store tx 안에서** 하고(단일 writer가 임계 판정 직렬화 — ADR-0005), 임계 도달 시에만 `TripHalt`.
  4. `haltMu` 해제.
  5. `inflightTrips--` — **자기 block-carrier를 발행한 뒤에만**(I7 발행-선행-감소). 균형 유지 = liveness. durable-error/panic arm의 block hold는 카운터가 아니라 래치(또는 `durableHalt=halted`)가 붙든다.
  - **panic 처리**: `inflightTrips` inc..dec span **내부**(worker recover 경계보다 안쪽)에 recover 경계를 두어, panic 포착 시 dec가 카운터를 놓기 **전에** 보수적 halt로 승격한다. 승격 대상은 **재량이 아니라 decision-locus로 결정**한다(W-B): count-first order-failure(결정이 tx 내부·재구성 가능)만 in-memory `bootHalt` 승격을 허용하고, **그 밖 모든 트립(수동·ambiguous 에스컬레이션·토큰 갱신 실패 — 결정이 tx 이전에 성립했거나 재구성 불가)은 sticky 래치를 set**해 durable-survivable하게 만든다. 이로써 stuck-block(dec 누락)·fail-open(무보호 defer dec)·lost-halt(bootHalt 유실) 세 horn을 동시에 닫는다.
- **`ClearHalt`**(수동, ADR-0004 point 6): `haltMu` → durable `ClearHalt` commit; 성공 시 `durableHalt=none` **및** 래치 clear **및 `bootHalt=false`**(operator clear가 in-memory 보수적/panic halt까지 실제로 풀어야 한다 — 안 그러면 durable 성공에도 `bootHalt`가 남아 영구 blocked, codex P2), 에러면 셋 다 유지(fail-closed) → 해제. **`inflightTrips`는 절대 안 건드린다** — 동시 트립의 `count>0`이 `durableHalt` 하강과 **독립적으로** block을 붙든다(W4/W7 구조적 봉쇄).
- **`ReportOrderSuccess`**(카운터 리셋, ADR-0012 Decision 4): 자기 store tx만. `haltMu`·`inflightTrips`·미러 전부 미접촉(I6).
- **`BootHalt`**(#36, ADR-0012 Decision 1(c)): in-memory halted(`bootHalt=true`), durable write 없음, 수동 `ClearHalt`까지 유지. panic-span 보수적 승격에도 재사용. **`bootHalt`도 래치와 마찬가지로 store가 반영 못 하는 in-memory-only halt이므로 clean-shutdown 자격을 차단한다**(아래).
- **clean-shutdown 자격 (제약 ③, #36 소비)**: store read로 안 보이는 **in-memory-only halt**는 둘이다 — 래치(`unpersistedPending && durableHalt==none`)와 `bootHalt`(in-memory halted, durable 없음). 따라서 **`HasUnpersistedPendingHalt = (unpersistedPending && durableHalt==none) || bootHalt`** 로 정의하고, 이게 참이면 #36은 clean sentinel을 기록하지 않는다. `bootHalt`를 빠뜨리면(래치만 보면) bootHalt-only halt를 가진 run이 스스로를 clean으로 인증해 재시작이 durable `none`을 믿고 재개방한다 — line "manual `ClearHalt`까지" 계약 위반(codex P1). (order-failure panic→`bootHalt` 케이스는 재시작 reconciler re-count가 별도로 복구하지만, #36 보수적-boot `bootHalt`는 operator clear만이 풀 수 있으므로 clean 차단이 필수다.)
- **`FinalizePendingHalt`**: 래치의 `haltReason`으로 `MarkHaltPending`→`TripHalt` 재커밋; 성공 시 `durableHalt=halted`+래치 clear, 실패 시 래치 유지(→ #36이 clean sentinel 기록 거부). #36은 finalize **전에** 리포터 경로를 quiesce한다. (`FinalizePendingHalt`는 래치를 durable로 승격하지만 `bootHalt`는 in-memory 계약이라 finalize 대상이 아니다 — `bootHalt`가 서 있으면 위 자격 술어가 clean을 막는다.)

### durable-error 래치는 '잃은 상태의 재구성 가능성'으로 스코프한다 (Fork 1 + 최종 하드닝 W-A + codex P1(token counter) — ADR-0004 point 7 / ADR-0012 Decision 1(c)·3)

durable-error의 **런타임 핫패스는 모든 경우 fail-closed**다(in-flight 창은 `inflightTrips>0`). 차이는 **어떤 durable-error가 sticky 래치를 set해 유실을 막느냐**이고, 진짜 기준은 **트리거 라벨도 decision-locus도 아니라 "잃은 durable 상태가 재시작에 재도출되는가"**다(ADR-0004 point 7 재시작 비대칭). decision-locus는 그 근사였을 뿐이라 정밀하지 않다 — token 카운터처럼 결정이 tx 내부여도 재구성 불가한 케이스가 있다(codex P1).

- **재구성 가능 — count-first order-failure만**: 실패 intent가 unresolved journal에 남고 durable 카운터가 원자 롤백되므로 **어떤 durable-error도** reconciler re-count(ADR-0012 Decision 3)로 복구된다 — 임계 미달 counter-persist 실패는 re-count가 카운트를 재충전하고, 임계 도달 `TripHalt` 실패는 re-count가 재-trip한다. **래치 없이 `inflightTrips--` 후 block 해제가 정상**이고 clean 종료를 허용한다(Consequences의 delayed-halt 창 참조).
- **재구성 불가 — 그 밖 모든 durable-error**: 잃은 상태를 재시작이 재도출할 수 없으므로 **sticky 래치를 set**한다(`HasUnpersistedPendingHalt` → #36 clean 거부 → 보수적 halted 부팅). 균형 `inflightTrips`는 무조건 dec되므로 이 hold는 래치가 붙든다. 구체적으로:
  - **수동 Trip·ambiguous 에스컬레이션의 `MarkHaltPending` 실패(durable=none)** — 트립 결정이 durable tx 밖/이전에 성립했고 재도출 경로가 없다. **ambiguous가 load-bearing**: persistence-wins는 durable `pending`/`halted`에서만 작동하는데 durable=none이라 INERT하고, live reconciler가 `unresolved-ambiguous` intent를 resolve하면 재계산 evidence마저 소멸한다 → 래치 없이는 live fail-open + 재시작 유실이다(ADR-0012 1(c)의 ambiguous 봉쇄는 `MarkHaltPending` **성공**=durable pending 기록을 전제했다 — W-A).
  - **`ReportTokenRefreshFailure`의 카운터 persist 실패 — 임계 미달 포함** — 토큰 갱신 실패는 journal 대응물이 없어 잃은 카운터 증분을 re-count할 수 없다(ADR-0004 point 7 재구성 불가). 따라서 **임계 도달 전이어도** counter-persist tx가 실패하면 래치를 set한다. 안 그러면 store-error/graceful-shutdown 루프가 증분을 계속 잃어 **임계를 영영 못 채우고 에스컬레이션을 우회**한다(ADR-0004 point 7이 명시적으로 기각한 "임계 도달 전 재시작이 카운터 리셋" fail-open — codex P1). (order 카운터 persist 실패는 위 재구성-가능 arm이라 래치 불요 — re-count가 복구.)
- **`TripHalt`만 실패(`MarkHaltPending`은 성공, durable=`pending`)**: 래치 불요 — durable `pending`이 이미 기록됐고 ADR-0012 persistence-wins + `store.Halt()` read가 재시작을 덮는다(unclean recovery가 pending을 halted 취급). 미러 `durableHalt`도 `pending`이라 핫패스가 계속 blocked.

### 불변식 (refined)

- **I1 fail-closed 즉시성**: `inflightTrips++`가 어떤 slow 대기보다 먼저.
- **I2 no-clobber (구조적)**: 세 carrier가 서로소 — `durableHalt`는 `ClearHalt`만, 래치는 `Finalize`/`ClearHalt`만, `inflightTrips`는 각 owner의 자기 ±1만 내린다. 어떤 경로도 남의 block 근거를 지울 구조가 없다.
- **I3 durable-before-visible**: `durableHalt=halted`는 `TripHalt` commit 뒤에만.
- **I4 전이 순서**: durable 전이는 `haltMu`로 직렬화.
- **I5 (정정)**: 핫패스는 **mu-only 단일 스냅샷**(store/`haltMu` round-trip 없음). **lock-free 아님** — 정합 스냅샷이 torn-read를 닫으려면 필수이고, 값싼 in-process 뮤텍스는 ADR-0004 point 5("값싼 in-process 읽기")를 만족한다.
- **I6 비-halting 무간섭**: `ReportOrderSuccess`는 `{haltMu, inflightTrips, 미러}` 어느 것도 안 건드린다.
- **I7 발행-선행-감소(publish-before-decrement)**: `inflightTrips--`는 각 트립의 **최종 스텝**이며, 반드시 그 트립의 **block-carrier 발행 이후에만** 실행된다 — 성공 arm은 `durableHalt=halted` 발행 후, durable-error/panic arm은 sticky 래치 set(또는 order-failure의 정당한 no-halt) 후. 어떤 arm에서도 dec가 자기 carrier 발행을 앞설 수 없다. (이전 구현이 순서를 step-번호로만 암시해 inspection-fragile했던 것 — 이 계열의 반복 실패 모드 — 을 명문 불변식으로 대체한다. **구현 가이드**: dec는 inc 직후 등록한 단일 `defer` 하나로만 두고 조기 명시 dec를 금지하며, "commit 성공 후 `durableHalt` 발행 전 dec" 인터리빙을 `-race` 회귀 매트릭스에 포함한다.)

## Alternatives considered

- **순수 disjoint(phase 미러 제거, {durableHalt store-미러 + 균형 카운터} 둘로만)** — 기각(적대 패널 확증): **lost-halt에 unsound**. `MarkHaltPending`까지 실패한 트립과 트립 없음이 관측상 `{none,0}`으로 붕괴(상태-동치)해 어떤 술어로도 구별 불가. 15개 확증 window 전부에서 현 구현이 닫은 durable-error/종료/재시작 창을 재개방 → 순 regression. **필수 refinement(sticky 사유-운반 래치)가 정확히 pending 미러를 되살리므로**, disjoint 전제를 부분 철회할 수밖에 없다.
- **단일 phase 미러 + generation 소유권 재조정(이전 #32 구현)** — 기각: 하나의 phase에 durable 상태와 in-flight block을 겹쳐 싣고 gen으로 재조정한 것이 W1–W7(특히 clobber 계열 W3/W4/W7)을 6 라운드에 걸쳐 냈다. gen 엣지는 inspection-취약. **disjoint block-carrier가 그 재조정을 통째로 제거**한다.
- **핫패스+모든 전이를 단일 lock으로** — 기각: durable store I/O를 lock 하에 들고 있으면 `CanSubmit`이 그 뒤에서 블로킹된다(ADR-0004 point 5 핫패스 값싸게 위반). durable I/O는 `haltMu`로 직렬화하되 핫패스는 `mu`만 잡는다.
- **`inflightTrips`를 lock-free로(mu 밖) 읽기** — 기각: `durableHalt`와 카운터를 다른 시점에 읽어 torn-read("durableHalt 방금 none됐는데 카운터 아직 안 올라온" 또는 그 반대). 단일 `mu` 스냅샷이 ~0 비용으로 닫는다(래치가 이미 핫패스에 `mu`를 강제하므로 카운터가 같은 스냅샷에 공짜 합류).
- **Reconfirm에 generation edge-detector 유지(Fork 4)** — 기각: 정당하게 clear된 halt를 과-차단한다(Reserve~Reconfirm 창 안 trip-then-clear는 ADR-0004 point 1 level 의미론상 "cleared → 진행"이 옳다). 카운터가 no-clobber를 담당하므로 level 재평가로 충분하다.
- **count-first order-failure에도 sticky 래치 적용(Fork 1)** — 기각: order 연속 실패는 **재구성 가능**(durable 카운터 롤백 + unresolved journal intent + reconciler re-count, ADR-0012 Decision 3)이라 어떤 durable-error도 재시작에 복구된다. 래치·보수적 boot는 order 실패 중 일시적 store blip마다 불필요한 보수적 halted 부팅을 강제한다(무인성 비용). 래치는 **재구성 불가한 잃은 상태**에만 — order-failure만 예외.
- **래치를 decision-locus로 스코프(중간 버전)** — 기각(codex P1): decision-locus("결정이 tx 밖/이전에 성립했나")는 근사일 뿐이다 — `ReportTokenRefreshFailure`는 결정이 tx 내부(임계 판정)여도 잃은 카운터 증분이 재구성 불가(journal 없음)라, 임계 미달 counter-persist 실패가 영구 undercount되어 에스컬레이션을 우회한다. 진짜 기준은 **잃은 durable 상태의 재구성 가능성**이다(ADR-0004 point 7). (그 전 '트리거 라벨' 스코프는 W-A로 이미 기각.)

## Consequences

- (좋음) **clobber window 계열(W3/W4/W7)이 구조적으로 닫힌다** — disjoint carrier가 gen 재조정을 제거한다. `ClearHalt`이 `durableHalt`를 내려도 동시 트립의 `inflightTrips>0`이 독립적으로 block을 붙든다. 정합성이 inspection이 아니라 구조로 성립한다.
- (좋음) **torn-read가 단일 `mu` 스냅샷으로 닫힌다**. 핫패스는 값싼 뮤텍스 1회(store round-trip 없음)라 ADR-0004 point 5를 지킨다.
- (좋음) **lost-halt/제약 ③은 필수 sticky 래치로 처리**되고, graceful-shutdown finalize + clean-sentinel에 배선돼 재시작 fail-open까지 닫는다.
- (비용) **block-carrier 삼원화**(`durableHalt` 미러 + 래치 + `inflightTrips` 카운터) — 단일 미러보다 상태가 많다. 그러나 각 조각은 소유자·역할이 하나씩이고, 취약했던 **재조정(gen)이 사라진다**. 순 트레이드는 "상태 수 ↑ vs 재조정 취약성 제거"이며 후자를 택한다.
- (비용) **I5가 lock-free가 아니라 mu-only**다 — 핫패스가 값싼 뮤텍스를 잡는다(이미 그랬다). 정합 스냅샷을 위해 불가피하며 point 5와 양립한다.
- (비용) **count-first order-failure durable-error가 reconciler re-count에 의존**한다(래치 없음) — ADR-0012 Decision 3의 re-count 계약(#35)이 load-bearing이다. durable-error 후 `inflightTrips--`가 block을 놓고 reconciler가 unresolved intent를 re-count해 재-trip하기까지 **라이브 블록-해제 창**이 있다. 이 창은 fail-open이 아니라 **delayed-halt**다(임계가 durable하게 안 넘어갔으므로 그 순간엔 halt 근거가 없고, re-count가 임계를 재충전하면 halt된다) — 그러나 정적 상황에서도 유계이려면 #35의 re-count 계약이 "기동-시 재계산"만이 아니라 **미해결 order intent의 bounded LIVE re-drive cadence**를 포함해야 한다(ADR-0004/0012는 재계산을 기동 시로만 서술 → 라이브 창이 미명세 cadence에 의존). 이 delayed-halt 창을 #35 수용 기준으로 booking한다.
- (제약 전파) **#32 구현(PR #62, 현재 mirror+gen·W7 열림)을 이 모델로 재작업**해야 한다: `inflightTrips` 카운터 도입, 단일 `mu` 스냅샷 읽기, gen 제거, durable-error 래치에 재구성 비대칭 적용, panic 경계를 span 내부로. dispatch 루프는 이 ADR을 지배 결정으로 재개한다.
- (검증 방법) I1–I6 + disjointness + 단일 스냅샷이 clobber/torn 계열을 **구조적으로 불가능**하게 만들고, 나머지 durable-lifecycle 전이는 `haltMu`로 직렬화된다. 구현 테스트는 W1–W7 인터리빙 매트릭스를 회귀 가드로 반드시 포함한다(temp dir 실제 store 엔진, `go test -race`).
- (amend) 이 ADR은 ADR-0004/0012를 supersede하지 않고, **ADR-0004 point 5가 구현에 위임한 "미러 sync primitive"를 구체 모델로 확정**하며 I5를 정정한다. **같은 PR에서 ADR-0004 point 5에 이 ADR로의 amend 포인터를 추가**한다(stateless 구현자가 point 5만 읽고 lock-free 미러를 가정하지 않도록).
- (후속) `/dispatch-issue`로 #32를 이 ADR 기준으로 재구현한다(각 인터리빙 window를 회귀 테스트로). 필요 시 `issue-drafter`가 재작업 범위를 이슈로 조정.
