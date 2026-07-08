---
id: "0010"
status: Accepted
date: 2026-07-08
deciders: [chnu-kim]
domain: [loop-governance, ci]
protects: [enforcement-integrity]
supersedes: []
superseded_by: null
verification:
  - reviewer: advisor
    date: 2026-07-08
    verdict: converged (2 rounds)
  - reviewer: codex:review
    date: 2026-07-08
    verdict: fixed (unquoted leading-zero id, stale template/README)
  - reviewer: codex:adversarial-review
    date: 2026-07-08
    verdict: approve (converged after 4 rounds)
  - reviewer: chnu-kim
    date: 2026-07-08
    verdict: approved
---

# ADR-0010: ADR SSOT는 frontmatter(YAML) + 기존 마크다운 하이브리드로 관리하고, 인덱스는 생성물로 전환한다

- **Status**: Accepted
- **Date**: 2026-07-08
- **Deciders**: chnu-kim
- **관련 이슈/PR**: #39

## Context

- **ADR-0008/0009가 기계적 게이트**(구조화 verdict, `protects` 필드 검증)를 요구하는데, 현재 ADR은 순수 마크다운 프로즈라 기계적으로 안전하게 파싱할 수 없다.
- 순수 JSON 전환을 검토했으나, Context/Alternatives 같은 서사적 깊이("왜 버렸는지")가 손실된다 — 이는 grilling 산출물의 핵심이며, `docs/adr/README.md`가 "이 칸이 ADR의 핵심"이라고 명시한 부분이다.
- 이 결정을 만든 대화 세션 자체가 이미 memory 시스템에서 frontmatter(`---` YAML) + 본문 하이브리드 패턴을 쓰고 있고, `MEMORY.md`가 각 memory 파일의 **생성 가능한 인덱스** 역할을 한다 — 같은 저장소 안에서 검증된 선례다.
- **advisor 하드닝 이후 ADR-0009가 정정한 사실**: frontmatter의 `protects` 필드를 "CI가 읽고 차단"하는 방식은 순환이다 — CI 잡은 loop가 고칠 수 있는 파일이라 자기 검증 근거가 될 수 없다. 실제 차단은 ADR-0009가 정의한 CODEOWNERS + branch protection이 한다. 이 ADR의 역할은 **frontmatter를 그 CODEOWNERS 게이트가 소비할 수 있는 기계 판독 가능 형태로 만드는 것**이지, frontmatter 자체가 게이트를 수행하는 게 아니다.

## Decision

1. **모든 ADR 파일 상단에 YAML frontmatter를 추가한다**: `id`, `status`(Proposed/Accepted/Superseded), `date`, `deciders`, `domain`(이슈 라벨 `area:` 축과 어휘를 통일 — killswitch/order/auth/persistence/loop-governance 등), `protects`(이 ADR이 지키는/건드리면 안 되는 sacred invariant id 배열, 없으면 빈 배열), `supersedes`/`superseded_by`, `verification`(독립 검증자 이력: 누가/언제/verdict). **`id`는 반드시 따옴표로 감싼 문자열로 쓴다**(`id: "0007"`, `id: 0007` 금지) — codex 리뷰가 지적한 대로, 앞자리 0이 있는 unquoted 숫자 스칼라는 YAML 파서에 따라 8진수나 다른 숫자로 잘못 해석될 수 있어 네 자리 파일명·상호 참조와 어긋날 수 있다.
2. **본문(Context/Decision/Alternatives/Consequences 4칸)은 형식 변경 없이 그대로 마크다운 프로즈로 유지한다.**
3. **`docs/adr/README.md`가 손으로 유지하던 인덱스 역할을 frontmatter 파싱 기반 생성된 인덱스로 대체한다**(구현 이슈: 생성 스크립트/CI 잡). 이 생성 잡은 순수 읽기(파싱→렌더)이므로 `enforcement-integrity` 보호 대상이 아니다 — 결과물을 조작해도 실제 게이트(CODEOWNERS)에는 영향이 없다.
4. **frontmatter가 있는 파일 자체가 `enforcement-integrity`의 보호 경로다 — CI가 아니라 CODEOWNERS가 게이트를 수행한다(ADR-0009 point 4).** 이 ADR은 그 CODEOWNERS 규칙이 참조할 **정적 경로 목록**만 제공한다(brace expansion 없이 각각 명시): `docs/adr/0004-*.md`·`0007-*.md`(`live-execution-human-gate`), `docs/adr/0008-*.md`·`0009-*.md`·`0010-*.md`(`enforcement-integrity`). **`protects`가 비어있지 않은 ADR을 CODEOWNERS가 동적으로 찾아 보호하는 메커니즘은 없다** — CODEOWNERS는 파일 경로만 매칭하고 frontmatter 값을 읽지 못하기 때문이다(codex adversarial-review 3차 지적). 미래의 새 sacred 결정이 이 목록에서 빠지지 않는 이유는 `docs/adr/README.md`의 "수정하지 말고 대체한다" 컨벤션 때문이다 — 기존 sacred ADR을 바꾸려는 시도는 반드시 그 원본 파일의 `status`/`superseded_by`를 편집해야 하고, 그 파일이 이미 정적으로 보호돼 있어 편집이 걸린다(자세한 논증은 ADR-0009 point 4). **CI는 오직 구조적 정합성만 검증한다** — frontmatter 스키마 well-formed 여부, `id` 중복·재사용, `supersedes`/`superseded_by` 참조 무결성. `status`/`protects` 값 자체를 근거로 merge를 막거나 허용하는 판단은 CI가 하지 않는다(그 판단이 곧 순환 지점이었다).
5. **`0000-template.md`와 `README.md`를 이 frontmatter 스펙에 맞게 갱신한다.**

