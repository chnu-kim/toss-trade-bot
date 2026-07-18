---
name: dispatch-issue
description: GitHub 이슈 하나를 worktree 격리 + 서브에이전트 위임으로 끝까지(TDD→테스트→PR→자체리뷰) 처리하는 오케스트레이터. 사용자가 "이슈 N 착수", "이슈 N 디스패치", "/dispatch-issue N", "이슈 N 작업 시작", "이거 에이전트한테 맡겨" 같이 특정 이슈를 자율 구현 흐름으로 넘기고 싶다는 신호를 줄 때 반드시 사용한다. 메인 에이전트는 얇게 유지하고 구현은 서브에이전트에 위임하는 것이 이 스킬의 핵심이다.
---

# dispatch-issue

이슈 1개를 **사람 게이트 2곳(이슈 검수 → PR 검수·머지)** 사이에서 자율 처리한다.
메인 에이전트는 **오케스트레이션만** 하고, 실제 구현(TDD→테스트→PR)은 **단일 서브에이전트**에 위임한다.
**머지는 절대 자동화하지 않는다 — 최종 책임은 사용자에게 있다.**

인자: 이슈 번호 `N` (예: `/dispatch-issue 1`). 정리 모드: `/dispatch-issue --cleanup N`.

---

## 0. 환경 도출 (하드코딩 금지 — 런타임에 구한다)

이 레포는 언제든 public 전환 가능하므로 절대 경로·계정명을 이 문서에 박지 않는다. 항상 아래로 도출한다:

```bash
REPO=$(git rev-parse --show-toplevel)
url=$(git -C "$REPO" remote get-url origin); url=${url%.git}
norm=${url//:/\/}                       # scp-style 'git@host:owner/repo'의 ':'를 '/'로 정규화
repo=${norm##*/}; rest=${norm%/*}; owner=${rest##*/}
SLUG="$owner/$repo"                       # owner/repo — https·ssh://·scp 모든 remote 형식 대응
```

> ⚠️ `dirname`/`basename`이나 BSD sed(lazy 미지원)로 파싱하면 scp-style URL에서 깨진다. 위 정규화 방식을 쓸 것.

모든 `gh` 호출에 `--repo "$SLUG"` 를 명시한다(기본 레포 미설정 + 잘못된 active 계정 fallback 방지).

**계정 핀**: active gh 계정이 이 레포에 접근 못 할 수 있고(예: 기본 계정이 collaborator 아님),
**active 계정은 명령 사이에 되돌아갈 수 있다.** 따라서 접근 가능한 계정을 한 번 알아낸 뒤, gh 작업(특히
이슈 조회·`pr create`)은 **같은 셸 명령 안에서 `gh auth switch` 직후 곧바로 실행**한다:

```bash
if ! gh repo view "$SLUG" >/dev/null 2>&1; then
  # gh auth status로 로그인된 계정들을 보고, 접근 가능한 계정으로 전환:
  #   gh auth switch --user <그 계정> && gh repo view "$SLUG"  (검증)
  # 끝까지 실패하면 정리하지 말고 사용자에게 보고.
  gh auth status
fi
# 이후 모든 gh 호출 예시: gh auth switch --user <그 계정> >/dev/null && gh <cmd> --repo "$SLUG" ...
```

