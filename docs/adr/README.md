# Architecture Decision Records (ADR)

이 디렉터리는 이 프로젝트의 **중요한 설계 결정**을 한 결정당 한 파일로 기록한다.

## 왜 쓰나

이 레포의 개발 파이프라인은 "에이전트가 구현, 사람은 검수만" 모델이다. 사람 검수자는 맥락을 누적하지만 **디스패치된 서브에이전트와 미래에 레포를 읽을 에이전트는 stateless**다. 결정의 *근거*가 휘발성 대화에만 있으면, 검수자는 검수할 기준이 없고 에이전트는 명시되지 않은 결정을 모른 채 위반한다. ADR은 그 근거를 레포 안에 영속화한다.

- **CLAUDE.md = 규칙**(불변식, 항상 켜진 원칙).
- **ADR = 그 규칙의 *why* + 검토하고 버린 대안.**

규칙만으로 막히면 ADR을 본다. 둘은 경쟁이 아니라 다른 고도다.

## 형식

각 ADR은 4칸으로 쓴다(`0000-template.md` 참조):

1. **Context** — 어떤 문제/제약/힘(forces)이 결정을 강제했나.
2. **Decision** — 무엇을 골랐나.
3. **Alternatives considered** — 검토한 대안과 **버린 이유**(이 칸이 ADR의 핵심).
4. **Consequences** — 이 선택이 무엇을 약속하게 만드나(좋은 것·나쁜 것·후속 결정 유발).

## 규칙

- 파일명: `NNNN-kebab-slug.md` (4자리 순번, 예: `0001-single-flight-token-refresh.md`).
- 번호는 **순차 증가**, 재사용 금지.
- `Status`: `Proposed`(검토 중) → `Accepted`(사용자 승인) → `Superseded by ADR-NNNN`(대체됨).
- **수정하지 말고 대체한다.** 결정이 바뀌면 기존 ADR을 `Superseded`로 표시하고 새 ADR을 쓴다. 역사를 지우지 않는다.
- ADR은 결정을 *기록*할 뿐 강제하지 않는다. 강제는 CLAUDE.md(원칙)·훅(enforcement)·코드 리뷰가 한다.

## 파이프라인과의 연결

- 아키텍처 결정은 `/architect`(grilling → ADR) 단계에서 만든다.
- `/issue-drafter`는 이슈를 분해할 때 각 이슈에 **지배 ADR을 링크**하고, 근거 ADR이 없는 아키텍처 포크를 만나면 멈춘다("`/architect` 먼저").
- `/dispatch-issue`는 이슈가 링크한 ADR을 구현 서브에이전트에게 전달해, 에이전트가 결정을 모른 채 위반하지 않게 한다.
