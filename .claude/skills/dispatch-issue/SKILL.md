---
name: dispatch-issue
description: GitHub 이슈 1개를 worktree 격리 + go-tdd-implementer 위임으로 PR까지 처리하는 오케스트레이터. 리뷰는 사다리(REVIEWER), 머지는 사람. 사용자가 "/dispatch-issue N", "이슈 N 디스패치"처럼 특정 이슈를 자율 구현 흐름으로 넘기려 할 때 사용한다.
---

# dispatch-issue

이슈 1개를 **사람 게이트 2곳(이슈 검수 → PR 검수·머지)** 사이에서 자율 처리한다. 메인은 **오케스트레이션만** 하고 구현은 이슈당 워커 1개(`subagent_type: go-tdd-implementer`)에 위임한다. **머지는 절대 자동화하지 않는다 — 최종 책임은 사용자에게 있다.**
인자: 이슈 번호 `N`. 정리 모드: `/dispatch-issue --cleanup N`.
이 디렉터리에 다른 파일을 추가하지 않는다 — `.claude/` 트리 전수 테스트가 등재 전까지 Red다(CLAUDE.md "게이트 승격 규칙").

## 0. 환경·경로 결정 (런타임 도출 — 하드코딩 금지)

**(a) SLUG** — 이 블록이 정본이다(issue-drafter는 여기를 가리킨다). 모든 `gh` 호출에 `--repo "$SLUG"`.

```bash
REPO=$(git rev-parse --show-toplevel)
url=$(git -C "$REPO" remote get-url origin); url=${url%.git}; norm=${url//:/\/}   # scp-style ':' → '/'
repo=${norm##*/}; rest=${norm%/*}; owner=${rest##*/}; SLUG="$owner/$repo"
```

**(b) HARNESS_ENV / GH_WRITE** — `CLAUDE_CODE_REMOTE`가 `true`면 `cloud`(GraphQL 불가 → 읽기는 `gh api` REST, `GH_WRITE`=`mcp__github__*`), 아니면 `local`(`GH_WRITE`=`gh`; 계정 선택은 프로파일 표 'GitHub 쓰기' 행 — `gh api "repos/$SLUG"`가 성공하는 계정을 `GH_ACCOUNT`로 워커에 넘긴다). 나머지 분기는 CLAUDE.md "모델/런타임 프로파일" 표만 참조한다.

**(c) 자격증명** — 세션 것을 그대로 쓴다. 새로 구성·토큰 교체·push/서명 identity 전환·remote 변경 금지(`gh` 계정 선택은 (b)만), 거부되면 정지·보고(ADR-0011 p2·5②). `local`이면 세션의 `SSH_AUTH_SOCK`을 워커에 전달한다. 첫 push에 승인 프롬프트가 뜰 수 있다 — 무인을 가정하지 않는다.

