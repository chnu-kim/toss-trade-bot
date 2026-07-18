---
id: "0015"
status: Accepted
date: 2026-07-18
deciders: [chnu-kim]
domain: [loop-governance, ci, auth]
protects: [enforcement-integrity, live-execution-human-gate]
supersedes: []
superseded_by: null
amends: ["0011"]
verification:
  - reviewer: architect (grounding — 선행 이슈 상태·ADR 번호·classifier 이력 재확인)
    date: 2026-07-18
    verdict: 개정 전 재확인 완료. (1) 다음 ADR 번호는 0015 — docs/adr/0014-reconciler-escalation-bounded-recount.md가 이미 존재해 0014 재사용 금지(README.md). (2) 선행 이슈: #46(narrowing spec)·#47(pr-creation.yml)·#48(verdict-gate.yml)·#49(presence-check c-1/c-2) CLOSED, #50(Phase B 절차서)만 OPEN·agent:blocked. #46 CLOSED는 spec·워크플로 머지일 뿐 런타임 narrowing 실행 완료가 아님(MEMORY '미실행' book) — 잔여 위험으로 명문화. (3) ADR-0011 point 1의 classifier 4연속 차단 실측을 로컬-materialize 금지의 근거로 재확인하되 durable-잔존 논거로 독립 지지.
  - reviewer: architect (3-렌즈 적대 하드닝 반영 — flip 메커니즘·핸드오프 순서·완료 판정 locus)
    date: 2026-07-18
    verdict: 3개 적대 렌즈의 blocking 7건 + 중대 6건을 소스 실측으로 grounding 후 전량 반영. (BLOCKING) point 7이 require_code_owner_reviews 보존·PUT의 전체-replace 시맨틱·flip 순서의 검증 위상을 누락 → flip-and-verify-or-rollback 트랜잭션으로 재작성. (BLOCKING) point 2/4가 verify-credential-narrowing.sh 실행을 에이전트-가능으로 오분류 → loop-runtime/사람 축으로 이관(스크립트 헤더 mandatory 인자 실측). (BLOCKING) SSH teardown을 '실측'으로 오서술 → 단언 성격 정직 book + API 열거 실측 승격. (BLOCKING) 핸드오프 열거가 dependency 역순 → 실행 순서로 재배열. (BLOCKING) 부트스트랩 자격을 파일-존재로 판정하는 자기모순 → operational 기준으로 재정의. 근거: branchprotection.go:47-48(code-owner fail-closed), verdict-gate.yml:505-512(required context 'verdict-gate' head-SHA 게시), ADR-0011 line 44(count=0·code-owner 동시 표현 config 검증) 및 lines 48-49(거짓 '문서 검증' 정정 이력). 렌즈 어느 것도 기각 없음 — 전부 새 결함.
  - reviewer: chnu-kim
    date: 2026-07-18
    verdict: approved (결정 자율 위임 — ADR-0009 point 1 경로: grilling 상대를 3-렌즈 적대 패널 + codex 리뷰로 대체. 이 세션 사용자 위임 "App키 취급을 에이전트에 위임하되 먼저 ADR-0011 개정"에 대해, 물리 차단(classifier)·durable-잔존으로 로컬 흐름이 불가함을 확인하고 sanctioned 사람-액션 부트스트랩으로 확정. ADR-0011 amend 포인터가 이 결정을 지배 문서로 참조하므로 같은 ship-ready PR에서 Accepted 확정(split-brain 방지). 최종은 PR admin 머지).
---

# ADR-0015: Phase A/B 활성화의 물리적으로 불가능한 두 스텝(App key materialize·사람계정 narrowing)은 대화 위임으로 옮기지 않고 sanctioned 사람-액션 부트스트랩으로 확정한다

- **Status**: Accepted (3-렌즈 적대 하드닝 + codex 리뷰 · ADR-0009 point 1 위임 승인)
- **Date**: 2026-07-18
- **Deciders**: chnu-kim
- **관련 이슈/PR**: #50 (Phase B 진입 절차서 — 직접 후속), #46/#47/#48/#49 (선행 인프라, 전부 CLOSED), ADR-0011 (지배 결정 — 이 ADR이 amend)

## Context

사용자(레포 소유자)는 **"에이전트에게 App키 취급 + env 프로비저닝을 위임하되, 먼저 ADR-0011을 개정한다"**고 결정했다. 이 ADR은 그 개정이다. 그러나 위임을 문자 그대로 — "에이전트가 로컬 세션에서 App private key를 다뤄 loop-pr environment에 넣는다" — 실현하는 것은 **물리적으로 불가능**하다: ADR-0011 Context가 실측한 대로, App private key를 로컬 오케스트레이터 세션으로 가져오려는 시도가 Claude Code 시스템 classifier에 **4연속 차단**됐고, 이 실측이 ADR-0011 point 1의 원칙("App 자격증명은 GitHub Actions 실행 컨텍스트 밖으로 나오지 않는다")으로 승격됐다. 따라서 이 ADR은 위임을 존중하되 **정직하게** 다음을 가른다: 에이전트가 실제로 할 수 있는 것, 물리적으로 못 하는 것, 그리고 후자를 대화 우회가 아니라 절차로 처리하는 sanctioned 부트스트랩.

