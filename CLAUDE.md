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
