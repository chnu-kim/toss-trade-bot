---
id: "0016"
status: Proposed
date: 2026-07-23
deciders: [chnu-kim]
domain: [loop-governance, ci, auth]
protects: [enforcement-integrity]
supersedes: []
superseded_by: null
amends: ["0011", "0015"]
verification:
  - reviewer: multi-agent adversarial workflow (5 렌즈: fail-open·fail-closed-direction·twin-artifact·operator-execution·self-consistency)
    date: 2026-07-24
    verdict: 5라운드 적대 하드닝(캠페인 전체 90건 반영, blocking 25). 실측 grounding — enforce_admins는 모순이 아니라 시제 분류 누락(ADR-0011:39 stale 스냅샷·:73 규범 미충족·ADR-0015:94 부트스트랩 창 관측·:82 오분류)로 판정, 목표값=true·Step 0.5 독립 액션·W0(sacred 머지 불가 창)로 확정. #76은 blocksPhaseB=false(ADR-0011:62 point 4(a) 비특권 잡 배제 + secret_scanning/push_protection 둘 다 disabled 실측). 근거: 라이브 GET protection·verify-credential-narrowing.sh:234(enforce_admins=false→INCONCLUSIVE→exit1).
  - reviewer: codex (예정)
    date: null
    verdict: null
  - reviewer: chnu-kim (예정 — Accepted 승격·머지 게이트)
    date: null
    verdict: null
---

# ADR-0016: `enforce_admins=false`는 stale 기록이 아니라 의도된 부트스트랩 임시값이다 — narrowing 앞에서 true로 복원하고, 그 복원이 여는 "sacred 머지 불가 창(W0)"이 활성화 실행 순서를 지배한다. 이슈 #76은 Phase B blocking이 아니다

- **Status**: Proposed (적대 하드닝 전 — ADR-0009/MEMORY `adr-hardening-before-accept`에 따라 Accepted 전 적대 수렴 필요)
- **Date**: 2026-07-23
- **Deciders**: chnu-kim
- **관련 이슈/PR**: ADR-0011(지배 결정 — 이 ADR이 amend), ADR-0015(활성화 부트스트랩 — 이 ADR이 amend), ADR-0017(이 ADR이 확정한 flip 헌장의 메커니즘 구현), #76(CI 시크릿 게이트 신뢰 경계 — 이 ADR이 지위 판정), #46/#47/#50(전부 CLOSED — 증거 기록 위치 아님)

## Context

이 세션의 목표는 Phase B(loop 자율 머지) 활성화이고, 사용자는 사람 액션을 **이번 세션에 직접 실행**하기로 결정했다. 그런데 활성화 문서군을 실측으로 훑은 결과, **문서만 읽고 실행하면 다섯 지점에서 멈추거나 잘못 실행된다**. 이 ADR을 강제하는 힘은 전부 오늘(2026-07-23) 소스·라이브 API 실측이다.

### 1. `enforce_admins`에 대해 지배 문서군이 서로 다른 값을 전제한다

라이브 실측(`gh api repos/chnu-kim/toss-trade-bot/branches/main/protection`, 2026-07-23):

```
enforce_admins.enabled            = false
required_approving_review_count   = 1
require_code_owner_reviews        = true
required_status_checks.checks     = [{context:"build · vet · gofmt · test-race", app_id:15368}]
required_status_checks.contexts   = ["build · vet · gofmt · test-race"]   ← checks와 동시 존재
restrictions                      = (키 자체 부재)
```

문서군의 네 지점이 이 필드에 대해 두 값으로 갈린다:

- `docs/adr/0011-loop-pr-credential-flow.md:39` — Context "branch protection 실측(classic, main): … `enforce_admins=true`" (2026-07-08 스냅샷).
- `docs/adr/0011-loop-pr-credential-flow.md:73` — Decision point 5 Phase B 문단 "허용/차단 판정은 GitHub branch protection이 한다(`enforce_admins=true`는 admin의 **머지-시점** 우회를 막는다 — 단 protection 설정 편집은 막지 못하므로 아래 precondition ②가 필수다)".
- `docs/adr/0015-loop-pr-amendment-bootstrap-activation.md:82` — point 7(b) "나머지는 **명시 보존**한다: `require_code_owner_reviews=true`…, `enforce_admins=true`, 기존 required contexts…".
- `docs/adr/0015-…:94` / `docs/runbooks/phase-b-entry.md:142` — point 8 "전제는 `enforce_admins=false`(현 실측)와 머지 수행자의 admin 권한" / "(이 레포의 부트스트랩 sacred PR들이 실제로 이 경로로 머지됐다.)"

즉 **같은 ADR-0015 안에서 point 7(b)와 point 8이 같은 필드에 다른 값을 쓰면서 서로를 참조하지 않는다.** 그리고 이를 구체화한 `docs/runbooks/phase-b-entry.md:111`은 값을 아예 떨어뜨려 `enforce_admins`만 남겼다 — **런북을 문자 그대로 따르면 스냅샷의 false가 그대로 Phase B 최종 상태로 굳는다.** 이것이 오늘 문서군의 기본 경로이자 실질 fail-open이다.

### 2. `enforce_admins=false`인 한 hard precondition ②는 원리적으로 통과 불가다

`scripts/verify-credential-narrowing.sh`는 스스로 이 값에 의존한다:

- `:232-233` 주석 — "(7-viii) 게이트-미충족 PR에 merge 호출 시 branch protection이 거부. ★ **`enforce_admins=false`면 admin-user 토큰이 우회해 merge가 성공할 수 있어 유효 판정 불가**."
- `:235-239` — main의 `enforce_admins`를 GET해 `false`면 `inconc "7-viii" "enforce_admins=false — 유효한 판정 불가(먼저 활성화 후 재실측)"`.
- `:339-344` — INCONCLUSIVE가 하나라도 있으면 `exit 1`("판정불능을 남긴 채 ② 완료로 간주하지 말 것").
- `:36-39` 헤더 — "`0` mandatory 전 항목 PASS — ② 완료 판정 가능".
- `docs/runbooks/phase-b-entry.md:43` — "통과판정: 7-ix green(**exit 0**) + admin 토큰 폐기 실측 + push 거부 실측".

⇒ **연역**: `enforce_admins=false` → 7-viii INCONCLUSIVE → 스크립트 exit 1 → ② 미완 → ADR-0011 point 5의 fail-closed("①~⑤ 중 하나라도 미충족이면 Phase A에 남는다")로 Phase B 진입 불가. **즉 이 값은 "flip 시점의 payload 필드"가 아니라 "narrowing보다 앞서야 하는 선행 스텝"이다.** 이 연역은 새 정책이 아니라 이미 커밋된 fail-closed 체인에서 나온다.

### 3. narrowing은 이 벡터에 도달하지 못한다 — 자격이 토큰 스코프가 아니라 계정 role에서 나오기 때문

`gh api repos/chnu-kim/toss-trade-bot/collaborators` 실측: **collaborator는 `chnu-kim` 단독, `admin:true`**. narrowing(ADR-0011 point 5 ②)은 fine-grained PAT의 **선언 스코프**를 좁힐 뿐 그 PAT를 발급한 계정의 **repo role**을 바꾸지 못한다. 그래서 위 `verify-credential-narrowing.sh:232-239`가 narrowed `NEW_PAT`로 merge를 시도하면서도 "결과 해석 불가"를 자백한다 — 이 레포의 검증 스크립트 자신이 "토큰을 좁혀도 role 기반 우회는 남는다"를 코드로 인정한 것이다.

그런데 ADR-0011 point 5가 열거한 벡터는 (a) 설정편집 / (b) check-위조 / (c) 승인-위조 셋뿐이고, **admin-role 머지-시점 bypass는 어디에도 이름 붙여 분석돼 있지 않다.** ADR-0015 point 8이 `gh pr merge --admin`을 실제 부트스트랩 경로로 채택하면서 그 존재를 사실상 활용하고 있는데도 그렇다.

### 4. `enforce_admins`를 켜는 순간 sanctioned sacred 머지 경로가 하나도 없는 구간이 열린다

세 소스가 물리적으로 맞물린다:

- `docs/runbooks/phase-b-entry.md:52` — ③(App key + env)은 "**②(narrowing) 완료 실측 후에만.**"
- `docs/runbooks/phase-b-entry.md:140-142` — 부트스트랩 admin 머지의 **전제가 `enforce_admins=false`**.
- `docs/runbooks/phase-b-entry.md:150` — "App key 프로비저닝 **이후** 모든 sacred 변경은 정상 App-작성 PR 경로."