이 결정을 강제하는 힘:

- **로컬 materialize는 두 축에서 기각된다 — classifier에만 의존하지 않는다.** (물리) classifier 4연속 차단은 이 세션에서도 재현되는 구조적 신호다. (durable-잔존) 설령 우회에 성공해 키가 로컬 에이전트 컨텍스트에 존재하면, 그 노출은 세션을 넘어 durable하며 rotate 전까지 무효화 불가다 — ADR-0011 Alternatives의 "성공했더라도 App key가 로컬에 존재하는 것 자체가 유출 표면"과 같은 논거. 그래서 로컬 흐름은 classifier가 완화되더라도 기각 유지다.
- **narrowing은 App 권한이 아니라 사람계정(chnu-kim) 자격증명을 revoke/교체한다**(ADR-0011 point 5 ②). admin classic PAT 제거·SSH authentication 등록 해제·fine-grained PAT(Contents RW + Pull requests read-only + Issues RW + Administration read-only) 발급은 **그 계정 소유자의 자격증명 조작**이다. 에이전트가 이를 대행하려면 chnu-kim의 admin 세션을 에이전트 컨텍스트에 들여야 하는데, 그것이 정확히 narrowing이 없애려는 벡터(loop가 admin/approve-capable 자격증명을 쥐는 것 — ADR-0011 point 5 (a)/(c))이다. 자기모순이므로 narrowing은 irreducible 사람 액션이다.
- **선행 인프라는 존재하나 '이슈 close ≠ 실행 완료'다.** #46(narrowing spec)·#47(pr-creation.yml)·#48(verdict-gate.yml)·#49(presence-check c-1/c-2)는 전부 CLOSED이고 #50(Phase B 절차서)만 OPEN·blocked다. 인프라 산출물(워크플로·presence-check 코드)은 main에 있다. 그러나 #46 CLOSED는 **spec·워크플로 머지**일 뿐 런타임 narrowing(admin 토큰 실폐기·PAT 교체·SSH 해제의 capability 실측 통과)의 실행 완료가 아니다 — MEMORY가 "#46 narrowing 미실행(오클로즈 확인 필요)"로 book한 상태다. 이슈 상태를 완료 증거로 읽는 것은 이 레포 거버넌스가 단속하는 자기보고 신뢰형(PR#22)이다.
- **활성화는 두 sacred invariant를 약화하지 않는다.** `live-execution-human-gate`: 이 ADR은 주문 authorize 경로를 일절 건드리지 않는다(ADR-0004/0007 계약 그대로, ADR-0011 point 12). `enforcement-integrity`: 활성화 절차가 잘못되면(예: ② 미완 상태로 App-작성 PR 구간 개시, 게이트 내린 뒤 사후 검증, PUT로 code-owner 게이트 silent drop) 약화될 수 있으므로 이 ADR이 그 순서·판정·flip 트랜잭션을 못박아 지킨다.
- **`require_code_owner_reviews`가 sacred/live-execution 사람 게이트의 실제 스위치다(적대 렌즈-1 grounding).** 이 레포에서 sacred 경로(ADR·`.github/workflows/**`·CODEOWNERS)의 사람 리뷰를 강제하는 단 하나의 branch protection 필드는 `required_pull_request_reviews.require_code_owner_reviews == true`다 — `internal/enforcement/branchprotection.go`가 이 값이 false거나 확인 불가면 presence-check를 fail-closed시킨다(라인 47-48). 이 사실이 아래 point 7의 flip 스펙을 지배한다.

이 결정을 가르는 힘(전제, 재-grilling 안 함):

- **App 자격증명 로컬 금지(ADR-0011 point 1).** 물리 차단 + durable-잔존 두 축으로 확정된 hard line — 대화 승인으로 옮기지 않는다.
- **narrowing 우선 순서(ADR-0011 point 5·Consequences).** narrowing이 App key 프로비저닝·workflow PR 머지·최종 flip에 선행한다. App-작성 PR과 approve-capable loop 자격증명이 공존하는 창((c) 벡터)을 만들지 않는 순서 제약이다.
- **완료 판정 = capability 실측(ADR-0011 라운드 7).** ②의 완료는 사람 단언이 아니라 loop 실행 컨텍스트가 resolve하는 자격증명으로 admin-only 동작(PUT protection)과 승인 제출(APPROVE)이 둘 다 거부됨을 실측(`scripts/verify-credential-narrowing.sh` 7-ix)해야 성립한다.
- **fail-closed 활성화(ADR-0011 point 5 도입부).** hard precondition ①~⑤ 중 하나라도 미충족이면 Phase A(사람 승인 체제)에 남는다.

## Decision

**위임 결정을 존중하되 안전 라인은 대화로 옮기지 않는다.** 에이전트는 물리 경계 안쪽에서 활성화의 최대치를 수행하고, 물리적으로 불가능한 두 스텝(App key materialize·사람계정 narrowing)과 그에 딸린 Administration:write 사람 액션은 **sanctioned 사람-액션 부트스트랩**으로 확정한다 — 대화 우회가 아니라 문서화된 절차로. 결론은 **"라인은 유지, 여기 sanctioned 부트스트랩"**이다.

