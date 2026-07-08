---
id: "0009"
status: Proposed
date: 2026-07-08
deciders: [chnu-kim]
domain: [loop-governance, ci]
protects: [live-execution-human-gate, enforcement-integrity]
supersedes: []
superseded_by: null
verification: []
---

# ADR-0009: ADR 저작·승인도 자율화한다 — sacred invariant(실거래 게이트·enforcement 무결성)만 예외

- **Status**: Proposed
- **Date**: 2026-07-08
- **Deciders**: chnu-kim
- **관련 이슈/PR**: (TBD)

## Context

- 기존 `/architect` 스킬은 "사람과의 grilling"을 전제로 ADR을 만든다(스킬 §1). loop engineering은 이 마지막 사람 자리도 없애길 요구한다.
- `docs/adr/README.md`("왜 쓰나")에 따르면 ADR은 **stateless 서브에이전트와 미래 검수자가 신뢰하는 계약**이다. 잘못된 ADR이 자동 `Accepted`되면, 이후 모든 자율 작업이 그 위에서 복리로 틀어질 위험이 있다 — 이 위험은 일반 코드 PR의 위험과 질적으로 다르다(코드는 그 PR만 틀리지만, ADR은 그 위에 쌓이는 모든 후속 결정을 틀리게 한다).
- **ADR-0007**이 "신규 노출 주문 시작은 사람 수동 authorize 없이는 불가능하다"는 불변식을 확정했다. 이 불변식이 미래의 어떤 ADR에 의해 조용히 약화되면(예: "특정 조건에서는 자동 재개 허용"으로 개정), loop engineering 전체의 안전 기반이 무너진다.
- **ADR-0008**이 정의한 기계적 검증 게이트(구조화 verdict + N-of-2)는 코드 PR뿐 아니라 ADR 승인에도 원칙적으로 적용 가능하다.
- **advisor 하드닝에서 발견한 구조적 구멍**: 초안은 sacred invariant를 "ADR 본문"만 보호했다. 그런데 그 보호를 강제하는 CI 워크플로 자체는 loop가 편집·머지할 수 있는 평범한 파일이다 — loop가 "PR 통과시키기"를 최적화하면 `protects` 필드를 직접 건드리는 대신 그 필드를 검사하는 CI 잡 코드를 고쳐서 우회할 수 있다. 정책 문서만 보호하고 그 집행 계층을 안 보호하면 보호가 성립하지 않는다.
- **이 세션에서 `gh auth status`로 실측 확인**: 현재 이 레포에서 PR을 만드는 데 쓰이는 `chnu-kim` 계정의 활성 토큰이 `repo, workflow, delete_repo` scope와 `ADMIN` 권한을 가지고 있다. CLAUDE.md의 기존 워크플로("`gh auth switch --user chnu-kim` 후 `gh pr create`")를 서브에이전트가 그대로 따르면, **loop가 일상적으로 쓰는 자격증명이 branch protection을 끄거나 레포를 삭제할 수 있는 자격증명과 동일**하다는 뜻이다.
- **GitHub 플랫폼 제약**: PR 작성자는 자기 PR을 스스로 approve할 수 없다(계정 단위로 강제됨). 이는 강력한 기본 방어선이지만, loop가 만드는 PR의 작성자 계정이 사람 검토자와 같은 계정(`chnu-kim`)이면 **사람 본인도 그 PR을 formal approve할 수 없어** 오히려 사람 게이트를 기술적으로 불가능하게 만든다.

## Decision