**(d) PR_PATH** — 기계적으로 정한다(presence-check c-1/c-2와 같은 증거): `gh api "repos/$SLUG/contents/.github/workflows/pr-creation.yml?ref=main"`이 존재 **AND** `gh api "repos/$SLUG/pulls?state=all&per_page=50"`에 `.user.login=="mechanu[bot]" and .base.ref=="main" and .head.repo.full_name==$SLUG`인 PR ≥1(c-1/c-2의 단순화 — 동일 레포·main 대상만 센다) → `dispatch`, 아니면 `human-bootstrap`(ADR-0015 p8). `human-bootstrap`인데 이슈 범위가 sacred 경로(`.github/CODEOWNERS`가 `@chnu-kim` 소유로 등재한 경로 = `internal/enforcement` `sacredRequiredPaths`)를 건드리면 디스패치 전에 self-approval 교착(ADR-0011 p6, PR #42/#44)을 사용자에게 알리고 확인받는다. 워커는 경로를 고르지 않는다.

**(e) REVIEWER**(ADR-0008) — 세션 Skill 목록에 codex 리뷰 스킬이 있으면 `codex-pr-review`(워커가 `--wait --base origin/main`으로 실행, 유일한 독립 검증자); 없으면 `risk:low/high` → `claude-advisory`(§4), `risk:critical` → `none-report`(대체 금지, 사용자 보고).

**(f) W0** — 부트스트랩 창·W0(sacred 머지 불가 창)의 정의·순서는 CLAUDE.md "PR 생성 흐름"(ADR-0016 point 5·6) — W0 안에서는 sacred 경로 PR을 만들 수 없다.

**(g) 절대 금지**: main/master 직접 작업, `gh issue develop`, 자동 머지, 주문/쓰기 경로 자동 재시도.
App private key·installation token은 이 오케스트레이터·워커 세션에 절대 없다(ADR-0011 p1). PR 생성 payload·폴링 spec은 `.claude/agents/go-tdd-implementer.md` "완료 절차"가 정본이다.

## 1. 전제 점검

이슈 fetch: local `gh issue view N --repo "$SLUG" --json number,title,body,labels,state` / cloud `gh api "repos/$SLUG/issues/N"` 또는 `mcp__github__issue_read`. 판정:

- state가 open이 아니면(대소문자 무시 — local `OPEN`, cloud REST `open`) → 중단, 보고.
- `agent:blocked`·`agent:needs-decision` → **중단**. 무엇이 풀려야 하는지 본문에서 찾아 보고.
- `agent:ready` 없음 → 중단, 사용자 확인.
- `risk:critical` → **사용자 명시 확인 전 진행 금지**(실주문·자금이동·비가역은 사람 리뷰 필수). `risk:high`는 진행하되 PR 본문에 위험 요지를 적도록 워커에 지시.

**지배 ADR**: 본문 `## 설계 근거`가 링크한 `docs/adr/NNNN-*.md` 전문을 확보해 §3 프롬프트에 첨부한다. frontmatter `status`가 `Accepted`가 아니면(Proposed/Superseded) **중단**하고 사용자에게 알린다 — 미승인·폐기된 결정을 근거로 구현하지 않는다.

```bash
git -C "$REPO" status --porcelain                                    # 비어 있어야 함
git -C "$REPO" checkout main && git -C "$REPO" pull --ff-only
```

**preflight**: `Agent` 툴에 `subagent_type: go-tdd-implementer`가 없으면 worktree 생성 **전**에 중단하고 보고한다. `general-purpose` 대체 절대 금지 — 규칙 없는 에이전트 = 무인 안전 계약 소실.

## 2. 브랜치 + worktree

`BR="<N>-<slug>"`(slug: 제목의 ascii 영숫자 토큰을 소문자 kebab으로, 없으면 `issue`; §6은 `<N>-` 접두로 복원한다). `WT`는 레포 밖 sibling — go 모듈 오염 방지. local·cloud 동일(cloud 부모 디렉터리 쓰기 가능 실측). Agent `isolation` 옵션은 쓰지 않는다(경로·baseRef·정리 의미 실측 전; 메인이 WT를 알아야 정리·진단 보존). sibling 생성 불가면 중단·보고.

```bash
WT="$(dirname "$REPO")/$(basename "$REPO")-worktrees/$BR"
git -C "$REPO" checkout -b "$BR" && git -C "$REPO" checkout main && git -C "$REPO" worktree add "$WT" "$BR"
```

## 3. 위임

`Agent` 툴, `subagent_type: go-tdd-implementer`, `effort: high`(`risk:high`면 `xhigh`). 페르소나·TDD 규칙·검증 게이트·완료 절차·반환 명세는 에이전트 파일이 가진다 — 프롬프트에는 **변수 블록만** 넘긴다(규칙 이중 보유 금지; 규칙 수정은 에이전트 파일에서). Workflow 툴로 다중 이슈를 병렬 디스패치하지 않는다(프로파일).

```
WT: {WT} / BR: {BR} / SLUG: {SLUG} / N: {N}
HARNESS_ENV: {local|cloud} / GH_WRITE: {gh|mcp__github__*}
PR_PATH: {dispatch|human-bootstrap} / REVIEWER: {codex-pr-review(워커가 --wait --base origin/main)|claude-advisory|none-report}
SSH_AUTH_SOCK: {local만} / GH_ACCOUNT: {local만 — §0(b) 접근 가능 계정}
이슈 #{N}: {title}
--- 이슈 본문 --- {전문}
--- 지배 ADR (따르라, 위반 금지 / 없으면 "해당 없음") --- {전문}
--- FINDINGS (재디스패치 시에만) --- {§4 findings JSON — codex 또는 advisory}
```

워커가 `null` 반환·사망하면 worktree를 **정리하지 말고**(진단 보존) 실패를 그대로 보고한다.

## 4. PR 확보 + 독립 리뷰 사다리

워커 반환의 PR URL 또는 '미생성+사유'를 확인한다.

- `PR_PATH=dispatch`인데 미생성(403/타임아웃) → 사용자에게 보고하고 중단. 임의로 `human-bootstrap`으로 전환하지 않는다.
- `PR_PATH=human-bootstrap` → 오케스트레이터가 만든다: local `gh pr create --repo "$SLUG" --head "$BR" --title … --body-file …`(기본 브랜치 사용), cloud `mcp__github__create_pull_request`. 본문에 `Closes #N` + "부트스트랩 사람 계정 PR(ADR-0015 p8)"을 명시한다.

`REVIEWER=codex-pr-review`: `dispatch`면 워커가 이미 수행했다(base `origin/main`, 대응은 반환의 "리뷰 대응"). `human-bootstrap`이면 PR 생성 직후 오케스트레이터가 Skill 툴로 `codex-pr-review`를 `--wait --base origin/main`으로 실행하고 findings는 FINDINGS로 §3 재디스패치(최대 3라운드, 진동·포크 판정은 아래와 동일).

`REVIEWER=claude-advisory`(advisory, 오케스트레이터가 실행 — 워커는 서브에이전트를 띄울 수 없다): `Agent` 툴 `subagent_type: general-purpose`, `effort: xhigh`, 읽기 전용 적대 리뷰 1회. 프롬프트: "WT에서 `git fetch origin main && git diff origin/main...HEAD`만 읽고 파일을 바꾸지 말 것. 관점은 무인 안전·지배 ADR 준수. 주장마다 실행한 명령·관찰을 evidence로. 출력 첫 줄 `advisory (same-model)`, 이어서 JSON `findings[{severity,file,line,claim,evidence,governing_adr,repeat_of}]`". findings가 있으면 §3의 워커를 같은 WT/BR + FINDINGS로 재디스패치 → 재푸시 → 재리뷰, **최대 3라운드**. 같은 지적이 2라운드 연속이면 진동 → 보류·보고; 매 라운드 새 지적인데 전부 같은 표면이면 설계 포크 → 후속 이슈. same-model 리뷰는 ADR-0008 p2의 독립 검증자가 **아니다** — 보고에 advisory로 표기한다.

`REVIEWER=none-report`: 리뷰 없이 "독립 리뷰 미수행 — 사람 리뷰 필수"로 보고한다.

## 5. 핸드오프 + 보고

### 5a. 회고 증거 PR 코멘트 (머지 안내보다 먼저)

cleanup·회고는 다른 세션에서 돌 수 있으므로 워커 반환의 **마찰·판단·리뷰 대응**을 지금 PR 코멘트로 persist한다. PR 코멘트는 커밋-타임 `opensource-maintainer` 게이트를 우회하므로 raw 덤프 금지 — 같은 기준의 public-readiness 게이트를 통과한 큐레이션 요약만 쓴다(시크릿·개인 경로·account/tool 식별자·env 값·raw 로그 없음). 안전하게 못 쓰면 게시하지 말고 `민감 skip`.

```
## 회고 증거 (retro evidence) · issue #N · PR #P · <UTC>
- 마찰: … / - 판단·모호: … / - 리뷰 대응: …
```

게시: local `gh pr comment P --repo "$SLUG" --body-file …` / cloud `mcp__github__add_issue_comment`. 게시 후 실제로 붙었는지 확인하고 handoff 상태를 `게시·검증됨` / `민감 skip` / `실패` 중 하나로 확정한다 — 실패를 성공처럼 계속하지 않는다(§7이 이 코멘트에 의존).

### 5b. 사용자 보고

- PR URL(사용자가 **검수하고 직접 머지**) / 검증 게이트 통과 현황 / 워커가 보고한 판단·모호점
- 리뷰어 종류(`REVIEWER`)·advisory 여부·독립 리뷰 미수행 여부
- handoff 상태(5a) — skip·실패면 "cleanup 회고가 오케스트레이션 수준으로 한정됨" 명시
- 안내: "검수 후 머지하면 `/dispatch-issue --cleanup N`으로 worktree·브랜치를 정리하겠습니다."

**메인은 절대 머지하지 않는다.**

## 6. 정리 `--cleanup N`

다른 세션일 수 있으므로 `N`에서 복원한다(REST — 양 환경):

```bash
BR=$(git -C "$REPO" worktree list --porcelain | grep -oE "refs/heads/${N}-[^[:space:]]*" | head -1 | sed 's#refs/heads/##')
WT="$(dirname "$REPO")/$(basename "$REPO")-worktrees/$BR"
P=$(gh api "repos/$SLUG/pulls?head=${SLUG%%/*}:$BR&state=all" --jq '.[0].number')
```

머지 확인 **후에만**: local `gh pr view "$P" --repo "$SLUG" --json state,mergedAt` / cloud `gh api "repos/$SLUG/pulls/$P" --jq '{state,merged_at}'` 또는 `mcp__github__pull_request_read`.

```bash
git -C "$REPO" worktree remove "$WT"
git -C "$REPO" checkout main && git -C "$REPO" pull --ff-only && git -C "$REPO" branch -d "$BR"
```

미머지면 정리하지 말고 보고한다. 이 이슈가 선행이던 `agent:blocked` 이슈가 있으면 `agent:ready` 전환을 제안한다. 이어서 §7.

## 7. 회고

`retro` 스킬을 호출한다. 증거는 컨텍스트가 아니라 5a 코멘트에서 fetch한다(다른 세션일 수 있다): `gh api "repos/$SLUG/issues/P/comments"`(양 환경; cloud는 `mcp__github__pull_request_read`도 가능)에서 `## 회고 증거 (retro evidence)`로 시작하고 `PR #P ·`를 **경계까지** 매칭하는 최신 1건. 없으면(`NONE`) 오케스트레이션 수준으로만 회고하거나 skip한다 — 추측 금지(retro §1의 서브에이전트 맹점). 학습이 CLAUDE.md·ADR·스킬급이면 프리뷰 후 승인받는다(retro §5).
