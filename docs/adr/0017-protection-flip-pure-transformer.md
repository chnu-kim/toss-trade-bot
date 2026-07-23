---
id: "0017"
status: Proposed
date: 2026-07-23
deciders: [chnu-kim]
domain: [loop-governance, ci, auth]
protects: [enforcement-integrity]
supersedes: []
superseded_by: null
amends: ["0015"]
verification:
  - reviewer: multi-agent adversarial workflow (5 렌즈: fail-open·fail-closed-direction·twin-artifact·operator-execution·self-consistency)
    date: 2026-07-24
    verdict: 5라운드 적대 하드닝. flip payload 생성기를 쓰기 능력 없는 순수 변환기로 확정 — GET/PUT 별개 타입으로 contexts[] 핀 강등을 표현 불가능하게(라이브 GET에서 contexts·checks 공존 실측), restrictions 부재↔PUT null 비대칭·enforce_admins bare bool·--branch 필수(probe·main 동일 경로)·롤백 payload 동시 산출·전체 동치 비교 검증·소스 레벨 무-쓰기 테스트. enforce_admins는 생성기가 결정하지 않고 assert만(verify-credential-narrowing.sh 연역).
  - reviewer: codex (예정)
    date: null
    verdict: null
  - reviewer: chnu-kim (예정 — Accepted 승격·머지 게이트)
    date: null
    verdict: null
---

# ADR-0017: Phase B flip payload 생성기는 쓰기 능력이 없는 순수 변환기다 — 엄격 파싱·단일 뮤테이터·롤백 동시 산출·브랜치 파라미터화, 그리고 phase 개념 없이 기계 생성 기대상태로 검증한다

- **Status**: Proposed (적대 하드닝 전)
- **Date**: 2026-07-23
- **Deciders**: chnu-kim
- **관련 이슈/PR**: ADR-0015 point 7(이 ADR이 amend — flip 트랜잭션의 미구현 residual을 해소), ADR-0016(이 ADR의 선행 결정 — `enforce_admins` 목표값·W0 순서), ADR-0011 point 5 (b)(app_id 핀 = check-위조 벡터의 봉쇄), `docs/runbooks/phase-b-entry.md:95-102`(런북이 스스로 건 fail-closed 게이트)

## Context

Phase B flip은 **오늘 실행 불가능하다 — 런북 자신이 그렇게 선언한다.**

> `docs/runbooks/phase-b-entry.md:95-102` — "🚧 **선행 게이트 (fail-closed) — 현재 flip은 차단 상태다.** … **그 스크립트가 아직 없으므로 Phase B flip은 수행할 수 없다** — 오퍼레이터가 파괴적 full-replace payload를 손으로 조립하는 것은 **금지**다 … 그 전까지 이 절의 나머지는 **설계 명세**이지 실행 지시가 아니다."

`ls scripts/` → `verify-credential-narrowing.sh` 단 하나. `find . -iname '*flip*'` → 0건. **즉 flip 생성기의 부재가 이 캠페인에서 에이전트가 물리적으로 해소할 수 있는 유일한 blocking precondition이다.** 이 ADR은 그 생성기의 헌장을 확정한다.

이 결정을 강제하는 힘:

### 1. `PUT`은 full-replace이고, 오늘의 GET 응답은 그 사실이 조용히 파괴할 수 있는 필드를 실제로 담고 있다

`ADR-0015:81` point 7(a) — "`PUT`은 patch가 아니라 **전체 replace**이므로 payload에서 빠진 필드는 default/null로 리셋된다." 오늘 라이브 GET(`gh api …/branches/main/protection`, 2026-07-23) 실측:

- top-level 키 12개: `url, required_status_checks, required_pull_request_reviews, required_signatures, enforce_admins, required_linear_history, allow_force_pushes, allow_deletions, block_creations, required_conversation_resolution, lock_branch, allow_fork_syncing`. **`restrictions` 키는 부재**(=비활성).
- `enforce_admins` 등 9개 필드가 GET에서는 `{enabled: bool}` 오브젝트인데 PUT 바디에서는 **bare boolean**이다.
- `required_signatures`는 GET aggregate에 포함되지만 **PUT 바디 파라미터가 아니다**(별도 `GET/POST/DELETE …/protection/required_signatures` 엔드포인트 소관).
- `restrictions`는 PUT에서 `(object or null)` **required** — 키 생략이 허용되지 않으므로, GET에 키가 없는 현 상태를 보존하려면 명시적 `"restrictions": null`을 보내야 한다.
- `required_status_checks`에 `url`·`contexts_url`이 있는데 PUT에는 없다.

### 2. 스냅샷을 그대로 복사하는 어떤 구현도 app_id 핀을 강등시킬 실질 위험이 있다

`docs/runbooks/phase-b-entry.md:113-124`의 🔴 지시 — "`checks[]`를 쓰고 `contexts[]`를 쓰지 마라 — **app_id 소스 핀 강등 = check-spoofing**(codex #73 R7 [high])". 근거: `count=0` 이후 동명 required check가 비-sacred 경로의 **주 게이트**이므로 핀 상실은 곧 `ADR-0011 point 5 (b)` check-위조 벡터의 재개방이다.

그런데 오늘 실측상 GET 응답에는 **`checks`와 `contexts`가 둘 다 존재한다**:
```
required_status_checks.checks   = [{"context":"build · vet · gofmt · test-race","app_id":15368}]
required_status_checks.contexts = ["build · vet · gofmt · test-race"]
```
즉 "스냅샷을 성실히 복사한다"는 순진한 구현이 `contexts`를 함께 실어 보낼 실질 경로가 열려 있다. 그리고 GitHub 공식 문서는 `contexts`를 `(required)`로 표기하면서 closing-down 공지를 달고 있고 PUT 예시에는 `checks[]`가 **아예 없다** — 문서만으로는 "checks만 보내도 되는가"가 확정되지 않는다(미해소 gap, 아래 point 8·잔여 위험).

또한 `checks[].app_id`는 옵션 필드이며 공식 문서가 "**Omit this field to automatically select the GitHub App that has recently provided this check, or any app if it was not set by a GitHub App**"이라 명시한다 — `checks[]` 표현을 쓰더라도 `app_id`를 빼면 핀이 "아무 소스나 허용"으로 격하된다.

### 3. 패키지 배치가 twin 배선 비용을 결정한다 (실측)

`internal/enforcement/codeowners_test.go:566-591` `TestSacredRequiredPaths_CoversEveryEnforcementGoFile`은 `os.ReadDir(".")`로 패키지를 전수 순회해 미등재 `.go` 파일마다 `t.Errorf`하고, `found == 0`이면 `t.Fatal`한다. `.github/CODEOWNERS:97`에 `/internal/enforcement/ @chnu-kim`도 이미 있다. **즉 이 패키지에 파일을 두면 sacred 등재를 잊는 순간 CI가 죽는다.** 신규 패키지를 만들면 그 자동 강제를 처음부터 다시 배선해야 하고, 배선을 잊으면 "새 게이트 아티팩트가 조용히 무보호로 남는" 이 레포의 반복 실패형이 된다.

### 4. bash를 고르면 CI 배선 실패 모드를 자발적으로 새로 만든다 (실측)

`.github/workflows/ci.yml:51`이 이미 `go test -race ./...`를 돌린다 → **Go 안이면 신규 CI 배선이 0줄**이고 배선 누락 실패 모드가 존재하지 않는다. 반면 같은 파일 `:63-68`이 이렇게 적고 있다:

> "커밋 전 시크릿 게이트(`scan.sh`)의 회귀 테스트를 CI에서도 강제한다. `scan_test.sh`는 **go test가 아닌 독립 셸 테스트라 CI에 명시 배선하지 않으면 게이트가 조용히 false-green으로 퇴행해도 CI가 초록으로 통과할 수 있다.**"

즉 이 레포는 셸 테스트의 미배선 false-green을 **이미 겪고 문서화한** 결함 클래스를 갖고 있다.

### 5. boot presence-check를 확장하면 Phase A가 깨진다 — 그런데 ADR-0015가 요구한 flip-전용 함수는 아직 없다

`internal/enforcement/branchprotection.go:19-23`의 `branchProtectionResponse`는 `required_pull_request_reviews.require_code_owner_reviews` **단 한 필드만** 파싱한다. `required_status_checks`도 `enforce_admins`도 구조체에 선언조차 없다.

`ADR-0015:83` point 7(c) 괄호가 그 이유를 명시한다 — "`CheckBranchProtection`을 verdict-gate 필수까지 검증하도록 확장하면 **Phase A(verdict-gate 미-required) presence-check가 fail-closed로 깨지므로**, 그 확장은 boot presence-check가 아닌 **flip-전용 함수**여야 한다." `docs/runbooks/phase-b-entry.md:126-127`도 같은 것을 "별도 `GET …/protection` assertion"으로 부른다. **그 함수는 문서 설계일 뿐 코드로 존재하지 않는다.**

`internal/enforcement/presence.go:47-88`의 `Run()`은 3 pillar를 fail-closed AND로 집계한다. 미실측 상수를 이 AND에 넣으면 값이 틀렸을 때 "자율작업 시작해도 되는가" 오라클 전체가 영구 false로 붕괴한다 — fail-closed가 아니라 과-halt다.

### 6. 롤백 지시가 문자 그대로는 실행 불가능하다

`docs/runbooks/phase-b-entry.md:132` — "**실패 시 원자 롤백**: 어느 검증이든 실패 → 즉시 **스냅샷으로 PUT 롤백**". 그러나 스냅샷은 GET 형상(`url`/`contexts_url`/`required_signatures`/`{enabled:…}` 포함)이라 **PUT 바디가 아니다.** 롤백에도 같은 GET→PUT 변환이 한 번 더 필요한데, 그 시점은 검증 실패 직후(사고 중)다 — "손 조립 금지" 원칙이 가장 깨지기 쉬운 순간이다.

### 7. probe 리허설이 mandatory인데 도구가 main 고정이면 리허설이 불가능하다

`docs/runbooks/phase-b-entry.md:63`이 precondition ④(main 무접촉 probe)의 요구사항으로 "**flip payload 문법·권한 리허설도 함께**"를, `:66` 통과판정에 "**payload 유효**"를 명시한다. 도구가 main을 하드코딩하면 probe 브랜치용 payload를 오퍼레이터가 **손으로 만들어야** 하고, 이는 런북이 금지한 그 행위를 하필 malformed payload를 검출하라고 만든 단계에서 수행하게 만든다.

### 8. 완결성 검사가 한 곳에만 있다 (실측)

