#!/usr/bin/env bash
# opensource-maintainer 커밋 전 스캔.
# 스테이징된 변경분(기본) 또는 추적 파일 전체(--all)에서, private이지만
# 언제든 public 전환 가능해야 하는 레포에 들어가면 안 되는 내용을 찾아낸다.
#
# 출력은 "후보"다 — 최종 판단은 호출한 모델이 한다(예: 모듈 경로의 소유자명은
# 정상, env.example의 빈 placeholder는 정상). 거짓 양성을 줄이되, 놓치는 것보다
# 과검출이 낫다는 기조로 패턴을 잡았다.
#
# 게이트 원칙: 시크릿 스캔은 커밋 전 마지막 안전망이므로 내부 오류 시 fail-open(조용히
# 통과)하지 않고 fail-closed(차단, exit 2)로 기운다 — 잘못된 초록(false-green)이 놓친
# 시크릿보다 위험하기 때문이다.
set -uo pipefail

cd "$(git rev-parse --show-toplevel 2>/dev/null)" || { echo "git 레포가 아님"; exit 2; }

MODE="staged"
[ "${1:-}" = "--all" ] && MODE="all"

# 스테이징된 변경이 없으면 자동으로 추적 파일 전체를 본다.
if [ "$MODE" = "staged" ] && git diff --cached --quiet; then
  echo "ℹ️  스테이징된 변경이 없어 추적 파일 전체(--all)를 스캔합니다."
  MODE="all"
fi

if [ "$MODE" = "staged" ]; then REF=(--cached); SCOPE="스테이징된 변경분"; else REF=(); SCOPE="추적 파일 전체"; fi

FOUND=0
HARD_FAIL=0
section() { printf '\n=== %s ===\n' "$1"; }
# git grep: -I(바이너리 제외) -n(줄번호) -E(ERE).
# git grep 종료코드: 0=매치, 1=매치 없음, 2+=오류.
#   - 매치 있으면 FOUND=1.
#   - 오류(2+)는 fail-closed: HARD_FAIL=1로 승격해 마지막에 exit 2. 게이트가 내부 오류를
#     "차단 사유 없음"으로 잘못 통과시키지 않게 한다.
# ${REF[@]+"${REF[@]}"}: --all 모드의 빈 배열 REF=()를 set -u 하에서 안전하게 확장한다.
#   naive한 "${REF[@]}"는 bash 4.4 미만(macOS 기본 /bin/bash 3.2)에서 unbound variable
#   오류를 내고, 그 오류가 명령 치환 서브셸에서 삼켜지면 모든 scan()이 매치 0건처럼 되어
#   전체 fail-open했다(L-11). 안전 확장으로 어떤 bash에서도 정상 동작한다.
scan() { # $1=설명  $2=패턴  $3(opt)=제외 grep -vE 패턴  $4(opt)=추가 git grep 플래그(예: -i)
  local out rc extra="${4:-}"
  # 이 스킬 자기 정의 디렉토리는 탐지 패턴·예시를 문서로 담고 있어 항상 걸린다 → 제외.
  out=$(git grep ${REF[@]+"${REF[@]}"} -nIE $extra "$2" -- . ':!.claude/skills/opensource-maintainer' 2>/dev/null)
  rc=$?
  if [ "$rc" -gt 1 ]; then
    echo "❌ 내부 오류: '$1' 스캔의 git grep 실패(exit $rc) — 게이트 fail-closed." >&2
    HARD_FAIL=1
    return
  fi
  [ -n "${3:-}" ] && out=$(printf '%s\n' "$out" | grep -vE "$3")
  out=$(printf '%s\n' "$out" | grep -v '^$')
  if [ -n "$out" ]; then section "$1"; printf '%s\n' "$out"; FOUND=1; fi
}

echo "🔎 opensource-maintainer 스캔 ($SCOPE)"