### 활성화 스텝의 물리적 분해

1. **대화 위임은 sacred 라인을 옮기지 못한다 — ADR-0011 point 1을 재확인한다.** App private key와 그 파생 토큰의 로컬 오케스트레이터·서브에이전트 세션 materialize 금지는 대화 승인으로 완화 불가능하다. 근거 두 축: (물리) classifier 4연속 차단은 이 세션에서도 우회 불가한 구조적 신호, (durable-잔존) 우회 시 노출이 세션을 넘어 durable해 rotate까지 무효화 불가. 위임의 실현 형태는 "에이전트가 로컬에서 키를 다룬다"가 아니라 **"에이전트가 물리 경계 안쪽 최대치를 하고, 못 하는 두 스텝은 사람 액션으로 남긴다"**로 재규정된다.

2. **에이전트가 로컬에서 할 수 있는 것 vs 물리적으로 못 하는 것을 명확히 가른다.**
   - **할 수 있는 것(에이전트 admin 범위)**: `.github/workflows/pr-creation.yml`·`verdict-gate.yml` 저작·머지(이미 #47/#48로 main에 존재) · presence-check check (c-1)(checkPRCreationWorkflow)·(c-2)(checkLoopPRAuthor) 구현(#49 완료) · runbook·절차서 문서 저작 · `GET /branches/main/protection` 조회(Administration:read — presence-check check (b) 및 flip 스냅샷) · protection-probe 브랜치 절차 준비·문서화 · red-team 인젝션 테스트 PR 저작 · **`scripts/verify-credential-narrowing.sh`의 문법·prereq 리허설(dry-run)** — 아래 point 4 단서를 반드시 함께 읽는다.
   - **물리적으로 못 하는 것**: (i) **App private key PEM을 loop-pr environment 시크릿에 등록** — 키가 로컬 세션에 존재해야 하는데 classifier가 봉쇄(point 1). (ii) **사람계정(chnu-kim) 자격증명 narrowing** — admin 토큰 revoke · SSH authentication 등록 해제 · fine-grained PAT 발급. 이는 그 계정 소유자의 자격증명 조작이라 에이전트가 대행하려면 없애려는 admin 세션을 세션에 들여야 하는 자기모순(Alternatives). (iii) **Administration:write 사람 액션** — protection-probe 임시 규칙 생성/제거 · 최종 branch protection flip · 전역 에스컬레이션 해제(공유 인프라, ADR-0008 point 7(c)). (iv) **`verify-credential-narrowing.sh`의 ②-완료 판정용 *실행*** — 아래 point 4 참조(에이전트 로컬 세션 불가).

3. **물리적으로 못 하는 스텝을 sanctioned 사람-액션 부트스트랩으로 정의한다 — 대화 우회가 아니라 절차.** ADR-0011 Consequences의 사람 액션 순서를 **실행 절차서 `docs/runbooks/phase-b-entry.md`(#50, 아직 미작성)**로 구체화한다. 각 스텝에 **확인주체·확인방법·통과판정기준·실패시경로**를 명시한다. **절차서는 로마숫자 나열이 아니라 실제 실행 순서로 서술한다**: narrowing → probe → App key rotate+environment 등록 → 최종 flip(아래 point 6). narrowing이 App key 프로비저닝·workflow PR 머지에 선행하는 순서 불변을 유지하고, **각 스텝에 "선행 스텝 완료 실측 전 다음 스텝 착수 금지"를 명문화**한다.

4. **②(narrowing) 완료 판정은 이슈 상태·사람 단언이 아니라 capability 실측이며, 그 실측 스크립트의 *실행*은 에이전트 로컬 액션이 아니다(적대 렌즈-2 F1·렌즈-1 부수 반영).** #46 CLOSED는 spec·워크플로 머지일 뿐 런타임 narrowing 실행 완료가 아니다(MEMORY '미실행' book).
   - **완료 조건**: **loop 실행 컨텍스트가 실제로 resolve하는 자격증명으로** `PUT .../branches/main/protection`과 `POST .../pulls/{n}/reviews`(APPROVE) 시도가 **둘 다 거부됨**을 `verify-credential-narrowing.sh` 7-ix로 실측해야 ② 완료다. admin 토큰 실폐기는 구 admin 토큰으로 admin 동작 시도가 거부됨을 실측한다.
   - **실행 locus(F1)**: 이 스크립트는 헤더가 mandatory로 `NEW_PAT`(narrowing 후 loop fine-grained PAT)·`OLD_ADMIN_TOKEN`(폐기했어야 할 구 admin classic PAT — 사람계정 자격증명)·`SSH_TEARDOWN_CONFIRMED=1`(사람 수동 확인)을 요구하고 "**loop 실행 컨텍스트에서 실행하라 — (7-ix)는 ambient gh 자격증명을 써서 admin 토큰 잔존을 잡는다**"고 명시한다. 이 입력·컨텍스트는 **에이전트 로컬 세션에서 도달 불가**하다(둘 다 에이전트가 못 쥐는 자격증명, 그리고 로컬 ambient gh 자격증명은 loop가 CI에서 resolve하는 자격증명이 아니다). 따라서 스크립트의 **②-완료 판정용 실행은 loop 실행 컨텍스트(CI)/사람 축**이고, **에이전트는 문법·prereq 리허설(dry-run)과 결과 해석·후속 배선만** 한다. 에이전트가 로컬에서 "돌렸다"고 ②를 green으로 읽는 것은 이 ADR이 규탄하는 false-completion(PR#22)이다.
   - **SSH leg는 '실측'이 아니라 단언임을 정직하게 book한다(렌즈-3 Finding2)**: 목록 8은 SSH auth 상태를 신뢰성 있게 프로브하지 못해 `SSH_TEARDOWN_CONFIRMED=1`(오퍼레이터 단언)에 의존한다. '단언≠실측'을 논지 축으로 세운 이 ADR이 (b) check-위조 enabler인 SSH push capability를 단언으로 판정하면 내부 비일관이다. 따라서 절차서는 (i) GitHub API로 **계정 authentication key를 전수 열거**해 각 key로 push 거부를 mandatory 실측하고(다중 auth key 미인지 봉쇄), (ii) API로 못 잡는 잔여만 단언으로 두되 **그 단언성을 잔여 위험으로 명문화**한다.

5. **이 세션에서 에이전트가 admin으로 수행 가능한 최대 활성화 범위를 명시한다.** precondition ① 산출물(verdict-gate required 후보·presence-check 3-pillar) 검증 · ②의 실측 스크립트 **문법·prereq 리허설과 결과 해석·배선**(실행 자체는 아님 — point 4) · ④ protection-probe 절차 준비·문서화 · ⑤ red-team 인젝션 실측 대부분(point 4 (g)에 따라 leg별 — codex leg·N-of-2면 Claude leg 각각). **그러나 이 범위의 어느 것도 main의 count를 내리지 않고, App key를 environment에 넣지 않으며, 사람계정 자격증명을 바꾸지 않는다** — 그 셋이 정확히 물리 경계 밖이다.

6. **남는 irreducible 사람-액션 핸드오프를 실제 실행 순서로 열거한다(적대 렌즈-3 Finding1 반영).** ADR-0011 라운드 7이 `(3)→(1)` 순서 + 프로비저닝-시점 rotate로 닫은 durable App-key 탈취 창은, 핸드오프를 dependency 역순(provision을 narrowing보다 먼저)으로 나열하면 stateless 오퍼레이터가 순서를 뒤집어 재개방된다. 따라서 **로마숫자를 실제 실행 순서로 정렬하고 각 스텝에 "선행 완료 실측 전 다음 금지"를 붙인다**:
   1. **사람계정 narrowing 실행** — fine-grained PAT 발급 + admin 토큰 revoke 실측 + SSH authentication 등록 해제(계정 auth key 전수 열거·push 거부 실측, point 4). **완료 판정 = `verify-credential-narrowing.sh` 7-ix green(loop 실행 컨텍스트).**  [ADR-0011 (3)]
   2. **protection-probe** — 일회용 브랜치에 임시 규칙을 걸어 flip payload의 문법·권한을 리허설(main 무접촉). 단 main-스코프 강제 거동(count=0 code-owner/verdict)은 여기서 검증 불가(point 7).  [ADR-0011 (4a)]
   3. **App key rotate + loop-pr environment 등록** — 기존 key 전부 revoke(구 key JWT가 GitHub에 거부됨 실측) 후 새 key 생성·등록 + deployment branch policy(main) 설정. narrowing(1) 완료 실측 후에만 착수 — 그 전엔 loop가 admin으로 environment 격리를 무력화해 프로비저닝 key/installation token을 탈취할 수 있다(Alternatives 마지막 항목).  [ADR-0011 (3)→(1)]
   4. **최종 branch protection flip** — flip-and-verify-or-rollback 단일 트랜잭션(point 7).  [ADR-0011 (4b)·③]
   5. **전역 에스컬레이션 해제** `workflow_dispatch`(잡 최상단 actor==chnu-kim 가드).
   이 다섯은 에이전트 대행 불가다.

### branch protection flip은 flip-and-verify-or-rollback 트랜잭션이다 (적대 렌즈-1 BLOCKING-1/2·중대-6, 렌즈-2 F2/F3 반영)

7. **최종 flip은 "완전-payload PUT → 즉시 검증 → 실패 시 원자 롤백"의 단일 트랜잭션이다.** 초안이 "단일 원자 PUT"의 원자성만 강조하고 (i) `PUT`의 전체-replace 시맨틱, (ii) code-owner 필드 보존, (iii) main-스코프 강제의 검증 위상을 누락한 것을 정정한다.
   - **(a) 스냅샷**: flip 전 `GET .../branches/main/protection`으로 현재 protection **전체**를 스냅샷한다. `PUT`은 patch가 아니라 **전체 replace**이므로 payload에서 빠진 필드(`enforce_admins`, `restrictions`, 기존 `required_status_checks.contexts`, `required_linear_history`, `require_code_owner_reviews`)는 default/null로 리셋된다(BLOCKING-2).
   - **(b) 완전 payload 재구성**: 스냅샷을 기반으로 오직 두 가지만 변경한 **완전한** 오브젝트를 만든다 — ① `required_approving_review_count` 1→0, ② required status check contexts에 **`verdict-gate`**(verdict-gate.yml이 PR head SHA에 게시하는 required check-run 이름, verdict-gate.yml:505-512) 추가. 나머지는 **명시 보존**한다: `require_code_owner_reviews=true`(BLOCKING-1 — 이 필드가 사라지면 count=0 + code-owner off로 risk:critical PR이 verdict만 통과하면 사람 리뷰 없이 머지된다), `enforce_admins=true`, 기존 required contexts(`build · vet · gofmt · test-race`, app_id 고정 — ADR-0011 Context 실측), `restrictions`, `required_linear_history`, `required_pull_request_reviews` 블록 자체(제거 시 direct-push로 게이트 전체가 PR 경로째 우회 — ADR-0011 라운드 7).
   - **(c) PUT → 즉시 검증**: 단일 PUT 직후 곧바로 (1) `internal/enforcement`의 `CheckBranchProtection` 재통과(require_code_owner_reviews·verdict-gate required 확인), (2) `GET` diff로 의도한 두 필드 외 **무변경 실측**(silent drop 검출), (3) **테스트 PR로 count=0에서 code-owner 리뷰와 verdict-gate가 실제로 blocking인지 실측**한다.
   - **(d) count=0 code-owner 거동은 Phase-A 실측이 이전되지 않는다(F2)**: 기존 code-owner 강제 실측은 count≥1에서 취해졌고(CODEOWNERS 헤더·ADR-0011 Context), count=0에서도 code-owner 리뷰가 blocking인지는 별개 상태다. ADR-0011 line 44는 두 설정의 **동시 표현 가능**을 문서 검증했으나 이는 config 표현성이지 런타임 blocking 거동이 아니며, 이 레포는 거짓 '문서 검증 완료' 라벨에 반복 데인 이력(ADR-0011 lines 48-49)이 있다. 따라서 count=0 blocking은 (c)-(3)에서 **거동으로 실측**해야 한다.
   - **(e) main-스코프 게이트의 권위 있는 최초 검증은 불가피하게 flip 직후다 — probe로 대체 불가(중대-6, 초안 정정)**: required status check와 code-owner 강제는 main branch protection에 스코프된다. probe 브랜치의 임시 규칙은 payload **문법·권한 리허설**까지만 정직한 범위이고, "count=0 + verdict-required + code-owner"가 main에서 어떻게 게이팅되는지는 main에 실제 PUT하기 전엔 검증 불가다. 그러므로 초안의 "flip 후 재확인은 추가 확증일 뿐, 최초 검증은 ④ probe에서 이미 끝나 있어야"는 **거짓이며 정정한다** — 최초 실검증은 flip 직후이고, 이 노출을 fail-closed 순서로 뒤집지 않도록 트랜잭션으로 감싼다.
   - **(f) 실패 시 원자 롤백**: (c)의 어느 검증이든 실패하면 **즉시 (a) 스냅샷으로 PUT 롤백**하고 Phase A에 남는다. verdict required context가 head SHA에 붙는 dispatch→PR 바인딩(PR별 `request-verdict` dispatch)이 operational함을 flip 전 precondition으로 확인한다 — 없으면 required check가 안 붙어 liveness 붕괴 또는 'this PR의 verdict' 미보증(F3).
   - **두 번의 별도 편집(순차 적용)은 금지**: count=0인데 verdict check가 아직 required가 아닌 무-게이트 창을 post-② loop의 잔존 merge-호출 능력이 악용 가능하다. 부득이 순차라면 verdict `verdict-gate` required 등재를 count 인하보다 반드시 선행시킨다.

### 부트스트랩 예외의 봉쇄 (openQuestion 결정 — 적대 렌즈-1 BLOCKING-3·중대-4·중대-5, 렌즈-3 Finding3 반영)

8. **부트스트랩 완화는 최초 1회성 예외이지 상시 운영 경로가 아니며, 그 *자격*은 파일 존재가 아니라 App-작성 경로의 operational 여부로 판정한다.**
   - **자격 정의(BLOCKING-3)**: 부트스트랩 예외는 "workflow 파일이 없는 PR"이 아니라 **"App-작성 경로가 아직 operational하지 않아(App key 미프로비저닝 → `mechanu[bot]` 토큰 발급 불가) `chnu-kim` 작성일 수밖에 없는 sacred PR"**에만 해당한다. `pr-creation.yml`은 이미 main에 존재하므로(#47 CLOSED) 파일-존재 기준을 쓰면 ADR-0015 PR이 부트스트랩 자격에서 탈락해 **머지 교착**이 되거나, 교착을 피하려 자격을 임의 확대해 백도어가 열린다. close≠complete를 자기 부트스트랩 자격에도 일관 적용한다. 이 정의로 ADR-0015 PR과 최초 pr-creation.yml PR은 부트스트랩 예외이고, App key 프로비저닝 **이후**의 모든 sacred 변경은 예외 없이 정상 App-작성 PR 경로(push → `repository_dispatch` → `mechanu[bot]` 작성 → 사람 승인)를 거친다.
   - **branch-global 노출 창(중대-4)**: `PUT protection`의 count/enforce_admins 완화는 **브랜치 전역**이라 특정 PR에만 걸 수 없다 — 완화 창 동안 열린 모든 PR·direct-push가 머지 가능해진다. 절차서는 **완화 창 개시 전 (i) 다른 open PR 부재 또는 draft화 확인, (ii) direct-push 금지 재확인**을 mandatory 프리체크로 넣는다.
   - **crash-safe 복원 backstop(중대-5·Finding3)**: 부트스트랩 구간은 정의상 pre-narrowing이라 loop가 admin/workflow/approve-capable `chnu-kim`을 쥔 적대 주체다. 완화 후 복원 전 세션 크래시는 서버-side에 durable한 무보호 main을 남긴다. 따라서 (i) 완화에 **짧은 만료** 또는 복원 미확인 시 **외부 알림**을 걸고, (ii) 같은-세션 복원을 규율이 아니라 **기술 강제**로, (iii) **복원 후 `CheckBranchProtection` + `GET` diff로 원상태 실측**(복원도 destructive PUT이므로 self-assert 금지)한다.

### 이 ADR을 enforcement-integrity 우산에 편입 (twin-artifact)

9. 이 ADR은 게이트 강제 메커니즘의 활성화 절차·순서·flip 트랜잭션을 정의하므로, 미래의 loop가 이를 조용히 개정하면 enforcement가 약화된다. **같은 PR에서** (a) `.github/CODEOWNERS`에 `/docs/adr/0015-*.md @chnu-kim`을 추가하고, (b) presence-check check (a)의 기대 sacred 경로 집합(`internal/enforcement/codeowners.go`의 `sacredRequiredPaths`)에 이 파일의 실제 경로를 반영하며 (c) `internal/enforcement/codeowners_test.go`에 0015 누락 fail-closed 케이스를 0011/0012/0013 패턴대로 추가하고, (d) ADR-0011 point 1·point 5·Consequences 사람 액션 순서에 이 ADR로의 amend 포인터를 추가하고, (e) runbook `docs/runbooks/phase-b-entry.md`를 저작한다(#50). CODEOWNERS는 frontmatter를 읽지 못하므로 정적 경로 등록이 유일한 방법(ADR-0010 point 4)이고, 문서만 바꾸고 강제 쌍둥이를 남기면 게이트가 거짓 안심한다(MEMORY twin-artifact-coupling).

## Alternatives considered

- **대화 승인으로 로컬 App key 흐름을 재개(위임을 문자대로 실현)** — 기각: classifier 물리 차단 + durable-잔존 두 축. 위임은 "형태를 바꿔" 존중한다 — 에이전트가 물리 경계 안쪽 최대치를 하고 못 하는 두 스텝만 사람 액션으로 남긴다. 안전 라인을 대화로 옮기는 것은 MEMORY hold-designed-gates-against-conversational-override의 정확한 실패형이다.
- **에이전트가 chnu-kim admin 세션을 빌려 narrowing을 대행** — 기각: narrowing이 제거하려는 바로 그 admin 자격증명(protection 편집·approve 위조 가능 — ADR-0011 point 5 (a)/(c))을 에이전트 컨텍스트에 들이는 자기모순이며, admin 자격증명의 로컬 존재 자체가 이 ADR이 닫으려는 유출 표면이다.
- **활성화 전체를 사람이 수동으로(에이전트 개입 0)** — 기각(과소 위임): 에이전트가 물리적으로 할 수 있는 것(워크플로·presence-check·runbook·probe 절차·red-team 실측·capability 스크립트 리허설)까지 사람에게 미루는 것은 사용자 위임 결정·자율성 선호(MEMORY user-autonomy-appetite)에 반한다. 라인은 물리 경계에 긋고 그 안쪽 최대치는 에이전트가 한다 — 단 안전설계(순서·완료 판정·flip 트랜잭션)는 타협 없이 엄밀하게.
- **에이전트가 `verify-credential-narrowing.sh`를 로컬에서 실행해 ②를 판정** — 기각(렌즈-2 F1): 스크립트는 mandatory로 `NEW_PAT`·`OLD_ADMIN_TOKEN`·`SSH_TEARDOWN_CONFIRMED`와 loop ambient 자격증명을 요구하며 이 중 어느 것도 에이전트 로컬 세션에서 도달 불가다 — 로컬 실행은 잘해야 INCONCLUSIVE(exit 1)이고, loop의 실제 runtime capability를 실측하지 못해 ②를 false-green으로 판정할 위험만 남긴다. 실행은 loop-runtime/사람 축, 에이전트는 리허설·해석·배선만.
- **SSH auth 등록 해제를 '실측'으로 서술** — 기각(렌즈-3 Finding2): 목록 8은 SSH를 신뢰성 있게 프로브 못 하고 오퍼레이터 단언(`SSH_TEARDOWN_CONFIRMED`)에 의존한다. '단언≠실측'을 논지 축으로 세운 ADR이 (b) check-위조 enabler를 단언으로 판정하면 PR#22 실패형의 재현이다. 계정 auth key 전수 열거 + push-거부 실측을 mandatory로 승격하고, API로 못 잡는 잔여만 단언으로 두되 잔여 위험으로 명문화한다.
- **flip을 "단일 PUT"으로만 서술(검증 위상·code-owner 보존·replace 시맨틱 생략)** — 기각(렌즈-1 BLOCKING-1/2·중대-6): `PUT`은 전체 replace라 빠진 필드가 리셋되고(특히 `require_code_owner_reviews`가 사라지면 sacred/live-execution 게이트 소멸), main-스코프 강제는 probe로 검증 불가하다. flip-and-verify-or-rollback 트랜잭션(point 7)으로 대체한다.
- **부트스트랩 자격을 workflow 파일 존재로 판정** — 기각(렌즈-1 BLOCKING-3): `pr-creation.yml`이 이미 존재하므로 파일-존재 기준은 ADR-0015 PR을 자격 탈락시켜 교착이거나 자격 임의 확대로 백도어를 연다. 자격을 "App-작성 경로 operational 여부(App key 프로비저닝)"로 정의한다(point 8).
- **부트스트랩 완화를 상시 운영 경로로 승인** — 기각: 반복되면 "일시적 완화"가 상시 백도어가 된다. 부트스트랩 예외를 App-작성 경로 미-operational sacred PR로 한정하고, 이후 sacred 변경은 정상 App-작성 PR 경로로 강제한다(point 8).
- **② narrowing 완료를 이슈 상태(#46 CLOSED)·오퍼레이터 단언으로 판정** — 기각(ADR-0011 라운드 7 재확인): 이슈 close는 spec·워크플로 머지일 뿐이고, 단언은 loop 실행 컨텍스트에 잔존한 admin 토큰(gh 캐시·keychain·env·credential helper)을 잡지 못한다. capability 실측(7-ix)만이 세 벡터 폐쇄의 linchpin인 ②의 완료를 ground-truth한다.
- **로컬 materialize 금지를 classifier 재현성에만 근거해 명문화** — 기각: classifier 동작이 세션-국한 우연일 가능성을 배제할 수 없으므로, 로컬 금지를 durable-잔존 논거로 독립 지지한다 — classifier가 완화돼도 로컬 흐름은 기각 유지. '영구 금지' 강도의 재검토 여지는 booked residual로 남긴다.
- **merge-호출 능력(Contents:write 잔존)을 이 ADR에서 구조적으로 봉쇄** — 기각(범위 밖): push 자체가 Contents RW를 요구하므로 제거는 loop 존재 이유를 없앤다(ADR-0011 Alternatives). 잔존은 게이트 우회가 아니다 — 허용/차단 판정은 branch protection이 한다. 별도 fine-grained scope 분리 등 구조적 봉쇄는 후속 /architect로 이관한다.
- **App key를 narrowing 전에 environment에 선-프로비저닝((3) 이전에 (1))** — 기각(ADR-0011 라운드 7 재확인): ② 완료 전 loop는 admin(deployment branch policy 편집으로 environment 격리 무력화) + workflow scope(exfil workflow push)를 쥐어 그 구간에 존재한 key/installation token은 탈취돼 Phase B까지 durable하게 잔존, 헤드라인 불변식을 falsify한다. (3)→(1) 순서 + 프로비저닝-시점 rotate가 필수이며, point 6의 핸드오프도 이 실행 순서로 열거한다.

## Consequences

- (좋음) **위임 결정이 존중되면서 sacred 라인이 유지된다** — 에이전트는 물리 경계 안쪽 최대치를 수행하고, 물리적으로 불가능한 두 스텝(App key materialize·사람계정 narrowing)과 Administration:write 사람 액션만 사람에게 남는다. "위임 거부"가 아니라 "물리 경계에 라인, 그 안쪽 최대 자율"이다.
- (좋음/정직성) **개정 전 재확인 결과 book**: #46/#47/#48/#49는 전부 CLOSED, #50만 OPEN·blocked. 인프라 산출물은 main에 존재한다.
- (알려진 잔여 위험 — 중대) **#46 CLOSED ≠ narrowing 실행 완료.** 이슈 상태와 런타임 사람 액션(admin 토큰 revoke·PAT 교체·SSH 해제의 capability 실측 통과)은 별개 축이다. **Phase A 진입/App-작성 PR 구간 개시 전 반드시 `verify-credential-narrowing.sh` 7-ix 실측(loop 실행 컨텍스트)으로 ② 완료를 ground-truth**하고, 미통과면 개시하지 않는다(fail-closed). MEMORY가 '#46 narrowing 미실행(오클로즈 확인 필요)'로 경고 — 활성화 착수 전 이 상태 재확인이 필수 선행이다.
- (알려진 잔여 위험 — 중대) **count=0 code-owner 거동은 flip 직후에야 권위 있게 실측 가능(F2·중대-6).** probe 브랜치는 payload 문법·권한 리허설까지만 정직한 범위다. flip을 flip-and-verify-or-rollback 트랜잭션으로 감싸 실패 시 즉시 스냅샷 롤백해 노출 창을 닫으나, 이 창은 트랜잭션 원자성으로만 유계이며 0이 아니다.
- (알려진 잔여 위험 — 중대) **SSH auth teardown 완전성은 API-열거 가능 key까지만 실측**되고 그 밖 잔여는 오퍼레이터 단언에 의존한다(Finding2) — 다중 auth key 미인지 시 (b) 벡터가 ② '완료'를 통과해 잔존 가능. 계정 auth key 전수 열거를 runbook 체크리스트로 강제하고 단언 leg를 잔여 위험으로 book.
- (booked residual) presence-check c-2의 transient straggler false-green(ADR-0011 point 10 — causal binding 강화 '결정하지 않음', 별도 /architect 필요)을 재-book.
- (booked residual) UNVERIFIED 2건((i) GraphQL enablePullRequestAutoMerge 요구 권한, (ii) App/bot 승인의 required count 카운트 여부 — ADR-0011 lines 48-49 정정)을 재-book. count=0 + required check 게이팅 설계는 bot-승인 가정에 의존하지 않으므로 미검증인 채 진행 무방하나 활성화 실행 전 명시적 재확인 대상이다.
- (booked residual) verdict required context `verdict-gate`의 PR-head-SHA 바인딩(PR별 request-verdict dispatch)이 operational하지 않으면 required check가 안 붙어 liveness 붕괴 또는 'this PR의 verdict' 미보증(F3) — flip 전 precondition으로 실측.
- (booked residual) merge-호출 능력이 Contents:write에 부수해 loop PAT에 잔존 — '게이트 판정은 branch protection이 한다'는 논증(운영 규율)에만 의존. 구조적 봉쇄는 후속 /architect로 이관.
- (booked residual) verdict 게이트는 LLM 판정이라 프롬프트 인젝션 완전 내성 증명 불가(ADR-0011 상속) — ⑤ red-team 실측은 상한을 올릴 뿐 보증이 아니며 epoch당 최대 M회 잔여 false-approve 예산이 남는다.
- (사람 액션 / irreducible handoff — **실행 순서로 열거**, ADR-0011 재확인·Finding1 반영) **(3)→(4a)→(1)→(4b)**: (1) 사람계정 PAT narrowing 실행 + admin 토큰 revoke 실측 + SSH auth 등록 해제(계정 auth key 전수 열거·push 거부 실측) — 완료 판정 7-ix green [(3)]. (2) protection-probe 임시 규칙 생성/제거(payload 리허설, main 무접촉) [(4a)]. (3) App key rotate + loop-pr environment 등록(narrowing 완료 실측 후에만) [(3)→(1)]. (4) 최종 branch protection flip-and-verify-or-rollback 트랜잭션(point 7) [(4b)·③]. (5) 전역 에스컬레이션 해제 workflow_dispatch(actor==chnu-kim). 각 스텝은 선행 완료 실측 전 착수 금지. 다섯 모두 에이전트 불가.
- (에이전트 액션 / 이 세션 최대 범위) precondition ① 산출물 검증 · ②의 실측 스크립트 **문법·prereq 리허설·결과 해석·배선**(실행 자체는 loop-runtime/사람) · ④ probe 절차 준비·문서화 · ⑤ red-team 인젝션 실측(leg별) · runbook `phase-b-entry.md` 저작(#50) · twin-artifact 배선(CODEOWNERS·sacredRequiredPaths·codeowners_test·ADR-0011 amend 포인터). 이 중 어느 것도 count를 내리거나 App key를 넣거나 사람계정 자격증명을 바꾸지 않는다.
- (제약 전파/부트스트랩) 이 ADR PR 자체가 부트스트랩 예외(App-작성 경로 미-operational sacred PR — point 8)다 — 사람이 protection을 일시 완화(완화 창 프리체크: 다른 open PR 부재·direct-push 금지) → 검토·머지 → **복원 후 CheckBranchProtection·GET diff 실측**하고 기록을 남긴다. 완화에 만료/알림 backstop을 걸어 크래시-durable 구멍을 막는다. 이 예외는 이 ADR·최초 pr-creation.yml에 한정된다.
- (검증 방법) 활성화 완결의 검증은 fail-closed 순서로 성립한다: ② narrowing capability 실측(7-ix, loop 컨텍스트) → ①/⑤ presence-check·red-team leg별 → ④ probe(payload 리허설, main 무접촉) → (1) rotate+environment → (4b) flip-and-verify-or-rollback 트랜잭션(즉시 CheckBranchProtection+GET diff+count=0 code-owner/verdict 실측, 실패 시 원자 롤백). 어느 단계 미통과면 Phase A에 남는다.
- (amend) 이 ADR은 ADR-0011을 supersede하지 않고(코어 결정 유효), **ADR-0011 point 1(로컬 금지 원칙)·point 5(Phase A/B precondition·② 완료 판정)·Consequences 사람 액션 순서를 "위임 하에서의 sanctioned 부트스트랩"으로 구체화**한다. **같은 PR에서 ADR-0011 그 세 곳에 이 ADR(0015)로의 amend 포인터를 추가**한다(stateless 독자가 ADR-0011만 읽고 위임=로컬 흐름으로 오인하지 않도록 — MEMORY twin-artifact-coupling).
- (후속) #50 절차서(`docs/runbooks/phase-b-entry.md`) 저작이 직접 후속이며 ①~⑤의 확인주체·확인방법·통과판정·실패경로 + 실행 순서 + flip 트랜잭션 + 부트스트랩 프리체크·복원 backstop을 담는다. narrowing 완료·activation 실행은 사람 핸드오프 게이트다.