`grep -rn "filepath.Glob" internal/enforcement/` → `instructionsurface_test.go:43` **단 1건**. 그 헬퍼 `assertGlobFullyRegistered`(`:41-69`)는 미등재 시 `t.Errorf`, 매치 0건이면 `t.Fatalf`(vacuous pass 방지)로 이미 올바른 형태이지만, 호출부는 `.claude/agents`·`.claude/skills/{dispatch-issue,opensource-maintainer}`뿐이다. **`scripts/`·`configs/gate/`·`docs/runbooks/`·`cmd/`에는 완결성 테스트가 없다** — 즉 이 ADR이 추가하는 신규 파일들이 손으로 등재하지 않으면 조용히 무보호로 남는 구조다. `.github/CODEOWNERS`에도 `/cmd/` 캐치올이 없다(`/cmd/verdict-gate/`가 유일한 cmd 규칙, `:72`).

---

## Decision

**flip 생성기는 `internal/enforcement` 안의 순수 변환 라이브러리 + 쓰기 능력이 없는 얇은 CLI다. "phase"라는 개념을 강제 경로에 도입하지 않고, 기대상태를 스냅샷에서 결정론적으로 파생해 전체 동치 비교로 검증한다. 파괴적 `PUT`은 도구가 절대 호출하지 않는다 — 사람이 한다.**

### A. 배치와 언어

1. **위치: `internal/enforcement/protectionstate.go` + `internal/enforcement/protectionflip.go` + `cmd/protection-flip/main.go`.** 신규 패키지를 만들지 않는다. 근거는 Context 3 — `TestSacredRequiredPaths_CoversEveryEnforcementGoFile`이 이 패키지의 신규 `.go` 파일에 대한 sacred 등재를 **자동으로 강제**하므로, 패키지 선택 자체가 "등재 망각" 위험 클래스를 제거한다. `cmd/protection-flip/`은 `/cmd/` 캐치올이 없으므로 CODEOWNERS 신규 줄 + `sacredRequiredPaths` 개별 등재 + carve-out 테스트를 같은 PR에 넣는다.

2. **Go로 쓴다 — bash + `jq`가 아니다.** 근거 셋. (i) Context 4의 CI 배선 비대칭. (ii) bash 구현은 테스트 가능성을 위해 `--snapshot <file>` 같은 **외부 JSON 입력구를 프로덕션 바이너리에 노출**해야 하는데, Go 라이브러리에서는 테스트가 CLI가 아니라 `Transform()`을 직접 부르므로 그 입력구가 존재할 이유 자체가 사라진다 — "조작된 스냅샷 주입" 벡터가 하드닝이 아니라 **구조적으로 소멸**한다. (iii) `jq`는 부재 키·오타 필드를 조용히 `null`로 반환하고, `set -uo pipefail`(`verify-credential-narrowing.sh:40` 컨벤션 — `-e` 아님) 하에서 10개 이상 `jq` 호출 중 하나라도 종료코드 체크를 빠뜨리면 malformed 출력이 payload로 흘러든다. Go 타입에서는 아래 point 4가 이 실패 형태를 **컴파일 불가**로 만든다.

### B. 타입과 파싱 — 안전 속성을 표현 불가능성으로 옮긴다

3. **GET과 PUT을 별개 타입으로 분리한다.** `GetProtection`(GET 응답 전체 형상) / `PutProtection`(PUT 바디). 변환은 `ToPutShape(GetProtection) (PutProtection, error)` 한 곳에만 존재한다.
   - `{enabled: bool}` 래핑 9필드(`enforce_admins`, `required_signatures`, `required_linear_history`, `allow_force_pushes`, `allow_deletions`, `block_creations`, `required_conversation_resolution`, `lock_branch`, `allow_fork_syncing`)는 언랩한다.
   - `required_signatures`는 **`PutProtection`에 필드 자체를 선언하지 않는다** — PUT 바디 스키마에 없으므로(Context 1) 방출 불가가 타입으로 보장된다.
   - `Restrictions *PutActors`에 `omitempty`를 **붙이지 않는다** — nil이어도 `"restrictions": null`이 항상 방출되게 한다. GET 키 부재(오늘 실측) ↔ PUT required의 비대칭을 태그 하나로 고정한다.
   - `GetActors`(오브젝트 배열) → `PutActors`(users는 `.login`, teams/apps는 `.slug` 문자열 배열).
   - `RenderPUT`은 top-level 11개 키를 **전부** 방출한다(4개는 값이 null이어도 키 필수).

4. **`PutRequiredStatusChecks`에 `Contexts` 필드를 선언하지 않는다.** `phase-b-entry.md:113`의 🔴 지시가 런타임 assertion이 아니라 **표현 불가능성**이 된다. Context 2가 보여주듯 오늘 GET에는 `contexts`와 `checks`가 둘 다 있어 "성실한 복사"가 실제로 핀을 강등시킬 수 있으므로, 타입에서 지우는 것이 유일한 구조적 봉쇄다. (문서상 `contexts` required 표기와의 충돌은 point 8의 probe 리허설이 liveness 실패로만 드러내게 한다.)

5. **파서는 엄격하다 — 모르는 것을 조용히 버리지 않는다.** `ParseProtectionGET`은 다음을 전부 **hard error**로 처리한다(이것이 이 설계의 핵심 안전 속성이다: full-replace PUT에서 파서가 모르는 필드를 버리면 그 필드가 default로 리셋된다).
   - top-level 허용 키 집합(오늘 실측 12개 + `restrictions`·`protection_url`) **밖의 키가 하나라도 존재**.
   - `required_pull_request_reviews.dismissal_restrictions` 또는 `bypass_pull_request_allowances`가 **값과 함께 존재** — PUT payload로 무손실 재현이 불가능하므로 사람 개입을 요구한다.
   - `{enabled}` 래핑 9필드 중 **어느 하나라도 키가 부재** — 응답 모양 변화를 조용히 `false`로 읽지 않는다.
   - `required_status_checks.checks`가 없고 `contexts`만 존재 — app_id 핀 없는 레거시 표현을 정본화하면 핀이 소실된다.
   - `required_status_checks` 블록 자체가 부재 — 현 레포 상태에서 이는 이미 게이트 소멸을 뜻한다.

### C. 뮤테이션 — 무-게이트 창을 탐지가 아니라 생성 불가로 만든다

6. **`Transform(s PutProtection, opt FlipOptions) (flip, rollback PutProtection, err error)` 하나가 유일한 export 뮤테이터다.** `required_approving_review_count` 1→0과 `checks[]`에 verdict context 추가를 **하나의 값 안에서 동시에** 수행한다. count만 낮추는 코드 경로가 존재하지 않으므로 `ADR-0015:87`이 프로즈로 금지한 무-게이트 창(count=0 + verdict 미등재)이 **산출 불가능**해진다 — 사후 탐지가 아니라 클래스 제거다.

   > **각주 (R37 반영 — ADR 내부 이름 불일치)**: 초판은 이 뮤테이터를 `ApplyPhaseBDelta`로, 입력 타입을 `ProtectionState`로 불렀으나 같은 ADR의 V1 테스트 목록(`TestTransform_*`)과 구현 이슈(03)는 전부 `Transform`이고, point 3이 확정한 타입은 `GetProtection`/`PutProtection`이다(`ProtectionState`라는 타입은 이 ADR 어디에도 정의돼 있지 않다). **`Transform` + `PutProtection`으로 통일**한다 — 입력을 PUT 형상으로 두면 point 7의 "rollback은 `ToPutShape(snapshot)`의 항등"이 타입 수준에서 성립한다.

   **abort 조건(전부 error, 부분 결과 반환 금지)**:
   - `snap.EnforceAdmins != true` → `ErrEnforceAdminsNotEnforced`. **override 플래그를 두지 않는다**(ADR-0016 point 3). 에러 메시지에 근거 연역(`verify-credential-narrowing.sh:232-239` + `:339-344` → precondition ②가 exit 0 불가)과 조치("먼저 `POST …/protection/enforce_admins`(ADR-0016 step 0.5)를 수행하라")를 담는다.
   - `required_pull_request_reviews` 블록 부재 또는 `require_code_owner_reviews != true` → abort (`ADR-0015` BLOCKING-1 — 이 필드가 사라지면 count=0 + code-owner off로 사람 게이트가 소멸한다).
   - `checks[]`가 비었거나 **어느 항목이든 `app_id`가 nil** → abort (핀 강등 검출).
   - `opts.VerdictContext == ""` 또는 `opts.VerdictAppID` 미지정/0 이하 → abort. **기본값을 두지 않는다**(point 11).
   - `opts.VerdictContext`가 이미 `checks[]`에 존재 → abort (중복 생성 금지).
   - `required_approving_review_count != 1` → abort (예상 밖 시작 상태를 추측하지 않는다).
   - `bypass_pull_request_allowances`가 값과 함께 존재 → point 5에서 이미 파싱 단계 abort.

7. **롤백 payload를 flip payload와 **같은 실행에서** 산출한다.** `plan`은 `snapshot.json`(원문 그대로 — 증거) · `flip-payload.json` · `rollback-payload.json` 세 아티팩트를 **PUT 이전에** 쓴다. 근거는 Context 6 — 롤백을 "실패 시 실행돼야 하는 코드"에서 "미리 계산된 아티팩트"로 강등하면, apply 프로세스가 통째로 죽어도 복구는 `gh api --method PUT … --input rollback-payload.json` 한 줄이다. `rollback`은 `ToPutShape(snapshot)`의 항등이며 `TestTransform_RollbackIsIdentityOfSnapshot`이 이를 고정한다.

### D. 쓰기 능력의 부재 — 사람 축과 에이전트 축을 코드 구조로 가른다

8. **CLI는 `plan`과 `verify` 두 서브커맨드만 갖는다. `apply`를 만들지 않는다.** 파괴적 `PUT`은 도구가 절대 호출하지 않고 사람이 `gh api --method PUT … --input payload.json`으로 실행한다. 이것이 `ADR-0015 point 2`의 "에이전트 admin 범위 vs 사람 전용(Administration:write)" 축을 문서 규율이 아니라 **코드 경계**로 고정하는 지점이며, 에이전트 저작 도구가 Administration:write를 쥐는 위험 클래스를 제거한다.
   - `plan --branch <name> --set-review-count 0 --add-check <context>:<app_id> --out <dir>` — `GH_TOKEN`은 **env로만** 받는다(플래그 금지 — 프로세스 인자·셸 히스토리 노출 방지). `GET …/branches/{branch}/protection`만 수행한다.
   - `verify --branch <name> --expect <payload.json>` — 재-GET → `ToPutShape` → 의도한 payload와 **구조적 deep-equal**. 불일치가 있으면 항목별 출력 후 non-zero.
   - **`--branch`는 필수이고 기본값이 없다.** main 하드코딩을 금지하는 이유는 Context 7 — probe 브랜치와 main이 **같은 코드 경로**를 타야 런북이 mandatory로 요구한 payload 리허설이 실제 리허설이 된다. 이 파라미터화 덕분에 미해소 gap(`contexts[]` 필수 여부)이 **probe 브랜치의 422**로만 드러난다(main 무접촉). 즉 이 설계는 문서 불확실성을 **liveness 쪽으로 몰고 safety 쪽에서 뺀다**.
   - **종료 코드**: `0` 성공 / `1` assertion·검증 실패 / `2` 인자·환경·네트워크 오류. `verify-credential-narrowing.sh:36-39`의 3치 계약과 동일하게 맞춘다.

