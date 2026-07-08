# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 프로젝트 현황

Go로 작성하는 Toss Open API 자동매매 봇. 매매 전략·대상 시장은 미정이나, 아래 두 가지는 확정이다:

1. **24시간 무인(unattended) 실행** — "무인 실행 제약"이 모든 설계 결정의 1순위 기준이다.
2. **Go 표준 패키지 레이아웃** — 아래 "패키지 구조"를 따른다.

최소 Go 버전은 `go.mod`를 따른다.

## 패키지 구조

[golang-standards/project-layout](https://github.com/golang-standards/project-layout) 관례를 따른다. 핵심 규칙:

- **`cmd/<binary>/main.go`** — 실행 진입점. `main`은 얇게: 설정 로드 → 의존성 조립(wiring) → 실행 시작만 한다. 비즈니스 로직 금지.
- **`internal/`** — 애플리케이션 본체. 외부에서 import 불가능하므로 봇 로직 대부분이 여기 들어간다. 도메인별 패키지로 분해한다(예: `internal/toss` API 클라이언트, `internal/order`, `internal/strategy`, `internal/market`, `internal/account`). 패키지는 도메인 경계로 자르고 순환 참조를 만들지 않는다.
- **`pkg/`** — 외부에서 재사용해도 안전한 라이브러리 코드에만 사용. 확실한 이유가 없으면 `internal/`을 기본으로 하고 `pkg/`는 비워둔다.
- **`configs/`** — 설정 템플릿/기본값.
- 루트는 깨끗하게 유지. 패키지 이름은 디렉토리명과 일치시키고, 단수형 도메인명을 쓴다(`util`/`common` 같은 잡탕 패키지 금지).

## 작업 방식

모든 작업(기능·버그·리팩터)에 TDD를 적용한다: 실패하는 테스트(Red) → 통과하는 최소 구현(Green) → 정리(Refactor). 단순 typo·설정 변경처럼 테스트가 불필요한 경우는 예외. 기능 단위 브랜치에서 작업하고 PR로 머지한다.

이 레포는 private이지만 **언제든 public 전환 가능한 OSS 품질**을 유지한다(시크릿·개인정보·환경 의존 내용 금지). **커밋 직전에는 반드시 `opensource-maintainer` 스킬을 실행**해 변경분을 점검한다.

### 개발 파이프라인 (고도별 분리)

기능을 만들 때 세 고도를 분리한다. 사람은 각 게이트에서 검수하고 머지는 사람만 한다.

1. **아키텍처 고도 — `/architect <주제>`**: 기능을 이슈로 쪼개기 *전에*, 그 기능을 지배할 설계 결정(동시성 모델·무인 안전 계약·패키지 경계 등)을 grilling으로 도출해 **`docs/adr/`에 ADR로 영속화**한다. 결정의 근거·버린 대안이 레포에 남아야 stateless 에이전트·검수자가 의도를 안다. CLAUDE.md = 규칙, ADR = 그 규칙의 why. (ADR 형식은 `docs/adr/README.md`.)
2. **태스크 고도 — `/issue-drafter <에픽>`**: 에픽을 병렬 가능한 이슈 묶음으로 분해한다. 각 이슈는 지배 ADR을 링크하고, 근거 ADR이 없는 아키텍처 포크를 만나면 멈추고 `/architect`를 먼저 돌린다. (게이트: 사용자 이슈 검수.)
3. **구현 고도 — `/dispatch-issue N`**: 이슈 하나를 worktree 격리 + 서브에이전트 위임으로 TDD→PR까지 처리한다. 이슈가 링크한 ADR을 서브에이전트에 전달해 결정 위반을 막는다. (게이트: 사용자 PR 검수·머지 → `/dispatch-issue --cleanup N`.)
4. **회고 고도 — `/retro`**: 비자명한 작업이 *완료된 뒤*, 결과물 품질·프로세스를 2축으로 평가하고 재사용 가능한 학습을 **실행 가능한 형태로** durable 표면(memory·ADR·이 CLAUDE.md 규칙)에 남긴다. 파이프라인 완료 게이트(`/dispatch-issue --cleanup`·`/architect` ADR 머지 후)에서 호출되거나 직접 부른다. 목적은 **여러 작업에 걸친 반복 패턴·프로세스 결함**을 잡아 승격하는 것 — trivial엔 self-skip한다(공격적으로 도는 회고는 의례다).

### PR 작성 identity (loop vs 사람)

- `/dispatch-issue`가 만드는 PR의 목표 작성자는 사람 계정이 아니라 GitHub App(`mechanu[bot]`)이다(ADR-0009 point 5) — 사람 검토자와 identity가 같으면 GitHub의 self-approval 차단 때문에 사람 본인이 그 PR을 승인할 수 없다(#41/#42에서 실측한 문제).
- 사람이 직접 만드는 PR(`/architect` grilling 세션 산출물, 수동 hotfix 등 `/dispatch-issue` 밖의 작업)은 이 전환과 무관하게 계속 사람 계정으로 만든다.
- App installation access token 발급 로직은 `internal/enforcement.InstallationTokenMinter`(`internal/enforcement/installtoken.go`)에 있다. **이 경로는 App 자격증명(App ID·installation ID·private key)이 실제로 공급됐을 때만 쓴다** — 공급되지 않으면(현재 기본 상태) `.claude/skills/dispatch-issue/`가 기존 `gh auth switch --user <사람 계정>` 흐름으로 fail-closed 폴백한다. 구체적 CLI 사용법(토큰을 git/gh에 먹이는 법)은 `.claude/agents/go-tdd-implementer.md`.
- 이 전환은 설계 단계다 — 실제 `mechanu[bot]` identity로 만든 커밋/PR이 GitHub에서 그렇게 표시됨을 실측 확인한 적은 아직 없다(#43. 검증은 별도 진행).

## 이슈 라벨

AI 에이전트가 이슈를 보고 디스패치 여부를 판단할 수 있도록 5축 라벨을 쓴다. 새 이슈에는 해당하는 축을 빠짐없이 붙인다(라벨 정의는 `gh label list` 참조).

- **`type:`** 작업 종류(feature/bug/refactor/test/docs/chore/spike)
- **`area:`** 코드 영역(auth/market/order/account/strategy/runtime/config/infra) — 에이전트에 줄 컨텍스트 결정. 여러 개 가능.
- **`agent:`** 디스패치 판단축 — `ready`(명세 완결, 자율 실행 가능) / `blocked`(선행 의존 대기) / `needs-decision`(시작 전 사람 결정 필요). 선행 이슈가 머지되면 `blocked`→`ready`로 바꾼다.
- **`risk:`** 폭발 반경 — `critical`(실주문·자금이동·비가역 → **사람 리뷰 필수**) / `high`(시크릿·인증·무인 안전장치) / `low`(읽기·문서·테스트).
- **`priority:`** p0(지금/블로킹)/p1/p2.

## 명령어

```bash
go build ./...
go run ./cmd/bot
go test ./...
go test ./internal/config/ -run TestName -v   # 단일 테스트
go test -race ./...           # 동시성 코드는 반드시 -race로 검증
go vet ./...
gofmt -l -w .
```

## Toss Open API (실측 정보)

문서: https://developers.tossinvest.com/docs
기계 판독용: https://developers.tossinvest.com/llms.txt
OpenAPI 스펙(엔드포인트/스키마의 단일 진실): https://openapi.tossinvest.com/openapi-docs/latest/openapi.json
새 엔드포인트나 파라미터가 필요하면 **추측하지 말고 위 OpenAPI 스펙을 먼저 확인**한다.

- **Base URL**: `https://openapi.tossinvest.com`
- **인증**: OAuth 2.0 **Client Credentials Grant** 전용
  - `POST /oauth2/token`, `Content-Type: application/x-www-form-urlencoded`
  - 본문: `grant_type=client_credentials`, `client_id`, `client_secret`
  - 모든 API 호출에 `Authorization: Bearer {access_token}`
- **토큰 수명**: 86400초(24시간), **refresh token 없음**.
  - ⚠️ **클라이언트당 유효 토큰은 1개뿐.** 토큰을 새로 발급하면 기존 토큰이 무효화된다 → 프로세스 여러 개가 각자 토큰을 발급하면 서로를 죽인다. 토큰은 한 곳에서 발급·캐시하고 만료 전 갱신한다.
- **계좌 헤더**: 계좌 관련 호출은 `X-Tossinvest-Account: {accountSeq}` 필요. `accountSeq`는 `GET /api/v1/accounts`에서 얻는다.

주요 엔드포인트:

| 용도 | Method | Path |
|------|--------|------|
| 계좌 목록 | GET | `/api/v1/accounts` |
| 보유 종목 | GET | `/api/v1/holdings` |
| 호가 | GET | `/api/v1/orderbook` |
| 현재가 | GET | `/api/v1/prices` |
| 주문 생성 | POST | `/api/v1/orders` |
| 주문 목록 | GET | `/api/v1/orders` |
| 거래일 캘린더 | GET | `/api/v1/market-calendar/{KR,US}` |

- **주문 생성**: `symbol`, `side`(BUY/SELL), `orderType`(LIMIT/MARKET) 필수. `quantity`(주) 또는 `orderAmount`(USD, 미국장 전용) 중 하나. LIMIT은 `price` 포함.
- API 카테고리: Auth, Market Data, Stock Info, Market Info(환율·거래일), Account & Asset, Order.
- Market data는 REST. WebSocket 제공 여부는 **미확인** — 스펙 확인 전까지 폴링을 전제로 설계한다.

## 무인 실행 제약 (설계 1순위)

24시간 방치 운영이므로 다음을 모든 코드의 기본 전제로 삼는다:

- **죽지 않는다.** panic은 주문 루프를 멈춘다. goroutine마다 recover 경계를 두고, 단일 API 실패가 전체를 내리지 않게 한다.
- **재시작 안전성.** 프로세스가 죽었다 살아나도 안전해야 한다 — 미체결 주문/포지션을 기동 시 API로 재조회해 재구성하고(로컬 상태를 신뢰하지 말 것), 같은 주문을 중복 제출하지 않게 한다(멱등성/주문 dedup).
- **시장 시간 인지.** 항상 도는 루프라도 장 마감/휴장에는 주문하지 않는다. `market-calendar`로 거래일·장중 여부를 판단한다.
- **레이트 리밋/백오프.** 한도는 스펙에서 확인. 429/5xx는 지수 백오프로 재시도하되, **주문 제출은 자동 재시도 금지**(중복 체결 위험) — 조회는 재시도해도 주문은 상태 재확인 후에만.
- **관측성.** 무인이므로 로그가 유일한 사후 진단 수단이다. 구조화 로깅 + 모든 주문/체결/에러 영속 기록. 치명적 상황은 외부 알림(미정).
- **킬 스위치.** 비정상(연속 실패, 예상외 손실, 토큰 갱신 불가) 시 신규 주문을 멈추는 안전장치를 둔다.
- **시크릿.** `client_id`/`client_secret`은 환경변수/시크릿 매니저로 주입. 커밋 금지.

## 테스트 방침

- Toss API를 직접 때리는 테스트 금지(실주문·레이트리밋 위험). HTTP 클라이언트를 인터페이스로 추상화하고 `httptest`나 mock으로 검증한다.
- 토큰 만료/갱신, 멱등 주문, 백오프, 장 시간 판정 등 무인 운영 로직은 반드시 단위 테스트로 커버한다.
