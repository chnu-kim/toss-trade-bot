# 회고: 루프 엔지니어링 자율 캠페인 (2026-07-18~19)

한 세션에서 거버넌스 ADR 2건 + 구현/하드닝 6건을 자율 도출·구현·적대검증·머지했다. 이 문서는 그 결과물과 프로세스를 2축으로 평가하고, 재사용 가능한 학습을 실행 가능한 형태로 남긴다.

## 산출물

| PR | 내용 | 적대 수렴 |
|---|---|---|
| #70 | ADR-0014 — reconciler ambiguous 국소 fail-closed·backlog 전역 에스컬레이션·bounded LIVE re-count | codex 5R |
| #71 | #28 store 하드닝 4건 (DB 0600·halt 거짓ack·resolve 충돌·DSN) | codex clean |
| #73 | ADR-0015 + `phase-b-entry.md` 런북(#50) | codex **9R** |
| #72 | #27 공급망·시크릿 게이트 하드닝 | codex **9R** |
| #74 | #64 `protects`→sacred 완결성 게이트 | codex 5R |
| #75 | **#35 단일 reconciler** (65 테스트) | codex 9R |
| #77 | #29 markers V4 무결성 제약 | codex 3R |
| #78 | **#36 cmd/bot 무인 골격** (sentinel 부팅·종료 판정) | codex 6R |
| #79 | #14 보존/prune 루프 (V5 인덱스) — 유일한 삭제 경로 | codex 3R |

부수 산출: 후속 이슈 #76(CI 스캐너 신뢰 경계)·#80(operator clear CLI). **ready 백로그 전량 소진.**

**도달점**: 봇이 재시작-안전하게 부팅(sentinel 판정 → 보수적 halted → running flip → reconciler 스캔 → 게이트 open)하고 graceful하게 종료한다. **전략만 꽂으면 되는 무인 골격**이 섰다.

## 축 1 — 결과물 품질

### 좋았던 것

**적대 검증이 실제로 결함을 잡았다.** codex 지적 중 진동은 거의 없었고 대부분 실제 결함이었다. 특히 money-safety 계열:
- ADR-0014의 전역 임계를 **시간-윈도우 rate → 미해소 backlog 건수**로 전환(hazard가 시간으로 노화하지 않으므로 rate는 fail-open) + inclusive `>=` off-by-one.
- `ClearSymbol`이 refcount가 아니라 boolean delete라, 한 종목 다중 ambiguous 중 하나만 해소돼도 종목 전체가 열리는 **과-해제 fail-open** → 잔여-0 확인.
- #35의 성공-리셋 계열 3연속 [high] → resolve-before-reset → in-process 순서 → **process watermark**(재시작 경계) 3중 봉쇄.
- #36의 `setLifecycle` RowsAffected 미검사 = **거짓 durable-ack**(#28의 L-1과 동일 클래스 재발) → `execSingleton` 일반화.

**구조 증명이 반복 패치를 이겼다.** persistence-wins 가드가 R2/R3 연속 [high]로 진동하려 할 때, 소스 실측 3-reader + loss/freeze 2-critic 워크플로로 전환해 **`no-guard`**(가드 자체 불필요)로 수렴했다. 근거는 구조적이다 — ambiguous는 orderId 핸들 부재로 **resolve 원천 불가**, order-failure는 count-before-resolve로 **prunable↔non-durable 상호배타**. 패치를 더 정교하게 만드는 대신 위험 클래스를 제거했다.

**위험 클래스 제거 > 하드닝**이 한 번 더 통했다. 부트스트랩 protection 완화의 롤백 backstop이 "규율일 뿐"이라는 지적에, backstop을 강화하는 대신 **완화 자체를 제거**하고 per-merge `--admin` bypass로 전환했다(설정 무변경 = 롤백 대상 없음). 이 세션이 실제로 그렇게 동작해 왔다는 실증이 근거가 됐다.

