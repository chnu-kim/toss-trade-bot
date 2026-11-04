---
name: issue-drafter
description: "기능·에픽 요청을 여러 팀원/에이전트가 병렬로 작업할 수 있는 GitHub 이슈 묶음으로 분해해 발행한다(local gh / cloud MCP). 사용자가 '이슈 발행해줘', '이 기능 이슈로 쪼개줘', '에픽 분해해줘', '이거 여러 이슈로 나눠줘', '이슈 만들어줘' 같은 신호를 줄 때 사용한다. 단건 이슈도 톤·형식을 맞추기 위해 이 스킬을 쓴다. 각 이슈에 지배 Accepted ADR을 링크하고, 근거 ADR이 없는 아키텍처 포크를 만나면 멈추고 /architect 를 먼저 돌리게 한다. 발행 전 반드시 프리뷰로 사용자 승인을 받는다."
---

# issue-drafter

에픽/기능 요청을 **병렬로 물 수 있는 이슈 묶음**으로 쪼개 이 레포 컨벤션으로 발행한다. **태스크 고도만** — 아키텍처 결정(동시성 모델·무인 안전 계약·패키지 경계)은 `/architect`가 ADR로 내리고, 이 스킬은 ADR을 *읽어서* 링크한다.

## 0. 환경

`SLUG`는 origin에서 도출한다 — 정본은 dispatch-issue §0(a), 하드코딩 금지. 전송·계정은 CLAUDE.md "모델/런타임 프로파일" 표만 따른다: local은 `gh`, cloud는 읽기 `gh api`(REST)·이슈 생성 `mcp__github__issue_write`.

## 1. ADR 읽기

`docs/adr/NNNN-*.md` 중 frontmatter `status: Accepted`인 것을 전부 읽는다. **Proposed·Superseded·`0000-template`은 지배 근거로 쓰지 않는다** — Proposed ADR이 전제면 링크하지 말고 `## 미정 사항`에 "ADR-NNNN 승인 대기"로 남긴다(Accepted는 architect가 이 스킬·dispatch-issue에 보내는 소비 신호다).

- 어떤 이슈가 특정 ADR의 결정에 의존하면 그 이슈에 **지배 ADR을 링크**한다(§4 `## 설계 근거`).
- **아직 결정 안 된 아키텍처 포크**에 부딪히면 **분해를 멈추고** 알린다: "이 이슈는 미결 설계 X에 의존한다. `/architect <X>`로 ADR을 먼저 만든 뒤 다시 부르라." 아키텍처를 여기서 추측해 본문에 박지 않는다.
- **ADR 존재 ≠ 결정 완료.** Accepted ADR이 **correctness-load-bearing 하위결정**(동시성 정합·전이 순서·미러↔durable 일관성 같은 money-guard 계약)을 "구현 이슈에 위임"했으면 그 하위결정은 미결 포크다 — Accepted ADR이 링크됐다는 이유만으로 `agent:ready`로 통과시키지 말고 위처럼 멈춘다. **판별**: 정답과 무관한 free-choice(`atomic.Bool` vs `RWMutex`)는 포크가 아니고, correctness 계약(미러 정합 모델·전이 순서)만 포크로 승격한다. 실측: ADR-0004 point 5 → #57/#32 → ADR-0013 Consequences.

## 2. 레포 컨텍스트

최근 이슈 ~10건과 라이브 라벨 목록을 매번 조회한다(컨벤션은 변한다): local `gh issue list --repo "$SLUG" --state all --limit 10`·`gh label list --repo "$SLUG"` / cloud `gh api "repos/$SLUG/issues?state=all&per_page=30" --jq '[.[] | select(.pull_request == null)][:10]'`(REST issues에는 PR이 섞인다)·`gh api "repos/$SLUG/labels?per_page=100"` 또는 MCP. 5축 라벨 어휘는 CLAUDE.md "이슈 라벨" — 새 이슈에 다섯 축을 빠짐없이 붙인다.

- `agent:` — 선행 의존이 머지돼야 시작 가능하면 `blocked`, 시작 전 사람 결정이 필요하면 `needs-decision`, 명세 완결이면 `ready`. **`agent:ready`만 자율 실행된다.**
- **`risk:critical`(실주문·자금이동·비가역)은 사람 리뷰 필수임을 본문에 명시한다**(ADR-0007/0008 실거래 사람 게이트).