9. **"나중에 `--apply`가 붙는" 드리프트를 사람 리뷰가 아니라 테스트로 막는다.** `internal/enforcement/protectionnowrite_test.go`가 **`internal/enforcement/`와 `../../cmd/protection-flip/`의 `.go` 파일 전수**(파일명 접두 조건 없음, 하위 디렉터리 포함)를 읽어 `http.MethodPut|MethodPatch|MethodDelete|"PUT"|"PATCH"|"DELETE"` 출현 시 `t.Errorf`한다. 파일 매치 0건이면 `t.Fatalf`(vacuous pass 봉쇄). **needle은 소스 리터럴이 아니라 조각 결합(`"Method"+"Put"` 등)으로 만든다** — 그래야 스캐너 자신이 자기 needle에 매치되지 않고, 따라서 **예외 목록(자기 파일 skip)이 아예 필요 없다**. 이 가드가 실제로 무는지는 임시로 리터럴을 넣어 실패를 확인한 뒤 되돌려 실증한다(V4).

    > **각주 (R62 반영 — 접두 열거는 다음 파일을 놓치고 vacuous 가드도 발동하지 않는다)**: 초판은 스캔 대상을 `internal/enforcement/protection*.go`로 **파일명 접두**로 지정했다. 그런데 같은 라운드의 구현 이슈 03 §3은 "`plan`/`verify`의 GET은 **base URL 주입 가능한 얇은 클라이언트**로 두고 테스트는 `httptest`를 쓴다"고 명세하므로 HTTP 코드가 담길 새 파일이 실제로 생기며, 그 이름이 `protection`으로 시작하지 않으면(예: `flipclient.go`) **스캔 대상 밖**이다. 게다가 `protection*.go`가 최소 2개(`protectionstate.go`·`protectionflip.go`) 매치되므로 `t.Fatalf` vacuous 가드는 **절대 발동하지 않아** 그 사각지대를 알려주지도 않는다. 이는 이 캠페인이 CLAUDE.md 따름 규칙 1로 승격한 "테스트 자체를 손으로 열거하지 않는다"가 **레퍼런스 구현에서 다시 어겨진** 사례다.
    >
    > **패키지 전수로 바꾸는 것이 과-교정이 아닌 근거(실측)**: `grep -ln 'MethodPut|MethodPatch|MethodDelete|"PUT"|"PATCH"|"DELETE"' internal/enforcement/*.go cmd/*/*.go` → **0건**. 즉 오늘 `internal/enforcement` 전 패키지가 이미 write-free이고(이 패키지는 설계상 read-only checker다 — `presence.go`·`codeowners.go`·`branchprotection.go` 전부 GET만 한다), 전수 스캔은 **즉시 GREEN**이다. 예외 allowlist를 아예 두지 않으므로 "예외가 늘어나는 것을 리뷰 신호로 삼는다"는 우회 여지도 없다 — 이 패키지에 write가 필요해지는 순간 그 자체가 별개 결정이고 이 테스트가 그것을 diff로 드러낸다.

### E. 검증 — 손 열거를 전체 동치로 대체하고, 기대상태를 phase가 아니라 아티팩트로 둔다

10. **사후 검증은 4개 항목 열거가 아니라 전체 동치 비교다. 단 `checks[]`는 context명 기준 정렬 후 비교한다(순서 비민감).**

    > **각주 (R8 반영 — 과-halt 방지)**: GitHub이 PUT 이후 재-GET에서 `checks[]` 순서를 전송 순서대로 보존한다는 보장은 문서에도 이 레포 실측에도 없다. `DiffProtection`이 순서-민감 비교(`reflect.DeepEqual`류)를 택하면 **Step 4-B(main 실제 flip)의 `verify`가 의미 없는 순서 차이만으로 실패해 즉시 롤백을 유발**한다 — 실패 방향은 Phase A 잔류(안전)지만 불필요한 파괴적 왕복이고, 이 레포가 실제로 겪은 과-교정 클래스다. 정렬로 느슨해지는 것은 `TestDiffProtection_ChecksSetDifferenceStillDetected`(항목 추가/삭제/`app_id` 변경은 여전히 잡힘)가 대조군으로 막는다. 실측은 probe 리허설(V3 ②)에서 전송 순서 대 재-GET 순서를 1회 `diff`해 잔여 위험 절에 기록한다.
 `phase-b-entry.md:126-131`이 열거한 네 검증(verdict-gate context 실재 / 항목별 app_id 유지 / 두 필드 외 무변경 / code-owner)은 손-열거라 다음 필드를 놓친다. `verify`가 재-GET → `ToPutShape` → `DiffProtection(want, got)`로 판정하면 silent drop이 어느 필드에서 나든 자동으로 잡힌다.
    **`internal/enforcement/presence.go`·`branchprotection.go`·`cmd/presence-check`를 `required_status_checks`/verdict-gate 축으로 확장하지 않는다 — 이 ADR의 산출물은 그 세 파일을 한 바이트도 바꾸지 않는다** — Context 5의 boot-brick 과-halt를 원천 차단한다.`verify`는 `CheckBranchProtection`의 대체가 아니라 **다른 축의 보완**이다(그 검사기는 `require_code_owner_reviews` 하나만 본다). 이 "건드리지 않음"을 명시 결정으로 기록해 후속 세션의 중복 구현·오확장을 막고, `TestRun_UnaffectedByVerdictGateAbsence`(verdict-gate가 required가 **아닌** protection 응답에서 다른 두 pillar가 met이면 `Run().Satisfied == true`)로 회귀 가드를 건다.

    > **각주 (R12·R26 반영 — 스코프 정정)**: 초판은 이 문장을 "판정 로직을 한 바이트도 바꾸지 않는다"로 무조건 서술했는데, **같은 라운드의 이슈 02가 정확히 `branchprotection.go`를 수정한다**(`branchProtectionResponse`에 `EnforceAdmins` 추가 + `CheckBranchProtection`에 무조건 assert 레그). 실측상 오늘 `branchprotection.go:19-23`은 `RequiredPullRequestReviews` 하나만 선언하므로 그 변경은 실제로 파일 내용을 바꾼다 — 즉 **게이트 검사기와 그것을 지배하는 규칙이 main에서 서로 모순된 상태로 착지**할 뻔했다. 금지의 진짜 근거는 Context 5(= `required_status_checks`/verdict-gate required 여부는 **phase-dependent**라 boot AND 집계에 넣으면 Phase A가 깨진다)이지 "이 파일을 절대 못 건드린다"가 아니다. `enforce_admins`는 Phase A/B 어느 쪽이든 목표값이 **항상 `true`**이므로(ADR-0016 point 1) phase-dependent가 아니고, 과-halt 비용도 실측상 0이다(identity pillar c-2가 이미 unmet이라 `Run().Satisfied`가 오늘도 false이고, presence-check는 어디에도 배선돼 있지 않다). **그래서 금지를 `required_status_checks`/verdict-gate 축으로 한정하고, `enforce_admins` 레그 추가는 ADR-0016 point 14가 명시 승인한다.**

11. **verdict check의 `app_id`는 실측값만 받는다 — 기본값도 하드코딩도 없다.** `15368`은 `verdict-gate.yml:1347-1353`이 ambient `GH_TOKEN`을 쓴다는 **코드 정독 기반 추정**일 뿐, 실제 게시된 check-run에서 측정된 적이 없다. 틀리면 두 방향 모두 실패한다: 존재하지 않는 소스로 핀하면 check가 영원히 안 붙어 **liveness 붕괴(과-halt)**, 핀을 빼면 위조 가능한 required check가 된다. 값은 `gh api repos/{o}/{r}/commits/<sha>/check-runs --jq '.check_runs[]|select(.name=="verdict-gate")|.app.id'`로 실측한 뒤 `--add-check verdict-gate:<측정값>`으로 넘긴다. **필수 인자이므로 도구 자체가 이 순서를 강제한다.**

12. **phase 개념을 강제 경로에 도입하지 않는다. 기대상태는 phase 열거(A/B)에서도 관측값(count) 파생에서도 오지 않고, 생성기가 스냅샷에서 결정론적으로 만든 정본 아티팩트에서 온다.**
    `기대상태 = 스냅샷 + 정확히 두 개의 델타`이므로 phase enum은 그 등식의 **손실 압축**이고, 손실되는 정보(app_id 핀, restrictions, 나머지 boolean들)가 바로 full-replace PUT이 조용히 파괴할 수 있는 것들이다. **그 정본 아티팩트는 point 7의 `flip-payload.json`이다 — 별도의 `expected.json`을 만들지 않는다.** flip 성공 후 사람이 **그 `flip-payload.json`을** `configs/gate/main-protection.expected.json`으로 커밋한다(code-owner 리뷰 필수, 절차는 `docs/runbooks/phase-b-entry.md` Step 4-B 성공 분기). 사전 등재는 불필요하다 — 파일이 커밋되는 순간 point 14의 glob 완결성 테스트가 Red가 되어 등재를 강제한다(forcing function).

    > **각주 (R40 반영 — 정본 아티팩트에 생산자도 절차도 없었다)**: 초판은 이 point에서 "`plan`이 낸 `expected.json`"을 전제했는데 같은 ADR point 7은 `plan`의 산출물을 `snapshot.json`·`flip-payload.json`·`rollback-payload.json` **세 개**로 못박고 구현 이슈(03)도 동일했다 — `expected.json`을 만드는 코드가 어디에도 없었다. 절차 쪽도 실측상 `grep -c "expected.json" docs/runbooks/phase-b-entry.md` → **0**(커밋 지시 부재). 그 결과 point 14의 `configs/gate` glob 테스트는 `risk-classification.json` 하나만 매치하는 **영구 green**으로 남아, "기계 생성 아티팩트가 손실 압축을 대체한다"(Alternatives의 phase.json 기각 근거)는 서술이 코드·절차 어디에도 실체가 없었다. **네 번째 아티팩트를 추가하는 대신 이름을 하나로 접었다** — `verify --expect`가 받는 파일과 커밋되는 파일이 같아야 "무엇이 correct인지"의 정의가 한 곳에만 존재한다. `:237`(사람 액션 핸드오프)·V9·twin 표도 같은 라운드에서 재서술했다.

