# CLAUDE.md

이 레포에서 Claude Code가 따르는 규칙. why는 `docs/adr/`, 재발 사례는 `docs/retros/`.

## 현황

Go로 작성하는 Toss Open API 24시간 무인 자동매매 봇. **"무인 실행 제약"이 모든 설계 결정의 1순위 기준이다.** 매매 전략만 미정(`internal/strategy`는 doc.go뿐)이고 무인 골격은 조립돼 휴면 중이다:

- 런타임 `internal/{toss,order,reconciler,killswitch,store(스키마 V5),audit,runtime,config,market,account}` / 거버넌스 `internal/{enforcement,gate}`·`configs/gate/` / 바이너리 `cmd/{bot,verdict-gate,presence-check}`. 첫 부팅·불명확 종료 후 부팅은 halt가 기본(ADR-0007).
- ADR 지도(전부 Accepted): 런타임 0001–0006·0012–0014 / 거버넌스 0007–0011·0015–0017. Go 버전·toolchain은 `go.mod`(govulncheck 바닥).

## 패키지 구조

[golang-standards/project-layout](https://github.com/golang-standards/project-layout)을 따른다.

- `cmd/<binary>/main.go`는 얇게: 설정 로드 → wiring → 실행. 비즈니스 로직 금지.
- `internal/`이 본체. 도메인 경계로 패키지를 자르고(현황의 목록) 순환 참조를 만들지 않는다.
- `pkg/`는 두지 않는다(외부 재사용이 확실한 코드가 생기기 전까지). `configs/`는 템플릿·기본값.
- 패키지명 = 디렉터리명, 단수형 도메인명. `util`/`common` 같은 잡탕 패키지 금지. 루트는 깨끗하게.

## 작업 방식

모든 작업(기능·버그·리팩터)에 TDD: 실패 테스트(Red) → 최소 구현(Green) → 정리(Refactor). 단순 typo·설정 변경은 예외. 기능 단위 브랜치에서 작업하고 PR로 머지한다.

이 레포는 private이지만 **언제든 public 전환 가능한 OSS 품질**을 유지한다(시크릿·개인정보·환경 의존 내용 금지). **커밋 직전에는 반드시 `opensource-maintainer` 스킬을 실행**해 변경분을 점검한다(스캐너 모드는 "모델/런타임 프로파일" 표).

### 게이트 승격 규칙

**어떤 변경이 X를 게이트·강제 장치·증거 생성기로 승격하면, 같은 PR에서 X 자신을 sacred로 등재한다** — `.github/CODEOWNERS` 규칙 + `internal/enforcement`의 `sacredRequiredPaths` + 누락 시 fail-closed 테스트. 판별 질문: **"이걸 고치면 게이트 판정이나 그 판정의 증거가 바뀌나?"** → 예면 sacred다.

- 디렉터리 규칙은 이후 동종 파일을 자동 보호하지만 **`sacredRequiredPaths`에는 개별 파일을 나열**한다 — CODEOWNERS는 last-match-wins라 후행 narrower 항목이 한 파일만 벗겨내는 것을 디렉터리 규칙으로는 못 잡는다(`codeowners.go` 주석).
- 기계 검사: `instructionsurface_test`가 `.claude/` 트리 전수·`docs/runbooks/` 전수를, `adrprotects_test`가 ADR `protects:` ↔ 등재를, `riskclasspairing_test`가 sacred ⇒ critical을 검사한다. **`.claude/` 아래의 새 파일(훅·`settings.json`·새 에이전트·새 스킬·보조 .md)은 등재 전까지 `go test`가 Red다.** 보조 자료는 `.claude/`가 아니라 `docs/`에 둔다.
- 근거·재발 사례: `docs/retros/2026-07-19-loop-engineering-campaign.md` "축 1 — 결과물 품질 / 부족했던 것", ADR-0011 point 4(b)("main에 있음 ≠ 보호됨").

### 개발 파이프라인

| 고도 | 스킬 | 산출물 | 사람 게이트 |
|---|---|---|---|
| 아키텍처 | `/architect <주제>` | `docs/adr/` ADR(형식·규칙은 `docs/adr/README.md`) | 사용자 승인 시 Accepted |
| 태스크 | `/issue-drafter <에픽>` | 이슈 묶음(각 이슈에 지배 ADR 링크; 근거 ADR 없는 포크면 정지 → `/architect` 먼저) | 프리뷰 승인 |
| 구현 | `/dispatch-issue N` | worktree 격리 + `go-tdd-implementer` → PR | PR 검수·머지 → `/dispatch-issue --cleanup N` |
| 회고 | `/retro` | durable 표면(CLAUDE.md·ADR·스킬) 갱신 — 보수적 트리거, trivial은 self-skip | 규칙·ADR 쓰기 승인 |

**머지는 사람만 한다**(Phase A — ADR-0011 point 5; 실거래 authorize는 항상 사람 — ADR-0007).

### PR 생성 흐름

불변식(ADR-0011):

1. App private key·installation token은 GitHub Actions `loop-pr` environment 안에만 존재한다. 로컬·클라우드·서브에이전트 어느 세션에도 materialize하지 않는다(point 1).
2. 커밋·push identity는 `chnu-kim`, loop PR 작성자는 `mechanu[bot]`(point 2): `repository_dispatch` `create-loop-pr` → main 고정 `.github/workflows/pr-creation.yml`이 PR 생성. 사람 검토자 = PR 작성자면 self-approval 교착(실측 PR #42/#44 — ADR-0011 Context).
3. sacred 경로를 건드리는 PR도 예외 없이 같은 경로를 거친다(point 6).
4. 독립 리뷰는 PR이 실제로 열린 것을 확인한 뒤, base는 항상 `origin/main`(worktree의 로컬 main은 stale — retro "축 2 — 프로세스 / 부족했던 것 / 마찰"). 리뷰어 선택(`REVIEWER`)은 프로파일.
5. `PR_PATH`(`dispatch` | `human-bootstrap`)는 오케스트레이터가 정하고 워커는 고르지 않는다(규칙은 프로파일).

현재는 부트스트랩 창이다 — `pr-creation.yml`은 main에 있으나 `loop-pr` environment 미프로비저닝·narrowing 미완으로 App 경로가 비활성이고, `enforce_admins=false`에서 사람 계정 PR + 사람 `--admin` 머지로 착지한다(ADR-0015 point 8). 이 창은 `enforce_admins` 재활성화(ADR-0016 point 2, step 0.5)로 닫히며, 그 뒤 App key 프로비저닝(precondition ③) 완료까지 sacred 머지 경로가 하나도 없는 창 **W0**가 열린다(ADR-0016 point 5) — W0 안에서는 이 파일을 포함한 sacred 파일을 고칠 수 없으므로(예외는 ADR-0016 point 10의 긴급 재개방 절차뿐) sacred 편집은 W0 진입 전에 끝낸다. 활성화 *순서*의 정본은 ADR-0016 Decision B(point 5·6 — step 0/0.5 포함), 활성화 *상태*의 정본은 `docs/runbooks/phase-b-entry.md`다 — 그 런북은 #83 §D 정정 전까지 step 0.5·W0를 반영하지 않는다. 프로비저닝은 `docs/runbooks/loop-pr-environment-provisioning.md`. 실행 payload·폴링 spec은 `.claude/agents/go-tdd-implementer.md` "완료 절차"가 정본.

## 모델/런타임 프로파일

- **대상 모델**: 이 하네스는 Claude Fable 5.1(`claude-fable-5-1`) 이상을 전제로 2026-11에 재구성했다. 하네스 파일에 모델 ID를 박지 않는다 — 서브에이전트는 `model: inherit`, effort만 역할별로 고정한다(구현 워커 `high`, `risk:high` 이슈는 호출 시 `xhigh`). 상시 로드 파일에는 규칙·명령·검사와 why 포인터(ADR·`docs/retros/`)만 적고 레시피·반복 경고·사고 서사를 적지 않는다. 단, **게이트·돈·자격증명에 관한 불변식은 모델 강도와 무관하게 전문을 유지한다** — 그것은 약한 모델 보정이 아니라 적대 loop 보정이다(ADR-0009 point 3).
- **환경 판정**: `HARNESS_ENV` = 환경변수 `CLAUDE_CODE_REMOTE`가 `true`면 `cloud`, 아니면 `local`. 스킬·에이전트는 이 변수와 아래 표만 참조하고 환경을 재서술하지 않는다.

| | local(사용자 머신) | cloud(claude.ai/code) |
|---|---|---|
| GitHub 읽기 | `gh` | `gh api`(REST) — `gh pr`/`gh issue`/`--json`은 GraphQL이라 403. `gh auth status`가 실패를 보고해도 `gh api`는 프록시로 동작한다 — 접근 판정은 `gh api repos/{SLUG}` 성공 여부로만 |
| GitHub 쓰기(이슈·코멘트·사람 PR) | `gh`(접근 가능 계정으로 같은 셸에서 `gh auth switch --user … && gh …`) | `mcp__github__*` |
| `repository_dispatch` | `gh api --method POST` | `gh api --method POST`(미실측 — 403이면 '미생성+사유' 보고, 임의 대체 금지) |
| push·커밋 서명 | 세션의 ssh-agent(오케스트레이터가 소켓을 워커에 전달) | HTTPS 프록시, 서명은 플랫폼 설정 그대로 |
| 자격증명 **신규** 설정·토큰 교체·push/서명 identity 전환·remote 변경 | 금지 — 거부되면 정지·보고(ADR-0011 p2·5②). 이미 로그인된 `gh` 계정 간 전환은 위 'GitHub 쓰기' 행만 | 동일(계정 전환 없음) |
| 사용자 전역 스킬(codex 리뷰 등) | 있으면 사용 | 없음 — 레포 `.claude/`만 로드 |
| `scan.sh` 모드 | 기본(커밋 이메일 검사 포함) | `--all --content-only`(커밋 identity가 플랫폼 관리) |
| 라벨·이슈 조회 | `gh label list`, `gh issue view` | `gh api "repos/{SLUG}/labels?per_page=100"`, `mcp__github__issue_read` |
| 워커 격리 | sibling worktree(`<repo>-worktrees/<BR>`) | 동일(부모 디렉터리 쓰기 가능 실측). Agent `isolation` 옵션은 경로·baseRef·정리 의미가 실측될 때까지 쓰지 않는다 |

- **워커·위임**: 이슈 1개 = `go-tdd-implementer` 1개 = worktree 1개. 페르소나는 에이전트 파일에만 있고 프롬프트에는 변수(WT·BR·SLUG·N·지배 ADR·`HARNESS_ENV`·`GH_WRITE`·`PR_PATH`·`REVIEWER`·FINDINGS)만 넘긴다. 워커는 PR을 직접 만들지 않고(어떤 경로로도), 서브에이전트를 띄우지 못하므로 Claude 측 리뷰는 오케스트레이터가 돌린다. 서브에이전트는 깨울 채널이 없다 — 블로킹 리뷰를 백그라운드로 보내고 턴을 끝내지 않는다.
- **PR 경로(`PR_PATH`)**: 오케스트레이터가 presence-check c-1/c-2의 단순화(동일 레포·main 대상 PR만 센다)로 정한다 — `pr-creation.yml`이 origin/main에 있고 `mechanu[bot]` 작성 PR이 1건 이상 존재하면 `dispatch`, 아니면 `human-bootstrap`(ADR-0015 p8; 오케스트레이터가 사람 계정 PR을 만들고 본문에 명시). 워커는 고르지 않는다. 현재는 Phase A(사람 승인·머지)이며 활성화 상태의 정본은 `docs/runbooks/phase-b-entry.md`다.
- **독립 검증(`REVIEWER`, ADR-0008)**: ① `codex-pr-review` 스킬이 세션에 있으면 워커가 `--wait --base origin/main`으로 실행한다(cross-engine, 유일한 독립 검증자; `human-bootstrap`이면 PR이 없으므로 오케스트레이터가 PR 생성 뒤 같은 인자로 실행) ② 없으면 `risk:low/high`는 `claude-advisory` — 오케스트레이터가 읽기 전용 적대 서브에이전트로 `origin/main` 기준 리뷰; same-model이라 ADR-0008 독립 검증이 아니며 `advisory (same-model)`로 표기한다 ③ `risk:critical`은 대체하지 않고 `none-report`(사용자에게 '독립 리뷰 미수행' 보고, fail-closed). base는 어느 경우든 `origin/main`(codex 줄·advisory diff 줄은 `internal/enforcement`의 `TestHarnessReviewBaseIsOriginMain`이 검사). Phase B 가동 후 이 역할은 verdict-gate가 흡수한다.
- **Workflow(ultracode)**: 하네스 절차의 일부가 아니다 — 리뷰·게이트·PR 생성·sacred 편집에 쓰지 않고, 다중 이슈 병렬 디스패치도 공유 sacred 접점(CODEOWNERS·sacredRequiredPaths·`cmd/bot/main.go`·go.mod) 충돌 규칙이 fan-out에서 실증되기 전까지 쓰지 않는다.
- App private key·installation token은 어느 환경의 어느 세션에도 존재하지 않는다(ADR-0011 p1).

## 이슈 라벨

5축 라벨을 빠짐없이 붙인다. 어휘의 정본은 라이브 라벨(local `gh label list` / cloud `gh api "repos/{SLUG}/labels?per_page=100"`).

- `type:` feature/bug/refactor/test/docs/chore/spike
- `area:` auth/market/order/account/strategy/runtime/config/infra/audit/store — 복수 가능, 에이전트에 줄 컨텍스트를 결정
- `agent:` `ready`(명세 완결) / `blocked`(선행 대기) / `needs-decision`(사람 결정 필요). **`agent:ready`만 자율 실행한다.** 선행 이슈가 머지되면 `blocked`→`ready`
- `risk:` `critical`(실주문·자금이동·비가역 → **사람 리뷰 필수**) / `high`(시크릿·인증·무인 안전장치) / `low`(읽기·문서·테스트)
- `priority:` p0(지금/블로킹)/p1/p2

## 명령어

```bash
go build ./...
go run ./cmd/bot              # TOSS_CLIENT_ID/SECRET/ACCOUNT_SEQ 필수(configs/env.example); 첫 부팅은 halt
go test ./...
go test ./internal/config/ -run TestName -v   # 단일 테스트
go test -race ./...           # 동시성 코드는 반드시 -race로 검증
go vet ./...
gofmt -l -w .
# CI와 동일한 추가 게이트
go install golang.org/x/vuln/cmd/govulncheck@v1.6.0 && "$(go env GOPATH)/bin/govulncheck" ./...
bash .claude/skills/opensource-maintainer/scripts/scan_test.sh
bash .claude/skills/opensource-maintainer/scripts/scan.sh --all --content-only
go run ./cmd/presence-check   # enforcement 계층 생존 확인(env는 configs/env.example)
```

## Toss Open API

문서: https://developers.tossinvest.com/docs · 기계 판독: https://developers.tossinvest.com/llms.txt · OpenAPI 스펙(엔드포인트·스키마의 단일 진실): https://openapi.tossinvest.com/openapi-docs/latest/openapi.json. 새 엔드포인트나 파라미터가 필요하면 **추측하지 말고 스펙을 먼저 확인**한다.

- Base URL `https://openapi.tossinvest.com`. 인증은 OAuth 2.0 Client Credentials 전용: `POST /oauth2/token`(form; `grant_type=client_credentials`, `client_id`, `client_secret`), 모든 호출에 `Authorization: Bearer {access_token}`.
- 토큰 수명 86400초, refresh token 없음. **클라이언트당 유효 토큰은 1개뿐 — 재발급하면 기존 토큰이 무효화된다 → 토큰은 한 곳에서 발급·캐시하고 만료 전 갱신한다**(ADR-0001 single-flight 매니저, `internal/toss`).
- 계좌 헤더: 계좌 관련 호출은 `X-Tossinvest-Account: {accountSeq}`(`GET /api/v1/accounts`에서 획득).
- 주문은 `POST /api/v1/orders`, 사후 진실 키는 `GET /api/v1/orders/{orderId}`(ADR-0002/0003). 나머지 엔드포인트는 `internal/{account,market,order}` + 스펙. Market data는 REST — WebSocket 제공 여부 미확인, 폴링 전제.

## 무인 실행 제약 (설계 1순위)

24시간 방치 운영이므로 모든 코드의 기본 전제다:
- **죽지 않는다.** panic은 주문 루프를 멈춘다. goroutine마다 recover 경계를 두고(`internal/runtime` Supervisor), 단일 API 실패가 전체를 내리지 않게 한다.
- **재시작 안전성.** 미체결 주문/포지션은 기동 시 API로 재조회해 재구성한다(로컬 상태를 신뢰하지 말 것). 같은 주문을 중복 제출하지 않는다(멱등성·주문 dedup — ADR-0002/0003/0014).
- **시장 시간 인지.** 항상 도는 루프라도 장 마감/휴장에는 주문하지 않는다. `market-calendar`로 거래일·장중 여부를 판단한다.
- **레이트 리밋/백오프.** 한도는 스펙에서 확인. 429/5xx는 지수 백오프로 재시도하되 **주문 제출은 자동 재시도 금지**(중복 체결 위험) — 조회는 재시도해도 주문은 상태 재확인 후에만(ADR-0001/0003).
- **관측성.** 로그가 유일한 사후 진단 수단이다. 구조화 로깅 + 모든 주문/체결/에러를 fsync 감사 sink에 영속 기록(ADR-0006). 치명적 상황의 외부 알림은 미정.
- **킬 스위치.** 비정상(연속 실패, 예상외 손실, 토큰 갱신 불가) 시 신규 주문을 멈춘다. fail-closed이며 전역 halt는 사람만 해제한다(ADR-0004/0012/0013/0014).
- **시크릿.** `client_id`/`client_secret`은 환경변수/시크릿 매니저로 주입(`config.Secret`으로 리댁션). 커밋 금지.

## 테스트 방침

- Toss API를 직접 때리는 테스트 금지(실주문·레이트리밋 위험). HTTP 클라이언트를 인터페이스로 추상화하고 `httptest`나 mock으로 검증한다.
- 토큰 만료/갱신, 멱등 주문, 백오프, 장 시간 판정, 킬 스위치 등 무인 운영 로직은 반드시 단위 테스트로 커버한다.
- `go test -race ./...`가 CI 게이트다(`store`/`audit`는 실제 SQLite·fsync라 느린 것이 정상).
