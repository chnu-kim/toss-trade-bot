---
name: opensource-maintainer
description: toss-trade-bot에서 `git commit` 직전(스테이징 후) 반드시 실행하는 public-readiness 게이트 — 변경분에 시크릿·개인정보·환경 의존 내용이 섞이지 않았는지 검사하고 명시적 verdict(통과/수정 필요)를 낸다. 사용자가 "커밋", "commit", "올려", "푸시", "PR"을 말하거나 커밋을 요청하면 커밋 명령을 내리기 전에 호출한다.
---

# opensource-maintainer

private ≠ 안전 — 히스토리에 들어가기 전 마지막 게이트다(레포 표준은 CLAUDE.md "작업 방식"). 통과(또는 사용자가 항목을 확인·수용)하기 전에는 커밋하지 않는다. CI(`.github/workflows/ci.yml`)가 `scan_test.sh` + `scan.sh --all --content-only`를 2차 게이트로 다시 돈다.

## 1. 실행 — `git commit` 직전, 스테이징 후

```bash
bash .claude/skills/opensource-maintainer/scripts/scan.sh   # 기본: 스테이징분(없으면 --all 자동). --all로 전체 강제
```
- 모드는 CLAUDE.md "모델/런타임 프로파일" 표의 `scan.sh` 행: local은 기본, `HARNESS_ENV=cloud`는 `--all --content-only`(커밋 identity가 플랫폼 관리라 이메일·서명 점검이 무의미). `git commit -a`·pathspec 커밋처럼 스테이징을 거치지 않으면 `--all`.
- exit 0 = 깨끗 / 1 = 후보 발견 또는 `user.email` 비-noreply(판정 필요) / 2 = 내부 오류·알 수 없는 인자 = 하드 차단. 2는 재시도로 넘기지 않고 원인을 고친다.

## 2. 판단

출력은 *후보*다 — 이 스킬은 차단이 아니라 판단을 돕는 게이트이고 판단은 내가 한다. 거짓 양성에 정상 커밋을 막지 말고, 실제 시크릿·개인정보는 단호히 막는다.
- 정상(통과): 모듈 경로의 소유자명(`github.com/<owner>/...`), 빈 placeholder·환경변수 *이름* 참조, example류 이메일/도메인, 합성 테스트 픽스처.
- 실제 문제(차단): 값이 채워진 시크릿·개인키 블록·하드코딩 Bearer, 개인 이메일, 사용자 홈 아래 절대경로, 특정 OS/아키텍처·패키지 매니저 경로 가정, 추적된 `.env`.
- 합법적 거짓 양성의 유일한 억제 경로는 `allowlist.txt`(`경로|매치된 텍스트 완전일치|사유`). 인라인 마커·패턴 완화·파일 제외는 CI가 인정하지 않는다. 실제 시크릿은 절대 allowlist 하지 않는다.

## 3. 수정·재스캔·보고

- 실제 문제는 수정(시크릿 → env 주입, 경로·문구 → 일반화, 이메일 → noreply)하고 **재스캔 통과 후** 커밋한다. 실제 시크릿은 제거+로테이션 — 그대로 커밋 절대 금지.
- local에서 `user.email`이 GitHub noreply가 아니면 repo-local noreply 설정을 제안한다(개인 이메일이 히스토리에 남지 않게). 서명 상태는 정보성.
- 보고는 한국어, 항상 이 구조로:

```
## 커밋 전 점검 결과
- 차단 항목: <없음 | N건>
- 주의 항목: <없음 | N건>

### 발견 (있을 때만)
- [카테고리] 파일:줄 — 무엇/왜 + 권장 조치

### 판단
<통과: 커밋 진행 가능> 또는 <수정 필요: 아래 조치 후 재스캔>
```

## 4. 게이트 자체 변경

- `scan.sh`·`scan_test.sh`·`allowlist.txt`·이 파일은 sacred다(CODEOWNERS + `sacredRequiredPaths`; CI가 `scan_test.sh`와 `scan.sh --all --content-only`를 실행). 패턴·allowlist 변경은 내용 변경과 섞지 않고 자기 PR로만(CLAUDE.md "PR 생성 흐름"), 같은 PR에서 `scan_test.sh`에 회귀 케이스를 추가해 실제 실행·green 확인. 새 유형의 유출을 겪으면 그 경로로 패턴을 추가한다.
- 향후 PreToolUse 커밋 훅을 두려면: `scan.sh`를 직접 걸지 말고 래퍼로 rc 1·2 → exit 2(차단) 매핑, 스캐너 부재 시 exit 2, 레포 루트는 `git rev-parse --show-toplevel`(cloud Bash에는 `CLAUDE_PROJECT_DIR`이 없다), `-C <dir>` 해석, 훅·`settings.json`·훅 테스트를 같은 PR에서 sacred 등재(CLAUDE.md "게이트 승격 규칙").
