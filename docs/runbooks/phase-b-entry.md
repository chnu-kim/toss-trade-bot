# Runbook: Phase B(자율 머지) 진입 절차 — hard precondition ①~⑤ + 활성화 실행

이 문서는 [ADR-0008](../adr/0008-independent-verification-gate.md)의 verdict 게이트와
[ADR-0011](../adr/0011-loop-pr-credential-flow.md)·[ADR-0015](../adr/0015-loop-pr-amendment-bootstrap-activation.md)이
사람 액션으로 남긴 Phase B 활성화 절차를 실행 가능한 체크리스트로 정리한다(이슈 #50).

> **fail-closed 원칙**: hard precondition ①~⑤ 중 하나라도 미충족·미실측이면 레포는
> **Phase A(사람 승인 체제)에 남는다**. 활성화는 live-execution-human-gate(주문 authorize
> 경로)를 약화하지 않으며, 각 완료 판정은 **오퍼레이터 단언이 아니라 capability 실측**이다.

## 축 구분 (ADR-0015 Decision 2)

| 축 | 주체 | 예 |
|---|---|---|
| A. 에이전트 admin 범위 | 에이전트 | 워크플로·presence-check·runbook 저작, `GET protection` 조회, probe 절차 준비, red-team PR 저작, narrowing 스크립트 **문법·prereq 리허설** |
| B. 물리적으로 불가 | 사람 | App key PEM을 loop-pr env에 등록(classifier 차단), 사람계정 PAT narrowing 실행 |
| C. Administration:write 사람 액션 | 사람 | probe 임시 규칙 생성/제거, 최종 branch protection flip, 전역 에스컬레이션 해제 |

## 실행 순서 (뒤집지 않는다 — ADR-0011 라운드 7 · ADR-0015 point 6)

**narrowing(①/③) → probe(④a) → App key rotate+env(①/③→①) → flip(④b) → 전역 해제**

각 스텝은 **선행 스텝 완료 실측 전 착수 금지**. dependency 역순(provision을 narrowing보다
먼저) 금지 — admin/workflow 자격증명이 loop 컨텍스트에 남은 동안 key를 env에 두면 탈취돼
Phase B까지 durable하게 잔존한다.

## Hard precondition ①~⑤

각 항목: **확인주체 · 확인방법 · 통과판정 · 실패경로**.

### ① verdict-gate가 실재하고 required check로 등재 가능
- 확인주체: 에이전트(산출물 검증) + 사람(등재)
- 확인방법: `.github/workflows/verdict-gate.yml`이 main에 존재하고 PR head SHA에 `verdict-gate`
  check-run을 게시(verdict-gate.yml:505-512), PR별 `request-verdict` dispatch 바인딩이 operational.
- 통과판정: probe PR에서 `verdict-gate` check-run이 head SHA에 실제로 붙고 green/red를 게시.
- 실패경로: 바인딩 미operational → required로 등재 시 liveness 붕괴(PR 영구 미머지) → 등재 보류, Phase A.

### ② 자격증명 narrowing 완료 (capability 실측)
- 확인주체: 사람(실행) — 에이전트는 스크립트 리허설만(ADR-0015 point 4)
- 확인방법: **loop 실행 컨텍스트가 resolve하는 자격증명으로** `scripts/verify-credential-narrowing.sh`
  7-ix 실행 → `PUT .../branches/main/protection`과 `POST .../pulls/{n}/reviews`(APPROVE) **둘 다 거부**.
  구 admin classic PAT로 admin 동작 시도 거부. 계정 authentication key 전수 열거 → 각 key로 push 거부.
- 통과판정: 7-ix green(exit 0) + admin 토큰 폐기 실측 + push 거부 실측.
- 실패경로: 하나라도 통과 시도가 성공(=자격증명 잔존) → 개시 금지, narrowing 재실행.
- ⚠️ **#46 CLOSED ≠ narrowing 실행 완료** — 이슈 상태는 spec·워크플로 머지일 뿐. 반드시 실측.
- ⚠️ SSH teardown leg: API 열거 가능 key까지만 실측, 그 밖은 오퍼레이터 단언(잔여 위험).

### ③ App key rotate + loop-pr environment 프로비저닝
- 확인주체: 사람
- 확인방법: 구 key 전부 revoke(구 key JWT로 `GET /app` → 401 실측, ADR-0011 실측목록 12),
  새 key 생성 → `loop-pr` env secret(`MECHANU_APP_ID`·`MECHANU_APP_PRIVATE_KEY`) 등록,
  deployment branch policy를 `main` 하나로 제한. **②(narrowing) 완료 실측 후에만.**
- 통과판정: 활성화 스모크에서 `mechanu[bot]` 작성 PR 생성(`gh pr view --json author`) + 비-main
  ref의 env secret 접근 거부 실측(ADR-0011 실측목록 3).
- 실패경로: 401 아님(구 key 유효) → rotate 미완, 중단.

### ④ main-무접촉 probe (pre-main 거동 실측 — ADR-0011 (4a)·목록 142.2)
- 확인주체: 에이전트(준비) + 사람(임시 규칙·실측)
- 확인방법: 일회용 **보호 브랜치**에 임시 규칙(count=0 + `require_code_owner_reviews=true` + "require a
  pull request before merging")을 걸어 **main 변경 전에** (i) code owner 승인 없는 머지가 실제로
  차단되는지, (ii) PR 없는 direct-push가 거부되는지를 **거동으로 실측**한다 — branch protection은
  per-branch이므로 이 거동은 임시 브랜치에서 검증 가능하다(ADR-0011 (4a)가 이를 hard precondition으로
  요구). flip payload 문법·권한 리허설도 함께. `probe/pr-creation-tamper-47`로 변조 정의 미실행 실측
  (main 정의만 실행). **verdict-gate required *context*의 PR-head-SHA 바인딩만** main/PR-특정이라
  flip 직후(④b) 테스트 PR에서 확증한다.
- 통과판정: count=0에서 code-owner 미승인 머지 차단 + direct-push 거부 실측 + payload 유효 + 변조 마커 부재.
- 실패경로: main 불변경 · Phase A 잔류 · ADR amend.

### ⑤ red-team 인젝션 실측 (verdict leg별)
- 확인주체: 에이전트
- 확인방법: verdict-gate codex leg(+ N-of-2면 Claude leg 각각)에 프롬프트 인젝션 PR을 던져
  false-approve 유도 시도 → 게이트가 버티는지 실측.
- 통과판정: 인젝션이 verdict를 approve로 뒤집지 못함.
- ⚠️ LLM 판정이라 완전 내성 증명 불가 — 상한을 올릴 뿐, epoch당 잔여 false-approve 예산 존재(booked).

## 최종 flip: flip-and-verify-or-rollback 트랜잭션 (ADR-0015 point 7)

`PUT`은 **전체 replace**다(patch 아님) — 빠진 필드는 default/null로 리셋.

1. **스냅샷**: `GET .../branches/main/protection`으로 현재 protection **전체** 저장.
2. **완전 payload 재구성**: 오직 두 가지만 변경 —
   - `required_approving_review_count` 1→0
   - required status check contexts에 `verdict-gate` 추가
   나머지 **명시 보존**: `require_code_owner_reviews=true`(사라지면 사람 게이트 소멸!),
   `enforce_admins`, 기존 contexts(`build · vet · gofmt · test-race`, app_id 15368),
   `restrictions`, `required_linear_history`, `required_pull_request_reviews` 블록.
3. **단일 PUT → 즉시 검증**:
   - `internal/enforcement`의 `CheckBranchProtection` 재통과 — **code-owner 강제만 확인**(이 검사기는 `require_code_owner_reviews`만 파싱하고 `required_status_checks`는 미검사, branchprotection.go:47-48)
   - **별도 `GET .../branches/main/protection` assertion으로 `required_status_checks`에 `verdict-gate` context 실재 확인** — verdict-gate required는 `CheckBranchProtection`이 아니라 이 GET이 검증한다(누락 시 verdict 게이트 없이 count=0이 되는 false-green 봉쇄 — codex #73)
   - `GET` diff로 의도한 두 필드 외 **무변경 실측**(silent drop 검출)
   - 테스트 PR로 **count=0에서 code-owner·verdict-gate가 실제 blocking인지 거동 실측**
     (Phase-A 실측은 이전되지 않는다 — count≥1 실측 ≠ count=0 거동)
4. **실패 시 원자 롤백**: 어느 검증이든 실패 → 즉시 스냅샷으로 PUT 롤백, Phase A 유지.
5. **순차 편집 금지**: count=0인데 verdict 미등재인 무-게이트 창 금지. 부득이 순차면 verdict 등재를
   count 인하보다 먼저.

## 부트스트랩 예외 (ADR-0015 point 8) — 이 절차 실행 자체엔 무관, 사전 sacred PR용

- **자격**: "App-작성 경로가 미operational(App key 미프로비저닝 → mechanu[bot] 토큰 발급 불가)해
  chnu-kim 작성일 수밖에 없는 sacred PR"에만. 파일-존재 기준 아님.
- **완화하지 않는 것이 1순위**: 부트스트랩 sacred PR은 **`gh pr merge --admin`(per-merge bypass)** 으로 머지한다 —
  branch protection 설정을 전혀 바꾸지 않으므로(해당 1건 머지에만 적용) **완화 창 자체가 없고 롤백할 상태도 없다**.
  전제: `enforce_admins=false`(현 실측) + 머지 수행자 admin. (이 레포의 부트스트랩 sacred PR들이 실제로 이 경로로 머지됐다.)
- **완화 경로는 admin bypass 불가 시(예: `enforce_admins=true`)만의 최후 수단**이며, 개시 전 다음 넷이 **mandatory**
  (하나라도 없으면 개시 금지 — fail-closed):
  1. 완화 **전** `GET .../protection` **전체 스냅샷 저장**(복원 payload 확보)
  2. **branch-global 프리체크**: 다른 open PR 부재/draft화 + direct-push 금지 재확인 (완화는 브랜치 전역이라 특정 PR에 한정 불가)
  3. **구체적 데드라인 + 독립 실행 가능한 복원 수단** — 세션·오퍼레이터 실패 시 실행할, 두 번째 오퍼레이터가 보유한
     복원 명령. **알림은 롤백이 아니다** — 알림만으로 갈음 금지
  4. 복원 후 `CheckBranchProtection` + `GET` diff로 **원상태 실측 + 증거 기록**(복원도 destructive PUT이라 self-assert 금지)
- App key 프로비저닝 **이후** 모든 sacred 변경은 정상 App-작성 PR 경로.