1. **ADR 저작(grilling→초안)과 하드닝(adversarial 수렴)은 사람 없이 완전 자율로 수행할 수 있다** — grilling 상대는 독립 adversarial 에이전트 역할로 대체 가능하다.
2. **ADR을 `Accepted`로 플립하는 것도 ADR-0008의 게이트(구조화 verdict + 기계적 required check)를 통과하면 사람 없이 가능하다.**
3. **두 개의 sacred invariant만 예외다:**
   - **`live-execution-human-gate`**(ADR-0007) — *"신규 노출 주문 시작은 사람 수동 authorize 없이는 불가능하다."*
   - **`enforcement-integrity`**(이 ADR이 신설) — *"이 게이트 체계를 강제하는 메커니즘 자체는 loop가 스스로 약화시킬 수 없다."* 보호 대상: `.github/workflows/**`, branch protection 설정, **`.github/CODEOWNERS` 자신**(CODEOWNERS가 자기 자신을 소유하지 않으면 loop가 CODEOWNERS를 고쳐 `chnu-kim`을 소유자에서 빼는 PR이 리뷰 없이 통과한다 — advisor 하드닝 2차에서 지적), ADR-0008이 정의한 `risk:critical` 경로 분류 매핑, 그리고 이 두 sacred invariant를 정의하는 ADR-0007/0008/0009/0010 본문.
   
   이 둘을 **약화·제거·우회하는 내용을 담은 ADR·PR은 절대 자동 `Accepted`/자동 merge될 수 없다.** 항상 사람이 직접 검토·승인해야 한다.
4. **`enforcement-integrity`는 CI가 아니라 GitHub branch protection + CODEOWNERS로 강제한다.** CI 잡은 loop가 고칠 수 있는 코드이므로 자기 자신을 지키는 근거가 될 수 없다 — branch protection 설정은 레포 관리자 권한으로만 바꿀 수 있는 GitHub 플랫폼 기능이라 loop가 못 건드리는 유일한 층이다. **CODEOWNERS는 gitignore 스타일 경로 매칭만 하고 brace expansion을 지원하지 않으며, frontmatter 값(`protects` 등)을 읽지 못한다**(codex adversarial-review 3차 지적, GitHub 공식 문서로 확인) — 그래서 보호는 "`protects`가 채워진 파일을 동적으로 찾아 보호"가 아니라 **정적으로 나열된 파일 경로 집합 + "수정하지 말고 대체한다" 컨벤션**으로 성립한다. 구체적으로:
   - `CODEOWNERS`에 현재 sacred 경로를 **각각 명시적으로(brace 없이) 나열**한다: `.github/workflows/**`, **`.github/CODEOWNERS` 자기 자신**, `docs/adr/0004-*.md`, `docs/adr/0007-*.md`, `docs/adr/0008-*.md`, `docs/adr/0009-*.md`, `docs/adr/0010-*.md`, risk-classification 매핑 파일. 소유자는 `chnu-kim`. **`CODEOWNERS` 파일 자신의 항목이 빠지면 이 전체 메커니즘이 자기 자신을 보호하지 못한다** — 반드시 자기 경로를 포함해 등록한다.
   - branch protection이 이 경로를 건드리는 PR에 **CODEOWNERS 리뷰(사람 approve)** 를 required로 요구한다.
   - `docs/adr/0004-*.md`를 포함하는 이유: ADR-0007이 흡수한 "전역 halt = 사람 수동 해제만" 절차의 원 출처가 ADR-0004이므로, ADR-0007을 안 건드리고 ADR-0004만 조용히 개정해도(예: point 6을 자동 재개 허용으로 수정) 같은 불변식이 깨진다. frontmatter 소급 마이그레이션에서 ADR-0004에 `protects: [live-execution-human-gate]`를 부여했다.
   - **미래에 새 ADR이 sacred invariant를 다루게 되는 경우, "수정하지 말고 대체한다"(`docs/adr/README.md`) 컨벤션이 안전망이다.** 기존 sacred 결정을 바꾸려면 반드시 원본 ADR을 `Superseded`로 표시해야 하는데, 그 표시 자체가 이미 CODEOWNERS로 보호된 파일(`docs/adr/000{4,7,8,9,10}-*.md`)의 `status`/`superseded_by` frontmatter를 건드리는 편집이라 자동으로 사람 리뷰가 걸린다. **완전히 새로운 sacred invariant를 도입하는 경우**(예: 향후 ADR-0009 자체를 개정해 새 invariant를 목록에 추가)는 point 7에 따라 ADR-0009 편집이 필요하고, ADR-0009 자체가 CODEOWNERS 보호 대상이므로 역시 사람 리뷰가 걸린다. 즉 "새 sacred 경로가 정적 목록에 없어서 빠져나가는" 경우는, 그 새 결정이 기존 sacred ADR을 대체하거나 sacred invariant 목록 자체를 건드리는 한 발생하지 않는다 — **기존 sacred 결정과 무관하게 완전히 새로운 파일에서 조용히 모순되는 내용을 선언하는 것**만 이 모델의 사각지대이며, 이는 CODEOWNERS의 구조적 한계(경로 기반)이지 이 설계의 결함이 아니다(ADR README의 "수정하지 말고 대체" 컨벤션을 우회하는 것 자체가 이미 이 레포의 다른 규범 위반이다).
