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

**절대 금지**: main/master 직접 작업, `gh issue develop`(권한 부족), 자동 머지, 주문/쓰기 경로 자동 재시도.
**git 서명/인증**: repo-local 설정이 이미 잡혀 있다. 단, **CLI로 커밋·푸시하기 전 SSH agent 소켓이
설정돼야** 서명·인증이 된다(이 머신은 1Password agent 사용 — 메인이 환경/메모리에서 실제 `SSH_AUTH_SOCK`
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
> SSH_AUTH_SOCK: {메인이 환경/메모리에서 구한 1Password/SSH agent 소켓 경로}
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

서브에이전트 결과를 받아 사용자에게 간결히 보고:

- PR URL (← 사용자가 **검수하고 직접 머지**)
- 테스트/검증 게이트 통과 현황
- 서브에이전트가 보고한 판단·모호했던 점
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