### F. 완결성 — 열거가 아니라 파생으로

13. **이 ADR과 신규 코드의 sacred 등재(게이트 승격 규칙).** 이 생성기는 "고치면 게이트 판정이나 그 판정의 증거가 바뀌나?"에 명백히 예다 — payload가 게이트 자체를 정의하고 `verify`가 그 판정의 증거를 만든다. 같은 PR에서:
    - `.github/CODEOWNERS`에 `/docs/adr/0017-*.md @chnu-kim`, `/cmd/protection-flip/ @chnu-kim`
    - `sacredRequiredPaths`에 `docs/adr/0017-protection-flip-pure-transformer.md`, `cmd/protection-flip/main.go`
    - `sacredADRRegistry`에 `"0017"`
    - `codeowners_test.go`에 0017 누락 fail-closed 케이스와 `/cmd/protection-flip/main.go` carve-out 케이스
    - `internal/enforcement/protection*.go`는 기존 `TestSacredRequiredPaths_CoversEveryEnforcementGoFile`이 자동 강제한다(별도 감지 계층 불요)

14. **완결성 테스트 5종(+ 비용 0의 보강 1종)을 신설한다 — 전부 매치 0건이면 `t.Fatalf`로 vacuous pass를 막는다.** 보강 1종은 위 Consequences 각주(R17)의 `.github/workflows/` 트리 순회로, 오늘 상태에서 즉시 green이다(회귀 가드 목적). 다섯 번째(`CoversEveryGateLogicFile`, `internal/gate` non-test 전수)도 오늘 즉시 green이다(R68). Context 8이 보여주듯 이 레포에는 완결성 검사가 한 곳뿐이라, 지금 추가하는 파일들도 손으로 안 적었으면 조용히 무보호로 남았을 것이다. "열거보다 완결성" 규칙의 정확한 적용 대상이다.
    - `TestSacredRequiredPaths_CoversEveryActivationScript` — `assertTreeFullyRegistered(t, "../../scripts", "scripts")`(확장자 고정 glob이 아니라 트리 전수 — `scripts/lib/*.sh`류 하위 자산도 잡는다, R49). **Red 증명: 오늘 추가하면 `sacredRequiredPaths`에 `verify-credential-narrowing.sh` 하나뿐이라 ADR-0016이 추가하는 신규 스크립트들이 미등재로 실패한다.**
    - `TestSacredRequiredPaths_CoversEveryGateConfigFile` — `configs/gate/*.json`. 오늘은 `risk-classification.json` 하나뿐이라 GREEN이지만, `main-protection.expected.json`이 커밋되는 순간 등재 없이는 Red가 된다(point 12의 forcing function).
    - `TestSacredRequiredPaths_CoversEveryRunbook` — `docs/runbooks/*.md`. **Red 증명: 오늘 실패한다** — `loop-pr-environment-provisioning.md`가 미등재이고, 이슈 01 §E-2가 `docs/verdict-gate-runbook.md`를 이 디렉터리로 옮기면 그 파일도 함께 지목된다(ADR-0016 point 12가 같이 해소). **디렉터리 밖에 있던 `docs/verdict-gate-runbook.md`는 이 glob이 원리적으로 못 잡았다는 점이 이 결정의 한계였고, 그래서 하드닝이 아니라 파일 이동(위험 클래스 제거)으로 닫는다.**
    - `TestSacredRequiredPaths_CoversEveryGateDependentCommand` — `cmd/*`를 순회하며 `go/parser`(`parser.ImportsOnly`)로 import를 읽어 `internal/enforcement` 또는 `internal/gate`를 import하는 디렉터리의 **모든 non-test `.go` 파일**이 등재됐는지 검사. cmd 디렉터리를 하나도 못 찾으면 `t.Fatalf`. **Red 증명: 오늘 실패한다** — `cmd/presence-check/main.go`가 `internal/enforcement`를 import하는데 `sacredRequiredPaths`에도 CODEOWNERS에도 **없다**(실측). 손 목록이 아니라 import 그래프에서 파생하므로 향후 추가되는 게이트-의존 커맨드가 자동 포함되고, `cmd/bot`은 둘 다 import하지 않아 **자동 제외**된다(손 예외목록 불요).
    - `TestSacredRequiredPaths_CoversEveryGateLogicFile` — `internal/gate`의 **non-test `.go` 파일 전수**(같은 `_test.go` 제외 필터를 위 항목과 공유). 오늘 10/10 등재라 **즉시 GREEN**(회귀 가드). 특권 verdict 잡이 이 패키지 전체를 컴파일·실행하므로 새 판정 로직 파일이 미등재로 들어오는 것을 막는다 — R41이 `*.go` 전수 변형을 기각한 근거는 non-test 한정 변형에는 성립하지 않는다(R68).

      > **각주 (R61 반영 — "모든 `.go`"는 별개 결정을 드라이브바이로 뒤집는다)**: 초판은 non-test 한정을 적지 않았다. 실측상 `cmd/verdict-gate/`에는 `main.go` 외에 `main_test.go`·`workflow_diff_fetch_test.go`·`workflow_lint_test.go` 셋이 더 있고 `codeowners.go:112`는 `main.go`만 등재하므로, 초판대로면 Red가 예고한 1건이 아니라 **4건**이고 green으로 만들려면 `codeowners.go:96`이 명시 결정으로 기록한 "gate 축은 **non-test만** 등재"를 뒤집게 된다(R41이 `internal/gate` glob에서 잡아 삭제한 것과 같은 실패형의 `cmd/*` 축 재발). **non-test로 한정**하고, 테스트 파일의 sacred 승격은 아래 "후속"(iv)에 book한다.

    **`/cmd/` 전체를 CODEOWNERS 디렉터리 규칙으로 승격하지는 않는다** — `cmd/bot/`(제출 게이트·킬스위치 부팅 순서, ADR-0004 영역)까지 Phase B에서 code-owner 게이팅 대상이 되는 것은 제품 코드 자율성을 줄이는 **별개 결정**이라 드라이브바이로 처리하지 않는다(후속 포크로 book).

---

## Alternatives considered

- **bash + `jq` 스크립트(`scripts/generate-phase-b-flip-payload.sh`) + `scan_test.sh` 스타일 픽스처 회귀 테스트** — 기각. (i) **자기 설계가 fail-open을 강제한다**: 테스트 가능성을 위해 `--snapshot <file>` 입력구를 프로덕션 바이너리에 노출해야 하므로 "조작된 스냅샷 주입" 벡터가 구조적으로 제거되지 않는다. Go 라이브러리에서는 테스트가 함수를 직접 부르므로 그 입력구 자체가 불필요해진다. (ii) **호출자 배선 리스크를 새로 만든다**: `ci.yml:51`이 이미 `go test -race ./...`를 돌리므로 Go는 신규 CI 배선 0줄인 반면, 셸 테스트는 명시 스텝이 필요하고 `ci.yml:63-68`이 그 누락을 "조용한 false-green 퇴행"으로 **이미 문서화**한 결함 클래스다. (iii) **`contexts[]` 금지가 런타임 assert로 남는다**: `jq`는 부재 키를 조용히 `null`로 반환하고 `set -uo pipefail` 하에서 종료코드 체크 하나만 빠뜨려도 malformed 출력이 payload로 흘러든다. Go 타입에서 `Contexts` 필드를 삭제하면 이 실패 형태는 컴파일 불가가 된다.

- **`workflow_dispatch`(actor==chnu-kim) CI 잡에서 스냅샷→변환→PUT→검증→롤백을 원자 수행** — 기각. `docs/adr/0009-adr-autonomy-sacred-invariant.md:57` 원문 — "이 GitHub App의 설치 권한은 enforcement-integrity를 기술적으로도 뒷받침해야 한다 — **Administration·Workflows 권한을 부여하지 않는다** … GitHub 플랫폼이 한 번 더 막는다(belt-and-suspenders)". Mechanu App에 Administration을 주는 것은 이 point의 정면 뒤집기다. 별도 admin App을 새로 만드는 변형은 `ADR-0011 point 5` 벡터 (a)를 새 identity로 재생산하면서 `verify-credential-narrowing.sh`가 narrowed PAT **한 개**만 검사한다는 커버리지 구멍을 통과해 narrowing PASS를 거짓 안심으로 만든다. PAT를 `workflow_dispatch` input으로 넣는 변형은 레포가 public이라 dispatch 즉시 전세계 공개다. MEMORY `시크릿 지역성 역전`의 전제(이미 존재하는 시크릿을 안전한 쪽으로 이동)가 성립하지 않는다 — Administration:write는 지금 이 레포 자동화 표면 어디에도 존재하지 않으며, 이 안은 그것을 **존재하게 만든다**.

- **CLI에 `apply` 서브커맨드를 포함(생성→PUT→검증→롤백을 한 도구가 수행)** — 기각. 도구가 Administration:write 자격증명으로 실행돼야 하므로 "에이전트 저작 가능(admin 범위 아님)"과 "사람만 실행 가능"의 경계가 코드 한 파일 안에서 흐려진다(`ADR-0015 point 2` 축의 소실). 원자성 이득은 롤백 payload 사전 산출(point 7)로 대부분 회수되고, 남는 "롤백 PUT도 실패" 경로는 어차피 사람이 파일을 들고 재시도해야 한다. 대신 `plan`이 리허설 대상까지 커버하도록 `--branch`를 필수화해 트랜잭션 안전성을 다른 축으로 확보한다.

- **`configs/gate/phase.json`(phase A/B + verdict identity)을 SSOT로 신설하고 라이브 GET과 양방향 대조** — 부분 채택, 형태 기각. "리뷰된 의도 기록이 상태 유도로는 대체 불가"·"boot pillar를 건드리지 않는 분리"는 옳고 채택했다. 기각 이유: phase enum + `{context, app_id}` 3필드는 실제 목표상태의 **손실 압축**이라 `require_code_owner_reviews`가 조용히 꺼진 `count=0` 상태도, `restrictions`가 리셋된 상태도 이 선언과 '일치'로 판정된다 — full-replace PUT이 파괴할 수 있는 것은 그 3필드가 아니라 12개 top-level 필드 전부다. 또 손으로 쓴 선언은 롤백 경로에서 갱신 누락 시 영구 FAIL을 만들고 오퍼레이터가 "경보를 끄려고 선언을 고치는" 학습을 유도한다. 기계 생성 아티팩트(point 12)는 `plan`의 출력(`flip-payload.json`)이 곧 새 정본 기대상태라 이 유혹이 없다.

