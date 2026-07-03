---
name: architect
description: 서브시스템·횡단 관심사의 아키텍처 결정을 relentless grilling으로 도출하고 ADR(docs/adr/) + glossary로 영속화하는 스킬. 사용자가 "아키텍처 잡자", "설계 결정해야 해", "이거 어떻게 설계하지", "/architect <주제>", "ADR 쓰자", "토큰/주문/킬스위치 설계" 같이 *기능 분해 이전의 시스템 설계*를 하고 싶다는 신호를 줄 때 사용한다. 결정의 근거와 버린 대안을 ADR에 남겨 stateless 에이전트·검수자가 의도를 알 수 있게 하는 것이 핵심. 기능을 이슈로 쪼개는 일(issue-drafter)과는 다른 고도다.
---

# architect

**아키텍처 고도**의 스킬이다. 기능을 이슈로 분해하기 *전에*, 그 기능을 지배할 설계 결정을 grilling으로 끝까지 캐묻고 **ADR로 영속화**한다.

산출물은 대화가 아니라 **레포 안의 파일**(`docs/adr/NNNN-*.md`, 필요 시 glossary)이다. 결정의 근거·버린 대안이 레포에 남아야 검수자와 stateless 서브에이전트가 의도를 안다.

인자: 설계할 주제/서브시스템 (예: `/architect 토큰 매니저`, `/architect 킬 스위치`).

> 이 스킬은 결정을 **기록**할 뿐 강제하지 않는다. 강제는 CLAUDE.md(원칙)·훅·코드리뷰의 몫이다.

---

## 0. 환경 + 기존 결정 파악

```bash
REPO=$(git rev-parse --show-toplevel)
ls "$REPO"/docs/adr/ 2>/dev/null      # 다음 순번 + 기존 결정 확인 (중복·모순 방지)
```

- `docs/adr/`가 없으면 `docs/adr/README.md`·`0000-template.md`가 있는지 확인하고, 없으면 먼저 만든다(형식은 README 참조).
- 기존 ADR을 훑어 **이미 내려진 결정을 다시 grilling하지 않는다.** 관련 ADR이 있으면 그 위에서 출발한다.

---

## 1. Grilling (아키텍처 고도 인터뷰)

`/grilling` 프로토콜을 그대로 따른다:

- **한 번에 하나씩** 질문하고 답을 기다린다. 여러 개를 한꺼번에 묻지 않는다.
- 설계 트리의 각 가지를 내려가며, 결정 간 **의존성을 하나씩 해소**한다.
- 질문마다 **권장 답을 제시**한다.
- **코드베이스로 답할 수 있는 질문은 묻지 말고 직접 탐색**한다(`internal/` 구조, 기존 ADR, CLAUDE.md 제약, OpenAPI 스펙).

아키텍처 고도에 머문다. "이걸 어떤 이슈로 쪼갤까"(태스크 고도, = issue-drafter의 일)로 내려가지 않는다. 다루는 것은 다음 같은 결정이다:

- 동시성/상태 모델(예: single-flight vs mutex vs actor), 경계와 책임 배치
- 무인 안전 계약(재시작 안전, 멱등성, 백오프 정책, 킬 스위치)
- 패키지 경계·의존 방향, 인터페이스 형태(테스트 가능성)
- 외부 제약(Toss API: 토큰 1개 제약, refresh 없음, 레이트 리밋 등 — 추측 말고 OpenAPI 스펙 확인)

도메인 용어가 갈리면 glossary/ubiquitous language를 함께 정리한다(`domain-modeling` 스킬이 있으면 활용, 없으면 `docs/`에 직접 — 이 스킬은 특정 글로벌 스킬 존재를 전제하지 않는다).

---

## 2. 결정을 ADR로 포착

grilling에서 **결정이 굳을 때마다** ADR을 쓴다(한 결정당 한 파일). `docs/adr/0000-template.md`의 4칸을 채운다:

- **Context** — 어떤 제약/힘이 결정을 강제했나(관련 CLAUDE.md 불변식·API 제약 명시).
- **Decision** — 무엇을 골랐나.
- **Alternatives considered** — grilling에서 검토하고 **버린 대안과 그 이유**. 이 칸이 ADR의 핵심이다 — 비우지 않는다.
- **Consequences** — 이 선택이 약속하게 만드는 것, 유발하는 후속 결정.

파일명 `NNNN-kebab-slug.md`, 순번은 기존 최대+1. 작성 시 `Status: Proposed`.

---

## 3. 사람 게이트: ADR 승인

**고-stakes ADR은 프리뷰 전에 하드닝한다.** 비가역·자금·인증·무인 안전을 지배하는 ADR은, 사용자에게 승인받기 *전에* adversarial 검토를 **수렴(approve)까지 반복**한다 — 한 번이 아니다(실측: 깊은 결함은 라운드를 거듭할수록 드러난다 — 스펙 사실 → 내부 정합성 → 물리 내구성). 메커니즘: `advisor`(PR 불필요) 또는 `codex adversarial-review`(Proposed ADR을 브랜치에 커밋해야 돎 — `codex-pr-review` 스킬/companion). **이유**: `Accepted`는 issue-drafter·dispatch-issue가 링크·구현하는 신호라, 하드닝 전 버전이 Accepted가 되면 결함 든 결정이 소비될 수 있다. 하드닝은 라운드를 늘리는 게 아니라 **PR 뒤에서 앞으로 앞당기는** 것 — 사용자는 이미 굳은 결정을 승인한다. (근거: 회고가 ADR-0002·0006에서 이 패턴을 잡았다.)

작성(고-stakes면 하드닝)한 ADR을 사용자에게 **프리뷰**한다(제목 + 4칸 요지). 사용자가:

- 승인하면 → `Status: Accepted`로 바꾼다.
- 고치라면 → grilling을 더 하거나 ADR을 다듬는다(대체가 아니라 아직 Proposed이므로 직접 수정 가능).

승인 없이 Accepted로 올리지 않는다. 최종 설계 책임은 사용자에게 있다.

---

## 4. 커밋 전 점검 + 다음 단계 안내

- 커밋하려면 **`opensource-maintainer` 스킬로 먼저 점검**한다(시크릿·개인정보·환경 의존 금지). 이 레포는 언제든 public 전환 가능해야 한다 — ADR에 자격증명·개인 경로를 적지 않는다.
- 커밋은 프로젝트 워크플로(브랜치/worktree/PR)를 따른다. main에 직접 쓰지 않는다.
- 안내: "이 ADR들을 토대로 기능을 이슈로 쪼개려면 `/issue-drafter <에픽>` — 각 이슈가 해당 ADR을 링크합니다."

## 5. 회고 (ADR 머지 후)

ADR PR이 머지되면 이 설계 작업을 `retro` 스킬로 회고한다 — **결과물보다 프로세스**를 본다: grilling이 수렴했나, 승인·머지 *뒤에* 리뷰(codex/advisor)가 grilling이 놓친 결함을 잡았나, 그렇다면 다음 ADR엔 무엇을 앞당겨야 하나. (예: 내구성·비가역 계약 ADR은 `Accepted`로 올리기 *전에* adversarial 패스를 돌리는 게 나은지.) 학습이 있으면 retro §4대로 표면에 남기고(프로세스 학습은 대개 memory, 재발·고비용이면 CLAUDE.md 규칙), 없으면 skip.
