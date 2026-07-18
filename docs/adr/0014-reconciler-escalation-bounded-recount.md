---
id: "0014"
status: Accepted
date: 2026-07-18
deciders: [chnu-kim]
domain: [order, killswitch]
protects: [live-execution-human-gate]
supersedes: []
superseded_by: null
verification:
  - reviewer: architect grounding pass (ADR-0003/0004/0012/0013 + killswitch/store/order source cross-read)
    date: 2026-07-18
    verdict: 초안 도출 — 세 포크(ambiguous fail-closed 정책 / clear-vs-escalation / bounded LIVE re-count)를 ADR-0003·0004 point7·0012 D1(c)/D3·0013 W-E과 killswitch 소스에 정렬.
  - reviewer: adversarial panel (3 lenses — money-safety interleaving / spec-mechanism consistency / platform-reality feasibility)
    date: 2026-07-18
    verdict: blocking 5건 반영 완료 — (L1F1) 전역 임계를 rate-window→backlog 건수로 전환(hazard 무노화), (L1F2 & L3F1-cadence) 종목 floor 전이·잔여-0 auto-clear를 LIVE ticker 스펙에 추가, (L1F3) persistence-wins를 실제 resolve/prune 경로로 재타깃 + HasUnpersistedPendingHalt로 in-memory 캐리어 가시화, (L1F4) open 주문 추적을 gate-차단 밖 비블로킹 트래커로 분리, (L1F5) reconciler 설정 zero-guard, (L3F1) ClearSymbol 잔여-0 조건, (L3F2) 성공-리셋 순서 가드로 지연 REJECTED 소실 봉쇄, (L3F3) ticker supervised fail-closed. platform-reality 렌즈는 "consume-only 실현가능성" clean pass 판정(모든 seam 실존·도달가능) + sacred-path 라우팅 명시 요구 반영. 인용 드리프트(WakeFunc submit.go:88-93) 정정.
  - reviewer: codex:review
    date: 2026-07-18
    verdict: fixed (P2 — ambiguous 전역 임계 비교가 Decision 1.2 "초과(>)"와 Decision 6/killswitch "≥"로 불일치 → 임계-1 만큼 지연되는 fail-open. 같은 PR에서 전역 임계를 inclusive `>=`로 통일, "임계값 N=N건째 halt" 명문화)
  - reviewer: codex:adversarial-review
    date: 2026-07-18
    verdict: needs-attention→해소 (high, No-ship — Accepted ADR-0004 amend가 Proposed ADR-0014에 의존하는 split-brain: 스테이트리스 구현자가 미승인 예외를 승인된 것으로 오인. 해소: ADR-0014를 같은 ship-ready PR에서 Accepted로 flip — ADR-0011과 동일하게 ADR-0009 point 1 위임 자율 승인 경로)
  - reviewer: chnu-kim
    date: 2026-07-18
    verdict: approved (결정 자율 위임 — ADR-0009 point 1 경로: grilling 상대를 적대 패널 3렌즈 + codex 2채널 하드닝으로 대체. 최종 확정은 PR #70 admin 머지)
---

# ADR-0014: 단일 reconciler는 ambiguous를 국소 fail-closed·backlog 임계로 전역 에스컬레이션하고, ADR-0013의 두 delayed-halt 창을 supervised bounded LIVE re-count로 유계화한다

- **Status**: Accepted (적대 패널 3렌즈 + codex 2채널 하드닝 · ADR-0009 point 1 위임 승인)
- **Date**: 2026-07-18
- **Deciders**: chnu-kim
- **관련 이슈/PR**: #35 (single reconciler 구현)

## Context

ADR-0003은 "제출이 불명이거나 프로세스가 죽었다 살아났을 때 무엇이 진실인가"를 **단일 reconciler**가 확정하도록 확정했고, ambiguous submit(2-마커 `submit-attempted` 有·`orderId` 無)을 **국소 fail-closed**(종목 차단) + **빈발 시 전역 에스컬레이션**으로 정했다. ADR-0004는 그 국소/전역 차단을 killswitch fail-closed 가드로 세우고 재시작 비대칭(재구성 가능 신호=기동 재계산, 불가 신호=persist)을 못박았다. ADR-0012는 durable-before-visible + count-before-resolve + persistence-wins를, ADR-0013은 미러 정합성(3 disjoint carrier)을 확정하며 — **두 곳 모두 그 완성을 `internal/reconciler`(#35)에 명시적으로 미뤘다.** 이 ADR은 그 미뤄진 빈칸을 채워 #35를 지배한다.

`internal/reconciler`는 아직 없다(greenfield). #35의 선행 이슈(#20 재구성함수+ack 경로, #32 killswitch 진입점, #33 GetOrder 래퍼, #34 제출경로)는 전부 머지됐다(main 2df013a/9b93034/3b7332f). 착수 가능 상태이며, 세 개의 아키텍처 포크가 남아 있다:

1. **ambiguous fail-closed 정책 — 임계 판단 locus와 척도.** ADR-0003은 "1건→종목, 빈발→전역"을 정했지만, `killswitch.Config`(switch.go:24-33)에는 `OrderFailureThreshold`/`TokenRefreshThreshold`/`TokenRefreshWindow`만 있고 **ambiguous 임계 필드가 없다.** ADR-0004 point 7은 "임계 판단은 killswitch 한 곳"을 원칙으로 세웠는데 ambiguous만 비어 있다. 게다가 #35는 **killswitch/store/order/toss 패키지를 수정하지 않는다**로 스코프가 못박혀 있어 "killswitch가 ambiguous 임계를 흡수(Config 확장)"는 불가능하다. 남은 미해결: **전역 임계를 무엇으로 재는가(시간-윈도우 rate vs 미해소 backlog 건수).**
2. **clear-vs-escalation.** 무엇이 halt/차단을 evidence 기반으로 clear/재-fire하는가. per-symbol 자동 clear vs 전역 사람-수동 clear의 경계(ADR-0004 point 6), 세 신호(order-failure=killswitch durable 카운터, ambiguous=journal, token-refresh=killswitch durable 카운터)의 재-fire 소유권 분리, 그리고 **성공-리셋과 지연 실패 재-count의 순서.**
3. **bounded LIVE re-count.** ADR-0013 Consequences는 두 delayed-halt 창(W-E: clear 후 임계-초과 evidence 재-fire / order-failure durable-error 후 re-count 재-trip)을 유계로 만드는 것이 "**#35 reconciler의 bounded LIVE re-count cadence**"라고 명시했다 — 그러나 ADR 스스로 "기동-시 재계산만으로는 라이브 창이 미명세 cadence에 의존한다"고 인정했고, #35 이슈 본문의 현재 AC는 **부팅 스캔 + 제출-직후 wake만** 명시하고 주기적 라이브 재-스캔을 요구하지 않는다.

이 결정을 가르는 힘(전제, 재-grilling 안 함):

- **비가역 위험 로직은 단일 표면(ADR-0003).** ambiguous-submit과 재시작 복구가 같은 reconciler 코드 경로 → `go test -race` 검증 표면이 하나.
- **reconciler는 절대 제출·재제출하지 않는다(ADR-0003 point 4).** query + journal 종결 + trip/clear/report만. 재발행은 전략 책임.
- **추측 binding 금지(ADR-0002 point 3 / ADR-0003).** orderId만이 유일한 진실 핸들. ABSENT 강등도, OPEN payload 매칭 auto-ack도 절대 금지 — 확정 못 하면 보존·차단.
- **재시작 비대칭(ADR-0004 point 7 / ADR-0012 D1(c)).** ambiguous backlog는 journal(`unresolved-ambiguous` intent)에서 **재구성 가능** → persist 안 하고 재계산. order-failure는 durable 카운터(재구성 불가지만 journal-결합 → count-first + re-count). token-refresh는 durable 카운터·latch(journal 대응물 없음 → killswitch 자기 소유).
- **durable이 진실, 미러는 값싼 읽기(ADR-0004 point 5 / 0012 / 0013).** 단, killswitch halt는 durable + **in-memory 캐리어**(switch.go:64-69 `unpersistedPending` latch·`bootHalt`)로 존재한다 — durable `store.Halt()`만 읽으면 `MarkHaltPending` 실패 arm·token latch·panic 승격 bootHalt를 `HaltNone`으로 오독한다. reconciler는 durable read에 더해 `killswitch.HasUnpersistedPendingHalt()`(pure query, switch.go)를 함께 읽어야 killswitch 자기 시야와 동등해진다.
- **#35 무수정 제약.** store/killswitch/order/toss는 소비만. 신규 로직은 오직 `internal/reconciler`에. `Intent.Payload`=`json.Marshal(order.OrderRequest)`(submit.go:298-305)이고 `OrderRequest.Symbol`이 export이므로, reconciler(이미 `order`를 GetOrder로 import)가 payload를 unmarshal해 종목을 복원, `Trip(ScopeSymbol, symbol)`/`ClearSymbol(symbol)`에 쓸 수 있다(seam 실존 확인).
- **`ClearSymbol`은 refcount가 아니라 boolean delete(switch.go:201-205), `Trip(ScopeSymbol)`도 boolean set(trip.go:32-34)** — 한 종목의 여러 ambiguous가 단일 boolean을 공유한다. 첫 해소가 종목 전체를 여는 과-해제를 막으려면 reconciler가 잔여 unresolved-ambiguous 0을 확인해야 한다.
- **`ReportOrderSuccess`(report.go:144-149)는 카운터를 0으로 무조건 durable 리셋**하고 "count-ordering 계약의 일부가 아니다"라고 소스가 명시한다 — 즉 순서 무결성은 **호출자(reconciler)** 책임이다.
- **무인 안전(CLAUDE.md).** 안 죽는다(goroutine recover), 애매하면 blocked(fail-closed), 주문 제출 자동 재시도 금지. 조회는 백오프 재시도 OK(단 유계 — `toss.Client.Get`이 maxRetries=4/backoffCap=5s로 호출당 유계, client.go:40-42/187-231). **안전-load-bearing 백그라운드 루프(ticker)는 죽으면 fail-closed로 승격**한다.

## Decision

`internal/reconciler`를 **query-only 진실확정·에스컬레이션 엔진**으로 만든다. 절대 제출·재제출하지 않고, store/killswitch/order/toss seam을 **수정 없이 소비만** 한다. halt 중에도 동작한다(ADR-0004 point 1 — query-only는 `CanSubmit` 차단 대상이 아니다). 세 포크를 아래로 확정한다.

### 1. ambiguous fail-closed 정책 — 국소 floor는 무조건 먼저, 전역 임계는 reconciler가 backlog 건수로 소유한다 (Fork 1)

1. **모든 ambiguous 1건 → 즉시 종목 차단(무조건 floor).** settle window 경과 후 `submit-attempted`·orderId 無 intent를 `unresolved-ambiguous`로 두고 `killswitch.Trip(ctx, ScopeSymbol, symbol, reason, submitAttemptedAt)`(trip.go:31-35, 메모리 전용 map write)을 호출한다. **어떤 임계 판단보다 먼저** — 단일 ambiguous가 임계 계산과 무관하게 즉시 봉쇄되는 money-safety floor다. **ABSENT로 강등하지도, OPEN payload 매칭으로 auto-ack하지도 않는다**(ADR-0003 Alternatives 명시 기각).
   - **이 전이는 부팅 스캔뿐 아니라 LIVE에서도 구동돼야 한다(Decision 11-i).** 라이브에서 `submit-attempted`→`unresolved-ambiguous` 전이의 유일한 즉시 경로인 `WakeFunc`(type submit.go:88-93; 발화 submit.go:365-373)는 **best-effort**다 — `SubmitterConfig.Wake`는 nil 허용(submit.go:157)이고 `wakeReconciler`는 recover로 삼킨다(submit.go:548-552). 따라서 wake 유실 시 종목 floor가 다음 부팅까지 지연되지 않도록, **LIVE ticker가 settle-window 만기 `submit-attempted`-only intent를 스캔해 `unresolved-ambiguous`로 전이시키고 `Trip(ScopeSymbol)`을 건다.** 이것이 없으면 "무조건 floor"가 wake-조건부가 된다.

2. **전역 에스컬레이션 임계 판단은 reconciler가 소유한다 — killswitch가 아니다(ADR-0004 point 7의 명시적 예외).** reconciler가 journal의 **현재 미해소 `unresolved-ambiguous` intent 건수(backlog)** 를 재계산하고 **reconciler 자신의 설정(backlog 임계)** 과 비교한다. **backlog가 임계 이상(`backlog >= 임계`, inclusive)이면** `killswitch.Trip(ctx, ScopeGlobal, "", reason, occurredAt)`(bare durable 전역 halt, trip.go:86-138)을 호출한다. 비교는 **inclusive `>=`** 로 못박는다 — killswitch의 기존 임계(`OrderFailureThreshold` 등)가 `>=`로 트립하고 Decision 6의 재-fire 조건도 `backlog ≥ 임계`이므로, "초과(`>`)"로 읽으면 전역 halt가 임계+1까지 지연돼 설정 임계가 1만큼 약해지는 fail-open이 된다(임계값 N은 "N건째에 halt").
   - **왜 backlog 건수인가(rate-window 기각의 핵심 — Lens1 F1):** ambiguous의 hazard는 "정체불명 live order가 밖에 떠 있을 수 있음"이다. 이 위험은 **시간이 지나도 노화하지 않는다** — 2시간 전 ambiguous도 지금 ambiguous만큼 위험하다(그 주문이 여전히 미확정 live일 수 있음). 시간-윈도우 rate(윈도우 내 `submit-attempted` 건수)로 재면, 미해소 backlog가 그대로인데 발생시각이 윈도우 밖으로 노화하면서 빈도가 0이 되어 **전역 gate가 hazard 소멸 없이 재개방되는 fail-open**이 생긴다. 그래서 척도는 **현재 미해소 backlog 건수**다. 이 정의는 Decision 6의 "sticky-until-human-resolves"를 기계적으로 참이 되게 한다 — 재-fire 조건(backlog ≥ 임계)은 사람이 backlog를 실제로 뺄 때만 떨어진다. (per-symbol floor는 이와 독립적으로 각 종목을 계속 막는다.)
   - **왜 reconciler-소유인가:** ambiguous의 **카운터는 곧 journal**이다(재구성 가능, reconciler 소유) — order-failure/token-refresh의 durable 카운터(killswitch 소유)와 성질이 다르다. `killswitch.Trip(ScopeGlobal)`은 **의도적으로 bare**하다(자체 evidence store 없음 — 재구성 evidence는 caller journal). #35는 killswitch를 수정할 수 없다(Config 확장 불가). 이 셋이 겹쳐, ambiguous 임계를 killswitch에 넣으려 해도 killswitch가 reconciler의 journal 재계산 feed에 의존해야 하므로 "한 곳"이 성립하지 않는다. 따라서 **ambiguous는 ADR-0004 point 7 "임계 판단 killswitch 한 곳"의 명시적 예외**이고, 이 ADR이 그 사실을 amend로 기록한다.
   - **bare `Trip(ScopeGlobal)`로 충분한 이유:** journal이 evidence store이고 reconciler가 재-fire를 소유한다(Decision 11). reconciler가 Trip을 호출하기 전에 이미 `submit-attempted` 마커가 durable하다(ADR-0013 W-D의 "evidence는 halt 여부와 무관하게 durable"을 journal write가 충족).

3. **전역 1건-즉시-halt는 채택하지 않는다.** ADR-0003이 이미 기각했다 — blind spot은 "응답 유실" 하나라 빈도가 극히 낮은데 1건마다 봇 전체를 멈추면 무인성을 과하게 희생한다. 국소 floor + 빈발 backlog 에스컬레이션이 비례적이다.

### 2. clear-vs-escalation — 자동 clear는 종목·잔여-0에 국한, 전역 clear는 사람 전용, 재-fire는 소유권별 분리, 성공-리셋은 순서 가드 (Fork 2)

4. **per-symbol 차단은 종목의 잔여 unresolved-ambiguous가 0일 때만 자동 clear(ADR-0004 point 6 + Lens3 F1).** `ClearSymbol`은 refcount가 아니라 **boolean delete**이므로(switch.go:201-205), 한 종목 S에 ambiguous intent가 둘 이상 걸린 상태에서 그중 하나만 해소됐다고 `ClearSymbol(S)`를 부르면 **나머지 미해소 intent가 남아 있는데도 종목 전체가 열리는 과-해제 fail-open**이 된다. 따라서 reconciler는 밑에 깔린 intent 하나가 `unresolved-ambiguous`를 벗어날 때 **journal을 재-스캔해 종목 S의 잔여 unresolved-ambiguous 건수가 0인지 확인한 뒤에만** `ClearSymbol(S)`를 호출한다. 0이 아니면 종목 차단을 유지한다. ambiguous **자체의 해소는 사람 몫**(ADR-0003 — payload 추측 금지)이고, reconciler는 잔여-0 결과로 종목 차단을 자동 해제할 뿐이다. (이 잔여-0 auto-clear도 LIVE ticker가 매 tick 재평가한다 — Decision 11-iii.)

5. **전역 halt는 사람 수동 clear 전용(ADR-0004 point 6).** reconciler는 **어떤 트리거의 전역 halt도 `ClearHalt`하지 않는다** — `ClearHalt`/`FinalizePendingHalt`(clear.go)는 운영자·#36 소유이고 reconciler는 clear 목적으로 소비하지 않는다. (단 `HasUnpersistedPendingHalt`는 **read-only halt-가시성 query**로 소비한다 — clear가 아님, Decision 8.)

6. **reconciler는 전역 halt를 clear하지 않지만 backlog evidence로 재-fire는 한다.** 운영자가 ambiguous-발 전역 halt를 clear한 뒤에도 live journal의 미해소 backlog가 여전히 임계 이상(`>= 임계`)이면, reconciler는 bare `Trip(ScopeGlobal)`을 **재-fire**한다(clear가 아니라 live evidence로 fail-closed 재천명). **귀결(의도된 fail-closed):** ambiguous-발 전역 halt의 운영자 clear는 사람이 **밑에 깔린 `unresolved-ambiguous` backlog를 임계 미만으로 실제 해소**할 때까지 sticky하게 다시 걸린다 — Decision 1.2가 척도를 backlog 건수로 정의했으므로 이 sticky 속성이 **기계적으로 보장**된다(rate-window였다면 노화로 깨졌음 — Lens1 F1 봉쇄).

7. **재-fire 소유권은 통일하지 않고 신호별로 분리한다(evidence 원천이 다르기 때문):**
   - **token-refresh** — killswitch 자기 카운터·latch(`ReportTokenRefreshFailure`)가 전담. journal 대응물이 없어 **reconciler 무관**.
   - **order-failure** — reconciler가 **이벤트-구동 per-intent**로 완성한다(통일 루프 아님): `acked` intent가 GetOrder 상세로 **REJECTED 확정**이면 count-before-resolve(Decision 8) 재-count, **FILLED 확정**이면 `killswitch.ReportOrderSuccess(ctx)`(report.go:144-149)로 연속실패 카운터 reset — 단 **Decision 8의 성공-리셋 순서 가드를 지킨다**.
   - **ambiguous** — reconciler가 journal backlog 재계산으로 소유(Decision 1·2·4·6). reconciler는 ambiguous(자기 소유)와 order-failure(per-intent 완성)만 만지고 **token은 만지지 않는다.**

8. **순서 계약(어긴 순간 fail-open):**
   - **count-before-resolve(ADR-0012 D3):** REJECTED 확정 경로에서 `killswitch.ReportOrderFailure(ctx, reason, occurredAt)`(report.go:18-27, 카운터++/임계 trip을 killswitch 자기 tx로 durable commit)를 `store.ResolveIntent(rejected)`보다 **먼저** commit한다. 원자 결합하지 않는다(TripTx 제거 — ADR-0012 D2). 위반은 permanent undercount fail-open.
   - **성공-리셋 순서 가드(Lens3 F2 봉쇄):** `ReportOrderSuccess`는 카운터를 무조건 0으로 리셋하고 "count-ordering 계약 밖"이라고 소스가 명시하므로(report.go:141-149), **순서 무결성은 reconciler 책임**이다. reconciler는 어떤 FILLED intent의 성공-리셋도, **그보다 이르거나 같은 `submit-attempted` 시각을 가진 order intent 중 진실 미확정(unresolved `acked` / GetOrder 소진 재-drive 대기)인 것이 하나라도 남아 있는 동안 유예한다.** 이유: `counterOrderFailure`는 연속-실패 streak인데, 나중 FILL의 리셋이 **더 이른 REJECT(GetOrder 지연으로 아직 미확정)** 를 건너뛰어 적용되면, 서버 발생순서상 임계에 도달했던 streak이 durable하게 관측되지 않고 **에스컬레이션이 지연이 아니라 소실**된다(임계=5, order1..5 REJECTED 중 order5의 GetOrder 소진 → 나중 order6 FILL이 카운터 4→0 리셋 → 뒤늦은 order5 재-count 0→1, 트립 없음). 가드는 "더 이른 in-doubt가 남았으면 리셋 보류"로 이 재배치를 봉쇄한다 — 보류는 카운터를 높게 유지하므로 **over-halt 방향(안전)**. 모든 더-이른 in-doubt가 확정되면 유예된 리셋을 적용한다.
   - **persistence-wins(ADR-0012 D1(c)) — 실제 resolve/prune 경로에, in-memory 캐리어까지 보고(Lens1 F3 봉쇄):** reconciler가 **evidence를 제거하는 모든 행위**(intent resolve, `FinalizeFullyAudited`/감사 finalize로 not-fully-audited 레코드 pruning) **직전에**, durable `store.Halt()`(미러 아님)**와** `killswitch.HasUnpersistedPendingHalt()`를 **둘 다** 읽어 어느 쪽이든 pending/halted를 가리키면 그 pruning을 보류한다. durable `store.Halt()`만 읽으면 `MarkHaltPending` 실패 arm·token latch·panic 승격 bootHalt처럼 **durable=none인데 in-memory로만 halt**인 상태를 `HaltNone`으로 오독해, ADR-0012 D1(c)가 막으려던 "in-memory pending halt의 evidence 소멸"의 절반만 막는다. 초안이 이 가드를 "unresolved-ambiguous resolve 전"에 걸었던 것은 **오배치**였다 — reconciler는 ambiguous를 resolve하지 않는다(Decision 4, 사람 몫). 가드의 올바른 대상은 reconciler가 실제로 밟는 evidence-pruning 경로(acked→terminal resolve, prepared→aborted resolve, 감사 finalize)다.
   - **ambiguous를 order-failure 카운터에 report하지 않는다** — 별도 트리거, 이중 계상 금지(제출경로 submit.go:368-372가 이미 이 분리를 강제).

### 3. bounded LIVE re-count — 두 패스 부팅 + gate-비블로킹 open 추적 + 유계 재조회 + supervised 라이브 cadence (Fork 3)

9. **부팅 스캔 = 한 부팅 goroutine 안 두 순차 패스. 패스 1은 분류+Trip 주입만 하고 gate를 연다 — open 주문 추적은 gate를 막지 않는다(Lens1 F4 봉쇄).**
   - **패스 1 — 마커 분기(원천: `store.LoadUnresolvedIntents`):** intent별 2-마커 분기(ADR-0003) — **prepared-only** → `ResolveIntent("aborted-before-submit")` + terminal 감사 / **`acked`(orderId 有)** → `order.Client.GetOrder`(api.go:351-389) **1회 분류 조회**: 닫힘→결과기록+`ResolveIntent`(REJECTED는 Decision 8 count-first + 성공-리셋 순서 가드, FILLED는 `ReportOrderSuccess`) · **열림→비블로킹 LIVE 트래커에 등록만 하고 다음 intent로 진행**(닫힐 때까지 동기 폴링하지 않는다 — 정상 미체결 지정가 1건이 `NotifyScanComplete`를 무기한 막아 봇이 신규 제출 불가가 되는 무인성 자해를 방지) / **`submit-attempted`·orderId 無** → Decision 1(종목 Trip, backlog 임계 이상(`>=`) 시 전역 Trip). **이 패스가 종목별·전역 Trip을 전부 주입한 뒤에만 `killswitch.NotifyScanComplete()`(switch.go:184-188)를 호출**한다(ADR-0004 point 3 — replay-gate가 빈 미러를 보고 통과시키면 재시작이 곧 안전장치 우회). **gate-open은 신규-노출 BLOCK 재도출(ambiguous 종목/전역)로 게이트되지, `acked` 진실확정으로 게이트되지 않는다** — 확정 못 한/미체결 `acked` intent는 종목 차단을 만들지 않으므로 gate를 열어도 신규 노출이 새지 않는다.
   - **패스 2 — 감사 re-emit 드라이버(원천: `store.LoadNotFullyAuditedIntents`, `ReconstructLifecycleRecords` audit_ack.go:53-86):** 미-ack lifecycle 레코드를 결정적으로 재도출해 sink로 re-emit(멱등키 병합) + `RecordAuditAck`/`FinalizeFullyAudited`로 플래그 수렴(#20 잔여, ADR-0006 point 4). **이 패스는 gate-open 뒤에 돌아도 안전**하다 — 감사 re-emit은 신규-노출 차단을 만들지 않는다(intent는 이미 resolve됨). 유일한 차단 효과인 감사 emit 실패(`FailClosedError`)→`Trip(ScopeGlobal)`(ADR-0006 point 6)는 durable fail-closed라 gate-open 뒤에 걸려도 재-block한다. **단 `FinalizeFullyAudited`는 Decision 8 persistence-wins 가드를 통과할 때만 실행한다**(pending/halted면 finalize 보류 — evidence pruning 봉쇄).

10. **`acked` intent GetOrder 재시도는 유계이며, 소진돼도 gate를 막지 않는다.** `toss.Client.Get`의 **호출당 유계 백오프(maxRetries=4, backoffCap=5s, client.go:187-231)** 를 쓴다. 소진 후에도 실패하면: **resolve하지 않고**(진실 없이 닫으면 추측 — 금지) intent를 unresolved로 남겨 **LIVE 재-drive 대상**으로 둔다. 진짜 REJECTED인데 못 가져온 intent의 order-failure count는 **지연**되며, **Decision 8 성공-리셋 순서 가드가 함께 걸려 있는 한에서만** "회피되지 않고 지연될 뿐"이 성립한다 — 그 가드가 없으면 나중 FILL의 성공-리셋에 삼켜져 **소실**된다(Lens3 F2). ADR-0013의 bounded delayed-halt와 동종이되, 유계성은 Decision 11 ticker의 지속 가동에 의존한다(Decision 11 supervision).

11. **supervised LIVE re-count cadence를 #35가 도입한다 — 부팅 스캔·wake만으로 부족하다.** #35는 **유계 주기 재평가 ticker(`reevalInterval`)** 를 도입한다. 매 tick(단일 recover 경계 하)에:
    - **(i) settle-window 만기 `submit-attempted`-only intent 전이:** journal을 스캔해 settle window 경과한 submit-attempted-only intent를 `unresolved-ambiguous`로 전이 + `Trip(ScopeSymbol)`(Decision 1의 floor를 wake-유실과 무관하게 라이브 구동 — Lens1 F2 봉쇄).
    - **(ii) 전역 backlog 재-fire:** 미해소 unresolved-ambiguous **backlog 건수** 재계산 → 임계 이상(`>=`)·standing halt 없으면 `Trip(ScopeGlobal)` 재-fire(이미 halted면 killswitch idempotent no-op, doGlobalHalt:104-109).
    - **(iii) per-symbol 잔여-0 auto-clear:** 각 종목의 잔여 unresolved-ambiguous가 0이면 `ClearSymbol` (Decision 4).
    - **(iv) unresolved order intent 재-drive:** retry 만기된 `acked` intent GetOrder 재시도(interval이 pacing), 확정 시 Decision 8 순서 계약 적용.
    - **근거:** ADR-0013이 두 delayed-halt 창(W-E clear-후-초과 / order-failure durable-error)을 "**정적 상황에서도 유계**"로 booking한 조건이 곧 이 cadence다. 제출-직후 wake(#34 WakeFunc)는 **활성 제출 시**에만 깨우므로 **조용한 시장(제출 없음→wake 없음)에서는 재-fire가 무기한 지연** — ticker가 그 갭을 닫는다.

12. **ticker는 supervised·fail-closed다 — 안전 주장의 load-bearing 컴포넌트이므로 그 자신의 durability를 명세한다(Lens3 F3 봉쇄).** 세 delayed-halt 창의 유계성 전체가 ticker의 지속 가동을 전제하므로:
    - **각 tick은 recover 경계 안에서 돈다** — 한 tick 내부 panic(GetOrder 재-drive 중 파생 등)이 goroutine을 죽이지 않고 다음 tick으로 넘어간다(CLAUDE.md 무인 안전).
    - **ticker/reconciler goroutine은 supervisor가 재기동한다** — main(#36) 와이어링이 reconciler 루프를 recover+재기동 감독 하에 띄운다.
    - **지속 가동 불가 시 fail-closed 승격** — 반복 panic으로 supervisor가 루프를 유지하지 못하거나 재기동이 실패하면 `killswitch.BootHalt()`(in-memory 전역 차단, switch.go) 또는 `Trip(ScopeGlobal)`로 승격해 신규 제출을 멈춘다. 조용히 죽은 ticker가 두 창을 무한으로 되돌리는 대신, 죽으면 봇이 신규 노출을 만들지 않는 쪽으로 실패한다.

13. **멱등·경계:** `Trip(ScopeGlobal)` 재-fire는 멱등(이미 halted면 killswitch no-op). `ReportOrderFailure`는 at-least-once with **안전 overcount**(overcount=과-halt=안전 방향). **무한 tight-retry 없음** — GetOrder는 toss 내부 유계, ticker가 재시도를 pacing. reconciler는 **절대 재제출 안 함**(ADR-0003). reconciler 자신의 감사 re-emit 경로도 order/submit.go `emit()`과 동형의 fail-closed 에스컬레이션을 적용한다(`FailClosedError`→전역 trip).

## Alternatives considered

- **전역 임계를 시간-윈도우 rate(`submit-attempted` 시각 기준 윈도우 내 건수)로 잰다** — 기각(Lens1 F1): ambiguous hazard(정체불명 live order)는 시간으로 노화하지 않으므로, 미해소 backlog가 그대로인데 발생시각이 윈도우 밖으로 빠지면 빈도가 0이 되어 hazard 소멸 없이 전역 gate가 재개방되는 fail-open이 생긴다. **미해소 backlog 건수**가 정직한 척도이고, Decision 6의 sticky-until-human-resolves를 기계적으로 보장한다.
- **`killswitch.Config`에 `AmbiguousThreshold` 추가해 killswitch가 ambiguous 임계를 흡수** — 기각: (1) killswitch 패키지 수정이라 #35 무수정 제약 위반. (2) ambiguous 카운터는 journal(reconciler 소유)이라 killswitch가 임계를 판정하려면 reconciler 재계산 feed에 의존 → "한 곳" 성립 불가. reconciler-소유가 정직하고 신호별 테스트 표면은 여전히 하나(order/token=killswitch, ambiguous=reconciler). ADR-0004 point 7의 예외로 명문화한다.
- **ambiguous 1건에 전역 halt** — 기각(ADR-0003 재확인): blind spot이 "응답 유실" 하나로 빈도 극저인데 1건마다 봇 정지는 무인성 과희생. 국소 floor + 빈발 backlog 에스컬레이션이 비례적.
- **ambiguous를 ABSENT 강등 또는 OPEN payload 매칭 auto-ack** — 기각(ADR-0003 Alternatives): payload 유일성은 API·암호 identity가 아니라 결과집합 내 추측. 무관 주문을 진실로 박아 중복 노출 은폐·journal 오염. 안전 자동복구는 세 불변식(known orderId 제외·봇 전용 namespace·매칭창 내 동시 동일주문 불가) 증명 후에만.
- **per-intent 해소 시 잔여 확인 없이 `ClearSymbol`** — 기각(Lens3 F1): `ClearSymbol`은 boolean delete(refcount 아님)라, 한 종목 다중 ambiguous 중 하나만 해소돼도 종목 전체가 열리는 live duplicate-exposure fail-open. 종목 잔여 unresolved-ambiguous=0 확인 후에만 clear한다.
- **성공-리셋을 발생순서와 무관하게 즉시 적용** — 기각(Lens3 F2): `ReportOrderSuccess`가 카운터를 무조건 0으로 리셋하고 count-ordering 계약 밖이므로, 나중 FILL의 리셋이 GetOrder-지연된 더 이른 REJECT를 건너뛰어 적용되면 임계에 도달했던 streak이 durable하게 관측되지 않아 에스컬레이션이 **소실**(지연 아님)된다. 더 이른 in-doubt가 남은 동안 리셋을 유예한다(over-halt 방향).
- **세 신호를 통일된 하나의 재평가 루프로 재-fire** — 기각: token은 killswitch 자기 소유(journal 대응물 없음), order-failure는 per-intent 이벤트-구동, ambiguous만 reconciler 재계산이다. 통일 루프는 killswitch의 token/order 소유를 중복하고 테스트 표면을 흐린다.
- **부팅 패스 1이 open `acked` 주문을 닫힐 때까지 동기 추적한 뒤 `NotifyScanComplete`** — 기각(Lens1 F4): 정상 미체결 지정가 1건이 gate를 장 마감/영원까지 잠가 신규 제출 불가(무인성 자해). open 주문은 분류만 하고 비블로킹 LIVE 트래커로 넘긴다. gate는 신규-노출 BLOCK 재도출에만 게이트되고 acked 진실확정에는 게이트되지 않는다.
- **부팅 스캔이 `acked` GetOrder 성공까지 `NotifyScanComplete`를 블로킹** — 기각: 확정 못 한 `acked` intent는 종목 차단을 만들지 않으므로, gate를 그것에 걸면 신규 제출 전체를 무의미하게 지연하고 안전 이득이 없다. 지연된 re-count는 유계(Decision 8 순서 가드 + Decision 11 supervised ticker)이지 fail-open이 아니다.
- **LIVE ticker 없이 부팅-스캔 + wake만(현 #35 AC 그대로)** — 기각: 조용한 시장에서 ADR-0013의 두 delayed-halt 창과 종목 floor 전이가 무기한이 된다. ADR-0013이 bounded cadence를 #35에 명시 배정했으므로 ticker는 #35 스코프다.
- **ticker 자체 durability를 명세하지 않음(전제로만 둠)** — 기각(Lens3 F3): 세 창의 유계성 전체가 ticker 가동에 의존하는데 조용한 ticker 사망이 창을 무한으로 되돌린다. tick recover + supervisor 재기동 + 지속 불가 시 fail-closed 승격을 Decision으로 못박아야 유계성이 실제로 성립.
- **persistence-wins를 durable `store.Halt()`만으로 판정** — 기각(Lens1 F3): killswitch halt는 durable + in-memory 캐리어(latch/bootHalt)로 존재하므로 durable read는 durable=none인 in-memory-only halt를 오독한다. reconciler는 `HasUnpersistedPendingHalt()`(pure query)를 함께 읽어 killswitch 자기 시야와 동등해진다. 또한 가드 대상을 (reconciler가 밟지 않는) "unresolved-ambiguous resolve"에서 실제 evidence-pruning 경로(acked→terminal resolve·감사 finalize)로 재타깃.
- **resolve-first(`ResolveIntent`를 `ReportOrderFailure`보다 먼저)** — 기각: permanent undercount fail-open(ADR-0012 D3).
- **reconciler가 ambiguous 해소 시 전역 halt를 자동 clear** — 기각: 전역 halt는 사람 수동 clear 전용(ADR-0004 point 6). reconciler는 backlog evidence로 재-fire할 뿐 clear하지 않는다.
- **부팅 두 패스를 병렬** — 기각: 같은 intent가 두 패스에 걸린다(패스1 resolve→not-fully-audited가 패스2 원천). 순차·마커분기-우선이 레이스를 없애고 "모든 Trip은 gate 전 주입"(ADR-0004 point 3)을 보장.

## Consequences

- (좋음) 비가역 진실확정·에스컬레이션 로직이 단일 reconciler에 모여 `go test -race` 표면이 하나다(ADR-0003).
- (좋음) ambiguous가 첫 발생에 종목 floor로 즉시 봉쇄되고(부팅·LIVE 양쪽), 전역 에스컬레이션은 미해소 backlog 재계산으로 재시작-안전 + hazard-노화-불가라 sticky가 기계적으로 참(Lens1 F1 봉쇄).
- (좋음) ADR-0013의 두 delayed-halt 창 + order-failure durable-error 창이 **supervised** LIVE ticker로 정적 상황에서도 유계 — ticker 사망 시 fail-closed 승격이라 유계성이 회복탄력적(Lens3 F3 봉쇄).
- (좋음) fail-safe 방향이 명확: 확정 못 하는/미체결 `acked`는 unresolved로 보존·비블로킹 재-drive(추측 없음, gate 안 막음), ambiguous는 국소 차단·backlog 전역, 감사 실패는 전역 trip, 성공-리셋은 더 이른 in-doubt가 남으면 유예(over-halt 방향).
- (좋음) persistence-wins가 in-memory 캐리어까지 봐서 killswitch 자기 시야와 동등 — evidence 소멸 창의 나머지 절반까지 봉쇄(Lens1 F3).
- (비용) **ambiguous backlog 임계 파라미터가 killswitch 밖(reconciler 설정)에 산다** — ADR-0004 point 7 "임계 한 곳"에 대한 정직한 예외. 임계 설정이 두 패키지로 나뉘나 신호별 테스트 표면은 하나로 유지.
- (비용) LIVE ticker가 주기 재조회 부하·supervisor 재기동 경로를 더한다(pacing으로 유계, 공짜 아님) + 부팅이 두 패스.
- (비용) ambiguous-발 전역 halt의 운영자 clear는 사람이 backlog를 임계 미만으로 뺄 때까지 sticky하게 재-fire된다 — 의도된 fail-closed지만 사람 개입 스텝이 하나 는다.
- (비용) 성공-리셋 유예로 정상 흐름에서도 카운터가 잠시 높게 유지될 수 있다(over-halt 방향, 안전) — reconciler가 in-doubt intent 집합을 `submit-attempted` 시각과 함께 추적해야 한다.
- (제약 전파) **#35 AC를 확장해야 한다**(현 AC에 없음): (a) supervised LIVE re-count cadence + **조용한 시장 재-fire·종목 floor 전이** 유계 테스트(시계 주입 ticker), (b) 두 패스 부팅 순서 테스트(모든 Trip이 `NotifyScanComplete` 전 주입; open 주문은 gate를 막지 않음), (c) `acked` GetOrder 유계 소진이 gate를 안 막고 unresolved 재-drive로 남는 테스트, (d) **성공-리셋 순서 가드 테스트**(더 이른 in-doubt REJECT가 나중 FILL 리셋에 삼켜지지 않고 임계 도달 시 트립), (e) **ClearSymbol 잔여-0 테스트**(한 종목 다중 ambiguous 중 하나만 해소 시 종목 차단 유지), (f) **persistence-wins in-memory 캐리어 가시성 테스트**(durable=none·in-memory-only pending halt에서 evidence pruning 보류), (g) **reconciler 설정 zero-threshold fail-closed 거부 테스트**, (h) **ticker 사망 fail-closed 승격 테스트**. 기존 AC(마커 3분기·ABSENT/auto-ack 금지·count-before-resolve·settle window)는 유지.
- (twin-artifact) **reconciler 설정 검증은 killswitch `Config.validate()`의 쌍둥이**(switch.go:35-46 — zero threshold/window를 "never trips fail-open"으로 거부). Fork 1이 ambiguous 임계를 reconciler로 옮기면서 이 zero-guard도 함께 옮긴다 — reconciler `New`가 backlog 임계 ≤0을 fail-closed로 거부한다(Lens1 F5).
- (amend·sacred-path 라우팅) 이 ADR은 ADR-0004/0012/0013을 supersede하지 않고 **ADR-0004 point 7 "임계 판단 killswitch 한 곳"에 ambiguous 예외를 확정**한다. **같은 PR에서 ADR-0004 point 7에 이 ADR로의 amend 포인터를 추가**한다(stateless 구현자가 point 7만 읽고 killswitch에 ambiguous 임계를 넣지 않도록). **이 amend는 `docs/adr/0004-*.md`(sacredRequiredPaths·CODEOWNERS 보호 경로)를 건드리고, 이 ADR 자체가 `protects: [live-execution-human-gate]`를 선언하므로 도입 PR은 보호 ADR을 둘 건드린다** → ADR-0011 point 6에 따라 `chnu-kim` 작성이면 self-approval 교착이다. **도입 PR은 loop-PR 자격증명 흐름(`POST dispatches` → `pr-creation.yml` → `mechanu[bot]` 작성)으로 라우팅한다**(Lens2 — 0011/0012/0013 등록과 동일 규율).
- (검증 방법) temp dir 실제 store·audit, httptest API, 시계 주입(settle window·ticker interval·임계 경계·성공-리셋 유예), `go test -race`, `go vet ./...`. `Trip(ScopeSymbol)` 다건 연타는 각자 `mu` 하 map write라 경합 없음(ADR-0013 disjoint 모델 범위 밖·의도) — 회귀 테스트로 확인.
- (후속) W-E의 더 강한 봉쇄(이유별 halt·clear-리셋 카운터·evidence-as-in-process-carrier)는 ADR-0012 escalation-카운터 모델과 얽힌 별개 포크로 **후속 ADR 이관**(필요 시 별도 `/architect`). 이 ADR은 미러-정합성·재-fire cadence·성공-리셋 순서만 확정하고 남은 창을 bounded delayed-halt로 정직히 booking한다.
- (게이트) **Accepted.** 적대 패널 3렌즈 + codex 2채널(review P2 threshold off-by-one fixed · adversarial-review high split-brain 해소) 하드닝 완료. 승인은 ADR-0009 point 1 위임 자율 경로(grilling 상대를 적대 검증으로 대체) — money-guard·동시성은 dispatch 전에 수렴(adr-hardening-before-accept). ADR-0004 amend가 Proposed 초안이 아니라 이 Accepted 결정에 의존하도록 같은 ship-ready PR(#70)에서 Accepted로 확정한다.