5. **loop가 만드는 PR의 작성자 identity는 사람 검토자(`chnu-kim`)와 분리된 별도 identity여야 한다** — 구체적으로 **GitHub App**을 만들어 그 앱의 bot identity(`<app-name>[bot]`)로 커밋·PR을 생성한다. 이렇게 하면 GitHub의 self-approval 차단이 "사람이 자기 자신을 승인 못 함"이 아니라 "**사람이 loop의 산출물을 실제로 승인할 수 있음**"으로 정확히 작동한다.
6. **이 GitHub App의 설치 권한은 `enforcement-integrity`를 기술적으로도 뒷받침해야 한다** — `Administration`·`Workflows` 권한을 부여하지 않는다. `Workflows` 권한이 없으면 GitHub이 `.github/workflows/**`를 건드리는 push 자체를 거부하고, `Administration` 권한이 없으면 branch protection 설정 변경 API를 호출할 수 없다. 즉 CODEOWNERS 게이트가 뚫리기 전에 GitHub 플랫폼이 한 번 더 막는다(belt-and-suspenders).
7. **이 예외 목록(sacred invariant 목록) 자체도 sacred다.** 목록에 새 항목을 추가하거나 제거하는 것 자체가 사람 승인을 요구한다(이 규칙이 재귀적으로 자기 자신을 보호한다).
8. **자율성은 기본값이 아니다 — enforcement 계층이 실재를 증명할 때까지는 기존 사람 게이트 체제로 남는다.** 이 ADR 군(0007~0010)이 `Accepted`되는 시점과, 그 결정이 기술적으로 서 있는(GitHub App 설치·CODEOWNERS 등록·branch protection 규칙·verdict required-check·risk-classification 매핑이 실제로 살아있는) 시점 사이에는 간극이 있다. **ADR이 `Accepted`라는 사실 자체는 자율 동작을 허가하지 않는다** — stateless 에이전트가 "loop는 sacred invariant만 빼고 자율"이라고 읽고 이 간극(오늘 empirically 확인한 admin 자격증명이 여전히 살아있는 구간 포함)에서 자율 행동하면, ADR-0007의 halt-기본값이나 ADR-0004의 기동 replay-gate가 막으려던 것과 같은 종류의 실패가 메타 레벨에서 재발한다. 따라서:
   - loop는 **기동/작업 시작 시 enforcement 계층의 실재를 기계적으로 점검**한다 — 최소 (a) `CODEOWNERS`가 존재하고 sacred 경로(§point 3–4, point 7의 자기 참조 포함)를 커버하는지, (b) branch protection이 그 경로에 required review를 요구하는지, (c) loop의 PR 작성 identity가 `chnu-kim`이 아닌 App identity로 실제 전환됐는지.
   - 이 중 하나라도 확인 불가하거나 미충족이면 **fail-closed = 기존 사람 게이트 체제(사람이 PR 검수·머지)로 남는다.** "증거 없음"을 "이미 안전함"으로 해석하지 않는다.