# 1) 시크릿 — 키워드 뒤에 대입되는 실값. 구분자는 `:`·`=`뿐 아니라 Go의 `:=`도 매칭한다.
#    값이 따옴표로 감싼 리터럴이면(의도적 하드코딩) 12자+ 무엇이든 후보로 본다. 따옴표 없는
#    값은 "숫자 1개 이상 + 충분한 길이"를 요구한다 — 실제 시크릿(토큰·키)은 거의 항상 숫자를
#    포함하는 반면, `field: identifier`(예: Go 구조체의 `clientSecret: clientSecret`) 같은
#    일반 코드 식별자 대입은 숫자가 없어 과검출되지 않게 하기 위함이다. 나머지 과검출은
#    아래 제외 목록·모델 판단이 흡수한다(놓치는 것보다 과검출이 안전한 방향).
scan "시크릿 의심 (키=값 대입)" \
  '(client_secret|client_id|api[_-]?key|secret_key|access_token|refresh_token|secret|token|password|passwd)["'"'"'`]?[[:space:]]*(:=|[:=])[[:space:]]*(["'"'"'`][A-Za-z0-9._/+-]{12,}|[A-Za-z0-9._/+-]*[0-9][A-Za-z0-9._/+-]{7,})' \
  'getenv|os\.Getenv|process\.env|example|placeholder|<.*>|YOUR_|xxx+|\$\{' \
  '-i'
scan "AWS 액세스 키" 'AKIA[0-9A-Z]{16}'
scan "개인키 블록" 'BEGIN[ A-Z]*PRIVATE KEY'
scan "Bearer 토큰 하드코딩" 'Authorization:[[:space:]]*Bearer[[:space:]]+[A-Za-z0-9._-]{16,}'

# 2) 개인정보
#    이메일: GitHub noreply와 example류는 제외.
scan "개인 이메일" \
  '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' \
  'users\.noreply\.github\.com|noreply@github\.com|example\.(com|org|net|test)|@example|@sentry|@your|@domain'
scan "홈 디렉토리/머신 절대경로" '/Users/[A-Za-z0-9._-]+|/home/[A-Za-z0-9._-]+|[A-Za-z]:\\\\Users\\\\'
scan "비공개 지침 파일 참조" '~/\.claude|\.claude/CLAUDE\.md|/Documents/project/CLAUDE\.md'

# 3) 환경 의존 — 특정 OS/머신/패키지매니저 경로 가정 + 운영자 머신-특정 개인 도구 식별자.
#    개인 도구명(예: 1Password)이 커밋되면 서명 키·시크릿 보관 환경이 노출돼 표적 정찰
#    정보가 된다(레포 정책: 환경 의존 내용 금지). 일반 표현으로 바꾼다.
scan "환경 의존(OS/머신 특정)" 'darwin/arm64|macOS/arm64|macos/arm64|x86_64-apple|/opt/homebrew|/usr/local/Cellar'
scan "개인 도구 식별자(머신-특정)" '1[Pp]assword'

# 4) 실수로 스테이징된 .env (example 제외)
ENVFILES=$( (git diff --cached --name-only 2>/dev/null; [ "$MODE" = "all" ] && git ls-files) \
  | grep -E '(^|/)\.env($|\.)' | grep -vE '\.env\.(example|sample|template)$|env\.example' | sort -u)
if [ -n "$ENVFILES" ]; then section ".env 파일 추적/스테이징됨"; printf '%s\n' "$ENVFILES"; FOUND=1; fi

# 5) 설정 점검 (diff가 아닌 환경) — 커밋 이메일 / 서명
section "커밋 설정 점검"
EMAIL=$(git config user.email 2>/dev/null)
if printf '%s' "$EMAIL" | grep -qE 'users\.noreply\.github\.com$'; then
  echo "✅ user.email = $EMAIL (noreply)"
else
  echo "⚠️  user.email = ${EMAIL:-(미설정)} — 개인 이메일 노출 위험. GitHub noreply 권장."
  FOUND=1
fi
SIGN=$(git config commit.gpgsign 2>/dev/null)
SKEY=$(git config user.signingkey 2>/dev/null)
if [ "$SIGN" = "true" ] && [ -n "$SKEY" ]; then
  echo "✅ 서명 활성 (commit.gpgsign=true, signingkey=$SKEY)"
else
  echo "ℹ️  서명 비활성 또는 키 미설정 (commit.gpgsign=${SIGN:-unset}, signingkey=${SKEY:-unset})"
fi

echo
if [ "$HARD_FAIL" != "0" ]; then
  echo "❌ 게이트 내부 오류로 fail-closed — 커밋 차단(exit 2)."
  exit 2
elif [ "$FOUND" = "0" ]; then
  echo "✅ 차단 사유 없음 — 커밋 진행 가능."
  exit 0
else
  echo "⚠️  후보 발견 — 위 항목을 검토하라. 실제 문제면 수정 후 재스캔."
  exit 1
fi