- **`CheckBranchProtection`이 `required_approving_review_count`에서 매 호출 phase를 파생하고 `count==0`일 때만 verdict-gate + app_id 핀을 추가 요구** — 기각. (i) **구조적 사각지대**: 기대치를 관측값에서 파생하는 검사기는 내적 일관성만 볼 수 있고 의도로부터의 이탈은 원리적으로 못 본다 — Phase B 성립 후 count가 1로 되돌아가고 verdict-gate가 required에서 빠지면 "Phase A니까 met"으로 **초록을 보고한다**. 이 결정이 답해야 할 drift 질문 자체에 대한 맹점이다. (ii) **과-halt/boot-brick**: 미실측 상수(point 11)를 boot presence-check의 AND 집계에 넣으면 값이 틀렸을 때 `Result.Satisfied`가 영구 false로 붕괴해 자율작업 오라클 전체가 죽는다. (iii) `ADR-0015:83` point 7(c)의 정면 위반. 다만 이 대안의 목표 — 무-게이트 창 금지를 프로즈에서 기계 불변식으로 — 는 사후 탐지가 아니라 **단일 뮤테이터로 생성 불가**하게 만드는 형태(point 6)로 강화 채택했다.

- **`schedule` cron 워크플로로 상시 branch-protection drift 감시** — 기각(이번 스코프). 진단("진짜 문제는 로직 위치가 아니라 누가 반복 실행하는가")은 옳으나 이 세션에 넣을 수 없다: **GitHub Actions `permissions:` 블록에 `administration` 스코프가 존재하지 않는다**(공식 workflow-syntax 문서에서 키 전수 확인 — actions, artifact-metadata, attestations, checks, code-quality, contents, deployments, discussions, id-token, issues, models, packages, pages, pull-requests, security-events, statuses, vulnerability-alerts). 즉 `GITHUB_TOKEN`으로는 branch protection을 GET할 수 없고, 새 Administration:read 자격증명을 레포 시크릿으로 **상주**시켜야 한다 — `ADR-0009 point 6`을 뒤집는 아키텍처 포크(`/architect` 선행)다. 60일 무활동 시 scheduled workflow 자동 비활성화·알림 채널 부재(CLAUDE.md가 "치명적 상황은 외부 알림(미정)"이라 명시)도 같은 결론을 지지한다. 후속 포크로 book.

- **`cmd/phase-b-activate` 상태 머신 오케스트레이터(6 스테이지, `Detect`는 라이브 재조회 전용, `advance`가 `Next()` 불일치 시 mutating 호출 0회)** — 기각. 헤드라인 불변식이 가장 중요한 스테이지에서 무너진다: narrowing 완료는 **재조회 불가능한 이벤트**다 — `verify-credential-narrowing.sh`는 revoke된 `OLD_ADMIN_TOKEN`(`:25`)과 순수 사람 단언 `SSH_TEARDOWN_CONFIRMED=1`(`:28-29`)을 mandatory로 요구하므로 narrowing이 끝난 뒤에는 그 스크립트를 다시 돌려 Done을 얻을 방법이 원리적으로 없다. 따라서 `Next()`는 영구히 narrowing 단계를 반환하고 flip 전진이 영구 거부된다(과-halt로 flip 자체를 브릭). 이를 피하려 Inconclusive를 관대하게 처리하면 그 순간 fail-open이다. 순서 강제 아이디어 자체는 각 도구의 **국소 precondition assert**(point 6의 abort 조건, `--branch` 필수)로 축소 채택한다.

- **신규 스크립트 없이 sacred 런북(`phase-b-entry.md`)에 리터럴 `gh api` heredoc + 4열 실측 기록표로 처리** — 기각. "원문 붙여넣기 강제"에는 non-zero exit 같은 기계적 강제가 없어, 이 레포가 네 번 데인 "문서 검증 완료" 거짓 안심이 정확히 재현된다. 더 결정적으로, 이 대안이 제시한 리터럴 payload 예시에 `enforce_admins`가 빠져 있었다 — "완전히 채워진 리터럴 블록이면 스크립트가 필요 없다"를 증명하려고 저술한 그 블록 자체가 틀린 것이, 손 열거 방식이 이 스텝에서 실패한다는 실증이다.

- **`--expect-enforce-admins=<bool>` 플래그로 기대값을 오퍼레이터가 지정** — 기각. ADR-0016 point 1이 목표값을 `true`로 확정한 이상 적법한 flip 시점에는 이미 `true`이므로 플래그는 실패 모드만 늘리고, 미해결 포크를 CLI 한 글자로 조용히 결정 가능하게 만든다. 하드 assert가 정답이다. 이 대안의 정당한 관심사(generate-time과 apply-time 사이의 드리프트)는 `verify`의 재-GET 전체 동치 비교가 흡수한다.

- **`plan`의 대상 브랜치를 main으로 고정(하드코딩 또는 `--live`)** — 기각. `phase-b-entry.md:63`이 precondition ④ probe에 "flip payload 문법·권한 리허설도 함께"를, `:66` 통과판정에 "payload 유효"를 mandatory로 명시한다(실측). main 고정이면 probe 브랜치 payload를 오퍼레이터가 손으로 만들어야 하고, 이는 런북이 금지한 "파괴적 full-replace payload 손 조립"을 하필 malformed payload 검출 단계에서 수행하게 만든다.

- **flip payload만 생성하고 롤백은 런북의 "스냅샷으로 PUT" 문구에 맡기기** — 기각. Context 6 — 그 문구는 문자 그대로 실행 불가능하다(스냅샷은 GET 형상이라 PUT 바디가 아니다). 도구가 롤백 payload를 안 주면 사고 시점에 손 조립이 강제된다 — 금지 규칙이 가장 위험한 순간에만 깨진다.

- **flip 생성기를 W1(App key 프로비저닝 이후, 정상 App-작성 PR 경로)로 미루기 — ③의 스모크 테스트로 겸용** — 기각. `phase-b-entry.md:150`이 "③ 이후 모든 sacred 변경은 정상 App 경로"라 명시하므로 문서상 합법이고 첫 App-작성 PR이 ③ 통과판정 스모크가 되는 매력이 있다. 그러나 그 시점의 sacred 머지 능력이 **아직 한 번도 실행된 적 없는 App 경로**에 100% 의존한다. App 경로가 고장 나면 `enforce_admins=true`가 이미 켜져 admin bypass도 없어 **flip에 필요한 생성기를 영원히 머지 못 하는 교착**에 빠지고, 탈출구는 `phase-b-entry.md:143-149`의 최후 수단뿐이다. 검증 안 된 경로에 임계 아티팩트를 인질로 걸지 않는다 — ADR-0016 point 5의 W0 안에서 끝낸다.

---

## Consequences

### 좋음

- **런북이 스스로 건 fail-closed 게이트(`phase-b-entry.md:95-102`)가 해소된다** — 이것이 이 캠페인에서 에이전트가 물리적으로 닫을 수 있는 유일한 blocking precondition이었다.
- **세 개의 안전 속성이 하드닝이 아니라 위험 클래스 제거로 확보된다**: (i) 무-게이트 창 → 단일 뮤테이터로 **생성 불가**, (ii) `contexts[]` 오방출 → 타입에서 **표현 불가**, (iii) 도구의 Administration:write 보유 → `apply` 부재 + no-write 테스트로 **구조상 불가**.
- **`--branch` 파라미터화 덕분에 문서 미해소 gap(`contexts[]` required 표기)이 main이 아니라 probe 브랜치의 422로만 드러난다** — 불확실성을 liveness 쪽으로 몰고 safety 쪽에서 뺐고, 그 대가(probe 1회 추가 실행)는 이미 런북 ④가 요구하는 작업이라 순증 비용이 0이다.
- **`internal/enforcement` 배치가 twin 배선 자체를 자동화한다** — 등재를 잊으면 CI가 죽는다. 신규 패키지였다면 이 자동 강제를 처음부터 다시 만들어야 했고, 그 배선 누락이 이 레포의 반복 실패형이다.
- **완결성 테스트 4종이 네 곳(`scripts/`·`configs/gate/`·`docs/runbooks/`·게이트-의존 `cmd/`)의 glob 구멍을 닫는다** — 넷 중 둘은 추가하는 즉시 실재하는 미등재(provisioning 런북, `cmd/presence-check/main.go`)를 잡는다.

  > **각주 (R17 반영 — 초판의 "마지막 구멍들" 서술은 거짓 안심이었다)**: 초판은 이 넷을 "이 레포에 남아 있던 **마지막** glob 구멍들"이라 단언했으나 실측상 최소 셋이 더 열려 있다. **(i) `.github/workflows/`**: 순회 테스트가 없고 `sacredRequiredPaths`는 워크플로 3개를 손 열거할 뿐이다(`codeowners.go:42,43,50`) — 오늘 `ls .github/workflows/`가 정확히 그 3개라 **우연히** 완결이지만, 네 번째 워크플로가 추가되면 `/.github/workflows/` 디렉터리 규칙만 걸리고 개별 등재가 빠져 **후행 narrower ownerless 항목이 그 한 파일만 벗겨내는 공격이 감지되지 않는다** — 이 ADR이 닫았다고 선언한 바로 그 클래스다. **(ii) `internal/gate/`**: 10개 손 열거, 순회 테스트 없음 — **R68에서 non-test 한정 순회(`TestSacredRequiredPaths_CoversEveryGateLogicFile`, 이슈 03 §4-bis)로 같은 PR에서 닫는다**(오늘 10/10 등재라 즉시 GREEN). **(iii) `protects:`를 의도적으로 비운 신규 ADR**: `parseADRProtects`가 `protects: []`를 정상 형태로 허용하므로 어느 완결성 테스트에도 걸리지 않는다(아래 잔여 위험에 이미 book).
  >
  > **(i)만** 오늘 상태로 즉시 green이므로 비용 0이다 — 같은 PR(이슈 03 §5)에서 `assertTreeFullyRegistered(t, "../../.github/workflows", ".github/workflows")` 한 케이스를 추가한다(`*.yml` 고정 glob이면 Actions가 인식하는 `.yaml` 확장자 워크플로를 원리적으로 못 본다 — R49). (`.claude/skills/*`의 구멍 — architect·issue-drafter·retro의 `SKILL.md`가 CODEOWNERS 무보호 — 은 **ADR-0016 point 15**와 이슈 01이 닫는다.)
  >
  > **각주 (R41 반영 — (ii)의 "비용 0"은 사실이 아니어서 지시를 삭제했다)**: 초판은 `assertGlobFullyRegistered(t, "../../internal/gate", "*.go", "internal/gate")`도 "즉시 green"이라며 같은 PR에 넣으라고 mandatory로 지시했으나 실측상 **즉시 red**다: `ls internal/gate/*.go` → 19개이고 그중 **9개가 `*_test.go`**인데(`diffparse_test`·`eligibility_test`·`outcome_test`·`pattern_test`·`retry_test`·`riskclassification_test`·`sanity_test`·`signal_source_test`·`verdict_test`) `internal/enforcement/codeowners.go:96-111`은 "**Every non-test .go source file** in internal/gate"라고 명시하며 non-test 10개만 등재한다. `assertGlobFullyRegistered`는 `*.go` 전부를 매치하므로 9건 `t.Errorf`로 실패한다. green으로 만들려면 `codeowners.go:171-179`이 enforcement(테스트 자체가 enforcement이므로 포함)와 gate(게이트 로직이 non-test에 있으므로 제외)를 **의도적으로 다르게 취급한 결정**을 드라이브바이로 뒤집게 된다.
  >
  > **각주 (R68 반영 — 위 R41의 "삭제한다"는 과잉이었다: 근거가 `*.go` 전수 변형에만 성립한다)**: 삭제 대신 **non-test 한정 변형으로 같은 PR에서 신설한다**(이슈 03 §4-bis, `TestSacredRequiredPaths_CoversEveryGateLogicFile`). 같은 이슈 §4가 `cmd/*` 축에 대해 이미 `strings.HasSuffix(name, "_test.go")` 제외 필터를 구현하므로 그 필터를 `internal/gate`에 그대로 쓰면 오늘 **10/10 등재라 즉시 GREEN**이고(`ls internal/gate/*.go | grep -v _test.go` → 10, `codeowners.go:96-111`이 그 10개를 전부 열거), "non-test만 등재"라는 기존 결정을 **전혀 건드리지 않는다**. 그대로 두면 특권 verdict 잡이 컴파일·실행하는 게이트 판정 로직에 non-test 파일이 새로 추가될 때 미등재로 남고, 후행 narrower ownerless 항목이 그 한 파일만 벗겨내는 공격이 감지되지 않는다 — `codeowners.go:96-101`이 개별 열거를 하는 이유로 명시한 바로 그 실패다. 실측상 `internal/gate`를 순회하는 완결성 테스트는 오늘 **0건**이다(`grep -rn "internal/gate" internal/enforcement/*_test.go` → 전부 CODEOWNERS 픽스처 문자열/carve-out; `codeowners_test.go:507-542`는 `sanity.go` 한 파일만 대표 검사). **`internal/gate/*_test.go`·`cmd/verdict-gate/*_test.go`의 sacred 승격만** 별개 결정으로 아래 "후속"에 book한다.

