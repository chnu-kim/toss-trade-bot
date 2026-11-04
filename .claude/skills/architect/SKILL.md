---
name: architect
description: 기능 분해 전 설계 결정을 인터뷰로 굳혀 docs/adr/ ADR로 영속화하는 아키텍처 고도 스킬. 사용자가 "/architect <주제>", "아키텍처 잡자", "ADR 쓰자", "설계 결정해야 해", "토큰/주문/킬스위치 설계"처럼 *기능 분해 이전의 시스템 설계*를 원할 때 사용한다. 결정의 근거와 버린 대안을 레포에 남겨 stateless 에이전트·검수자가 의도를 알게 하는 것이 핵심. 기능을 이슈로 쪼개는 issue-drafter(태스크 고도)와는 다른 고도다.
---

# architect

인자: 설계할 주제/서브시스템(예: `/architect 킬 스위치`). 파이프라인상 위치는 CLAUDE.md "개발 파이프라인".

## 범위

- 산출물은 대화가 아니라 `docs/adr/NNNN-*.md`다. ADR은 결정을 기록할 뿐 강제하지 않는다 — 강제는 CLAUDE.md·CODEOWNERS·`internal/enforcement` 테스트의 몫.
- 아키텍처 고도의 결정만 다룬다: 동시성/상태 모델과 경계, 무인 안전 계약(재시작 안전·멱등·백오프·킬 스위치), 패키지 경계·인터페이스(테스트 가능성), 외부 제약(Toss API는 추측 말고 OpenAPI 스펙 확인).
- "어떤 이슈로 쪼갤까"(태스크 고도)로 내려가지 않는다 — issue-drafter의 일.
- 기존 Accepted ADR은 다시 grilling하지 않고 그 위에서 출발한다. 결정을 바꾸려면 수정이 아니라 supersede/amend(`docs/adr/README.md` "규칙").

## 1. 인터뷰

- 한 번에 한 질문, 답을 기다리고, 질문마다 권장 답을 제시한다. 의존 관계가 있는 결정부터 해소한다.
- `internal/`·기존 ADR·CLAUDE.md·OpenAPI 스펙으로 답할 수 있는 것은 묻지 않고 직접 탐색한다.
- 사용자 전역 인터뷰 스킬은 있으면 써도 되지만 전제하지 않는다. 도메인 용어가 갈리면 `docs/` 아래 glossary로 정리한다.

## 2. ADR 작성

- 결정이 굳을 때마다 한 결정에 한 파일. 순번은 기존 최대+1, 파일명 `NNNN-kebab-slug.md`.
- `docs/adr/0000-template.md` 전체를 채운다 — frontmatter(따옴표 `id` == 파일 순번, `status: Proposed`(본문 `Status`와 동일), `date`, `deciders`, `domain`(`area:` 라벨 어휘), `protects`(sacred invariant id 또는 `[]`), `supersedes`/`superseded_by`(보완 ADR이면 `amends` — 템플릿에 없으니 직접 추가, 의미는 README "형식"), `verification`) + 본문 4칸.
- Alternatives considered를 비우지 않는다 — 이 칸이 비면 ADR이 아니다. 형식·규칙의 정본은 `docs/adr/README.md`.

## 3. 하드닝 + 사람 게이트

**고-stakes ADR(비가역·자금·인증·무인 안전·enforcement를 지배)은 Accepted 전에 적대 검토를 수렴까지 돌린다** — 한 라운드가 새 결함을 내지 않을 때까지. 같은 지적이 2라운드 연속이면 진동 → 정지·보고(`go-tdd-implementer` 운영 불변식 5와 동일). 근거: Accepted는 issue-drafter·dispatch-issue가 소비하는 신호라, 하드닝 전 버전이 Accepted가 되면 결함 든 결정이 구현된다(retro "축 2 — 프로세스 / 좋았던 것"; ADR-0006 "Alternatives considered"의 codex 지적 항목들).

검토자는 CLAUDE.md "모델/런타임 프로파일"의 `REVIEWER` 사다리와 같다:

- 세션에 codex 리뷰 스킬이 있으면 그 경로 — codex는 diff를 봐야 하므로 Proposed ADR을 피처 브랜치에 먼저 커밋한 뒤 `codex-pr-review`를 `--base origin/main`으로 수렴까지 실행한다. 브랜치의 Proposed 커밋은 main이 아니라 무해.
- 없으면 `claude-advisory` — 메인 에이전트가 읽기 전용 적대 서브에이전트를 `origin/main` 기준으로 돌린다(절차는 dispatch-issue §4). advisory (same-model) — ADR-0008 독립 검증자가 아니며 보고에 그렇게 표기한다.

하드닝한 ADR을 사용자에게 프리뷰한다(제목 + 4칸 요지). 승인하면 `Status: Accepted`(frontmatter·본문 동시); 고치라면 인터뷰를 더 하거나 다듬는다(아직 Proposed라 직접 수정 가능). **승인 없이 Accepted로 올리지 않는다. 최종 설계 책임은 사용자에게 있다.**

## 4. 커밋·배선·다음 단계

- 커밋 직전 `opensource-maintainer` 스킬. ADR에 자격증명·개인 경로를 적지 않는다. main에 직접 쓰지 않는다.
- **`protects:`가 비어있지 않으면 같은 PR에서 twin을 배선한다**: `sacredRequiredPaths`(`internal/enforcement/codeowners.go`) + `sacredADRRegistry`(`adrprotects.go`) + `.github/CODEOWNERS` `/docs/adr/NNNN-*.md @chnu-kim`. 누락은 `TestADRProtectsCompleteness_RealRepo`·`TestSacredADRRegistry_*`가 fail-closed로 잡는다 — 테스트 통과가 배선 증거다(게이트 승격 규칙은 CLAUDE.md).
- sacred 경로(CODEOWNERS·`internal/enforcement`)를 건드리는 ADR PR은 CLAUDE.md "PR 생성 흐름"을 따른다 — 여기서는 이 스킬을 돌리는 메인 에이전트가 오케스트레이터다: `PR_PATH`를 dispatch-issue §0(d)와 같은 증거로 정하고(지금은 부트스트랩 창이라 `human-bootstrap` — ADR-0015 p8), `human-bootstrap`이면 dispatch-issue §4의 사람 계정 PR 절차(local `gh pr create` / cloud `mcp__github__create_pull_request`)로 연다. §3의 advisory 리뷰는 WT 대신 `$REPO`의 피처 브랜치에서 돌린다.
- 안내: "이 ADR들을 토대로 기능을 이슈로 쪼개려면 `/issue-drafter <에픽>` — 각 이슈가 해당 ADR을 링크합니다."

## 5. 회고

ADR PR 머지 후 `retro`를 부른다 — 프로세스 축: 인터뷰가 수렴했나, 승인 뒤 리뷰가 인터뷰가 놓친 결함을 잡았나(잡았다면 다음 ADR에 무엇을 앞당기나).