**loop PR 작성 identity(ADR-0011)**: 3단계 서브에이전트가 만드는 PR의 목표 작성자는 사람 계정이
아니라 GitHub App(`mechanu[bot]`)이다 — 사람 검토자와 identity가 같으면 GitHub의 self-approval
차단 때문에 사람 본인이 그 PR을 승인하지 못한다(#41/#42에서 실측). 흐름: 커밋·push는 그대로
`chnu-kim`(identity 분할 — point 2), 이후 `gh pr create`를 직접 부르지 않고
`POST /repos/{owner}/{repo}/dispatches`(`event_type: create-loop-pr`)로 트리거하면 main에 고정된
`.github/workflows/pr-creation.yml`이 narrowing된 App 토큰으로 PR을 생성한다 — 구체적 CLI 사용법·
`client_payload` 구성·PR 확인 폴링은 `.claude/agents/go-tdd-implementer.md`의 "완료 절차"에 있다.
App private key는 main-제한 GitHub Actions environment(`loop-pr`) 시크릿에서만 읽히고, 이
오케스트레이터·서브에이전트 세션에는 절대 존재하지 않는다(point 1 — 조회·발급 시도조차 금지).
**사람이 직접 개입하는 PR**(`/architect` grilling 세션 산출물, 수동 hotfix 등 이 스킬 밖의 작업)은
이 흐름과 무관하게 계속 사람 계정으로 만든다.

> ⚠️ **가동 전제**: 이 흐름은 `pr-creation.yml`이 main에 존재하고 + loop 자격증명 narrowing(ADR-0011
> point 5 ②)이 완료되고 + `loop-pr` environment가 실키로 프로비저닝된 뒤에만 실제로 동작한다(절차:
> `docs/runbooks/loop-pr-environment-provisioning.md`). 그 전(부트스트랩 구간 — 이 workflow 자체를
> 도입하는 PR들이 여기 해당한다)에는 서브에이전트가 기존 `gh auth switch --user <사람 계정>` →
> `gh pr create` 경로로 폴백한다(go-tdd-implementer.md에 명시).

**절대 금지**: main/master 직접 작업, `gh issue develop`(권한 부족), 자동 머지, 주문/쓰기 경로 자동 재시도.
**git 서명/인증**: repo-local 설정이 이미 잡혀 있다. 단, **CLI로 커밋·푸시하기 전 SSH agent 소켓이
설정돼야** 서명·인증이 된다(메인이 환경/메모리에서 실제 `SSH_AUTH_SOCK`
값을 구해 서브에이전트 프롬프트에 넣어 전달한다. 이 문서엔 개인 경로를 박지 않는다). 첫 push 때 승인
프롬프트가 뜰 수 있다 — 사용자가 PR 단계에 함께 있을 때이므로 정상. 무인 가정하지 말 것.

---

## 1. 전제 점검 (메인 에이전트)

```bash
gh issue view N --repo "$SLUG" --json number,title,body,labels,state
```

이슈 본문·라벨을 읽고 **디스패치 가능 여부**를 판정한다:

- `state != OPEN` → 중단, 사용자에게 보고.
- 라벨에 `agent:blocked` 또는 `agent:needs-decision` → **중단**. "자율 디스패치 대상 아님(선행 의존/사람
  결정 대기)"이라 알리고, 무엇이 풀려야 하는지 본문에서 찾아 보고.
- 라벨에 `agent:ready` 없음 → 중단, 사용자 확인 요청.
- 라벨에 `risk:critical` → **사용자 명시 확인 전까지 진행 금지**(실주문·자금이동·비가역은 사람 리뷰 필수).
  `risk:high`면 진행하되 PR 본문에 위험 요지를 적도록 서브에이전트에 지시.

**지배 ADR 읽기**: 이슈 본문의 `## 설계 근거`에 `ADR-NNNN` / `docs/adr/NNNN-*.md` 링크가 있으면 해당 파일을
`cat`해 전문을 확보한다. 이 결정의 *근거와 버린 대안*을 서브에이전트가 모르면 구현 중 결정을 모른 채 위반한다
(이 파이프라인이 실제로 겪은 통증). 확보한 ADR 전문은 3단계 프롬프트에 첨부한다.
**단, 링크된 ADR의 `Status`를 확인한다** — `Accepted`가 아니면(Proposed/Superseded) **중단하고 사용자에게 알린다**:
미승인·폐기된 결정을 확정 근거로 구현하면 안 된다. 사용자가 그 ADR을 Accepted로 올리거나 이슈 링크를 고친 뒤 재개한다.

작업 트리가 깨끗한지 확인하고 main을 최신화한다:

```bash
git -C "$REPO" status --porcelain   # 비어 있어야 함
git -C "$REPO" checkout main && git -C "$REPO" pull --ff-only
```