### booked residual (알려진 잔여 위험)

- **이것은 detector tier이지 enforcer가 아니다.** `verify`는 사람이 부를 때만 돌고, boot presence-check는 무변경이며, presence-check 자체가 `cmd/bot`·모든 workflow·모든 `.claude/skills` 어디에도 배선돼 있지 않다(실측). 실제 차단력은 여전히 GitHub branch protection + 사람 code-owner 리뷰뿐이다(`adrprotects.go:32-58`이 자기 자신에 대해 명시한 것과 같은 신뢰 계층). "이 도구가 Phase B를 강제한다"고 서술하면 거짓이다.
- **상시 drift 감시가 없다.** flip 이후 main의 protection이 바뀌어도 사람이 `verify`를 수동 실행할 때까지 무기한 미검출. 자동화에는 CI 상주 Administration:read 자격증명이 필요하고 그것은 `ADR-0009 point 6`을 뒤집는 별도 포크다.
- **손 조립 우회 경로는 열려 있다.** Administration:write를 쥔 주체가 이 도구를 건너뛰고 직접 PUT하면 무-게이트 창을 만들 수 있다 — 도구는 "정상 경로로는 생성 불가"를 보장할 뿐 "어떤 경로로도 불가"를 보장하지 않는다. 닫으려면 상시 drift 감시가 필요하다.
- **`contexts[]` 생략의 합법성이 문서로 확정되지 않았다.** 공식 문서는 `contexts`를 `(required)`로 표기하면서 closing-down 공지를 달고 PUT 예시에는 `checks[]`가 없다. **V3의 probe 리허설이 통과한 API 버전·시점에 한해서만 증명된다** — 422가 나오면 대응은 "contexts를 checks와 함께 싣는다"가 아니라 **핀 소실 위험 재평가 후 별도 판단**이어야 한다(둘 다 실으면 서버 해석에 따라 checks가 contexts로 덮일 수 있다).
- **`enforce_admins`에 리터럴 `false`를 보낼 때의 동작이 문서화돼 있지 않다**(공식 문서는 `true`와 `null`만 서술). Phase B 목표값이 항상 `true`이므로 flip payload에는 영향이 없으나, **롤백 payload가 스냅샷의 `false`를 되돌려 보낼 때** 동작이 미정이다 — 롤백은 `DELETE …/protection/enforce_admins` 전용 엔드포인트를 쓰는 편이 안전하다는 것을 런북에 적되 실측은 하지 않는다.
- **`app_id` 핀 비교는 하드닝이지 클래스 제거가 아니다.** 이름 일치를 신원 일치로 승격할 뿐이며, Actions app이 게시하는 어떤 check-run이든 같은 `app_id`를 갖는다 — workflow scope를 쥔 주체가 `verdict-gate`라는 이름의 check-run을 같은 app으로 위조하는 것은 `app_id` 핀으로 판별 불가하다(`ADR-0011 point 5 (b)`가 이미 book한 한계). 이 벡터는 narrowing의 Workflows 권한 부재로만 닫힌다.
- **`restrictions`/`dismissal_restrictions`/`bypass_pull_request_allowances` 변환 경로는 이 레포의 실데이터로 검증될 수 없다** — 오늘 셋 다 GET 응답에 키 자체가 없고 앞으로도 설정 계획이 없다. 합성 픽스처에만 의존하므로, 향후 누군가 설정한 뒤 flip을 재실행하면 미검증 경로가 처음 발현된다.
- **골든 픽스처가 stale해질 수 있다.** `testdata/protection-live-2026-07-23.json`은 오늘 스냅샷이다. 실제 flip 시점의 main 설정이 달라져 있으면 골든 테스트는 여전히 green인데 실제 변환은 미검증 경로를 탈 수 있다. `plan`이 항상 라이브 GET을 하므로 안전 방향이지만(픽스처는 회귀 검출용), "테스트 green = 실제 스냅샷 커버"는 아니다.
- **`verify`의 전체 동치 비교가 과-halt를 낼 수 있다.** GitHub이 PUT 후 서버측 정규화(기본값 주입 등)를 하면 `Diff`가 비지 않아 불필요한 롤백 왕복이 생긴다. 실패 방향은 Phase A 잔류(안전)지만, probe 리허설(V3)이 이 오탐을 main 전에 드러내는 장치다.
- **`GH_TOKEN`을 env로 받는 것이 세션 전체의 자격증명 위생을 대체하지 않는다.** 오퍼레이터가 Administration:write 토큰을 이 env에 넣어도 도구는 GET만 하지만, 같은 셸에서 이어지는 `gh api --method PUT`은 당연히 write를 쓴다. no-write 보장은 **도구 안**에서만 성립한다.
- **기대상태 아티팩트(`main-protection.expected.json`)도 편집 가능한 파일이다.** sacred 등재로 code-owner 리뷰가 걸리지만 CODEOWNERS 자신과 같은 신뢰 계층이며, "이 파일을 고치면 무엇이 correct인지가 바뀐다". detector-only라는 사실은 **`internal/enforcement/codeowners.go`의 `sacredRequiredPaths` 등재 주석**에 `adrprotects.go:32-58` 스타일로 명시한다.

  > **각주 (R59 반영 — 두 결함: 읽는 곳이 없었고, 요구한 명시 형태가 point 12와 양립 불가였다)**: ① 초판은 "**파일 헤더 주석**에 명시"를 mandatory로 요구했는데, **JSON에는 주석이 없고** point 12(`:156`)와 런북 Step 4-C는 `plan` 산출물(`flip-payload.json`)을 **바이트-동일**하게 커밋하도록 못박는다 — `_comment` 키를 넣으면 그 파일은 더 이상 그대로 PUT할 수 있는 payload가 아니게 되어 "`verify --expect`가 받는 파일 = 커밋되는 파일"이라는 point 12의 등식이 깨진다. 그래서 주석을 **파일 밖(등재 주석)**으로 옮긴다. ② 더 큰 문제: 초판 전체에서 이 아티팩트를 **읽는 곳이 하나도 없었다** — 생산(런북 Step 4-C)·등재 강제(point 14 glob)·존재 확인은 있는데 `verify --expect`의 인자로 이 경로를 쓰는 명령이 **0건**이라(런북의 `verify` 호출은 전부 `/tmp/**` payload), phase.json 대안을 기각한 근거인 "drift 탐지"가 코드·절차 어디에도 실체가 없었다. **런북 Step 4-C에 `verify --branch main --expect <main의 정본>` 실행을 넣고 그 exit 0을 `STEP4C_PASS`의 assert에 포함**시켜 소비자를 만든다.
- **`cmd/bot/`은 여전히 CODEOWNERS 무보호이고 import-파생 완결성 테스트에서도 자동 제외된다**(`internal/enforcement`·`internal/gate`를 import하지 않으므로). `cmd/bot`을 sacred로 올릴지는 제품 코드 자율성 축소를 수반하는 별개 결정 — 후속 포크로 book.
- **`internal/enforcement/adrprotects.go:83-87`의 "이 파일은 CODEOWNERS 보호 대상이 아니다"라는 주석이 stale하다** — `.github/CODEOWNERS:97` + `sacredRequiredPaths:180-181`로 이제 보호된다. 이 결정과 무관한 문서 drift이나 같은 PR에서 정정할 가치가 있다.

### 사람 액션 핸드오프