## Alternatives considered

- **ADR 승인 전체를 계속 사람 게이트로 유지** — 기각: loop engineering의 핵심 요구(검증까지 자율화)를 포기하는 것과 같다. 사용자가 명시적으로 이를 원치 않는다고 확인했다.
- **모든 ADR을 완전 자율(예외 없음)** — 기각: "잘못된 ADR이 복리로 틀어진다"는 위험이 가장 큰 지점(실거래 게이트)을 무방비로 둔다. 단 하나의 사람 개입점도 없으면 loop가 스스로 자신의 안전 계약을 지울 길이 열린다.
- **`risk:critical` 라벨 기준으로 예외 범위를 넓게 잡기** (`risk:critical` 관련 ADR 전체는 사람 게이트) — 기각: ADR-0007과 동일 논리다. "위험도"가 아니라 "그 불변식을 건드리는가"가 진짜 경계다. 킬 스위치 내부 구현을 다루는 ADR이라도 authorize 불변식 자체를 안 건드리면 자율 승인 가능해야 loop engineering이 의미가 있다.
- **sacred invariant를 ADR 본문(정책)만 보호하고 CI로 강제** — 기각(advisor 하드닝 지적): CI는 loop가 편집 가능한 파일이라 자기 자신을 지키는 근거가 될 수 없다. 정책과 집행 메커니즘을 같이 보호해야 한다.
- **loop의 PR을 계속 `chnu-kim` 계정으로 생성하고 admin scope만 줄인 PAT를 발급** — 기각(advisor 하드닝 지적, 사용자 재확인): scope를 줄여도 identity가 `chnu-kim`으로 동일하면 GitHub의 self-approval 차단 규칙 때문에 정작 사람 본인이 그 PR을 formal approve할 수 없다. 검토자와 다른 identity가 필수다.
- **별도 GitHub 계정(사람 계정처럼)을 봇용으로 새로 생성** — 기각: identity 분리는 되지만 이메일 인증·2FA 등 계정 관리 부담이 추가되고, GitHub App처럼 설치 단위 세밀 권한 제어(예: `Workflows` 권한 배제)를 기본 제공하지 않는다. GitHub App이 같은 목적을 더 적은 부담으로 달성한다.
- **ADR `Accepted` 시점부터 즉시 자율 동작 허용(presence-check 없음)** — 기각(advisor 하드닝 2차 지적): enforcement 계층 구축(사람의 GitHub App 설치·CODEOWNERS 작성)에는 이 세션 밖의 시간이 걸린다. 그 사이를 "이미 자율"로 읽으면 오늘 실측한 admin 자격증명이 여전히 살아있는 구간에서 자율 행동이 시작될 수 있다 — 정확히 이 ADR 군이 막으려는 상황이 메타 레벨에서 재발한다.
- **CODEOWNERS 자기 항목 생략(다른 sacred 경로만 등록)** — 기각(advisor 하드닝 2차 지적): CODEOWNERS가 자기 자신을 소유하지 않으면, loop가 CODEOWNERS를 고쳐 `chnu-kim`을 제거하는 PR이 리뷰 없이 통과해 전체 메커니즘이 스스로 열린다.
- **CODEOWNERS 경로를 brace expansion(`docs/adr/000{7,8,9,10}-*.md`)으로 축약** — 기각(codex adversarial-review 3차 지적): GitHub CODEOWNERS는 gitignore 스타일 매칭이라 brace expansion을 지원하지 않는다. 이 패턴은 의도한 파일을 하나도 매칭하지 못해 보호가 전혀 작동하지 않았을 것이다 — 각 경로를 명시적으로 나열한다.
- **`protects`가 채워진 모든 ADR을 CODEOWNERS가 동적으로 찾아 보호** — 기각(codex adversarial-review 3차 지적): CODEOWNERS는 파일 경로만 매칭하고 YAML frontmatter 값을 읽지 못한다 — "동적 보호"는 애초에 이 플랫폼 기능으로 구현 불가능한 약속이었다. 정적 목록 + "수정하지 말고 대체한다" 컨벤션(point 4)으로 대체한다.

