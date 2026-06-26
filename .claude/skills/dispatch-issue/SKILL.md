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

작업 트리가 깨끗한지 확인하고 main을 최신화한다:

```bash
git -C "$REPO" status --porcelain   # 비어 있어야 함
git -C "$REPO" checkout main && git -C "$REPO" pull --ff-only
```

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

`Agent` 툴로 **단일 서브에이전트**를 띄운다. `isolation` 옵션은 쓰지 않는다(우리가 만든 named worktree에서
작업해야 하므로). 아래 프롬프트를 채워 전달한다. `{WT}`/`{BR}`/`{SLUG}`/`{N}`/`{title}`/이슈 본문을 실제 값으로 치환.

> **[서브에이전트 프롬프트 템플릿]**
>
> 너는 격리된 git worktree에서 GitHub 이슈 하나를 TDD로 끝까지 구현하는 에이전트다.
> 모든 작업은 이 디렉토리 안에서만: `{WT}`  / 레포 슬러그: `{SLUG}`
> 절대 메인 레포 디렉토리나 main 브랜치를 건드리지 마라. 머지하지 마라.
>
> **이슈 #{N}: {title}**
> ```
> {issue body 전문}
> ```
>
> **반드시 지킬 규칙 (CLAUDE.md):**
> 1. **TDD**: 실패 테스트 먼저(Red) → 통과 최소 구현(Green) → 정리(Refactor). 기존 테스트 커버 여부 먼저 확인.
> 2. **패키지 레이아웃**: golang-standards/project-layout. 로직은 `internal/` 도메인 패키지, `cmd/<binary>/main.go`는
>    얇게, 순환 참조 금지, 패키지명=디렉토리명·단수 도메인명.
> 3. **Toss API 추측 금지**: 새 엔드포인트/파라미터는 OpenAPI 스펙
>    (https://openapi.tossinvest.com/openapi-docs/latest/openapi.json) 먼저 확인. Toss API 직접 호출 테스트 금지 —
>    `httptest`/mock으로 검증.
> 4. **무인 운영 제약**: panic 방지 recover 경계, 재시작 안전, 조회만 백오프 재시도(주문은 금지), 토큰은 한 곳에서만 발급·캐시.
> 5. **검증 게이트(전부 통과 필수)**:
>    ```bash
>    cd "{WT}"
>    gofmt -l -w . && go vet ./... && go test -race ./...
>    ```
>    `-race`는 동시성/single-flight 검증을 위해 필수.
> 6. **커밋 직전 필수**: `opensource-maintainer` 스킬로 시크릿·개인정보·환경 의존 내용 점검. 통과 후에만 커밋.
> 7. **커밋**: repo-local git 설정 그대로 사용. 메시지는 명확히, 이슈 컨텍스트 반영.
>
> **완료 절차:**
> ```bash
> export SSH_AUTH_SOCK="{메인이 전달한 1Password/SSH agent 소켓 경로}"   # 미설정 시 서명·푸시 실패
> git -C "{WT}" push -u origin "{BR}"
> gh pr create --repo "{SLUG}" --base main --head "{BR}" \
>   --title "<요약>" --body "<무엇을·왜, Acceptance Criteria 충족 근거, risk:high면 위험 요지>. Closes #{N}"
> ```
> (gh 호출 전 active 계정이 `{SLUG}`에 접근 가능한지 `gh repo view "{SLUG}"`로 확인, 실패 시 접근 가능한 계정으로 전환.)
> PR 생성 직후 **반드시 `codex-pr-review` 스킬을 `--wait --base main`으로 실행**(글로벌 지침: codex 리뷰+적대적
> 리뷰 병렬, 결과 verbatim 회수). Skill 툴로 `codex-pr-review` 호출, args `--wait --base main`.
>
> **리뷰 결과 처리 (review→fix 루프):**
> - 리뷰가 **진짜 결함**(CLAUDE.md 위반·버그·무인 안전 구멍 등)을 보고하면 → **TDD로 고친다**(실패 테스트
>   추가 → 수정 → 게이트 `gofmt`/`go vet`/`go test -race` 재통과) → 추가 커밋 → 같은 브랜치로 재푸시.
> - false positive·범위 밖·의도적 trade-off는 고치지 말고 **최종 보고의 "판단" 항목에 근거와 함께 남긴다**
>   (사용자 검수가 판단). 모호하면 보수적으로 고치되 그 이유를 보고.
> - 자동 재시도 금지 원칙은 유지 — 주문/쓰기 경로 자동 재시도를 리뷰가 권해도 도입하지 않는다.
>
> **최종 반환값(메인에게 보고할 raw 데이터):**
> - PR URL
> - `go test -race ./...` 결과 요약(통과/실패, 커버 케이스)
> - **codex 리뷰가 보고한 항목 + 각각 대응(고침/근거 남기고 보류)**
> - 구현 중 내린 판단·이슈 명세에서 모호했던 점(있으면) — 사용자 검수용
> - 검증 게이트 각각 통과 여부

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