- **`verdict-gate` check-run의 `app_id` 실측**(point 11) — verdict 시크릿 프로비저닝(ADR-0016 step 3) 후 최초 실행이 실제 check-run을 게시한 뒤에만 가능하다. 이 값 없이는 `plan`이 돌지 않는다.
- **probe 브랜치 리허설과 main flip의 `PUT` 실행**(Administration:write) — 도구는 GET만 한다.
- **`plan`이 낸 `flip-payload.json`을 `configs/gate/main-protection.expected.json`으로 커밋**(flip 성공 후, sacred 등재 동반 — 런북 Step 4-B 성공 분기가 이 스텝을 명령으로 담는다).

### 검증 방법 (전부 capability 실측)

- **V1(에이전트, 지금)** — 순수 변환기 테스트. `go test -race ./internal/enforcement/... ./cmd/protection-flip/...` green. 골든 픽스처는 `gh api repos/chnu-kim/toss-trade-bot/branches/main/protection > internal/enforcement/testdata/protection-live-2026-07-23.json`(공개 메타데이터, 시크릿 없음 — 커밋 전 `scan.sh`로 확인). 필수 케이스와 각각의 **Red 이유**:
  - `TestToPutShape_DropsGetOnlyFields` — 출력에 `url`·`contexts_url`·`required_signatures`·`contexts` 키가 하나도 없어야 한다. *Red: 순진한 passthrough는 이 키들을 흘린다.*
  - `TestToPutShape_EmitsExplicitNullRestrictions` — `restrictions` 키가 없는 스냅샷 → 출력에 `"restrictions": null`이 **존재**. *Red: `omitempty`를 붙이면 키가 사라진다.*
  - `TestToPutShape_UnwrapsToggles` — 9필드가 bare bool.
  - `TestParse_UnknownTopLevelKeyIsHardError` — 픽스처에 `"future_field": {"enabled": true}` 주입 → error이고 메시지에 키 이름 포함. *Red: 관용 파서는 조용히 무시하고 nil error를 반환한다.* **이 케이스가 이 설계의 핵심 안전 속성이다.**
  - `TestParse_DismissalRestrictionsIsHardError` / `..._BypassAllowancesIsHardError` / `..._MissingToggleKeyIsHardError` / `..._ContextsOnlyWithoutChecksIsHardError`.
  - `TestTransform_AbortsOnTodaysLiveSnapshot` — 오늘 픽스처(`enforce_admins.enabled=false`) → `ErrEnforceAdminsNotEnforced`, payload **nil**. *Red: assert 없는 구현은 payload를 만들어버린다.*
  - `TestTransform_NeverUpgradesEnforceAdmins` — 같은 입력에서 payload가 **존재하지 않음**을 단언(조용한 override 회귀 방지).
  - `TestTransform_PreservesExistingAppIDPin` — 기존 `app_id=15368` 보존 + 신규 verdict 항목도 `app_id` 보유.
  - `TestTransform_AbortsWhenAddCheckHasNoAppID` / `..._UnpinnedExistingCheckAborts` / `..._CodeOwnerReviewsOffAborts` / `..._StartingCountNotOneAborts` / `..._DuplicateContextAborts`.
  - `TestTransform_OnlyIntendedFieldsChange` — `Diff(ToPutShape(snap), flip)`이 **정확히 2건**(count, checks 추가). *Red: 어떤 필드든 흘리거나 떨어뜨리면 건수가 어긋난다.*
  - `TestTransform_RollbackIsIdentityOfSnapshot` — `rollback == ToPutShape(snapshot)` deep-equal.
  - `TestDiff_DetectsSilentDrop` — `got`에서 `require_code_owner_reviews`를 false로 만든 케이스가 Diff에 잡힘. *Red: 열거식 비교 구현은 놓친다.*
  - `TestFixture_TodaysSnapshotStillHasEnforceAdminsFalse` — 픽스처 자체를 단언해, 픽스처 변조로 abort 테스트가 vacuous해지는 경로를 닫는다.
- **V2(에이전트, 지금) — 오늘의 진짜 데이터로 abort 실증.** `GH_TOKEN=… go run ./cmd/protection-flip plan --branch main --set-review-count 0 --add-check 'verdict-gate:<측정값>' --out /tmp/plan` → **오늘은 non-zero exit(`ErrEnforceAdminsNotEnforced`)이 나야 정상**이다. 이것이 이 도구가 fail-closed임을 증명하는 첫 실측이며 세션 로그에 남긴다.
- **V3(사람, probe 브랜치 — main 무접촉) — 이 ADR의 핵심 실측.** ① main 스냅샷으로 `plan`(ADR-0016 step 0.5 이후라 통과) → ② `rollback-payload.json`을 **probe 브랜치에 PUT**해 main과 동일한 보호 상태를 복제. **여기서 422가 나면 `contexts[]` 생략이 불가하다는 결정적 증거이고, main을 건드리기 전에 알게 된다** → ③ `plan --branch probe/…` → flip payload PUT → `verify --branch probe/… --expect flip-payload.json` exit 0(항목별 `app_id` 보존을 `verify`가 자동 판정) → ④ **롤백 실증**: `rollback-payload.json` PUT → `verify` exit 0. **롤백 경로가 사고 시점이 아니라 리허설에서 한 번 실제로 돌아본 것**이 되어야 한다 → ⑤ probe 브랜치·protection 삭제.
- **V4(에이전트) — 가드가 실제로 무는지.** `TestGeneratorPerformsNoWrites`에 임시로 `http.MethodPut` 리터럴을 넣어 **실패**를 확인한 뒤 되돌린다. 실패하지 않으면 그 가드는 장식이다.
- **V5(에이전트) — 완결성 테스트의 Red 실증.** 신규 glob 테스트 4종을 CODEOWNERS/`sacredRequiredPaths` 수정 **없이** 먼저 추가해 `docs/runbooks/loop-pr-environment-provisioning.md`와 `cmd/presence-check/main.go`를 지목하며 FAIL하는지 확인 → 등재 후 GREEN 전환. 두 출력을 PR 본문에 첨부한다. 추가로 `internal/enforcement/`에 더미 `.go`를 등재 없이 만들어 기존 enforcement glob 테스트가 무는지도 확인 후 삭제. **FAIL이 안 나면 그 테스트는 vacuous이므로 폐기·재작성.** 파괴적 Red는 커밋된 baseline 위에서 수행한다(MEMORY `worktree-stale-main-ref`).
- **V6(사람, main flip)** — V3과 동일 시퀀스를 `--branch main`으로. `verify` exit≠0이면 즉시 `rollback-payload.json` PUT → 재-GET `verify` → Phase A 잔류. 추가로 `go run ./cmd/presence-check`로 `CheckBranchProtection`(다른 축 — `require_code_owner_reviews`만 본다) 재통과 확인.
- **V7(사람, flip 직후) — negative 검증.** `gh api …/branches/main/protection --jq '.required_status_checks.checks'`가 정확히 2개만 반환하는지(`build · vet · gofmt · test-race`, `verdict-gate`). 이번 결정이 추가한 다른 context가 하나도 없어야 한다.
- **V8(에이전트) — 과-halt 가드.** `Run()`의 pillar 목록에 flip-전용 검증을 **일부러** 추가한 로컬 변경으로 `TestRun_UnaffectedByVerdictGateAbsence`가 **실패**하는지 확인 후 되돌린다.
- **V9(사람, flip 성공 후) — forcing function 실증.** `plan`이 낸 `flip-payload.json`을 `configs/gate/main-protection.expected.json`으로 커밋하고 `sacredRequiredPaths` 등재 **없이** `go test ./internal/enforcement/` → `TestSacredRequiredPaths_CoversEveryGateConfigFile`이 FAIL해야 한다. 등재 후 GREEN. (이 커밋 자체가 sacred 변경이므로 flip 이후에는 `create-loop-pr` dispatch → `mechanu[bot]` PR → code-owner 승인 경로로만 머지된다.)

**공통 원칙**: 어느 단계에서도 "사람이 화면의 ✅ 줄을 훑어 판정"하지 않는다 — 판정은 전부 `echo $?`다. 증거(종료 코드 + 항목별 HTTP 상태, 시크릿 원문 제외)는 ADR-0016 point 13의 새 추적 이슈에 기록한다.

### twin-artifact 배선 지시 (같은 PR에서 함께 움직인다)