## Consequences

- (좋음) 재귀적으로 일관된 구조 — loop는 자기 자신의 안전 계약을 스스로 강화할 순 있어도 약화는 못 한다.
- (좋음) ADR 자율화로 architect 고도까지 loop engineering 범위에 들어온다 — 사용자 목표를 완전히 달성한다.
- (좋음) sacred invariant 보호가 "정책 문서" 차원이 아니라 GitHub 플랫폼이 기술적으로 강제하는 계층까지 내려간다 — CI를 고쳐서 우회하는 경로가 막힌다.
- (좋음) point 8의 presence-check 덕에 "ADR은 Accepted됐지만 enforcement는 아직 안 서 있는" 위험한 간극이 자동으로 기존 사람 게이트로 fail-closed된다 — 이 ADR 군 자체가 자기가 기술하는 안전 철학(fail-closed, 증거 없음≠안전함)을 어긴 채 발효되지 않는다.
- (알려진 잔여 위험) CODEOWNERS 보호는 **정적 파일 경로 목록**이지 `protects` 필드를 동적으로 읽는 게 아니다(codex adversarial-review 3차 지적, point 4). 기존 sacred ADR을 "수정하지 말고 대체"하는 정상 경로는 항상 걸리지만, 이론적으로는 **기존 sacred 파일을 전혀 건드리지 않는 완전히 새로운 ADR이 조용히 모순되는 내용을 선언**하는 경우까지는 이 메커니즘이 못 막는다. CODEOWNERS의 구조적 한계이며, `docs/adr/README.md`의 "수정하지 말고 대체" 컨벤션 준수에 의존한다 — 완벽한 보증이 아니라 실용적 완화다.
- (비용) GitHub App을 실제로 만들고 설치해야 한다 — 이건 사람의 GitHub 웹 UI 작업이 필요하고 이 세션에서 대신 완료할 수 없다(구현 이슈, 사람 액션 필요).
- (비용) 기존 CLAUDE.md의 "`gh auth switch --user chnu-kim` 후 `gh pr create`" 관행을 이 GitHub App 기반 flow로 갱신해야 한다 — 사람이 직접 만드는 PR(예외적 수동 개입)과 loop가 만드는 PR을 구분해야 하므로 워크플로 문서 갱신이 필요하다(구현 이슈).
- (비용) `CODEOWNERS`·branch protection 설정 자체를 실제로 구성해야 한다 — 공유 인프라 변경이라 사람이 직접 확인하며 적용한다.
- (후속) GitHub App 생성·설치 (사람 액션, 구체 권한 스펙은 이 ADR point 6).
- (후속) `CODEOWNERS` 파일 작성 + branch protection 규칙 실제 적용 — 이때 **`enforce_admins`(관리자도 우회 못 하게 할지) 여부는 이 ADR이 결정하지 않는다.** 이건 "사람의 정당한 긴급 override 능력"과 "루프도 admin 우회를 못 쓰게 하는 엄격함" 사이의 실질적 트레이드오프라, branch protection을 실제로 켜는 시점에 사람이 직접 정한다(조용한 기본값 금지).
- (후속) risk-classification 매핑 파일의 구체 위치·형식 확정(ADR-0008과 연계).
- (후속) sacred invariant 목록이 늘어날 가능성에 대비해, 이 목록 자체의 관리 절차(현재는 이 ADR 본문이 SSOT)를 명확히 유지한다.
- (후속) point 8의 presence-check 구현(구현 이슈) — 점검 로직 자체도 최초 1회는 사람이 직접 확인해 "점검 로직이 항상 통과를 리턴하도록 조작되지 않았는지" 검증한다.