## 3. 분해

결과 먼저(이 이슈가 끝나면 무엇이 달라지나 1~2줄), 측정 가능한 AC("잘 됨" 금지), 모르는 건 `## 미정 사항`. **2~5개, 이슈당 1~3일분** — 1개면 분해 불필요, 6개 이상은 과분해. 의존성 트리는 fan-out·fan-in이 정상이고 사이클·밀집 그래프는 분해 실패 신호, 선형(`A→B→C`)은 병렬 여지가 없으니 쪼갤 가치를 재검토한다.

- (a) **`internal/<도메인>` 경계 = 병렬 단위.** 한 도메인을 세로로 쪼개지 않는다.
- (b) **공유 접점**(`cmd/bot/main.go` wiring·`go.mod`·공유 인터페이스·CODEOWNERS/`sacredRequiredPaths`)은 **가장 먼저 머지되는 이슈**가 담당하도록 의존성을 건다(실측: 3-워커 병렬의 유일한 충돌 — retro "축 2 — 프로세스 / 좋았던 것").
- (c) **파일 소유권으로 분리/병합.** 같은 `.go` 본문을 두 이슈가 편집하면 충돌 확정 → 병합하거나 선행 인터페이스 이슈(타입·시그니처 고정)로 뗀다.

## 4. 본문 템플릿

기존 이슈 스타일(`## 목적`/`## 작업`/`## 규칙`/`## Acceptance Criteria`)을 베이스로, 아래 두 절을 **항상** 끝에 붙인다 — dispatch-issue가 `## 설계 근거`에서 지배 ADR을 읽어 워커에 넘긴다.

```markdown
## 설계 근거
- 지배 ADR: ADR-NNNN (docs/adr/NNNN-...md) — <이 이슈가 따라야 하는 결정 한 줄>

## 병렬 작업 메모
- **건드리는 영역**: <구체 파일/패키지 경로 — 두 이슈를 집합 비교해 충돌을 판단할 수준>
- **선행 이슈**: <#번호 또는 "없음">
- **공유 접점**: <다른 이슈와 겹치는 파일과 겹치는 방식 — 있을 때만>
```

AC는 `go test -race ./...`·`go vet`로 체크 가능한 문장으로 쓴다. 미발행 선행은 앵커명(`선행: 이슈 A`)으로 쓰고 §6에서 실제 번호로 치환한다 — 번호를 추측해 박지 않는다.

## 5. 프리뷰 승인 (필수)

묶음 전체 + 의존 트리 + 5축 라벨 + 지배 ADR을 마크다운으로 보여주고 **명시적 OK 전에는 발행하지 않는다** — "알아서 발행"이라 해도 첫 호출은 프리뷰다(이슈 번호는 영구 소비된다). 피드백이 오면 바뀐 이슈만 다시 쓰고 변경분만 짚는다.

## 6. 발행

위상정렬 순서(선행 먼저)로 발행한다. 본문은 파일/변수로 넘기고, 발행된 번호를 후속 본문의 앵커에 치환한다. local `gh issue create --repo "$SLUG" --title … --body-file … --label …` / cloud `mcp__github__issue_write`. 끝나면 `✓ #N <제목> — URL`로 한 줄씩 요약.

## 7. 실패·엣지

- 중간 실패: **그 자리에서 멈추고** 생성된 번호 + 실패 지점을 보고. 자동 롤백(이미 만든 이슈 close) 금지, 라벨 미존재 시 임의 생성 금지 — 사용자에게 권한 확인.
- 버그 리포트: 분해하지 말고 단건(재현·기대/실제·환경), `type:bug`.
- 가상 의존(코드 선행 vs 이슈로만 존재하는 선행): `병렬 작업 메모`에 두 줄로 분리하고 프리뷰 preamble에 "#X 머지 전제"로 고지.
- 기존 이슈와 제목·스코프가 겹치면 프리뷰에 `⚠️ #N과 중복 가능성`으로 표시하고 사용자 판단에 맡긴다.