**서브에이전트 preflight** (worktree 만들기 *전* — stateful 셋업이 시작되면 늦다): 3단계는
`subagent_type: go-tdd-implementer`에 전적으로 의존하고, 프롬프트엔 변수만 담겨 규칙이 없다. 따라서 **이
에이전트가 사용 가능한지 먼저 확인한다** — `Agent` 툴의 "Available agent types" 목록(시스템 리마인더)에
`go-tdd-implementer`가 있어야 한다. 없으면(`.claude/agents/go-tdd-implementer.md` 미로드·이름 변경·해당 런타임이
프로젝트 에이전트 미지원 등) **worktree를 만들기 전에 중단**하고 사용자에게 알린다(예: "go-tdd-implementer
에이전트가 로드되지 않았다 — .claude/agents/ 확인 필요"). **절대 general-purpose로 폴백하지 마라** — 지금
프롬프트엔 TDD·검증 게이트·PR·리뷰 루프 규칙이 빠져 있어, 규칙 없는 일반 에이전트가 구현을 시도하면 무인 안전
계약이 통째로 사라진다(조용한 성능 저하가 하드 실패보다 나쁘다).

---

## 2. 브랜치 + worktree 격리 (메인 에이전트)

이슈 제목에서 ascii kebab slug를 만든다 (예: "internal/toss: OAuth2 토큰 매니저" → `oauth2-token-manager`).
CLAUDE.md의 worktree 댄스를 그대로 따른다(`gh issue develop` 금지):

```bash
BR="<N>-<slug>"                                   # 예: 1-oauth2-token-manager
WT="$(dirname "$REPO")/$(basename "$REPO")-worktrees/$BR"   # 레포 밖 sibling — go 모듈 오염 방지

git -C "$REPO" checkout -b "$BR"
git -C "$REPO" checkout main
git -C "$REPO" worktree add "$WT" "$BR"
```

---

## 3. 구현 위임 (서브에이전트 — 위임은 여기가 전부)

`Agent` 툴로 **단일 서브에이전트**를 띄우되 `subagent_type: go-tdd-implementer`로 지정한다
(`.claude/agents/go-tdd-implementer.md`). 이 에이전트가 **TDD 규칙·검증 게이트·완료 절차·리뷰-수정 루프·반환값
명세라는 워커 페르소나를 시스템 프롬프트로 이미 갖고 있다** — 메인은 그걸 프롬프트에 다시 쓰지 않고, **per-issue
변수만** 넘긴다. `isolation` 옵션은 쓰지 않는다(우리가 만든 named worktree에서 작업해야 하므로).

> 페르소나를 인라인으로 반복하지 않는 이유: 오케스트레이터 출력 토큰 절감 + 워커 툴 스키마 slim(에이전트가
> `tools:`로 스코프됨) + 페르소나 drift 방지. 규칙을 고치려면 **여기가 아니라 에이전트 정의 파일을 고친다** —
> 이 §3과 `go-tdd-implementer.md`가 규칙을 이중으로 들고 있으면 안 된다.

아래 변수 블록만 채워 `prompt`로 전달한다(`{...}`를 실제 값으로 치환):

> ```
> 작업 디렉토리(WT): {WT}
> 브랜치(BR): {BR}
> 레포 슬러그(SLUG): {SLUG}
> SSH_AUTH_SOCK: {메인이 환경/메모리에서 구한 SSH agent 소켓 경로}
> gh 접근 가능 계정: {§0에서 검증한, 이 레포 접근 가능한 계정}
>
> 이슈 #{N}: {title}
> --- 이슈 본문 ---
> {issue body 전문}
> --- 지배 ADR (있으면 — 이 결정을 따르라, 위반 금지 / 없으면 "해당 없음") ---
> {이슈가 링크한 docs/adr/*.md 전문}
> ```

서브에이전트가 `null` 반환/사망 시, 메인은 worktree를 **정리하지 말고**(진단 보존) 실패를 그대로 보고한다.

---

## 4. 사람 게이트 2 + 보고 (메인 에이전트)

머지 안내를 하기 **전에** 회고 증거를 먼저 handoff한다(4a) — 그래야 성공/실패를 알고 사용자 리포트(4b)에 그 상태를 함께 담아, 실패를 모른 채 머지시키지 않는다.

### 4a. 회고 증거 durable handoff (§6 선행, 리포트보다 먼저)

cleanup·회고는 **다른 세션에서 머지 후** 돌 수 있어 그때 서브에이전트 반환이 컨텍스트에 없다. 그래서 지금(반환이 손에 있을 때) 서브에이전트의 **마찰·판단·codex 대응**을 PR 코멘트로 persist한다 — 세션에 안 묶이고 PR에 붙는 유일한 durable 표면이다.

> ⚠️ **게시 전 public-readiness 게이트(필수).** PR 코멘트는 durable GitHub 히스토리인데 **커밋-타임 `opensource-maintainer` 게이트를 우회**한다. 이 레포는 언제든 public 전환 가능하므로, 반환 필드를 raw로 덤프하지 말고 **큐레이션한 구조적 요약**으로 옮기며 시크릿·`/Users/…` 등 개인 경로·account/tool 식별자·env 값·raw 명령 출력/로그가 없는지 확인한다(opensource-maintainer와 같은 기준). **안전하게 못 쓰겠으면 게시하지 말고** handoff 상태를 `민감으로 skip`으로 둔다.

**코멘트는 self-identifying해야 한다** — §6이 낡은/다른 코멘트를 evidence로 오인하지 않도록 헤더에 issue#·PR#·시각을 박는다. (PR 코멘트는 사람·자동화가 편집·추가할 수 있어 마커만 맞으면 옛 retry 코멘트를 소비할 위험. **solo private repo라 author 위조 방어는 과설계로 스코프 아웃**하고, staleness/중복만 막는다.)

```bash
# $FRICTION/$JUDGMENT/$CODEX 는 위 게이트를 통과한 public-safe 요약. TS=실행 시각(date -u +%FT%TZ).
# §0 규칙: active gh 계정이 명령 사이 되돌아갈 수 있으므로 검증 계정으로 핀 후 같은 셸에서 게시.
gh auth switch --user <§0 검증 계정> >/dev/null && \
  gh pr comment <PR> --repo "$SLUG" --body "$(printf '## 회고 증거 (retro evidence) · issue #%s · PR #%s · %s\n- 마찰: %s\n- 판단/모호: %s\n- codex 대응: %s\n' "$N" "$PR" "$TS" "$FRICTION" "$JUDGMENT" "$CODEX")"
```

게시 후 코멘트가 검증 계정 author로 실제 붙었는지 확인한다. **handoff 상태**를 `게시·검증됨` / `민감 skip` / `실패` 중 하나로 확정한다. 실패면 persist된 척 계속하지 않는다(§6이 이 코멘트에 의존).

### 4b. 사용자 보고

- PR URL (← 사용자가 **검수하고 직접 머지**)
- 테스트/검증 게이트 통과 현황
- 서브에이전트가 보고한 판단·모호했던 점
- **회고 handoff 상태**(4a): `게시·검증됨` / `민감 skip` / `실패` — skip·실패면 "cleanup 회고가 오케스트레이션 수준으로 한정됨"을 명시.
- 안내: "검수 후 머지하면 `/dispatch-issue --cleanup {N}` 로 worktree·브랜치를 정리하겠습니다."

**메인은 절대 머지하지 않는다.**

---

## 5. 정리 모드: `/dispatch-issue --cleanup N`

사용자가 PR을 머지한 **후에만** 실행. 먼저 머지 확인:

```bash
gh pr view <PR번호> --repo "$SLUG" --json state,mergedAt
```

머지됐으면:

```bash
git -C "$REPO" worktree remove "$WT"
git -C "$REPO" checkout main && git -C "$REPO" pull --ff-only
git -C "$REPO" branch -d "$BR"
```

머지 안 됐으면 정리하지 말고 사용자에게 알린다.
이 이슈가 선행이던 `agent:blocked` 이슈가 있으면 `agent:blocked`→`agent:ready` 라벨 전환을 제안한다.

## 6. 회고 (정리 직후 — 작업이 진짜 닫히는 지점)

정리가 끝나면 이슈가 완전히 종결된 것이다 — 회고의 이상적 트리거다. `retro` 스킬을 돌려 이번 이슈에서 **재사용 가능한 학습을 실행 가능한 형태로 durable 표면에 남긴다**(없으면 self-skip).

**증거는 컨텍스트가 아니라 PR 코멘트에서 fetch한다** — cleanup은 dispatch와 다른 세션일 수 있어 서브에이전트 반환이 컨텍스트에 없다. §4가 남긴 "회고 증거" 코멘트를 읽는다:

```bash
# §0 규칙대로 검증 계정 핀 후 같은 셸에서 fetch. gh pr view <PR>가 이미 이 PR로 스코프하지만,
# PR 마커는 경계까지 매칭한다("PR #2"는 substring이라 "PR #21"에도 걸림 — 실증 확인. "PR #<PR> ·"로 경계).
gh auth switch --user <§0 검증 계정> >/dev/null && \
  gh pr view <PR> --repo "$SLUG" --json comments --jq \
    '[.comments[] | select(.author.login=="<§0 검증 계정>" and (.body|startswith("## 회고 증거")) and (.body|contains("PR #<PR> ·")))] | sort_by(.createdAt) | last | .body // "NONE"'
```

`NONE`이면 증거 부재 — 오케스트레이션 수준으로만 회고하거나 skip한다(없는 걸 추측하지 않는다). 후보가 모순되게 여럿이면(예: 서로 다른 재실행 흔적) 최신을 쓰되 사용자에게 그 사실을 알려 확인받는다.

이 코멘트의 **마찰·판단·codex 대응**이 회고 증거다. 너는 서브에이전트 transcript를 못 봤으므로 회고를 그 증거 + 오케스트레이션 수준으로 한정한다(retro §1의 서브에이전트 맹점). 코멘트가 없으면(구버전 PR 등) 증거 부재를 인정하고 오케스트레이션 수준으로만 회고하거나 skip한다 — 없는 걸 추측하지 않는다. 학습이 memory면 자율 기록, CLAUDE.md·ADR·스킬급이면 프리뷰 후 승인받는다.