narrowing 완료 후 loop PAT은 `Pull requests: read`뿐이라(ADR-0011 point 5 ②) PR 생성조차 못 하고, 사람이 웹 UI로 `chnu-kim` 작성 sacred PR을 열어도 self-approval 차단(ADR-0011 Context — PR #42/#44 실측) + `require_code_owner_reviews=true` + admin bypass 부재로 **머지 불가 교착**이다.

### 5. 두 개의 순환 의존이 ②를 이번 세션에 어떤 순서로도 통과 불가능하게 만든다

- **APPROVE_TARGET_PR**: `scripts/verify-credential-narrowing.sh:26`이 "(mandatory) APPROVE 프로브 대상 — **반드시 비-chnu-kim 작성**"을 요구한다. 그러나 `gh pr list --state all` 실측상 열린 PR 0건이고 작성자는 전부 `chnu-kim`이며, 비-chnu-kim PR을 만드는 유일한 sanctioned 경로(`mechanu[bot]`)는 ③ 이후에만 존재한다. 미지정이면 mandatory 누락 → INCONCLUSIVE → exit 1.
- **7-viii의 destructive write**: 같은 스크립트 `:241`이 `MERGE_TARGET_PR`에 **실제 `PUT …/merge`를 시도**한다. 이 프로브의 대상이 main-base PR이면, narrowing이 실패했을 때 검증 스크립트가 **검증 대상(main)에 실제로 머지한다** — MEMORY `verification-scripts-fail-closed`("검증 대상에 destructive write 금지 → 일회용 타깃 격리")가 규탄한 패턴이 sacred 스크립트에 아직 살아 있다.

### 6. 사용자가 프레이밍한 "사람 액션 4스텝"은 실제 핸드오프 수보다 적고, 하나는 liveness를 붕괴시킨다

`ADR-0015 point 6`(`docs/adr/0015-…:70-76`)이 열거하는 것은 다섯이다(4스텝 + 전역 에스컬레이션 해제). 더 심각하게, **verdict LLM 시크릿 2종이 어느 스텝에도 없다**: `.github/workflows/verdict-gate.yml:742-752`(codex leg)과 `:1102-1111`(claude leg)이 각각 `CODEX_VERDICT_API_KEY`/`CLAUDE_VERDICT_API_KEY`에 명시적 `-z` 가드를 걸어 부재 시 `exit 1`한다. 오늘 실측상 네 시크릿(`MECHANU_APP_ID`, `MECHANU_APP_PRIVATE_KEY`, `CODEX_VERDICT_API_KEY`, `CLAUDE_VERDICT_API_KEY`)이 **전부 부재**이고 `loop-pr` environment는 존재하나 `deployment_branch_policy: null`(무제한) · `secrets.total_count: 0`이다. LLM 시크릿 없이 `verdict-gate`를 required로 등재하면 **모든 PR이 영구 미머지**가 된다 — fail-closed가 아니라 과-halt다.

### 7. 이슈 #76이 Phase B blocking으로 오인될 위치에 있다

`#76`은 `ci.yml`의 시크릿 스캔 스텝이 PR-통제 체크아웃에서 실행되는 것을 pwn-request 결함으로 프레이밍하며, "count=0이면 required check가 유일한 머지 게이트"를 전제한다. 그러나:

- `docs/adr/0011-…:62` point 4(a) 원문 — "(a) **비특권 잡**(`pull_request` 트리거, PR 브랜치 정의, `contents: read`, 시크릿 없음) … 이 잡의 모든 산출물(go test pass/fail, 로그, 아티팩트)은 loop-통제 결과이므로 **Phase B 머지 게이트의 신뢰 입력이 될 수 없다** … 적대적 loop에 대한 게이트 하중은 전적으로 (b)의 verdict check가 진다."
- `docs/adr/0011-…:85` point 7 원문 — "PR이 workflow가 아니라 **테스트 코드·스크립트를 고쳐** ci.yml check를 green으로 만드는 경로는 CODEOWNERS에 걸리지도 않고 **애초에 막을 대상도 아니다**."
- `.github/CODEOWNERS:84` + `internal/enforcement/codeowners.go:123-125` — `scan.sh`/`scan_test.sh`/`allowlist.txt`가 이미 개별 sacred 등재.
- `docs/runbooks/phase-b-entry.md:110` — flip은 `require_code_owner_reviews=true`를 명시 보존.

그리고 `#76` 본문이 "현재 완화"로 열거한 "GitHub Push Protection이 이 레포에서 실제 동작 중 — 실측됨"은 **오늘 거짓**이다(`gh api repos/chnu-kim/toss-trade-bot --jq .security_and_analysis` → `secret_scanning: disabled`, `secret_scanning_push_protection: disabled`, `visibility: public`).

### 8. 게이트-정의 런북 하나가 여전히 무보호다

`grep loop-pr-environment-provisioning .github/CODEOWNERS internal/enforcement/codeowners.go` → **0건**. 형제 문서 `phase-b-entry.md`는 `.github/CODEOWNERS:50` + `codeowners.go:88`로 보호된다. 그런데 narrowing-before-App-key 순서와 main-only deployment branch policy를 실제로 담고 있는 것은 provisioning 쪽이다 — ADR-0015 point 9(f)가 `phase-b-entry.md`를 sacred로 올린 근거("loop가 App key 프로비저닝을 narrowing 앞으로 옮기거나 rollback을 빼는 식으로 활성화 경로를 조용히 약화")가 **글자 그대로** 이 문서에 적용된다.

---

## Decision

**`enforce_admins`의 목표값은 `true`이며, 그 복원은 flip payload의 필드가 아니라 narrowing보다 앞선 독립 사람 액션(step 0.5)이다. 그 복원이 여는 "sacred 머지 불가 창(W0)"이 이번 캠페인의 실행 순서와 산출물 스코프를 지배한다. 이슈 #76은 Phase B blocking precondition이 아니다.**

### A. `enforce_admins` — 상태 모순의 해소

1. **`enforce_admins`의 Phase B 목표값은 `true`다. 문서군의 불일치는 "어느 기록이 stale인가"가 아니라 "세 종류의 문장이 시제 없이 병치돼 있다"이다 — 각각을 다음과 같이 재분류한다.**
   - `ADR-0011:39`은 **stale한 관측**이다(2026-07-08 스냅샷). 2026-07-09에 소유자가 PR #51/#52의 자가승인 교착 해소를 위해 의도적으로 비활성화했고, 2026-07-18(ADR-0015 point 8)·2026-07-23(오늘) 두 독립 실측이 `false`로 수렴한다. **stale한 것은 이 한 줄뿐이다.**
   - `ADR-0011:73`은 **규범 문장**이다 — "지금 true다"가 아니라 "Phase B의 머지 판정이 성립하려면 true여야 한다". stale이 아니라 **미충족**이다.
   - `ADR-0015:94`(및 `phase-b-entry.md:142`)는 **부트스트랩 창 한정 관측**이다. 그 창 안에서 참이고, 같은 point 8이 스스로 "완화 경로는 admin bypass가 불가능할 때(예: `enforce_admins=true`)만의 최후 수단"이라 써서 true로의 전이를 이미 예상한다.
   - **진짜 결함은 `ADR-0015:82`다** — 목표값 `enforce_admins=true`를 "나머지는 **명시 보존**한다" 목록에 넣어 **목표를 보존 대상으로 오분류**했다. flip 생성기가 이 프로즈를 문자 그대로 "스냅샷 값을 복사"로 읽으면 오늘의 `false`가 Phase B로 새고, 반대로 "true를 주입"으로 읽으면 "오직 두 필드만 변경한다"는 생성기 헌장을 깬다. 이 항목은 **보존 대상이 아니라 "PUT 전 mandatory precondition assert"로 재분류**한다(아래 point 3, 메커니즘은 ADR-0017).

2. **복원 시점은 narrowing(precondition ②) 직전의 독립 사람 액션(step 0.5)이며, 전용 서브리소스를 쓴다.** 명령은 `POST /repos/chnu-kim/toss-trade-bot/branches/main/protection/enforce_admins`다 — full-payload PUT이 아니므로 다른 필드가 리셋될 위험이 구조적으로 없고, 실패해도 원자적이며 `DELETE` 한 번으로 가역이다. 시점 근거는 취향이 아니라 Context 2의 연역이다: `false`인 한 `verify-credential-narrowing.sh`는 exit 0에 도달할 수 없고, 따라서 ②는 완료 판정에 도달할 수 없다.

3. **flip payload 생성기는 이 값을 바꾸지도 추론하지도 않는다 — `enforce_admins != true`인 스냅샷을 받으면 payload를 만들지 않고 abort한다.** override 플래그를 두지 않는다. 이유 둘: (i) 생성기가 값을 고쳐주면 오퍼레이터가 step 0.5를 건너뛸 수 있고, 그러면 **7-viii가 판정불능인 채 = narrowing이 미검증인 채로 flip이 성립**해 검증 순서 자체가 붕괴한다. (ii) ADR-0015 point 7(b)의 "오직 두 필드만 변경" 헌장을 지킨다. payload에 `enforce_admins: true`가 실리기는 하지만 그것은 **assert된 스냅샷 값의 운반**이지 생성기의 결정이 아니다. 구현 계약은 ADR-0017 point 6.

4. **ADR-0011 point 5의 벡터 목록에 (d)를 추가한다: admin-role 머지-시점 bypass.**
   > **(d) admin-role 머지-시점 bypass** — admin role을 가진 계정의 자격증명으로 `gh pr merge --admin`류의 per-merge bypass를 수행해, protection 설정을 **전혀 편집하지 않고** code-owner 리뷰 요건과 required status check를 통째로 우회하는 경로. **narrowing은 이 벡터에 도달하지 못한다** — 자격이 토큰 스코프가 아니라 계정 role에서 나오기 때문이며, `scripts/verify-credential-narrowing.sh:232-239`가 narrowed PAT로도 판정 불가임을 스스로 자백한다. **유일한 봉쇄 레버는 `enforce_admins=true`이고, 그 활성화는 precondition ② 앞이다.** 이 벡터를 구조적으로 제거하는 대안(loop 자격증명을 non-admin write 머신 계정으로 이관)은 ADR-0011 point 2(identity 분할)를 뒤집으므로 별도 `/architect` 포크로 book한다.

### B. 실행 순서 — 불변식 W0

5. **불변식 W0(sacred 머지 불가 창)을 명문화한다.**
   > **W0**: `enforce_admins`를 `true`로 올리는 순간부터 precondition ③(App key rotate + `loop-pr` environment 프로비저닝)이 완료될 때까지, **sanctioned sacred 머지 경로가 하나도 존재하지 않는다.** `chnu-kim` 작성 PR은 self-approval + code-owner 요건으로 막히고, admin bypass는 `enforce_admins=true`가 막고, `mechanu[bot]` 경로는 아직 미가동이다.
   >
   > 따라서 **precondition ②·④·③에 필요한 모든 sacred 아티팩트는 step 0.5 이전에 main에 있어야 한다.** W0가 열리기 전에는 PR을 몇 개로 쪼개든 비용이 admin-머지 이벤트 1회씩뿐이지만, W0가 열린 뒤에는 개수가 아니라 **존재 자체**가 불가능하다.
   >
   > W0 안에서 부득이 sacred 변경이 필요해지면 유일한 경로는 `phase-b-entry.md:143-149`의 4-mandatory 완화 절차이며, 그 (iii)항은 이 레포에서 충족 불가능하므로 아래 point 10의 대체 절차를 따른다.

6. **사람 액션은 4스텝이 아니라 6개의 순차 핸드오프다. 각 스텝은 선행 스텝의 완료 실측 전 착수 금지다.**
   - **step 0 — 잔여 sacred PR 전부 머지.** 이 캠페인의 모든 에이전트-저작 sacred 산출물(ADR-0016/0017, provisioning 런북 sacred 등재, flip 생성기, 검증 스크립트, 완결성 테스트)을 `gh pr merge --admin`으로 머지한다. 착수 전 `gh pr list --state open`으로 미머지 sacred PR이 0건임을 확인한다.
   - **step 0.5 — `enforce_admins` 활성화.** `POST …/protection/enforce_admins` → `GET`으로 `{"enabled":true}` 확인 → 전체 `GET` diff로 이 필드 외 무변경 확인. **이 액션이 W0를 연다.**
   - **step 1 — narrowing(precondition ②).** 완료 판정 = `verify-credential-narrowing.sh` exit 0. 아래 point 7의 두 순환 의존 해소가 선행돼야 한다.
   - **step 2 — protection probe(precondition ④).** main 무접촉 일회용 보호 브랜치. flip payload 리허설을 포함한다(ADR-0017 point 8).
   - **step 3 — App key rotate + `loop-pr` environment + **H4**.** 구 key JWT `GET /app` → 401 실측, 새 key 등록, `deployment_branch_policy`를 main 단일로 제한(오늘 `null`=무제한), 그리고 **verdict LLM 시크릿 2종(`CODEX_VERDICT_API_KEY`·`CLAUDE_VERDICT_API_KEY`) 등록**. 등록만으로 끝내지 않고 테스트 dispatch 1회로 `-z` 가드를 넘어 leg가 실제 실행됨을 확인한다. **W0는 여기서 닫힌다.**
   - **step 4 — flip(precondition ④b).** ADR-0017의 생성기·verify를 쓴다.
   - **step 5 — 전역 에스컬레이션 해제** `workflow_dispatch`(actor==chnu-kim). 활성화 자체의 precondition은 아니나 ADR-0015 point 6의 다섯째 스텝이므로 완료 체크리스트에 남긴다.

   **H4를 별도 항목으로 못박는 이유**: verdict LLM 시크릿 없이 `verdict-gate`를 required로 등재하면 `verdict-gate.yml:742-752`/`:1102-1111`의 `-z` 가드가 항상 exit 1이라 check가 절대 green이 되지 않는다 — 모든 PR이 영구 미머지가 되고, 압력에 밀려 required에서 다시 빼면 `count=0` + 게이트 소멸이라는 최악 상태가 된다.

7. **precondition ②의 두 순환 의존을 다음과 같이 해소한다 — `verify-credential-narrowing.sh`를 수정한다.**
   - **(a) APPROVE 프로브 대상**: `APPROVE_TARGET_PR`은 보조 계정(`chanwoo040531`)의 **fork에서 연 draft PR**로 하고, base를 **step 1이 자기 안(런북 Step 1-A)에서 만드는 probe 브랜치**(`PROBE1` — `verify-protection-probe.sh setup`이 main HEAD에서 분기해 main-동형 임시 보호를 건 것)에 둔다. fork PR은 write 권한이 필요 없고, PR 자체는 base 레포에 존재하므로 `POST /pulls/{n}/reviews`의 정당한 대상이 된다. draft + 비-main base이므로 승인이 성공(=narrowing 실패)해도 main으로 아무것도 흐르지 않는다. 실측 직후 PR과 브랜치를 함께 정리한다.

     > **각주 (codex PR#82 R1 [P1] 반영 — ADR↔런북 불일치가 순서 순환으로 읽혔다)**: 이 point의 초판은 base를 **step 2의 probe 보호 브랜치**로 두라고 적었는데, 실행 순서(point 6)상 step 2(precondition ④ probe)는 step 1(narrowing) **다음**이라 step 1 실행 시점에 그 브랜치가 아직 없다 → mandatory `APPROVE_TARGET_PR`을 만들 수 없어 활성화가 순서대로 실행 불가능한 것으로 읽혔다(자기 순환). **실측상 런북은 이미 올바르게 구현돼 있었다** — Step 1-A가 narrowing 자신의 프로브용 브랜치(`PROBE1`)를 step 1 안에서 먼저 만들고 Step 1-B가 그걸 `APPROVE_TARGET_PR`의 base로 쓴다. 즉 결함은 로직이 아니라 **ADR 본문이 런북과 다른 브랜치(step 2의 것)를 지목한 서술 불일치**였다. base가 비-main이기만 하면 안전 속성("승인이 main으로 흐르지 않는다")은 성립하므로, ADR을 런북의 실제 구현(step 1 자체 probe 브랜치)에 맞춘다. step 2의 probe 브랜치는 code-owner 차단·direct-push 거부 **거동 실측(precondition ④)** 전용으로 남는다 — 두 브랜치는 목적이 다르다.
   - **(b) 7-viii의 두 레그 분리**: 현행 7-viii은 `MERGE_TARGET_PR`에 실제 merge를 시도하는데(`:241`), 그 대상이 main-base면 검증 스크립트가 검증 대상을 파괴한다. 다음으로 교체한다.
     - **(b-1) 상태 assert(유지하되 역할을 재명명한다 — `7-viii(a) branch protection 온전성`)**: main의 `enforce_admins`를 GET해 `true`가 아니면 INCONCLUSIVE.

       > **각주 (R6 반영 — 초판의 근거는 방향이 반대였다)**: 초판은 "이 assert가 빠지면 'narrowing 완료 = 안전'이라는 거짓 안심이 된다"고 썼는데 실제로는 **남겨두는 쪽이 정보 없는 PASS를 만든다**. 이 레그의 판별력은 오직 `scripts/verify-credential-narrowing.sh:232-233` 주석이 밝힌 "`enforce_admins=false`면 admin-user 토큰이 우회해 merge가 성공할 수 있어 유효 판정 불가"에서 온다. 그런데 이 ADR point 2가 `enforce_admins=true`를 ② **이전** 필수 스텝으로 확정하므로, step 0.5 이후 이 레그는 **구조적으로 FAIL이 불가능**해진다 — 그럼에도 그 PASS가 ② 완료 판정의 PASS 건수로 계수된다. **narrowing 증거의 하중은 7-vi(APPROVE 거부)와 7-ix(ambient PUT protection·APPROVE 거부)가 진다.** 단 7-ix가 그 하중을 실제로 지려면 **ambient가 사람 오퍼레이터의 admin 세션이 아니라 loop의 narrowed 토큰이어야 한다** — 사람이 chnu-kim admin 세션(`admin:true`)으로 스크립트를 돌리면 7-ix의 ambient PUT protection·APPROVE가 항상 성공해 FAIL이 되어 ②가 원리적으로 미완이 된다(R81). 그래서 런북 Step 1-D는 스크립트를 `GH_TOKEN="$NEW_PAT"`(loop의 narrowed PAT)로 감싸 돌린다 — `docs/runbooks/phase-b-entry.md` Step 1-D 각주(R81)가 이 배선의 지배 절차이고, 이 하중 배정과 twin이다.
       >
       > 그래서 레그를 **없애지는 않는다**(branch protection 드리프트 — 예: 누군가 `enforce_admins`를 다시 껐다 — 를 감지하는 가치는 남는다). 대신 (i) 라벨을 `7-viii(a) branch protection 온전성`으로 바꾸고, (ii) 출력 문구에 "**이 항목의 PASS는 narrowing 증거가 아니다**"를 명기하며, (iii) 스크립트 헤더 주석에 같은 내용을 1줄로 남겨 후속 세션이 이 PASS를 narrowing 근거로 오독하지 않게 한다. (iv) 판별력을 원하면 probe 브랜치에서 `enforce_admins=false` 대조군을 1회 돌려 음성/양성 쌍(아래 V2/V3)을 얻는다 — 그 쌍만이 "`true`가 admin bypass를 막는다"를 텍스트 논증에서 실측으로 옮긴다.
     - **(b-2) 거동 프로브(대상 변경)**: merge 프로브의 대상은 **probe 보호 브랜치를 base로 하는 일회용 PR**로 한정하고, 그 브랜치의 protection에도 `enforce_admins=true`를 건다(실행자가 admin이므로 이 필드 없이는 측정이 무의미해진다). `MERGE_TARGET_PR`의 base가 `main`이면 프로브를 수행하지 않고 **exit 2로 거부**한다.
   - **(c) 파싱 강건화 — 그리고 부재 시의 판정 방향을 명시한다**: 현행은 `grep -o`로 JSON을 읽어(`:237`) 응답 포맷이 바뀌거나 GET이 실패하면 조용히 빈 문자열이 된다. `jq` 부재 시 `exit 2`로 강제하고 `jq -e`로 필드 부재와 파싱 성공을 명시 구분한다.

     > **각주 (R6 반영 — 현행 실패 방향이 fail-open이다)**: 초판은 "조용히 빈 문자열 → 영구 INCONCLUSIVE로 굳는다(가용성 실패)"로 서술했으나 코드는 반대로 동작한다. `:238`이 `[ "${ea:-}" = "false" ]`이므로 **빈 문자열은 "false 아님"으로 읽혀 else 분기로 가고 merge 프로브를 그대로 수행한다** — INCONCLUSIVE가 아니라 **guard를 건너뛰는 fail-open**이다. 따라서 (c)는 파싱 형태만 바꾸는 것이 아니라 **판정 방향을 못박아야 한다: GET 실패·필드 부재·파싱 실패는 전부 INCONCLUSIVE**(증거 없음을 안전함으로 읽지 않는다). 이 방향 명시가 없으면 `jq -e`로 바꾸는 것만으로는 같은 구멍이 남는다.
   - **(d) 짝 테스트 신설**: 이 스크립트에는 현재 짝 테스트가 없다(`find scripts -name '*_test*'` → 0건). `.claude/skills/opensource-maintainer/scripts/scan_test.sh` 전례대로 `scripts/verify-credential-narrowing_test.sh`를 만들어 최소 두 케이스를 고정한다 — (i) base==main인 `MERGE_TARGET_PR`이면 exit 2, (ii) mandatory prereq 하나를 비우면 조용한 skip이 아니라 exit 1.

### C. 이슈 #76의 지위

8. **이슈 #76은 Phase B blocking precondition이 아니다.** 근거는 세 갈래이며 각각 독립적으로 성립한다.
   - **(a) 범주적 배제** — `ADR-0011:62` point 4(a)가 `ci.yml`을 비특권 잡으로 명시 분류하고 그 **모든** 산출물을 Phase B 머지 게이트의 신뢰 집합에서 뺐다. `ci.yml` 스캔 스텝의 green은 애초에 자율 머지의 근거가 아니다.
   - **(b) 이미 닫힌 시나리오** — #76이 겨냥한 정확한 파일 3종은 `.github/CODEOWNERS:84` + `internal/enforcement/codeowners.go:123-125`로 개별 sacred 등재돼 있고, flip은 `require_code_owner_reviews=true`를 명시 보존한다. 따라서 "체크 초록 + 사람 리뷰 0"의 조합이 이 파일 클래스에서는 `count=0`에서도 성립 불가다.
   - **(c) 게이트 독립성** — `required_status_checks`는 AND이고, `verdict-gate`는 `ci.yml`의 판정을 입력으로 읽지 않고 `base_sha...head_sha` compare API로 diff를 특권 컨텍스트에서 직접 읽는다(`verdict-gate.yml:258-390`).

   **단, `ADR-0011:85` point 7의 면제 *문장*은 #76에 문자 그대로 적용되지 않는다.** point 7의 조건절은 "CODEOWNERS에 걸리지도 **않고**"인데 `scan.sh`는 #72 이후 CODEOWNERS에 걸린다. 면제되는 것은 point 7의 문장이 아니라 **point 4(a)의 이유**다. 이 구분을 흐린 채 point 7을 인용하는 후속 산출물은 근거가 한 칸 어긋난 것이다.

9. **#76을 재스코프한다: "머지 게이트 구멍 수정"이 아니라 "push-time 예방 + 신호 정직성 + allowlist 출처 핀"이다. 그리고 required_status_checks에 신규 context를 등록하지 않는다.**
   - **(a) 진짜 harm은 merge-time이 아니라 push-time이다.** 레포는 public이므로(실측) 커밋은 push되는 순간 fetch 가능하고, 어떤 CI 기반 스캔도 정의상 post-push라 노출을 예방하지 못한다. 그리고 오늘 `secret_scanning`·`secret_scanning_push_protection`이 **둘 다 disabled**다 — #76 본문이 "현재 완화"로 열거한 유일한 push-time 방어가 거짓이다. **사람 액션 A1**: 두 설정을 활성화한다(`-f`는 중첩 오브젝트를 만들지 못하므로 JSON body로 PATCH). 이것이 코드 0줄로 harm class를 직접 줄이는 조치이며 #76이 제안하는 CI 재배선보다 앞선다.
   - **(b) `scan.sh`의 allowlist 출처가 고정돼 있지 않다.** `scan.sh:15`가 `cd "$(git rev-parse --show-toplevel)"`로 **호출 시점 cwd 기준** 저장소 루트로 이동하고 `:43`이 `ALLOWLIST_FILE`을 레포-상대 경로로 잡는다(R73: 초안의 `:39`는 오인용 — 실측 `grep -n "ALLOWLIST_FILE=" .claude/skills/opensource-maintainer/scripts/scan.sh` → `43:`). 그래서 "base의 scan.sh로 PR 내용을 스캔"하는 **모든** 설계가 base 스캐너를 돌려도 **PR 자신의 allowlist**를 읽는다 — 한 줄 추가로 우회된다. `--allowlist=<절대경로>` 플래그를 추가하고 상대경로는 `exit 2`로 거부한다. `scan.sh:26`의 기존 catch-all(unknown arg → exit 2)은 그대로 두면 버전 skew가 이미 fail-closed다 — 별도 capability 마커를 만들지 않는다.
   - **(c) 신규 required context 등록 금지(negative decision).** base-정의 트리거 잡의 암묵 check-run이 PR **head SHA**에 부착되는지는 미측정이다. 반증 증거 둘: `pull_request_target`에서 `GITHUB_SHA`는 default 브랜치 tip으로 resolve된다는 플랫폼 changelog, 그리고 **이 레포의 `verdict-gate`가 바로 그래서 암묵 부착에 기대지 않고 `gh api …/check-runs -f head_sha="$HEAD_SHA"`로 명시 게시한다**(`verdict-gate.yml:1347-1353`). 부착이 안 되는데 required로 등록하면 모든 PR이 영구 unmergeable(liveness brick)이다 — 안전한 fail-closed가 아니라 과-halt다. 이번 flip payload는 기존 `build · vet · gofmt · test-race`(app_id 15368) 유지 + `verdict-gate` 추가, 그 외 신규 context 없음.
   - **(d) 라벨은 유지한다** — `risk:high`(시크릿 취급 작업), `priority:p2`, `agent:ready`. blocking으로 재분류하지 않는다.

### D. 가용성 — W0가 닫힌 뒤의 유일한 contingency

10. **`ADR-0015 point 8(iii)`의 "두 번째 오퍼레이터가 보유한 복원 수단" mandatory를, `enforce_admins` 재개방 케이스에 한해 "loop PAT revoke 선행"으로 대체한다.** 이 레포의 collaborator는 `chnu-kim` 단독이므로(실측) "두 번째 오퍼레이터" 요건은 **구조적으로 충족 불가능**하고, 충족 불가능한 mandatory는 fail-closed가 아니라 **사람이 게이트 전체를 우회할 압력**을 만든다(과-hold도 실패다). 완화 창의 위험원은 그 창 동안 살아 있는 다른 admin-role 자격증명(=loop가 쥔 PAT)이므로, 그 사실 자체를 없앤다.
    > **긴급 재개방 절차(1인 운영)**: (1) loop PAT revoke → (2) loop 실행 컨텍스트가 아무 자격증명도 resolve하지 못함을 확인 → (3) `DELETE …/protection/enforce_admins` → (4) 복구 머지 → (5) `POST …/protection/enforce_admins` 재활성 → (6) PAT 재발급. 각 단계의 증거를 기록한다.

    revoke는 1인이 즉시 실행 가능하고 **서버-side에 durable**하다 — 세션이 죽어도 실패 방향이 롤백이 아니라 안전(loop 정지, main은 사람만 접근)으로 고정된다. 이 절차는 이번 세션에 probe 브랜치에서 1회 리허설하는 것을 권장하며, 리허설하지 않으면 미측정 설계로 book한다.

### E. 이 ADR 자신과 형제 런북의 sacred 등재 (게이트 승격 규칙)

> **⚠️ 착지 단계 명시 (codex PR#82 R2/R3 [P1] 반영 — "같은 PR"의 지시 대상)**: 아래 point 11~15와 twin-artifact 표의 **"같은 PR"은 각 승격을 *수행하는* PR을 뜻한다** — 게이트 승격 규칙의 정의(CLAUDE.md: "X를 게이트로 승격하면 같은 PR에서 X를 sacred로 등재")가 승격 행위와 등재를 한 PR에 묶는 것이지, "이 ADR을 담은 아무 PR"이 아니다. 이 캠페인은 사용자 결정으로 **두 단계로 착지한다**:
> 1. **이 ADR-착지 PR(#82)** — ADR-0016/0017을 `status: Proposed`로 올리고, **그 두 ADR 파일 자신의** sacred 등재만 수행한다(point 11의 `sacredADRRegistry`·`sacredRequiredPaths`·CODEOWNERS·`validCodeowners` — 두 ADR이 `protects:`를 선언하므로 완결성 테스트가 강제하는 forced 등재). 이 PR은 `.claude`·런북·`risk-classification.json`을 **승격하지 않으므로** point 12·15·twin 표의 승격 행위가 일어나지 않고, 따라서 그 등재 의무도 이 PR에 걸리지 않는다(게이트 승격 규칙은 승격을 *하는* PR을 구속한다).
> 2. **이슈 01 구현 PR** — point 12·13·15와 twin-artifact 표의 승격을 **실제로 수행하며**, 각 승격과 그 등재·완결성 테스트·`risk-classification` 변경을 **한 PR에** 담는다. 이 PR은 `blocksPhaseB=true`(아래 사람 액션·Consequences)이므로 **Phase B flip 전에 반드시 머지**된다.
>
> **왜 이 분리가 fail-open이 아닌가(잔여 위험 book)**: point 15가 지목하는 무보호 표면(`.claude/skills/architect|issue-drafter|retro`, 형제 런북)의 노출은 **`count=0`(Phase B)에서만 live**하다 — code-owner 미매칭 + `.claude/**` non-critical이라 codex 단일 leg만으로 자율 머지되는 경로. 그런데 (i) 지금은 Phase A/부트스트랩이라 `count≥1`이고 **모든 머지가 사람 `--admin`을 거친다**(self-approval 불가 — 이 PR #82 자신이 그 경로다), (ii) `count=0`은 위 이슈 01 구현 PR(그 무보호를 닫는 바로 그 배선)이 머지되기 전에는 **도달 불가능**하다(flip은 point 6 순서의 step 4, 이슈 01은 blocksPhaseB=true precondition). 즉 이 표면이 자율 머지에 노출되는 순간과 그것을 닫는 배선은 **순서로 상호배제**된다. `status: Proposed`인 이 ADR은 아무것도 활성화하지 않는다. 그럼에도 "이 표면이 이슈 01 머지 전까지 code-owner 무보호"라는 잔여는 명시적으로 book한다(Consequences booked residual).

11. **이 ADR은 활성화의 순서·완료 판정·벡터 목록을 정의하므로 그 자신이 게이트 정의 문서다. 같은 PR에서 sacred 4종을 배선한다.**
    - `.github/CODEOWNERS`에 `/docs/adr/0016-*.md @chnu-kim`
    - `internal/enforcement/codeowners.go`의 `sacredRequiredPaths`에 `docs/adr/0016-phase-b-activation-ordering.md`
    - `internal/enforcement/adrprotects.go`의 `sacredADRRegistry`에 `"0016"`
    - `internal/enforcement/codeowners_test.go`에 0016 누락 fail-closed 케이스(0011/0012/0013/0015 패턴)

    앞의 셋 중 둘은 자동으로 강제된다 — `TestADRProtectsCompleteness_RealRepo`와 `TestSacredADRRegistry_NewDeclarationMustRegister`가 `protects:`를 선언한 ADR의 미등재를 잡는다. **이 자동 강제가 실제로 무는지를 PR 본문에 Red 출력으로 증명한다**(등재 없이 파일만 추가해 실패를 확인한 뒤 등재).

12. **`docs/runbooks/loop-pr-environment-provisioning.md`를 같은 PR에서 sacred로 승격한다** — CODEOWNERS 줄 + `sacredRequiredPaths` 개별 등재 + 누락 fail-closed 테스트. 그리고 **`docs/runbooks/`에 디렉터리 규칙과 glob 완결성 테스트를 신설**해 이후 추가되는 런북이 자동 보호되게 한다(구체 테스트 스펙은 ADR-0017 point 14). 근거: Context 8. 열거는 다음 항목을 놓치므로 디렉터리 규칙 + glob 파생을 함께 건다.

13. **이번 세션의 실측 증거를 기록할 열린 추적 이슈를 새로 연다.** `#46`(narrowing)·`#47`(pr-creation)·`#50`(절차서)은 전부 **closed**이고 `#47`은 PR이 아니라 이슈다(실측). 그런데 `verify-credential-narrowing.sh:347`은 "결과 요약을 이슈 #46에 코멘트로 기록하라"고, `docs/runbooks/loop-pr-environment-provisioning.md`는 여러 곳에서 "이 PR(#47) 코멘트로 기록"이라고 지시하며, **`docs/verdict-gate-runbook.md`(이 ADR이 `docs/runbooks/`로 이동시키는 파일 — 아래 twin-artifact 표)도 죽은 `#47` 참조와 "Blocked" 상태표를 담고 있다** — 셋 다 죽은 대상이다(GAP2, 완결성 크리틱). 새 추적 이슈를 만들고 **이 세 문서**의 기록 지시·상태 서술을 그리로 돌린다(구체 작업은 이슈 01 §E-2). 증거 없는 완료 판정은 이 레포가 네 번 데인 실패형이다.

14. **boot pillar `CheckBranchProtection`에 phase 조건 없는 `enforce_admins == true` 레그를 추가한다.**

    > **각주 (R12·R26 반영 — 이 Decision point가 없어 게이트 검사기와 규칙이 모순될 뻔했다)**: 이 캠페인의 이슈 02가 `internal/enforcement/branchprotection.go`를 수정하는데(오늘 `:19-23`은 `RequiredPullRequestReviews` 하나만 선언, `:47-50`이 그 필드만 검사), 그 파일을 언급하는 유일한 ADR인 ADR-0017 point 10·twin 표는 초판에서 "한 바이트도 바꾸지 않는다"고 무조건 못박았고 이 ADR에는 그 변경을 승인하는 point가 **없었다**. 이슈 02는 `blocksPhaseB=true`·`risk:high`라 반드시 머지되는 산출물이므로, 규칙과 검사기가 main에서 서로 모순된 상태로 착지한다.

    - **레그**: `branchProtectionResponse`에 `EnforceAdmins *struct{ Enabled bool }`를 추가하고, `parsed.EnforceAdmins == nil || !parsed.EnforceAdmins.Enabled`면 unmet(사유에 "벡터 (d) — admin-role 머지-시점 bypass 열림"과 이 ADR을 인용). 두 레그는 short-circuit 없이 모두 평가해 사유를 누적한다(`CheckIdentity`의 `reasons` 패턴).
    - **phase 조건절을 두지 않는 이유**: `required_status_checks`(verdict-gate required 여부)는 Phase A/B에서 값이 다른 **진짜 phase-dependent 필드**라 boot AND 집계에 넣으면 Phase A가 깨진다 — `ADR-0015:83` point 7(c)가 금지한 것은 정확히 그 축이다. 반면 `enforce_admins`의 목표값은 **Phase A/B 어느 쪽이든 항상 `true`**(이 ADR point 1)이므로 조건절이 필요 없고, 조건절을 두면 판별자 자신이 새 fail-open이 된다(Alternatives의 `DetectPhase()` 기각 참조).
    - **과-halt 없음(실측)**: `presence.go`의 `Run()`은 3 pillar를 fail-closed AND로 집계하는데, PR 작성자가 전부 `chnu-kim`이라 identity pillar(c-2)가 이미 unmet → `Result.Satisfied`가 **오늘도 이미 false**다. 게다가 presence-check는 `cmd/bot`·모든 workflow·모든 `.claude/skills` 어디에도 배선돼 있지 않다. **새로 브릭되는 표면이 존재하지 않는다.**
    - **twin**: 같은 PR에서 `ADR-0015:83` point 7(c)의 "이 검사기는 `require_code_owner_reviews`만 파싱하고 `required_status_checks`는 보지 않으므로 code-owner 강제만 확인한다"는 문장이 stale해지므로 amend 포인터를 넣는다(아래 amend 절). ADR-0017의 비-twin 행도 같은 라운드에서 `required_status_checks`/verdict-gate 축 한정으로 재서술한다. **이슈 01(이 ADR)과 이슈 02는 같은 W0 창 안에서 이 순서로 머지한다.**

15. **`.claude/skills/`의 무보호 표면을 같은 창에서 닫는다 — architect·issue-drafter·retro의 `SKILL.md`.**

    > **각주 (R1 반영 — 초안 문서군이 전혀 다루지 않은 표면)**: `ls .claude/skills/` 실측 = 5개인데 `.github/CODEOWNERS:84,112`는 `opensource-maintainer`·`dispatch-issue` **둘만** 등재하고 `/.claude/skills/` 디렉터리 규칙이 없다 → 나머지 셋은 **매칭 패턴 0건 = count=0에서 사람 승인 불요**. `configs/gate/risk-classification.json:39`가 `.claude/**`를 non-critical로 두고 이후 어떤 규칙도 `.claude/skills/**`를 critical로 되돌리지 않으므로(`:29-48` 전수 확인) N-of-2도 아니라 **codex 단일 leg 승인만으로 자율 머지**된다. 그 세 파일은 실제로 게이트를 지시한다 — `architect/SKILL.md`의 "`protects:`를 선언한 ADR은 `sacredRequiredPaths` + CODEOWNERS에 등재"(= 이 ADR point 11과 ADR-0017 point 13이 자동 강제의 근거로 삼는 지시), `issue-drafter/SKILL.md`의 "`risk:critical`은 사람 리뷰 필수임을 본문에 명시". **게이트 코드를 한 줄도 안 건드리고 게이트를 끄는 경로**이며 이 레포의 게이트 승격 판별 질문을 그대로 통과한다. 이 ADR이 Phase B를 활성화하는 문서인 이상 이 표면을 열어둔 채 flip하는 것은 point 5(W0) 다음가는 실질 fail-open이다.

    같은 PR에서: (a) `.github/CODEOWNERS`에 **`/.claude/ @chnu-kim` 루트 규칙** + `/.claude/skills/ @chnu-kim` 디렉터리 규칙 + 세 파일 개별 줄, (b) `sacredRequiredPaths`에 세 `SKILL.md` 개별 등재, (c) `instructionsurface_test.go`의 하드코딩 2개 목록(`:23-36`의 함수 **전체**를 in-place 교체)을 `.claude/skills/*/` **디렉터리 순회**로 바꾸고, 그 안의 파일 순회도 `*.md` 한 단계가 아니라 **트리 전수**(`filepath.WalkDir`, 확장자 무관)로 한다(0건이면 `t.Fatalf`) + `.claude/` **루트 파일** 순회 테스트를 신설한다, (d) **이 PR이 새로 sacred로 올리는 모든 경로**에 대해 `risk-classification.json`의 분류를 확인·좁힌다 — **`.claude/**` non-critical 카브아웃(`:39`)을 삭제**해 레포 기본값 critical로 되돌리고, `{"pattern": "docs/runbooks/**", "class": "critical"}`를 `docs/**` 뒤에 넣는다(last-match-wins), (e) 그 (d)를 사람 규율이 아니라 **검사**로 고정한다 — `TestSacredRequiredPaths_AreAllCriticalInRiskClassification`. (c)는 오늘 실행하면 세 파일을 이름으로 지목하며 Red고, (e)는 (d) 전에 추가하면 `.claude` 9개 + 런북 전부를 지목하며 Red다.

    > **각주 (R46·R47·R48 반영 — 같은 클래스가 한 단계 위에서 두 번 더 반복됐다)**: (a)의 초판은 `.claude/` **아래 디렉터리**만 소유자에 걸고 **루트를 열어뒀다**. `.claude/settings.json`은 프로젝트 훅(임의 명령 실행)을 정의하며 `.gitignore:13`이 `settings.local.json`만 무시하므로 **커밋 가능**하다 — 매칭 CODEOWNERS 패턴 0건이면 code-owner 승인 0건 + codex 단일 leg로 Phase B 자율 머지된다. (d)의 초판은 `.claude/**` non-critical을 남긴 채 `.claude/skills/**`만 되돌려, **이미 sacred인 `.claude/agents/go-tdd-implementer.md`**(이 ADR의 twin 표가 W0 문단 삽입을 mandatory로 지정한 바로 그 파일)가 non-critical로 남았다. 좁은 되돌림 규칙을 하나씩 얹는 것은 하드닝이고 다음 항목을 또 놓친다 — 이 레포의 규율은 **"위험 클래스 제거 > 하드닝"**이므로 카브아웃 자체를 지운다. 그리고 (e)가 없으면 이 판별이 계속 사람 눈에 남는다: 실제로 (a)와 (d)의 누락은 **두 라운드를 살아남았고 둘 다 외부 적대 리뷰가 잡았다**.

    > **각주 (R31·R33 반영 — 규칙을 반쪽만 적용했다)**: (d)의 초판은 `.claude/skills/**`만 다뤄, **같은 PR이 sacred로 승격하는 `docs/runbooks/`**(point 12)를 빠뜨렸다. 실측: `configs/gate/risk-classification.json:32`가 `docs/**`를 non-critical로, `:33`이 `docs/adr/**`만 critical로 되돌리며 `docs/runbooks/**`를 되돌리는 규칙은 없다 → `internal/gate/riskclassification.go:46-58`의 last-match-wins로 런북은 **non-critical 확정**이고, 이 ADR이 sacred로 올린 런북들을 고치는 PR이 **codex 단일 leg**만 받는다. `.claude/skills/**`에 대해 결정적이라고 판단한 바로 그 비대칭이 형제 승격에 남는다. 그래서 (d)의 근거를 **"이번 PR이 sacred로 올리는 모든 경로"**로 일반화했다. (c)의 초판은 순회 **대상 디렉터리**만 파생하고 파일 패턴을 `*.md`로 고정했는데, `.claude/skills/opensource-maintainer/`에는 `scripts/scan.sh`·`scan_test.sh`·`allowlist.txt` 3개의 비-`.md` 게이트 파일이 실재하며 `filepath.Glob`은 재귀하지 않는다 — 같은 규칙이 파일 축에서 다시 손 열거로 되돌아가 있었다.

---

## Alternatives considered

- **`enforce_admins=false`를 Phase B 최종값으로 승인하고 7-viii의 INCONCLUSIVE 분기를 제거(또는 잔여 위험으로 book하고 진행)** — 기각. 두 겹으로 실패한다. (i) `verify-credential-narrowing.sh`는 sacred 등재된 증거 생성기다(`codeowners.go:95`) — 그 fail-closed 분기를 제거해 ②를 "통과"로 만드는 것은 **게이트의 증거 생성기를 고쳐 게이트를 통과시키는** 정확히 그 금지 패턴이고, 이 레포가 거짓 "문서 검증 완료" 라벨에 네 번 데인 실패형의 다섯 번째 재발이다. (ii) `count=0` 이후에는 required check 하나가 유일 게이트인데, vector (d)를 사람의 명시적 위협모델 판단 없이 Phase B의 영구 상태로 굳힌다 — `ADR-0011:73`이 "허용/차단 판정은 GitHub branch protection이 한다"를 Phase B 안전 논증의 축으로 쓰는데 `false`는 그 판정을 admin에게 재량적 '허용'으로 바꾼다. 부수적으로 `ADR-0011:73`은 관측이 아니라 규범 문장이라 'stale' 재분류 자체가 범주 오류다.

- **flip payload 생성기가 `enforce_admins`를 `false`→`true`로 명시 override** — 기각. 방향(목표값 true)은 옳지만 시점과 메커니즘이 둘 다 틀렸다. (i) 시점: Context 2의 연역상 `false`인 한 ②가 exit 0에 도달할 수 없으므로 활성화는 flip이 아니라 narrowing보다 **앞**이어야 한다 — flip 시점 override는 ②가 미검증인 채로 flip이 성립하게 만든다. (ii) 메커니즘: 생성기가 값을 고쳐주면 step 0.5 누락이 조용히 흡수되어 fail-closed가 아니라 fail-quiet이 된다. assert-and-abort로 바꾸면 같은 목표값을 강제하면서 누락이 non-zero exit으로 드러난다.

- **`--expect-enforce-admins=<bool>` / `--ack-enforce-admins=<bool>` 같은 CLI 플래그로 오퍼레이터가 기대값을 지정** — 기각. 미해결 상태였던 아키텍처 포크를 CLI 플래그 한 글자로 조용히 결정 가능하게 만든다("설계 게이트를 대화·편의 승인으로 우회"와 같은 클래스). 그리고 이 ADR이 목표값을 `true`로 확정한 이상, 적법한 flip 시점에는 이미 `true`여야 하므로 플래그는 실패 모드만 늘린다. 하드 assert가 정답이다. (단, "review-time과 execute-time 사이의 드리프트를 잡는다"는 이 대안의 정당한 관심사는 ADR-0017의 `verify`가 재-GET 전체 동치 비교로 흡수한다.)

- **`DetectPhase()` 같은 phase 판별자로 bootstrap/post-bootstrap을 구분해 assertion을 조건부 적용** — 기각. 판별자가 실측으로 반증된다. 가장 그럴듯한 판별자였던 presence-check c-2(`internal/enforcement/identity.go` — `mechanu[bot]` 작성 PR 존재)는 step 3(App key) **이후에만** true가 되는데, `enforce_admins` 요구는 step 0.5부터 구속력을 갖는다 → **요구가 처음 바인딩되는 구간 전체를 "bootstrap"으로 오분류해 assertion을 끄는 fail-open**이다. 판별자 도입의 대가로 판별자 자신이 새 구멍을 만든다. 게다가 무조건 assert의 liveness 비용은 실측상 0이다: PR 49건 작성자가 전부 `chnu-kim`이라 c-2가 이미 unmet이고 presence-check 집계는 이미 false이며, presence-check는 어디에도 배선돼 있지 않다. "위험 클래스 제거 > 조건절 하드닝"이 여기서는 "phase 파라미터를 아예 두지 않는다"를 가리킨다.

- **loop 자격증명을 admin이 아닌 write 권한 머신 계정으로 이관해 vector (d)를 구조적으로 제거** — 원리적으로 이 결정보다 강하다(role 기반 자격 자체가 없어져 `enforce_admins` 값과 무관하게 (d)가 닫히고, `chnu-kim`은 `--admin` 부트스트랩 경로를 영구 보존해 W0 비용이 0이 된다). 그러나 (i) `ADR-0011 point 2`가 "커밋·push identity는 그대로 `chnu-kim`(identity 분할)"을 명시 결정으로 고정했고 이를 뒤집는 것은 근거 ADR 없는 아키텍처 포크다(MEMORY `메커니즘 수정에 정책 얹지 말기`), (ii) 보조 계정 `chanwoo040531`은 read 전용이라(ADR-0011 Context 실측) write collaborator 승격·SSH/PAT 재구성·서명 정책 재검토가 줄줄이 따라온다, (iii) 사람 계정 자격증명 탈취 축은 여전히 열려 있어 `enforce_admins=true`가 주는 방어를 대체하지 못한다. 이번 세션에 flip을 실행하는 제약상 채택 불가 — **W0의 가용성 비용이 실제로 물리면 이 축으로 되돌아온다**는 후속 포크로 book한다.

- **7-viii의 판정 대상을 probe 브랜치로 완전히 옮겨 main의 `enforce_admins`를 판정에서 빼기** — 부분 채택, 부분 기각. destructive write를 검증 대상에서 제거하는 부분은 옳고 채택했다(point 7 (b-2)). 그러나 main의 값을 판정에서 **완전히** 빼면 vector (d)가 판정 시야에서 사라져 "narrowing 완료 = 안전"이라는 거짓 안심이 되고, step 0.5의 강제 근거도 함께 증발한다. 두 레그로 분리해 **상태 assert는 main에 유지하고 거동 프로브만 probe 브랜치로 옮긴다**.

- **#76을 Phase B blocking precondition으로 승격하고 flip 전에 구현** — 기각. point 8의 세 근거로 실익이 없고, 승격하면 flip을 지연시킬 뿐 아니라 급하게 구현할 경우 head-SHA 부착 미측정 위험을 안은 채 required 등록으로 직행할 개연성이 높다 — **승격 자체가 liveness brick 위험을 키운다.**

- **`ci.yml`의 트리거를 `pull_request` → `pull_request_target`으로 교체(#76 해결안)** — 기각. `ADR-0011 point 4(b)` 정면 위반이다: 그 point는 base-정의 트리거 잡에서 "PR-통제 콘텐츠의 실행(PR 코드의 빌드·테스트·스크립트 호출)"을 금지하는데, 이 제안은 `go build`/`vet`/`test -race`/`scan_test.sh`를 전부 그 컨텍스트로 옮긴다 — 금지 조항을 인용하면서 교과서적 pwn-request를 만든다. 게다가 **이미 required인** context를 건드리므로 부착이 어긋나면 flip 전 Phase A에서 즉시 전 머지가 죽는다.

- **`ci.yml` 안에서 base ref를 별도 경로로 체크아웃해 그쪽 `scan.sh`를 실행(#76 본문 제안)** — 기각. 구조적으로 무효다. `ci.yml`은 `on: pull_request`라 정의 자체가 PR-가변이므로, 공격 PR이 같은 diff에서 그 새 체크아웃 스텝을 통째로 삭제하면 방어가 사라진다. 자기 자신이 PR-통제 파일인 곳에 신뢰 경계를 세울 수 없다. 추가로 `scan.sh:15`+`:43`의 상대 `ALLOWLIST_FILE` 때문에 스텝이 남아 있어도 PR의 allowlist를 읽어 우회된다.

- **#76을 재분류하고 이슈 코멘트만 남긴 뒤 종료(비용 0)** — 판정 방향은 옳으나 기각. 그렇게 끝내면 오늘 측정한 사실(push protection이 꺼져 있다)을 그대로 방치하게 되고, public 레포에 push-time 방어가 **전무한** 채 머지-게이트 논증(정확하다)이 노출 논증(다른 축)의 부재를 가리는 안전 착시가 생긴다. 판정은 유지하되 A1(활성화)과 `--allowlist` 핀을 실행 조치로 승격한다.

- **활성화 산출물을 단일 ADR로 묶기(F6 결정)** — 기각. F6의 유일한 비용 논거는 "자동 게이트가 0인 admin-bypass 머지 이벤트 수 최소화"인데, **ADR 개수는 PR 개수가 아니다**. F6 자신이 결정→메커니즘 의존성을 근거로 PR을 둘로 쪼갰으므로, 메커니즘 ADR을 그 PR-2에 실으면 admin-bypass 머지 이벤트가 **한 건도 늘지 않는다**. 반대로 묶으면 (i) F6 자신의 분리 논거("파괴적 PUT 생성기는 적대 리뷰 라운드를 산문과 섞지 않고 독립적으로 받아야 한다")가 ADR 층에서 무효화되고, (ii) probe 실측 결과(예: `contexts[]` 422)로 메커니즘을 개정해야 할 때 이미 부분 실행된 자격증명·순서 결정까지 재개방된다. 그래서 **ADR은 둘(0016 정책·순서, 0017 메커니즘), PR도 둘**로 간다.

- **ADR-0015 point 8(iii)의 "두 번째 오퍼레이터" mandatory를 그대로 유지** — 기각. 이 레포의 collaborator는 1명이라 구조적으로 충족 불가능하고, 충족 불가능한 mandatory는 실질적으로 "정당한 긴급 복구조차 봉쇄 → 사람이 게이트 전체를 우회"로 귀결된다. 조건을 protection-side("나중에 복원하겠다"=규율)에서 credential-side("먼저 PAT를 죽인다"=서버-side durable 상태 변경)로 옮기면 1인 운영에서도 실행 가능하면서 실패 방향이 안전으로 고정된다.

---

## Consequences

### 좋음

- **문서군의 상태 모순이 값 판정이 아니라 *시제 분류*로 해소된다** — `ADR-0011:39`(stale 관측) / `:73`(미충족 규범) / `ADR-0015:94`(창 한정 관측) / `ADR-0015:82`(오분류)가 각각 어떤 종류의 문장인지 레포에 남아, stateless 독자가 다시 "어느 쪽이 맞나"로 재-litigate하지 않는다.
- **`enforce_admins=true`가 vector (d)의 봉쇄 레버로 명명돼, narrowing이 닫지 못하는 축이 처음으로 지도에 올라간다.** 지금까지 이 사실은 검증 스크립트 주석에만 존재했다.
- **W0 불변식이 "산출물을 언제까지 머지해야 하는가"를 취향이 아니라 물리 제약으로 답한다** — 다음 세션이 순서를 뒤집어 교착에 빠질 확률이 크게 준다.
- **#76의 지위가 durable 표면에 기록돼** 후속 세션이 원문(`risk:high` + "시나리오 방지" 프레이밍)만 보고 blocking으로 재-오인하는 것을 막는다.
- **A1(push protection)은 코드 0줄로 실제 harm class를 줄인다** — CI 재배선보다 앞선 조치이며, `#76`이 "이미 있다"고 서술한 방어를 실제로 존재하게 만든다.

### booked residual (알려진 잔여 위험)

- **전제 자체가 아직 미측정이다.** "`enforce_admins=true`가 admin-role 머지-시점 bypass를 막는다"는 플랫폼 문서(true/null 의미만 서술, `false`는 undocumented, fine-grained PAT와 계정 role의 상호작용 미명시)와 `ADR-0011:40`의 서술에 기댄 보수적 독해다. 아래 V2/V3의 probe 음성·양성 대조 전까지는 텍스트 논증이며, **V3가 실패하면 이 ADR 전체가 반증되고 Phase A로 되돌아간다.**
- **W0가 닫힌 뒤 App 경로가 고장 나면(App key 만료·`pr-creation.yml` 버그·environment 오설정) sacred 교착이다.** point 10의 긴급 재개방 절차는 이번에 설계했을 뿐 실행된 적이 없다(V8 리허설은 권장이지 필수가 아니다). 실제 장애 순간에 처음 돌리면 압박 하의 미검증 절차다.
- **PAT revoke의 완결성은 오퍼레이터 단언에 남는다** — "loop 컨텍스트가 아무 자격증명도 resolve하지 못함"을 어떻게 실측할지는 `verify-credential-narrowing.sh` 7-ix가 부분 커버하지만 긴급용 축약 버전이 없다. SSH teardown이 "API 열거 가능 key까지만 실측, 잔여는 단언"으로 이미 booked된 것과 같은 계열(ADR-0015).
- **사람 계정 자격증명 탈취 축은 줄되 닫히지 않는다.** `enforce_admins=true`는 머지-시점 우회만 막고 `PUT …/protection` **설정 편집 자체**는 막지 못한다(`ADR-0011:40` — classic protection에 설정 잠금 옵션 없음). 탈취된 admin 자격증명은 `enforce_admins`를 스스로 끌 수 있다. 이 축을 닫는 레버는 이 레포에 없으며 ADR-0009의 admin blast radius로 known-open 유지.
- **`bypass_pull_request_allowances`는 `enforce_admins`와 직교한다.** 오늘 실측상 부재이고 flip 생성기가 부재를 assert하지만(ADR-0017), 사람이 flip 경로 밖에서(웹 UI 등) 나중에 추가하면 `enforce_admins=true`여도 그 액터는 PR 리뷰 요건을 우회한다. `CheckBranchProtection`이 이 필드를 보게 하는 확장은 이 ADR 스코프 밖(후속).
- **verdict 프롬프트에 시크릿·자격증명 탐지 mandatory 지시가 없다**(`verdict-gate.yml:706-740`은 "diff가 안전한가 / 안전·게이팅 메커니즘을 약화하지 않는가"라는 일반 지시뿐). Phase B에서 비-sacred 경로에 `scan.sh` 정규식과 GitHub provider 패턴을 둘 다 피하는 형태(base64·분할·비표준 포맷)로 인코딩된 시크릿을 심은 PR은 자율 머지될 수 있다. 이 ADR도 #76도 이걸 닫지 않는다 — LLM의 일반 안전 판단이 결정론적 패턴 매칭보다 이 축에서 구조적으로 약하다. **저비용 후속(verdict 프롬프트에 시크릿 검사 항목 추가)으로 별도 book.**
- **GitHub push protection은 provider가 등록한 패턴만 잡는다.** 이 레포 고유 harm class(Toss `client_id`/`client_secret` 형태, 개인정보, 환경 의존 값)는 `scan.sh`만 커버하고, 비-sacred 경로에 대해서는 그 `scan.sh`가 검사하는 *대상*이 PR 저작이다. A1은 provider-패턴 축만 닫는다 — 두 계층은 대체재가 아니라 보완재다.
- **판정 8(b)의 전제가 이 레포에서 미실측이다** — "CODEOWNERS 매치 파일이 하나라도 있으면 PR 전체에 code-owner 승인이 요구된다"는 GitHub 표준 동작을 전제로 삼았다. V5가 probe에 저비용으로 얹어 닫도록 설계했고 **런북 Step 1-B에서 `PASS`를 게이팅한다** — 파일 단위 부분 적용으로 판명되면 #76의 프레이밍이 되살아나므로 그 결과는 곧 **flip 중단 신호**다. 실측 전까지는 '강한 근거를 가진 미실측'이며, 이 잔여 위험은 `STEP1_PASS`가 기록되는 시점에 닫힌다.
- **base-정의 트리거 잡의 check-run head-SHA 부착 여부는 여전히 UNVERIFIED다.** 이 ADR은 신규 context를 등록하지 않음으로써 불확실성을 **회피**할 뿐 해소하지 않는다 — #76 재스코프 구현 시 V7이 반드시 선행해야 한다.
- **point 12·15·twin 표의 무보호 표면(`.claude/skills/architect|issue-drafter|retro`, 형제 런북, `.claude` 루트 파일)은 이 ADR-착지 PR(#82)이 아니라 이슈 01 구현 PR이 닫는다(codex PR#82 R2/R3 반영 — §E 착지 단계 노트).** 그 사이 구간에는 이 표면들이 **code-owner 무보호**로 남는다. 이 잔여가 fail-open으로 실현되지 않는 것은 순서 상호배제에 의존한다: 노출은 `count=0`에서만 live인데, `count=0`은 그 배선을 담은 이슈 01(blocksPhaseB=true)이 머지되기 전에는 도달 불가능하고, 그 전 구간(Phase A/부트스트랩)은 모든 머지가 사람 `--admin`을 거친다. 따라서 잔여의 성격은 "노출"이 아니라 **"안전이 코드 게이트가 아니라 착지 순서 규율에 의존한다"**이다 — 이슈 01이 머지되면 소멸한다. 이슈 01을 flip 전 hard precondition으로 유지하는 것(사람 액션 체크리스트·검증 방법)이 이 잔여의 유일한 완화다.
- **A1은 히스토리 전체 스캔을 촉발해 과거 커밋의 유출 alert를 띄울 수 있다.** 이 레포는 2026-07-08에 public으로 전환됐으므로 그 이전 private 시절 커밋에 무언가 있었다면 지금 드러난다. **alert가 뜨면 그것은 A1의 실패가 아니라 A1이 처음으로 보여주는 것**이며, 해당 자격증명 rotate가 별도 즉시 작업이 된다 — 세션 계획에 이 분기를 미리 넣는다.
- **flip 이후 `chnu-kim`이 직접 연 PR은 구조적으로 머지 불가다 — 설계 의도이지만 초판이 이를 적지 않았다(R5).** `verdict-gate`가 required가 되는데 `internal/gate/eligibility.go:35`의 `Eligible()`이 `!IsCrossRepository && Author == "mechanu[bot]"`을 AND로 요구하고, `.github/workflows/verdict-gate.yml:1224`(`finalize` job `if: always() && needs.resolve.outputs.eligible == 'true'`)와 `:1280`(publish 스텝)이 **부적격 PR에 어떤 check-run도 게시하지 않는다**(`:250` notice 원문: "No verdict check will be set and no further job in this workflow runs for it"). 따라서 사람 작성 PR의 required check는 **영구 pending**이고, `enforce_admins=true`라 admin bypass도 없다. **flip 이후 main으로 가는 유일한 경로는 `create-loop-pr` dispatch로 만든 `mechanu[bot]` PR + approve verdict다.** 이 사실을 문서화하지 않으면 오퍼레이터가 영구 pending을 **flip 실패로 오판해 불필요하게 롤백**하거나, 압력 하에 required에서 `verdict-gate`를 빼버린다(= `count=0` + 게이트 소멸 = 최악 상태). 런북 서두 4항과 Step 4-B 배너에 명시했고, **flip 후 거동 실측도 사람 작성 PR이 아니라 `mechanu[bot]` PR로 수행한다**(초판 런북은 chnu-kim PR로 지시해 그 스텝이 영구 미완이었다). 긴급 시 유일한 탈출구는 point 10의 긴급 재개방 절차다.
- **`request-verdict` 가드는 "사람"이 아니라 "`chnu-kim` identity" 검사다 — loop도 통과한다. 즉 Phase B는 반자동이 아니라 PR 생성부터 verdict 개시까지 loop 단독 수행이 가능한 상태다.**

  > **각주 (R42 반영 — 초판은 존재하지 않는 사람 게이트를 잔여 위험의 완화 근거로 기록했다)**: 초판 문장은 "`request-verdict` dispatch를 자동으로 쏘는 주체가 없다 … flip 이후에도 Phase B는 'PR마다 사람이 verdict를 수동으로 쏘는 **반자동**' 상태"였다. 실측상 이는 **틀렸다**. `.github/workflows/verdict-gate.yml:151-163`: `SENDER_LOGIN: ${{ github.event.sender.login }}` → `if [ "$SENDER_LOGIN" != "chnu-kim" ]; then … 'is not the allowlisted **loop identity**'`. 가드가 허용하는 것은 사람이라는 **주체**가 아니라 **`chnu-kim`이라는 계정 identity**이며, 워크플로 자신의 에러 문구가 그것을 "loop identity"라고 부른다. 그리고 `POST /repos/{o}/{r}/dispatches`는 **Contents:write**만 요구하는데, narrowing 후 loop PAT의 스코프가 정확히 `Contents:RW, PR:read, Issues:RW, Admin:read`다(`docs/runbooks/phase-b-entry.md` Step 1-D). 결정적으로 **loop는 이미 같은 엔드포인트로 `create-loop-pr`를 쏜다**(ADR-0011 point 3, CLAUDE.md "PR 생성 흐름" 2·3항) — 즉 이 dispatch를 쏠 능력은 이미 실증돼 있고 event_type만 다르다.

  **정확한 잔여 위험**: flip 이후 loop는 (1) `create-loop-pr`로 `mechanu[bot]` PR을 만들고 (2) `request-verdict`로 자기 PR의 verdict를 개시하고 (3) approve verdict가 나오면 특권 잡이 머지하는 **전 구간을 사람 개입 없이** 돌 수 있다. 남는 실질 게이트는 **verdict 판정 자체**(비-sacred·비-critical은 codex 단일 leg, `risk:critical`은 N-of-2)와 **sacred 경로의 code-owner 리뷰**뿐이다 — "사람이 매 PR마다 개시한다"는 추가 게이트는 **존재하지 않는다.**

  **오늘 loop가 그 dispatch를 쏘지 않는 것은 기술적 불가가 아니라 지시 표면의 정책이다**(`.claude/agents/go-tdd-implementer.md`·`.claude/skills/dispatch-issue/SKILL.md`가 "이 워커는 쏘지 않는다"고 지시). 정책은 강제 장치가 아니므로 **잔여 위험 계산에 게이트로 계상하지 않는다.** 사람 개입을 실제로 강제하려면 별도 메커니즘이 필요하다 — (i) dispatch 트리거를 App 토큰 전용으로 좁히기, (ii) `workflow_dispatch`(actor 검사) 전용으로 만들고 `repository_dispatch` 레그 제거, (iii) loop PAT에서 Contents:write 분리. 셋 다 ADR-0011 point 3/4(f)의 트리거 설계와 narrowing 스코프를 동시에 건드리므로 **`/architect` 포크**다(아래 후속 (ii)). **이 정정 없이 flip하면 사용자가 존재하지 않는 사람 게이트를 전제로 Phase B를 승인하게 된다.**
- **UNVERIFIED 2건 재-book**: GraphQL `enablePullRequestAutoMerge`의 요구 권한, App/bot 승인의 required count 카운트 여부(`ADR-0011` lines 48-49). 채택안은 후자에 의존하지 않고, 전자는 실패해도 branch protection이 실제 게이트이므로 머지 안전성에는 영향이 없다(운영 편의 저하만).
- **`sacredADRRegistry`·`sacredRequiredPaths`는 여전히 수기 배열이고, `protects:`를 의도적으로 비운 신규 ADR은 어느 완결성 테스트에도 걸리지 않는다**(`parseADRProtects`가 `protects: []`를 정상 형태로 허용). 게이트를 실질적으로 재정의하는 ADR을 `protects` 비운 채 저작하면 machine-enforced 등재를 우회한다 — 이번 스코프에서 닫지 않고 별도 이슈로 book.

### 사람 액션 핸드오프 (에이전트 물리 불가)

point 6의 6개 스텝 전부 + 아래 둘.

- **A1 — secret scanning + push protection 활성화** (repo admin):
  ```
  gh api -X PATCH repos/chnu-kim/toss-trade-bot --input - <<'JSON'
  {"security_and_analysis":{"secret_scanning":{"status":"enabled"},
   "secret_scanning_push_protection":{"status":"enabled"}}}
  JSON
  ```
  사후 확인: `gh api repos/chnu-kim/toss-trade-bot --jq .security_and_analysis` → 두 필드 모두 `"enabled"`.
- **A2 — 이슈 #76 정정 코멘트**: 본문의 "Push Protection이 이 레포에서 실제 동작 중 — 실측됨"이 2026-07-23 실측상 거짓임을 기록하고, point 8/9의 판정을 파일:라인과 함께 남긴다. 라벨 변경 없음.

### 검증 방법 (전부 capability 실측 — 사람 단언·"문서 검증 완료" 라벨은 증거가 아니다)

- **V1(에이전트, 지금)** — 완결성 테스트의 Red 실증. `docs/runbooks/` glob 테스트와 ADR 자동 등재 테스트를 **등재 없이 먼저** 추가해 각각 `loop-pr-environment-provisioning.md`와 `0016`을 지목하며 FAIL하는지 확인한 뒤 등재해 GREEN 전환 출력을 PR 본문에 첨부한다. **FAIL이 안 나면 그 테스트는 vacuous이므로 폐기·재작성한다.** 파괴적 Red는 커밋된 baseline 위에서 수행한다(MEMORY `worktree-stale-main-ref`).
- **V2(사람, probe 브랜치 — main 무접촉)** — vector (d) 존재의 음성 대조. probe 브랜치에 main과 동일한 보호 규칙(`count=0` + `require_code_owner_reviews=true` + required check + **`enforce_admins=false`**)을 걸고, 체크 미충족·code-owner 미승인 PR을 `gh pr merge --admin`으로 시도 → **머지 성공이면 (d) 존재가 실증된다.**
- **V3(사람, 같은 probe 브랜치) — 이 ADR의 핵심 측정.** probe 브랜치의 `enforce_admins`만 `true`로 올리고 V2와 **동일한 조작**을 반복 → `gh pr merge --admin`이 **405로 거부**되어야 한다. 거부되지 않으면 **전제가 반증된 것이므로 flip을 중단하고 Phase A에 잔류, ADR amend.**
- **V4(사람, main)** — step 0.5 실행과 격리 확인. `POST …/protection/enforce_admins` → `GET …/protection/enforce_admins` = `{"enabled":true}` → 전체 `GET` diff를 step 0.5 직전 스냅샷과 대조해 이 필드 외 무변경 확인.
- **V5(사람, probe에 얹음) — 게이팅 실측이다.** 혼합 diff의 code-owner 요구. probe 브랜치를 base로 임시 PR 둘을 **새로** 만들어 (i) 비-sacred 파일만, (ii) 비-sacred + sacred가 **같은 diff에 섞인** PR — `reviewDecision`이 (ii)에서만 `REVIEW_REQUIRED`인지 확인. 판정 8(b)의 미실측 전제를 닫는다. 추가 비용은 PR 2개, main 무접촉.

  > **각주 (R52·R53 반영)**: 초판 문언("mergeability/필요 리뷰어 **표시** 확인")은 비-게이팅 관측으로 읽혔고, 런북 초안이 실제로 그렇게 구현했다 — (i)만 만들고 대조군으로 **순수 sacred**인 `MERGE_TARGET_PR`을 재사용해 **"혼합" 시나리오 자체를 한 번도 실측하지 않았으며**, 판정에 `PASS=0` 분기가 없는데 `STEP2_PASS` 마커는 "V5 관측 완료"를 실었다(CLAUDE.md 따름 규칙 4 위반). **V5는 이 ADR이 booked residual로 남긴 판정 8(b)를 닫는 유일한 실측**이므로 반대 결과(혼합 PR에 code-owner 미요구)가 나오면 **#76의 프레이밍이 되살아나 flip을 중단해야 한다** — 즉 본질적으로 게이팅이다. 런북 Step 1-B에서 진짜 혼합 PR로 재구성하고 `PASS`에 게이팅하도록 고쳤고(마커도 `STEP1_PASS`로 이동), 두 PR은 **draft로 만들지 않는다**(GitHub은 draft PR에 code-owner 리뷰를 자동 요청하지 않아 관측이 판별력을 잃는다).
- **V6(사람)** — precondition ② 완결. point 7의 세 수정 적용 후 `verify-credential-narrowing.sh` 재실행 → 7-viii가 INCONCLUSIVE가 아니라 PASS(405/403/409/422)로 떨어지고 **스크립트 전체 exit 0**. V4 이전에는 구조적으로 불가능했던 상태다. 항목별 HTTP 상태·판정 요약을 point 13의 새 추적 이슈에 기록한다.
- **V7(#76 재스코프 구현 시의 선행 실측 — 지금은 아님)** — base-정의 워크플로를 만들면 required 등록 **이전에** 일회용 PR에서 `gh api repos/…/commits/$HEAD_SHA/check-runs --jq '.check_runs[].name'`로 그 context가 **PR head SHA에** 나타나는지 실측한다. 나타나지 않으면 `verdict-gate`와 동일하게 명시 게시로 고친다. **이 실측 없이 required 등록은 금지.**
- **V8(권장, 사람)** — 긴급 재개방 절차(point 10)를 probe 브랜치에서 1회 리허설. 리허설하지 않으면 미측정 설계로 book.
- **V9(사람, flip 직후)** — negative 검증. `gh api …/branches/main/protection --jq '.required_status_checks.checks'`가 정확히 2개(`build · vet · gofmt · test-race`, `verdict-gate`)만 반환하는지. 이번 결정이 추가한 context가 하나도 없어야 한다.

### twin-artifact 배선 지시 (같은 PR에서 함께 움직인다)

| 층 | 아티팩트 | 조치 |
|---|---|---|
| 규칙 | `docs/adr/0016-phase-b-activation-ordering.md` | 신규(`protects: [enforcement-integrity]`) |
| 자기 자신 | `.github/CODEOWNERS` | `/docs/adr/0016-*.md @chnu-kim` 추가 |
| 자기 자신 | `internal/enforcement/codeowners.go` | `sacredRequiredPaths`에 0016 실파일 경로 |
| 자기 자신 | `internal/enforcement/adrprotects.go` | `sacredADRRegistry`에 `"0016"` |
| 자기 자신 | `internal/enforcement/codeowners_test.go` | 0016 누락 fail-closed 케이스(0015 패턴 복제) |
| 형제 런북 | `docs/runbooks/loop-pr-environment-provisioning.md` | CODEOWNERS 줄 + `sacredRequiredPaths` 개별 등재 + 누락 fail-closed 테스트 + 증거 기록 대상을 새 추적 이슈로 정정 |
| 완결성 | `.github/CODEOWNERS` | `/docs/runbooks/ @chnu-kim` 디렉터리 규칙 추가(기존 `phase-b-entry.md` 정확 경로 줄은 유지 — 같은 소유자라 last-match-wins 무해) |
| 완결성(검사) | `internal/enforcement/instructionsurface_test.go` | **`TestSacredRequiredPaths_CoversEveryRunbook`**(`assertTreeFullyRegistered(t, "../../docs/runbooks", "docs/runbooks")` — 확장자 고정 glob이 아니라 **트리 전수**, R49) 신설. **오늘 실행하면 `loop-pr-environment-provisioning.md`를 지목하며 실제로 FAIL한다** — 디렉터리 규칙만 걸고 이 테스트를 빼면 이후 추가되는 런북이 무보호 + non-critical(`risk-classification.json:32`가 `docs/**`를 non-critical로 두고 `docs/adr/**`만 되돌린다)로 codex 단일 leg 자율 머지된다(R4·R11) |
| 무보호 표면 | `.github/CODEOWNERS` | **`/.claude/ @chnu-kim` 루트 디렉터리 규칙(R46) + `/.claude/skills/ @chnu-kim` + architect/issue-drafter/retro `SKILL.md` 개별 줄**(point 15). 루트 규칙이 없으면 `.claude/settings.json`(프로젝트 훅 = 임의 명령 실행)이 매칭 패턴 0건으로 들어온다 — `.gitignore:13`은 `settings.local.json`만 무시하므로 커밋 가능하다. self-owned `/.github/CODEOWNERS` 줄은 마지막 유지 |
| 무보호 표면 | `internal/enforcement/codeowners.go` | 세 `SKILL.md`를 `sacredRequiredPaths`에 개별 등재(point 15) |
| 무보호 표면(검사) | `internal/enforcement/instructionsurface_test.go` | `:31`의 하드코딩 `[]string{"dispatch-issue","opensource-maintainer"}`를 **`.claude/skills/*/` 디렉터리 순회**로 교체(0건이면 `t.Fatalf`) — 이후 추가되는 스킬 자동 포함. 오늘 실행하면 세 파일을 지목하며 Red(point 15). **추가로 `TestSacredRequiredPaths_CoversEveryClaudeRootFile`** — `.claude/` **트리 전수**(gitignore 대상 제외) 등재 강제(R46 + R71: 초안은 루트 파일만 봤고 "하위는 위 두 테스트가 이미 전수 순회한다"고 주석에 적었으나 그 둘의 실제 커버리지는 `.claude/skills/*`와 `.claude/agents/`뿐이라 `.claude/commands/`·`hooks/` 같은 새 하위 디렉터리가 어느 완결성 테스트에도 걸리지 않았다). **그리고 `:19-21`의 `CoversEveryAgentInstructionFile`도 `assertTreeFullyRegistered`로 in-place 교체**한다(R70 — `*.md` 고정 glob이라 `.claude/agents/lib/*.sh`류를 원리적으로 못 본다. 오늘 1/1 등재라 즉시 GREEN, 파괴적 Red로 실증) |
| 무보호 표면(분류) | `configs/gate/risk-classification.json` | **`{ "pattern": ".claude/**", "class": "non-critical" }`(`:39`) 줄을 삭제**해 레포 기본값(`:30` `**` = critical)으로 되돌리고(R47 — 좁은 되돌림 규칙을 얹는 하드닝 대신 카브아웃 자체를 제거), **`{"pattern": "docs/runbooks/**", "class": "critical"}`를 `docs/**`(`:32`) 뒤에** 추가(last-match-wins). 그래야 `.claude` 전 트리·런북 변경이 codex 단일 leg가 아니라 N-of-2를 받는다(point 15 (d), R31·R47). 실측상 `git ls-files .claude` 9개가 전부 이 PR 이후 sacred이므로 카브아웃 삭제의 순증 비용은 0 |
| 무보호 표면(분류-검사) | `internal/enforcement/riskclasspairing_test.go` | **신규** `TestSacredRequiredPaths_AreAllCriticalInRiskClassification` — `sacredRequiredPaths` 전 항목을 `gate.ClassifyChangedPaths`로 분류해 `critical`인지 단언(0건이면 `t.Fatalf`). 따름 규칙 5를 사람 규율에서 **검사**로 승격한다(R48). 이 검사가 없어서 `.claude/agents/go-tdd-implementer.md`가 "sacred인데 non-critical"로 두 라운드를 살아남았다 |
| 무보호 표면(경로) | `docs/verdict-gate-runbook.md` → `docs/runbooks/verdict-gate-runbook.md` | **이동**. 이 파일은 자기 헤더에서 "ADR-0011 point 4(e)/precondition ⑤와 실측 목록 10/11이 요구하는 재현 절차"라고 선언하는 게이트 절차서인데 `grep -rn "verdict-gate-runbook" .github/CODEOWNERS internal/enforcement/ configs/` → **0건**이고 `docs/` 직하라 `/docs/runbooks/` 규칙·glob 테스트·`docs/runbooks/**` critical 분류 **셋 다** 비껴간다(R30). 하드닝 대신 디렉터리 편입으로 위험 클래스를 제거하고, `sacredRequiredPaths`에도 개별 등재한다. 레포 안 참조 0건이라 이동 비용은 상대 링크 수정뿐 |
| 무보호 표면(절차) | `docs/adr/README.md` | CODEOWNERS 줄 + `sacredRequiredPaths` 개별 등재 + carve-out 테스트. `/docs/adr/00XX-*.md` 글롭에 매치되지 않아 무보호였는데, ADR status 절차(`Proposed → Accepted`)와 frontmatter 스키마를 정의하고 그것을 `codeowners.go:22`·`adrprotects.go:223,229`·`dispatch-issue/SKILL.md` §1이 직접 인용한다(R30). **glob 완결성 테스트는 두지 않는다** — `docs/adr/*.md` 전수 순회는 `protects:`가 빈 일반 ADR까지 등재를 요구하는 과-교정이고, 그 축은 `TestADRProtectsCompleteness_RealRepo`가 frontmatter 파생으로 이미 담당한다 |
| 무보호 표면(스키마 정본) | `docs/adr/0000-template.md` | CODEOWNERS 줄 + `sacredRequiredPaths` 개별 등재 + carve-out 테스트(R69). **`README.md`와 같은 근거가 더 직접적으로 적용된다**: `:7`의 `protects: []`가 `/architect`가 복사하는 정본이라, 그 줄을 지우거나 키 이름을 바꾸면 이후 모든 ADR이 빈 선언을 상속하고 아래 booked residual("`protects:`를 의도적으로 비운 신규 ADR은 어느 완결성 테스트에도 걸리지 않는다")이 **예외가 아니라 기본 경로**가 된다 → sacred ADR의 twin 자동 강제가 조용히 꺼진다. 오늘 무보호임은 실측: `.github/CODEOWNERS:21-45`의 ADR 규칙이 전부 `/docs/adr/00XX-*.md` 형태라 매치 0건이고, `adrFileNameRE`는 매치하지만 `protects: []`라 `TestADRProtectsCompleteness_RealRepo`가 건너뛴다. 등재 즉시 기존 `TestCheckCodeowners_EverySacredADRLineDroppedFailsClosed`(`codeowners_test.go:78-115`)가 `docs/adr/` 접두 경로를 자동 커버하므로 추가 검사 비용 0. (`why-adr.md`는 게이트 스키마·절차를 정의하지 않아 판별 질문에 **아니오** — 등재하지 않는다) |
| 검사기(승인된 확장) | `internal/enforcement/branchprotection.go`, `branchprotection_test.go` | point 14의 `enforce_admins == true` 레그(이슈 02). ADR-0017의 "비-twin" 행은 같은 라운드에서 `required_status_checks`/verdict-gate 축 한정으로 재서술한다 |
| 명령(수정) | `scripts/verify-credential-narrowing.sh` | point 7의 (a)~(c) 반영. 7-viii 상태 assert 레그가 load-bearing임을 헤더 주석에 1줄 명기(후속 세션이 '노이즈'로 오인해 제거하는 것 방지) |
| 검사(신설) | `scripts/verify-credential-narrowing_test.sh` | point 7(d)의 두 케이스. `/scripts/` 디렉터리 규칙이 커버하지만 `sacredRequiredPaths` 개별 등재 필수(last-match-wins) |
| 명령(수정) | `.claude/skills/opensource-maintainer/scripts/scan.sh` | point 9(b)의 `--allowlist=<절대경로>`. 이미 sacred 등재됨 — 내용 변경만 |
| 검사(수정) | `.claude/skills/opensource-maintainer/scripts/scan_test.sh` | `--allowlist` 회귀 3케이스(절대경로 존중·상대경로 exit 2 + stderr 문구·오타 인자 catch-all 보존). 이미 sacred |
| 절차 | `docs/runbooks/phase-b-entry.md` | ②에 step 0.5 선행 명시 · `:111`의 `enforce_admins`를 **목표값 `true`**로 · ③에 H4 신설 · '4스텝'→**6 핸드오프** · 긴급 재개방 절차 신설 · provisioning 런북 링크 추가 · point 7의 프로브 prereq 명문화 · **Step 0 통과판정의 파일 assert를 `origin/main` 기준으로 게이팅**(R64 — 로컬 트리를 검사하면 캠페인 산출물이 main에 없어도 W0가 열린다) · **"그 검사가 존재하는가"를 `go test` 종료 코드가 아니라 `--- PASS:` 출력으로 단언**(R65 — `-run` 매치 0건은 exit 0) · **Step 4-C에 `sacredRequiredPaths` 등재 커밋 명령 추가**(R66 — 없으면 그 PR의 CI가 영구 Red라 STEP4C_PASS 도달 경로가 없다) |
| 절차(상시 로드) | `CLAUDE.md` · `.claude/agents/go-tdd-implementer.md` · `.claude/skills/dispatch-issue/SKILL.md` | 세 파일 모두에 W0 문단("이 부트스트랩 창은 `enforce_admins` 재활성화(step 0.5)로 닫힌다 + App key 프로비저닝 완료까지 sacred 머지 경로 없음")을 넣는다. 없으면 다음 세션이 '부트스트랩 예외는 언제든 쓸 수 있다'고 오인해 교착에 빠진다. **셋 다 이미 sacred**(`CODEOWNERS:104`·`:105`·`:112`)이므로 **W0가 닫힌 뒤에는 App 경로 없이 고칠 수 없다** → 이 편집은 W0 진입 전에 끝나야 한다. **구현 작업 항목은 이슈 01 §J이며, 런북 Step 0 통과판정이 `grep -q 'W0'`로 세 파일을 assert해 누락 시 `STEP0_PASS`가 물리적으로 기록되지 않는다**(R34 반영: 초판은 이 행을 mandatory로 지정했는데 어느 이슈에도 대응 작업 항목이 없어 규칙만 바뀌고 절차는 구판 그대로 착지할 상태였다) |

### ADR-0011 / ADR-0015 amend 포인터 (어느 point에 무슨 문장을 넣는가)

- **`docs/adr/0011-…:39`** (Context branch protection 실측 항목) 끝에:
  > **Amend (ADR-0016)**: 이 `enforce_admins=true`는 **2026-07-08 스냅샷**이다. 2026-07-09에 소유자가 부트스트랩 자가승인 교착 해소를 위해 의도적으로 비활성화했고 2026-07-18·2026-07-23 두 독립 실측이 `false`로 수렴한다. **이 한 줄만 stale이며 아래 point 5의 규범 서술은 stale이 아니다** — 목표값은 여전히 `true`이고 그 복원 시점은 precondition ② 앞(ADR-0016 point 2, "step 0.5").
- **`docs/adr/0011-…:73`** (point 5 Phase B 문단의 `enforce_admins=true` 괄호) 뒤에:
  > **Amend (ADR-0016)**: 이 문장은 관측이 아니라 **규범**이다(Phase B 머지 판정이 성립하려면 `true`여야 한다). 2026-07-23 현재 값은 `false`이므로 이 요구는 **미충족**이며, 활성화는 flip payload가 아니라 별도 사람 액션(ADR-0016 point 2)이다.
- **`docs/adr/0011-…` point 5의 (a)/(b)/(c) 벡터 목록** 뒤에 **(d)** 항목 추가 — 본 ADR point 4의 블록 인용문 전문.
- **`docs/adr/0011-…:85`** (point 7) 뒤에:
  > **Amend (ADR-0016)**: 이 point의 면제 조건절("CODEOWNERS에 걸리지도 **않고**")은 #72 이후 `scan.sh`·`scan_test.sh`·`allowlist.txt`에는 성립하지 않는다(세 파일 모두 sacred 등재). 그럼에도 **이슈 #76은 Phase B blocking이 아니다** — 근거는 이 point의 문장이 아니라 point 4(a)의 범주적 배제다. `ci.yml`의 required-ness는 Phase B에서 게이트가 아니라 **비적대 회귀 신호**이며, public 레포의 push-time 노출은 CI가 아니라 GitHub push protection이 담당한다(ADR-0016 point 8·9).
- **`docs/adr/0015-…:82`** (point 7(b)의 "명시 보존" 목록):
  > **Amend (ADR-0016)**: `enforce_admins=true`는 이 목록에서 제외한다 — **보존 대상이 아니라 PUT 전 mandatory precondition assert**다. 생성기는 스냅샷이 `true`가 아니면 payload를 만들지 않고 abort하며, 스스로 `false`→`true`로 고치지 않는다(ADR-0016 point 3, 메커니즘은 ADR-0017 point 6).
- **`docs/adr/0015-…:94`** (point 8의 "전제는 `enforce_admins=false`(현 실측)"):
  > **Amend (ADR-0016)**: 이 전제는 **step 0.5 이전의 부트스트랩 창에만** 유효하다. 활성화 이후 이 경로는 구조적으로 봉쇄되며, 그때부터 W0 불변식(ADR-0016 point 5)이 적용된다.
- **`docs/adr/0015-…:83`** (point 7(c)의 "이 검사기는 `require_code_owner_reviews`만 파싱하고 `required_status_checks`는 보지 않으므로 code-owner 강제만 확인한다"):
  > **Amend (ADR-0016 point 14)**: 이 서술은 `CheckBranchProtection`에 `enforce_admins == true` 레그가 추가된 이후 부분적으로 stale하다 — 이제 code-owner 축 **과** `enforce_admins` 축 둘을 본다. **`required_status_checks`(verdict-gate required 여부)를 보지 않는다는 부분은 여전히 유효하며 그것이 이 point의 금지 대상이다**(phase-dependent → boot AND에 넣으면 Phase A가 깨진다). `enforce_admins`는 Phase A/B 어느 쪽이든 목표값이 항상 `true`이므로 phase 조건절 없이 assert한다.
- **`docs/adr/0015-…:95`** (point 8의 완화 경로 mandatory (iii)):
  > **Amend (ADR-0016)**: "두 번째 오퍼레이터가 보유한 복원 수단"은 collaborator가 1명인 이 레포에서 구조적으로 충족 불가능하다. `enforce_admins` 재개방 케이스에 한해 **"완화 개시 전 loop PAT revoke + loop 컨텍스트 무자격증명 실측"**으로 대체한다(ADR-0016 point 10).
- **`docs/adr/0015-…:70-76`** (point 6 핸드오프 목록):
  > **Amend (ADR-0016)**: 이 목록 앞에 **step 0(잔여 sacred PR 전부 머지)**과 **step 0.5(`enforce_admins` 활성화)**를 추가한다 — 총 6+1개 핸드오프(ADR-0016 point 6).

### 후속

- **ADR-0017**(flip 메커니즘 헌장)이 직접 후속이며, 이 ADR point 3의 assert-only 계약을 코드로 구현한다.
- **후속 포크(근거 ADR 없음, `/architect` 필요)**: (i) loop 자격증명의 non-admin 머신 계정 이관으로 vector (d) 구조적 제거, (ii) **`request-verdict` 개시 주체의 재설계** — 현재 가드는 sender **identity**(`chnu-kim`) 검사라 loop PAT(Contents:write)도 통과한다(위 booked residual, R42). 사람 개입을 강제하려면 App-토큰 전용 트리거·`workflow_dispatch` 전용화·loop PAT의 Contents:write 분리 중 하나가 필요하고, 반대로 loop 개시를 명시 허용하려면 그 사실을 위협모델에 반영해야 한다. 어느 쪽이든 ADR-0011 point 3/4(f)를 건드리는 결정이다, (iii) verdict 프롬프트의 시크릿 검사 항목 명시, (iv) 상시 branch-protection drift 감시(CI에 Administration:read 자격증명을 상주시키는 것 자체가 ADR-0009 point 6을 뒤집는 포크).
