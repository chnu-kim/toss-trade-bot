#!/usr/bin/env bash
# opensource-maintainer 커밋 전 스캔.
# 스테이징된 변경분(기본) 또는 추적 파일 전체(--all)에서, private이지만
# 언제든 public 전환 가능해야 하는 레포에 들어가면 안 되는 내용을 찾아낸다.
#
# 출력은 "후보"다 — 최종 판단은 호출한 모델이 한다(예: 모듈 경로의 소유자명은
# 정상, env.example의 빈 placeholder는 정상). 거짓 양성을 줄이되, 놓치는 것보다
# 과검출이 낫다는 기조로 패턴을 잡았다.
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
section() { printf '\n=== %s ===\n' "$1"; }
# git grep: -I(바이너리 제외) -n(줄번호) -E(ERE). 매치 있으면 FOUND=1.
scan() { # $1=설명  $2=패턴  $3(opt)=제외 grep -vE 패턴  $4(opt)=추가 git grep 플래그(예: -i)
  local out extra="${4:-}"
  # 이 스킬 자기 정의 디렉토리는 탐지 패턴·예시를 문서로 담고 있어 항상 걸린다 → 제외.
  out=$(git grep "${REF[@]}" -nIE $extra "$2" -- . ':!.claude/skills/opensource-maintainer' 2>/dev/null)
  [ -n "${3:-}" ] && out=$(printf '%s\n' "$out" | grep -vE "$3")
  out=$(printf '%s\n' "$out" | grep -v '^$')
  if [ -n "$out" ]; then section "$1"; printf '%s\n' "$out"; FOUND=1; fi
}

echo "🔎 opensource-maintainer 스캔 ($SCOPE)"

# 1) 시크릿 — 키워드 뒤 따옴표로 감싼 실값만(빈 placeholder·환경변수명 참조·코드
#    식별자 대입은 제외). 따옴표 없는 .env류 유출은 아래 ".env 파일" 검출이 커버한다.
scan "시크릿 의심 (따옴표 리터럴)" \
  '(client_secret|client_id|api[_-]?key|secret_key|access_token|refresh_token|secret|token|password|passwd)["'"'"'`]?[[:space:]]*[:=][[:space:]]*["'"'"'`][A-Za-z0-9._/+-]{12,}' \
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

# 3) 환경 의존 — 특정 OS/머신/패키지매니저 경로 가정.
scan "환경 의존(OS/머신 특정)" 'darwin/arm64|macOS/arm64|macos/arm64|x86_64-apple|/opt/homebrew|/usr/local/Cellar'

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
if [ "$FOUND" = "0" ]; then
  echo "✅ 차단 사유 없음 — 커밋 진행 가능."
  exit 0
else
  echo "⚠️  후보 발견 — 위 항목을 검토하라. 실제 문제면 수정 후 재스캔."
  exit 1
fi
