---
id: "0008"
status: Accepted
date: 2026-07-08
deciders: [chnu-kim]
domain: [loop-governance, ci, runtime]
protects: [enforcement-integrity]
supersedes: []
superseded_by: null
verification:
  - reviewer: advisor
    date: 2026-07-08
    verdict: converged (2 rounds)
  - reviewer: codex:review
    date: 2026-07-08
    verdict: fixed (fail-open consequence wording contradicted Decision point 5)
  - reviewer: codex:adversarial-review
    date: 2026-07-08
    verdict: approve (converged after 4 rounds)
  - reviewer: chnu-kim
    date: 2026-07-08
    verdict: approved
---

# ADR-0008: PR 머지 게이트는 기계적 구조화 verdict다 — self-cert 금지, `risk:critical`은 N-of-2

- **Status**: Accepted
- **Date**: 2026-07-08
- **Deciders**: chnu-kim
- **관련 이슈/PR**: (TBD)

## Context

loop engineering은 "인간이 검증하는 부분도 AI에게 맡긴다"를 요구한다. 이 결정을 가르는 힘:

- **이 레포의 실측 실패 사례(PR#22)**: 동일 계열 에이전트가 "완료됨/게이트 통과/CONFIRMED"를 자기보고했으나 세 번 연속 거짓이었다(memory: `verify-reviewer-claims-empirically`). self-certification(생산자=검증자)은 이미 이 레포에서 실패가 실증됐다.
- **기존 검증 채널**: `codex-pr-review` 스킬이 PR 생성 직후 `codex:review` + `codex:adversarial-review`를 병렬로 돌린다(글로벌 CLAUDE.md). 이는 독립적이지만, 지금까지는 그 판정문을 **사람이 읽고** 최종 머지를 결정하는 구조였다.
- **핵심 문제**: 검증자가 codex처럼 프로세스·모델이 독립적이어도, 그 판정문을 "읽고 최종 판단"하는 게 생산자와 같은 계열의 orchestrating 에이전트라면 self-cert 문제가 형태만 바꿔 재발한다 — 판정을 과신하거나 오독할 수 있다(정확히 PR#22의 실패 형태).
- **ADR-0007**이 "실거래 authorize" 하나만 사람 게이트로 남기기로 했으므로, 그 외 모든 PR 머지(구현·리뷰 포함)는 이 ADR이 정의하는 자율 검증 게이트를 통과해야 한다.
- **advisor 하드닝에서 드러난 gap**: (a) "독립 세션"이 same-model(Claude 포크가 Claude 산출물을 검증)이면 PR#22와 같은 맹점을 공유하는 상관 실패(correlated failure)가 형태만 바꿔 재발한다. (b) `risk:critical` 판정을 생산자(PR)가 자기선언하면, 생산자가 스스로 non-critical로 낮춰 N-of-2를 회피할 수 있다 — 분류는 게이트의 보안 경계 그 자체이지 구현 디테일이 아니다.

## Decision

1. **모든 PR 머지는 기계적 게이트를 통과해야만 가능하다.** "에이전트가 판정 프로즈를 읽고 자유 판단"은 게이트로 인정하지 않는다. 게이트는 (a) 검증자가 스키마로 강제된 구조화 verdict(approve/reject + 근거)를 산출하고, (b) 그 verdict가 CI required status check로 변환되어 GitHub이 기계적으로 merge를 막거나 허용하는 두 단계로 구성된다.
2. **단일-검증자 경로의 기본값은 항상 codex(cross-engine)로 고정한다.** same-model 세션(생산자와 같은 계열의 별도 fork)은 독립 검증자로 인정하지 않는다 — 프로세스 경계는 있어도 맹점이 같아 판단 다양성을 추가하지 못한다. 이미 글로벌 CLAUDE.md가 "PR 생성 직후 항상 `/codex-pr-review` 실행"을 요구하므로, 이 결정은 기존 관행을 강제 가능한 게이트로 승격하는 것뿐 — 새 비용이 없다.
3. **`risk:critical` 경로(주문/킬스위치/정합/인증)를 건드리는 PR은 N-of-2를 요구한다** — codex adversarial-review와 독립 Claude adversarial 세션(비-codex, cross-check 목적) 둘 다 approve해야 머지 가능. 그 외 경로는 point 2의 codex 단일 검증으로 충분하다.
4. **codex 가용성에 따른 폴백은 위험 등급별로 비대칭이다**:
   - **non-critical**: codex 호출 불가(쿼터 소진·API 장애 등) 시 독립 Claude adversarial 세션으로 대체한다. same-model이라 다양성은 줄지만, git으로 되돌릴 수 있고 자금이 움직이지 않는 경로라 loop 정지보다 낫다.
   - **`risk:critical`**: codex 호출 불가 시 **대체하지 않고 fail-closed로 대기(큐잉)한다.** Claude 세션 2개로 채우는 것은 point 2가 기각한 same-model 문제를 그대로 재도입할 뿐 실질적 diversity를 안 늘린다 — 비가역·자금 경로에서 그 타협은 정당화되지 않는다.
5. **`risk:critical` 분류는 생산자가 자기선언하지 않는다.** enforcement 계층이 PR의 diff를 **경로 기반 매핑**(예: `internal/order/**`, `internal/killswitch/**`, 정합·인증 관련 패키지를 건드리면 critical)으로 기계적으로 계산한다. 이 매핑 자체는 ADR-0009가 정의하는 `enforcement-integrity` 보호 대상에 포함되어, loop가 자기 PR을 봐주려고 매핑을 축소할 수 없다. **매핑에 없는 경로는 기본값이 non-critical이 아니라 critical(N-of-2)이다.** 새 패키지·아직 매핑에 안 올라간 경로(예: 향후 `internal/broker/**`)가 생겼을 때 잘못 fail-open되어 단일-검증 경로로 새는 것을 막는다 — "모르면 위험하다고 취급"이 이 봇의 기존 fail-closed 철학(ADR-0003/0004)과 일관된다.
6. **검증은 empirical해야 한다** — 실제 `go test -race ./...` 실행 결과, 실제 git diff, 재현 가능한 실패 케이스에 근거해야 하며 "이 코드는 맞을 것이다" 류의 추론만으로는 approve할 수 없다.
7. **CI를 이 결정에 맞춰 강화한다**: 최소 (a) lint 잡(golangci-lint 등) 추가, (b) 검증자 verdict를 받아 실제로 required check를 세팅/해제하는 잡 추가, (c) branch protection에서 이 required check + `risk:critical` 경로의 N-of-2를 강제한다. (c)는 레포 공유 설정 변경이므로 사람 확인 후 적용한다. **(b)의 verdict-해석 로직과 point 5의 risk-classification 매핑은 `enforcement-integrity`로 보호되는 경로에 둔다** — 일반 `Contents` 권한만으로 고칠 수 있는 위치에 두면 게이트가 스스로를 지키지 못한다(ADR-0009).
8. **검증자 호출과 verdict 기록은 `enforcement-integrity`로 보호되는 워크플로 안에서 스스로 일어나야 한다 — loop orchestrator가 검증자를 호출하고 그 결과를 API로 전달하는 중계자가 되어서는 안 된다.** loop가 "codex를 실행해서 결과를 알려주는" 역할을 하면, 그 결과를 있는 그대로 전달하는지 왜곡해서 전달하는지에 대해 다시 loop 자신을 신뢰해야 한다 — PR#22의 실패가 인프라 계층에서 재발한다. 검증 잡은 `.github/workflows/**` 안에서 트리거되고 자신의 required check 상태를 스스로 설정해야 하며, 이게 실제로 지켜지는 이유는 point 6(ADR-0009)의 App이 `Workflows` 권한을 갖지 않아 loop가 그 워크플로 자체를 고쳐 자신을 중계자로 끼워넣을 수 없기 때문이다.

## Alternatives considered

- **사람이 codex/advisor 판정문을 읽고 최종 머지 결정** — 기각: loop engineering 목표(검증까지 자율화)에 반한다. 또한 PR#22가 보여준 self-cert 위험이 "사람이 읽는 대신 다른 에이전트가 읽는" 형태로만 이동할 뿐 구조적으로 사라지지 않는다.
- **모든 PR에 N-of-2를 균일 적용** — 기각: 검증 비용이 커서 loop 속도가 죽는다. 이 레포는 아직 실거래가 없으므로(ADR-0007) 대부분의 코드가 비-critical이다 — 비용을 정당화할 위험이 없는 곳까지 이중화하면 loop engineering의 이점을 깎는다.
- **단일 검증자로 항상 충분(N-of-2 불필요)** — 기각: PR#22가 단일-검증자 실패를 이미 실측했다. `risk:critical` 경로는 비가역·자금 관련이라 이중화 비용이 정당하다.
- **검증자 verdict를 자유 텍스트로 남기고 사람이 상시 감사** — 기각: "상시 감사"는 결국 사람 개입점을 재도입하는 것과 같다. 구조화+기계 게이트만이 완전 자율과 양립한다.
- **same-model 별도 fork 세션을 기본 독립 검증자로 인정** — 기각(advisor 하드닝 지적): 프로세스·컨텍스트는 분리돼도 모델이 같으면 맹점이 같다. PR#22의 실패는 정확히 이 형태(같은 계열 에이전트의 자기보고)였다 — 형태만 "fork"로 바꿔도 근본 위험은 그대로다.
- **`risk:critical` 여부를 PR/이슈 라벨(생산자 선언)로 판정** — 기각(advisor 하드닝 지적): 게이트 자체를 우회하는 자유도를 생산자에게 주는 셈이다. 분류는 생산자 입력이 아니라 enforcement 계층이 diff에서 기계적으로 도출해야 게이트가 의미를 가진다.
- **codex 불가 시 `risk:critical`도 Claude 세션 2개로 대체** — 기각: 같은 모델을 두 번 돌리는 건 검증 다양성을 늘리지 않는다 — N-of-2의 취지(서로 다른 맹점을 가진 두 관점)를 형식적으로만 충족하고 실질적으로는 point 2가 기각한 same-model 문제를 재도입한다. 비가역·자금 경로에서는 대기(fail-closed) 비용이 이 타협보다 싸다.
- **매핑에 없는 경로는 non-critical(단일 검증)로 기본 처리** — 기각(advisor 하드닝 2차 지적): 새 위험 경로가 매핑에 반영되기 전까지 fail-open 창이 생긴다. 자금이 걸린 봇에서 "모르면 안전하다고 취급"은 이 레포 전체의 fail-closed 철학(ADR-0003/0004)과 정면으로 모순된다.
- **loop orchestrator가 검증자를 호출하고 결과를 API로 중계** — 기각(advisor 하드닝 2차 지적): 결과 전달의 충실성을 다시 loop 자신에게 의존하게 되어, PR#22와 같은 자기보고 실패가 인프라 계층으로 이동할 뿐 사라지지 않는다. 검증은 loop가 못 건드리는 워크플로 안에서 자기완결적으로 끝나야 한다.

## Consequences

- (좋음) 사람 없이도 merge 안전성이 구조적으로 보장된다 — self-cert 재발 경로가 기계 게이트로 봉쇄된다.
- (좋음) PR#22 교훈이 프로세스 상의 "주의사항"이 아니라 강제 가능한 CI 계약으로 승격된다.
- (좋음) codex 단일-검증 기본값이 기존 관행(글로벌 CLAUDE.md의 `/codex-pr-review`)을 그대로 승계해 새로운 조직적 마찰이 없다.
- (비용) 검증자 verdict를 구조화 스키마로 강제하는 새 워크플로/잡을 설계·구현해야 한다(구현 이슈).
- (비용) `risk:critical` 경로 기반 매핑을 실제로 설계·구현해야 한다(구현 이슈).
- (비용) `risk:critical` 경로에서 codex 가용성에 의존하게 되어, codex 장애가 길어지면 critical PR 머지가 지연된다 — 의도된 fail-closed 비용이다.
- (비용) 매핑이 초기엔 sparse할 수밖에 없어, 매핑에 없는 새 경로는 전부 N-of-2로 처리된다 — 초기엔 실제보다 보수적으로(비용을 더 내는 쪽으로) 치우친다. 매핑이 성숙할수록 정확도가 올라간다.
- (비용) 검증자 호출이 `.github/workflows/**` 안에서 자기완결돼야 하므로, "loop가 임의 스크립트로 codex를 호출"하는 유연한 구현은 불가능하다 — 워크플로 YAML(또는 그 안에서만 실행되는 action)로 못박아야 한다(구현 제약).
- (후속) branch protection 실제 설정 변경은 공유 인프라이므로 사람 확인이 필요하다.
- (후속) ADR-0009가 이 게이트를 ADR `Accepted` 승인 절차에도 그대로 적용한다.
- (후속) risk-classification 매핑과 verdict-해석 로직의 구체적 보호 경로(CODEOWNERS 설정)는 ADR-0009/0010과 함께 구현한다.