**워커들의 판단 품질이 높았다.** 지시 범위를 넘지 않으면서도 옳은 판단을 했다:
- #28: codex 두 채널의 **상호 배타적 요구**(unreadable repair vs fd-fchmod)를 실측(0o000은 fd 획득 불가)으로 판별하고 위협모델 사안으로 이관.
- #29: acked orderId 유일성 제약을 거부 — acked는 **비가역 POST 이후** 기록되므로 거기서 거부하면 이미 돈이 움직인 뒤 전역 halt를 트립하고, 그 경로는 ADR-0002가 **"미정의"**라 명시한 서버 replay 거동뿐이다.
- #35: 적대 지적이 ADR-0014 Decision 9와 충돌하자 **ADR을 뒤집지 않고 ADR의 안전 근거를 실증**했다(제출 경로가 POST 전에 감사 → 죽은 sink면 비가역 전에 fail-closed). 그리고 *그 불변식이 깨지면 ADR 논증도 무너진다*는 조건을 테스트 주석에 남겼다.
- #36: 자기 근거를 **뒤집었다** — "close 후 유실은 크래시와 동일한 잔여"라 적었다가, 크래시는 sentinel을 running으로 남겨 fail-closed인 반면 graceful은 clean을 적극 기록하므로 더 나쁘다고 인정. 잔여가 아니라 이 PR이 도입한 fail-open이었다.
- #14: mutation M4가 SURVIVED하자 **중복 조건을 지우는 대신** 그것이 독립적으로 일하는 유일한 경우(`ResolveIntent`↔`FinalizeFullyAudited` 사이 NTP 벽시계 역행)를 강제하는 테스트를 추가해 **non-vacuous화**했다. 죽지 않는 mutant를 만나면 가드를 지우는 게 아니라 그 가드가 필요한 상태를 찾는 것이 옳다.