## Alternatives considered

- **순수 JSON** — 기각: 서사(Context/Alternatives)가 자유 텍스트일 때 grilling 깊이가 살고, JSON 스키마로 강제하면 작성자가 서사를 채우기보다 스키마 칸을 채우는 데 최적화된다 — 진짜 이유를 안 적고 형식만 맞추는 실패 모드를 유발한다.
- **마크다운은 그대로 두고 별도 파생 파일(예: `docs/adr/index.json`)을 손으로 병행 관리** — 기각: 손 관리 이중 소스는 드리프트가 필연적이다. 이 레포의 `MEMORY.md`가 이미 "인덱스는 파생물, 손으로 내용을 채우지 않는다"를 원칙으로 하는 이유와 동일하다.
- **frontmatter 변경 추적을 CI가 아니라 코드리뷰(사람/에이전트가 diff를 읽고 판단)에 맡기기** — 기각: ADR-0008의 원칙("프로즈 읽고 판단" 금지, 기계적 게이트만 인정)과 직접 모순된다.
- **`status`/`protects` 값을 CI가 읽고 merge 허용/차단을 직접 결정** — 기각(advisor 하드닝 지적, 최초 draft의 오류): CI는 loop가 편집·머지할 수 있는 파일이므로 자기 자신을 지키는 근거가 될 수 없다(순환). 실제 차단 권한은 loop가 못 건드리는 층(CODEOWNERS + branch protection, ADR-0009)에 있어야 한다.

## Consequences

- (좋음) ADR-0008/0009가 요구하는 기계 검증이 실제로 구현 가능해진다.
- (좋음) 인덱스 드리프트 위험이 제거된다.
- (좋음) 향후 "ADR들끼리 모순이 있는가" 같은 자동 점검을 `domain`/`protects` 필드 기반으로 확장할 수 있다.
- (비용) 기존 ADR-0001~0006에 frontmatter를 소급 추가했다(이 결정과 같은 커밋에 포함 — codex adversarial-review가 "규범을 선언하면서 기존 코퍼스를 위반 상태로 남기면 안 된다"고 지적해 후속 이슈에서 이 결정 범위로 앞당김). 본문 내용은 안 바뀌었다.
- (비용) frontmatter 스키마 자체의 버전 관리/진화 문제가 생긴다(필드 추가 시 하위 호환) — 지금은 스키마를 단순하게 유지하고, 필요해지면 별도 ADR로 확장한다.
- (후속) 생성 인덱스 스크립트/CI 잡 구현.
- (후속) frontmatter 구조적 정합성 검증 CI 잡 구현(스키마·`id`·참조 무결성만 — ADR-0008의 CI 강화 항목과 통합).
- (후속) `CODEOWNERS`에 이 ADR point 4의 경로 목록을 실제로 등록(ADR-0009와 연계).