| 층 | 아티팩트 | 조치 |
|---|---|---|
| 규칙 | `docs/adr/0017-protection-flip-pure-transformer.md` | 신규(`protects: [enforcement-integrity]`) |
| 자기 자신 | `.github/CODEOWNERS` | `/docs/adr/0017-*.md @chnu-kim`, `/cmd/protection-flip/ @chnu-kim`, **`/cmd/presence-check/ @chnu-kim`** (R15 반영: 초판은 아래 행에서 `cmd/presence-check/main.go`를 `sacredRequiredPaths`에 넣으라면서 짝이 되는 CODEOWNERS 줄을 빠뜨렸다 — 현행 cmd 규칙은 `:72 /cmd/verdict-gate/` 하나뿐이라 그대로 따르면 `CheckCodeowners`가 "매칭되는 CODEOWNERS 패턴이 없음"으로 **영구 unmet**을 반환하고 `TestCheckCodeowners_RealFileSatisfies`가 실패한다. 이슈 03은 두 줄을 정확히 지시했으므로 durable ADR만 틀렸었다.) `/.github/CODEOWNERS` self-owned 줄은 마지막에 유지 |
| 자기 자신 | `internal/enforcement/codeowners.go` | `sacredRequiredPaths`에 0017 실파일 + `cmd/protection-flip/main.go` + `cmd/presence-check/main.go`(**현재 양쪽 모두 부재 — 실재하는 게이트 승격 규칙 위반**) |
| 자기 자신 | `internal/enforcement/adrprotects.go` | `sacredADRRegistry`에 `"0017"` |
| 자기 자신 | `internal/enforcement/codeowners_test.go` | 0017 누락 fail-closed + `/cmd/protection-flip/main.go` carve-out + `/cmd/presence-check/main.go` carve-out |
| 명령 | `internal/enforcement/protectionstate.go`, `protectionflip.go` | 신규. `/internal/enforcement/`(CODEOWNERS:97)가 커버하고 `TestSacredRequiredPaths_CoversEveryEnforcementGoFile`(codeowners_test.go:566)이 등재를 **자동 강제** |
| 명령 | `cmd/protection-flip/main.go` | 신규. `plan`/`verify`만 — `apply` 없음 |
| 검사 | `internal/enforcement/protectionstate_test.go`, `protectionflip_test.go`, `protectionnowrite_test.go` | V1·V4의 케이스. 같은 자동 강제 적용 |
| 검사 | `internal/enforcement/testdata/protection-live-2026-07-23.json` | 오늘 실측 GET 원본(공개 메타데이터). **이슈 02가 생산·`sacredRequiredPaths`에 개별 등재**하고 `TestFixture_TodaysSnapshotStillHasEnforceAdminsFalse` + `TestSacredRequiredPaths_CoversEveryProtectionFixture`(`testdata/*.json` 전수 순회)를 함께 싣는다. 이슈 03은 이 파일을 **재사용**한다(R35·R36 반영: 초판은 이 픽스처를 "fixture 데이터일 뿐"이라며 등재 대상에서 뺐고 자기검사 테스트도 어느 이슈에도 없어, 픽스처의 `enforce_admins.enabled`를 한 줄 바꾸면 이 캠페인의 핵심 안전 속성 테스트 셋이 전부 vacuous해졌다. `codeowners_test.go:566-591`의 전수 순회는 `:579`에서 디렉터리·비-`.go`를 건너뛰므로 `testdata/*.json`을 원리적으로 못 잡는다) |
| 완결성 | `internal/enforcement/completeness_test.go`(또는 `instructionsurface_test.go` 확장) | point 14의 glob 테스트 4종 + 보강(`.github/workflows/*.yml`, `internal/enforcement/testdata/*.json`). 전부 매치 0건이면 `t.Fatalf` |
| 자기 자신(기대상태) | `configs/gate/main-protection.expected.json` | flip 성공 후 사람이 **`plan`의 `flip-payload.json`을 이 이름으로** 커밋(런북 Step 4-B 성공 분기의 명령). 별도 `expected.json` 산출물은 존재하지 않는다(R40). `/configs/gate/`(CODEOWNERS:76)가 디렉터리 커버하지만 last-match-wins 때문에 `sacredRequiredPaths` 개별 등재 필수 — glob 테스트가 강제 |
| 명시적 비-twin | `internal/enforcement/presence.go`, `branchprotection.go`, `cmd/presence-check` | **이 ADR의 산출물은 세 파일을 한 바이트도 바꾸지 않는다.** 금지의 범위는 **`required_status_checks`/verdict-gate 축의 확장**이며(phase-dependent → boot AND에 넣으면 Phase A가 깨진다, Context 5), phase-무관한 `enforce_admins` 레그 추가는 **ADR-0016 point 14가 별도로 승인**한다(이슈 02). `TestRun_UnaffectedByVerdictGateAbsence`가 이 경계를 회귀 가드로 고정하며, **그 테스트의 구현 담당은 이슈 02**다(`branchprotection.go`를 실제로 수정하는 유일한 이슈 — R63: 초판은 담당 이슈를 적지 않아 어느 이슈도 구현하지 않았고 V8은 실행 대상 자체가 없었다). 이슈 02 AC가 **V8 실측 로그 첨부를 mandatory**로 싣는다 |
| 호출자(CI) | `.github/workflows/ci.yml` | **이 ADR의 Go 산출물에 한해 변경 불필요** — `:51`의 `go test -race ./...`가 신규 패키지를 자동 커버(실측). bash 안이었다면 필요했을 명시 스텝과 그 누락 실패모드(`:63-68`이 이미 문서화한 false-green 클래스)가 이 선택에서는 존재하지 않는다. **이 사실을 PR 본문에 명시**해 리뷰어가 "배선 누락 아님"을 확인할 수 있게 한다. **단 같은 캠페인의 셸 산출물(이슈 04의 `scripts/*.sh` + `verify-credential-narrowing_test.sh`)에는 이 면제가 적용되지 않는다** — 그쪽은 정확히 `:63-68`의 클래스에 해당하므로 **이슈 04 §8이 `ci.yml`에 `scripts/*_test.sh` 전수 실행 스텝을 배선한다**(R64: 초판은 이 비대칭을 적지 않아, bash 기각 근거로 인용한 결함을 같은 캠페인이 자기 산출물에 새로 팠다) |
| 호출자(사람) | `docs/runbooks/phase-b-entry.md` | `:95-102`의 🚧 배너 제거 · `:104-134`를 `plan`/PUT/`verify`/롤백-PUT 명령 시퀀스로 재작성(`:126-131`의 손-열거 4항목 검증 → `verify` 전체 동치로 대체하되 `CheckBranchProtection` 재통과는 유지) · `:57-67` precondition ④에 **probe 브랜치 대상 `plan`+PUT+`verify`+롤백 리허설**을 명시 스텝으로 추가(`:63`의 "payload 문법·권한 리허설" 요구를 실행 지시로 승격) · ① 절에 `app_id` **측정** 스텝 신설(15368 가정 금지) |
| 정정 | `internal/enforcement/adrprotects.go:83-87` | "이 파일은 CODEOWNERS 보호 대상이 아니다" 주석이 stale — 정정. **담당 이슈: 03**(ADR-0017을 저술하며 `internal/enforcement`를 이미 건드리는 이슈, 같은 PR diff에 포함). R65: 초판은 담당 이슈를 적지 않아 `grep -rln "adrprotects.go:83"` 결과가 이 ADR 1건뿐이었다 — mandatory 행에 작업자가 없었다 |

### ADR-0015 amend 포인터 (어느 point에 무슨 문장을 넣는가)

- **`docs/adr/0015-…:80`** (point 7 도입부 — "그 스크립트가 아직 없으므로 flip 금지") 뒤에:
  > **Amend (ADR-0017)**: 이 fail-closed 게이트를 해소하는 생성기의 헌장이 ADR-0017이다. 생성기는 **쓰기 능력이 없는 순수 변환기**(`internal/enforcement/protection*.go` + `cmd/protection-flip`, `apply` 서브커맨드 부재 + no-write 테스트)이며, **대상 브랜치를 필수 인자로** 받아 probe 리허설과 main flip이 같은 코드 경로를 타게 하고, **롤백 payload를 flip payload와 같은 실행에서** 산출한다.
- **`docs/adr/0015-…:81`** (point 7(a) 스냅샷) 뒤에:
  > **Amend (ADR-0017)**: 스냅샷은 **GET 형상**이라 그대로 PUT 바디가 될 수 없다(`{enabled:…}` 래핑·`url`/`contexts_url`/`required_signatures`·`restrictions` 키 부재). 파서는 **모르는 top-level 키를 만나면 hard error**로 abort한다 — full-replace PUT에서 파서가 모르는 필드를 조용히 버리면 그 필드가 default로 리셋되기 때문이다(ADR-0017 point 5).
- **`docs/adr/0015-…:82`** (point 7(b)의 `checks[]` 🔴 지시) 뒤에:
  > **Amend (ADR-0017)**: 이 지시는 런타임 assertion이 아니라 **타입의 표현 불가능성**으로 구현한다 — PUT 타입에 `Contexts` 필드를 선언하지 않는다(ADR-0017 point 4). 오늘 GET 응답에는 `contexts`와 `checks`가 **둘 다** 존재하므로, 스냅샷을 성실히 복사하는 구현이 실제로 핀을 강등시킬 경로가 열려 있다. 또한 `verdict-gate`의 `app_id`는 **실측값만** 받는다(기본값·하드코딩 금지 — ADR-0017 point 11).
- **`docs/adr/0015-…:83`** (point 7(c)의 괄호 — "그 확장은 boot presence-check가 아닌 flip-전용 함수여야 한다") 뒤에:
  > **Amend (ADR-0017)**: 그 flip-전용 함수는 `cmd/protection-flip verify`이며, **손-열거 검증이 아니라 재-GET → PUT 형상 변환 → 전체 동치 비교**다. `presence.go`·`branchprotection.go`·`cmd/presence-check`는 **한 바이트도 바뀌지 않으며**, 그 경계를 `TestRun_UnaffectedByVerdictGateAbsence`가 회귀 가드로 고정한다(ADR-0017 point 10).
- **`docs/adr/0015-…:86`** (point 7(f) 원자 롤백) 뒤에:
  > **Amend (ADR-0017)**: "스냅샷으로 PUT 롤백"은 문자 그대로 실행 불가능하다(스냅샷은 GET 형상). 롤백 payload는 flip payload와 **같은 실행에서 미리** 산출되며(`rollback-payload.json`), 사고 시점에 손 조립을 요구하지 않는다(ADR-0017 point 7).
- **`docs/adr/0015-…:99`** (point 9 (g) — `/scripts/` 디렉터리 규칙이 "이후 추가되는 flip payload 생성기도 자동 보호") 뒤에:
  > **Amend (ADR-0017)**: 생성기는 `scripts/`가 아니라 `internal/enforcement/` + `cmd/protection-flip/`에 산다(ADR-0017 point 1) — `internal/enforcement`의 기존 전수 순회 테스트가 sacred 등재를 자동 강제하기 때문이다. 따라서 `/cmd/protection-flip/` CODEOWNERS 규칙과 `sacredRequiredPaths` 개별 등재를 별도로 건다. 아울러 `scripts/`·`configs/gate/`·`docs/runbooks/`·게이트-의존 `cmd/`에 대한 **glob 완결성 테스트가 지금까지 전혀 없었다**(실측: `internal/enforcement`에서 `filepath.Glob` 사용처는 `instructionsurface_test.go:43` 단 1건) — ADR-0017 point 14가 넷을 신설한다.

### 후속

- **후속 포크(근거 ADR 없음)**: (i) 상시 branch-protection drift 감시(CI에 Administration:read 자격증명 상주 — `ADR-0009 point 6` 반전이므로 `/architect` 선행), (ii) `cmd/bot/`의 sacred 승격 여부(제품 코드 자율성 축소를 수반), (iii) `protects: []`를 의도적으로 비운 ADR이 완결성 테스트를 우회하는 구멍의 봉쇄, (iv) **`internal/gate/*_test.go`·`cmd/verdict-gate/*_test.go`의 sacred 승격 여부**(R41·R61 — 오늘은 `codeowners.go:96-111`이 non-test만 등재하고 `cmd/verdict-gate/`도 `main.go`만 등재한다. 올리면 두 축 모두 확장자 무관 전수 순회가 가능해지지만, enforcement(테스트 자체가 enforcement라 포함)와 gate(게이트 로직이 non-test에 있어 제외)를 다르게 취급한 기존 결정을 뒤집는 별개 판단이다).
- **선행 의존**: 이 ADR의 `plan`은 `verdict-gate` check-run의 `app_id` 실측 없이는 실행되지 않으며, 그 실측은 ADR-0016 step 3(verdict 시크릿 등록 포함) 이후에만 가능하다. **그러나 이 ADR의 코드·테스트·sacred 배선은 ADR-0016 point 5의 W0가 열리기 전에 머지돼야 한다** — W0 안에서는 sacred 머지 경로가 존재하지 않는다.