**보호를 조건절이 아니라 구조로 세운 사례**(#14): halt·sentinel·카운터는 prune 조건에 예외로 넣은 게 아니라, **그 파일의 어떤 SQL statement도 해당 테이블을 명시하지 않는다**(실측 확인: `audit_acks`·`markers`·`intents` 셋만 등장). 조건은 실수로 빠질 수 있지만 "언급하지 않음"은 빠질 수 없다.

### 부족했던 것

**self-catch가 0에 가까웠다.** twin-artifact/문서 정합 결함을 **한 번도 스스로 먼저 잡지 못했다** — 전부 외부 적대 리뷰가 잡았다. #73에서만 8건 중 5건이 이 클래스였다(검사기 능력 과대서술·런북 무보호·지배 ADR stale 절차·⑤ 축소·증명 스크립트 무보호).

**패턴을 알면서도 재구축했다.** #64 워커는 손-미러 드리프트를 없애러 와서 **첫 수정에서 고정 ID 목록**으로 같은 드리프트를 한 층 위에 재생산했다. 나 자신도 ADR-0015에서 "중대-6" 하드닝으로 ADR-0011의 pre-main probe 요구를 **정면 모순**하는 과-교정을 했다(config 표현성과 behavioral-scope 혼동).

**선례를 근거 없이 이식했다.** #64 워커가 enforcement 자기보호를 비-테스트 `.go`로만 한정했는데, `internal/gate`의 구분(게이트 로직=비-테스트)을 기계적으로 옮긴 것이었다. 이 패키지는 **테스트가 곧 강제 로직**이고 재검증층이 없다.

## 축 2 — 프로세스

### 좋았던 것

- **`/architect` 선행이 옳았다.** #35 dispatch 전에 ADR-0014를 적대 패널로 확정한 덕에, 구현 워커가 세 포크(ambiguous 정책·clear-vs-escalation·bounded re-count)를 재발명하지 않고 곧장 구현에 들어갔다.
- **워커 자율 + 오케스트레이터 검증 분리**가 작동했다. 워커 보고를 그대로 믿지 않고 CI·codex·diff 스팟체크로 ground-truth한 결과, 보류 판단들이 전부 건전함을 독립 확인할 수 있었다.
- **병렬 dispatch**가 실질 처리량을 냈다(최대 3 워커 동시). 충돌은 공유 접점(#72↔#74의 CODEOWNERS·sacredRequiredPaths) 1회뿐이었고 합집합으로 해소됐다.

### 부족했던 것 / 마찰

1. **codex `--base main`이 stale 로컬 ref를 썼다** — 워크트리는 로컬 `main`을 갱신하지 않는다. 내 PR #73 리뷰 9회가 전부 superset diff였다(findings가 마침 내 콘텐츠라 무해했으나 운). → `--base origin/main` 고정.
2. **파괴적 Red 실증이 미커밋 편집을 삼켰다** — `git checkout --`로 되돌리다 배선 편집 2개가 사라져 이후 실증 2건이 **거짓 음성**. → 커밋된 baseline 위에서.
3. **codex 자기보고("샌드박스로 go test 실패")를 매번 로컬 재실행으로 반증**해야 했다.
4. **마이그레이션 버전 드리프트 재현** — #29 본문의 "V3"이 stale(V3=#20 소진) → V4. 메모리 경고가 정확했다.
5. **세션 중단으로 백그라운드 워커 2개 유실 위험** — push한 쪽은 무손실, 착수 전이던 쪽은 재dispatch.
6. **9라운드가 두 번 나왔다**(#73·#72). #72는 근본 원인이 **신뢰 경계**(레포 안에 살며 PR 체크아웃에서 실행되는 스캐너)라 regex 수정으로 끝날 수 없었다 — 아키텍처 포크를 늦게 인지했다.
7. **두 codex 채널은 비대칭이다 — `review` 통과를 수렴으로 읽으면 안 된다.** #14에서 1차 수정(인덱스 추가)이 **불충분한 채 `review` 채널을 통과**했고, `adversarial`만 2차에서 "인덱스가 존재하지만 range bound가 없다"를 잡았다. 더 나쁜 건 워커가 만든 **"인덱스 사용 확인" 테스트가 그 불충분한 상태에서 green**이었다는 점이다 — 검증이 잘못된 속성을 봤다. 교훈: 성능·자원 점유 수정은 "메커니즘이 존재하는가"가 아니라 **"의도한 경계가 실제로 걸리는가"**(여기서는 range bound)를 단언해야 한다.
8. **자기 주석이 거짓 주장을 담고 있었다.** #14 1차 코드는 "배치 한도가 write 락 점유를 유계로 만든다"고 주석에 적었는데, `MaxBatch`는 *삭제* 행 수만 유계로 만들고 *검사* 행 수는 아니었다. codex가 그 주장 자체를 반증했다 — **주석에 쓴 안전 주장도 검증 대상**이다.

## 실행 가능한 개선 (이 PR에 포함)

1. **CLAUDE.md — 게이트 승격 규칙 신설**: "X를 게이트·강제·증거 생성기로 승격하면 같은 PR에서 X를 sacred 등재." 판별 질문과 세 재발 사례, 디렉터리 규칙 vs 개별 등재의 이유 포함.
2. **`go-tdd-implementer` 에이전트 정의 — 운영 불변식 8개 신설**: `--base origin/main` / 주기적 push / 커밋된 baseline 위 파괴적 실증 / codex 자기보고 반증 / **ADR 충돌 시 실증 또는 보류(임의 개정 금지)** / 진동 vs 새 결함 판별 / 게이트 승격 규칙 / push·서명 차단 시 우회와 보고.
3. **메모리 갱신**: `twin-artifact-coupling`을 방향성 규칙으로 승격, `worktree-stale-main-ref` 신규.
4. **후속 이슈 분리**: #76(CI 스캐너 신뢰 경계 — base ref 실행), #80(operator clear CLI — 전략 부착 전 필수).

## 남은 것 (의도적 미완)

- **Track B 자율 머지 활성화**는 사람-액션 3종(App key 프로비저닝·verdict leg 시크릿·사람계정 narrowing)에 막혀 있다. 셋 다 classifier 물리 차단 또는 시크릿 매니저/웹 UI 접근이 필요해 **에이전트가 물리적으로 불가**하며, ADR-0015가 이를 sanctioned 사람-액션 부트스트랩으로 확정하고 `phase-b-entry.md`가 절차를 인도한다.
- **branch protection flip은 의도적으로 하지 않았다** — verdict 시크릿 부재로 verdict-gate가 green이 될 수 없고 narrowing 미완으로 loop가 admin을 쥔 상태에서 count 1→0은 비기능·비안전 게이트 개방이다. ADR-0015가 payload 생성기 부재를 근거로 flip을 fail-closed 차단 중이다.
