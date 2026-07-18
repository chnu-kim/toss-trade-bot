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
CONFIG_CHECK=1
for arg in "$@"; do
  case "$arg" in
    --all) MODE="all" ;;
    # CI용: 레포 "내용" 스캔은 전부 그대로 하고, 로컬 커밋 설정(이메일·서명) 점검만 건너뛴다.
    # 그 점검은 커밋하는 개발자 머신의 git config를 보는 것이라 CI 러너에선 의미가 없고,
    # 러너엔 user.email이 없어 항상 실패한다. 유출 탐지 스캔은 하나도 건너뛰지 않는다.
    --content-only) CONFIG_CHECK=0 ;;
    *) echo "알 수 없는 인자: $arg" >&2; exit 2 ;;  # 오타로 게이트가 약해지지 않게 fail-closed
  esac
done

# 스테이징된 변경이 없으면 자동으로 추적 파일 전체를 본다.
if [ "$MODE" = "staged" ] && git diff --cached --quiet; then
  echo "ℹ️  스테이징된 변경이 없어 추적 파일 전체(--all)를 스캔합니다."
  MODE="all"
fi

if [ "$MODE" = "staged" ]; then REF=(--cached); SCOPE="스테이징된 변경분"; else REF=(); SCOPE="추적 파일 전체"; fi

FOUND=0
HARD_FAIL=0
section() { printf '\n=== %s ===\n' "$1"; }

# allowlist 매니페스트(경로|리터럴|사유). CODEOWNERS 보호 대상 = 항목 추가에 소유자 리뷰 필요.
ALLOWLIST_FILE=".claude/skills/opensource-maintainer/allowlist.txt"
# allowlisted: 매니페스트에 (경로 == $1) 이고 (리터럴 == **매치된 텍스트** $2) 인 항목이
# 있으면 성공(=허용). 두 가지가 핵심이다:
#  - 판정 단위가 '줄'이 아니라 '매치'다. 줄 단위면 한 줄에 허용값과 진짜 값이 같이 있을 때
#    진짜 값까지 사라진다.
#  - 비교가 부분일치가 아니라 **완전일치**다. 부분일치면 허용된 리터럴을 접두/접미로 품은
#    더 긴 값(`<허용값>PLUS_REAL_SECRET`)이 그대로 통과한다 — 의도적 우회 경로가 된다.
allowlisted() { # $1=파일 경로 $2=매치된 텍스트
  [ -f "$ALLOWLIST_FILE" ] || return 1
  local epath eliteral _rest
  while IFS='|' read -r epath eliteral _rest; do
    case "$epath" in ''|'#'*) continue ;; esac
    [ "$epath" = "$1" ] || continue
    [ -n "$eliteral" ] || continue
    [ "$2" = "$eliteral" ] && return 0
  done < "$ALLOWLIST_FILE"
  return 1
}

# 한 줄에 매치가 여러 개일 수 있다(압축된 JSON/YAML 한 줄에 example 값과 진짜 키가 함께
# 오는 경우). 그래서 '첫 매치'나 '줄 전체'로 판정하면 안 된다 — 앞쪽 example 매치 하나가
# 뒤쪽 진짜 자격증명을 통째로 가려버린다(false-green).
# 매치를 **전부** 평가해, 제외 목록에도 allowlist에도 걸리지 않는 매치가 하나라도 남으면
# 그 줄을 후보로 보고한다. 제외/allowlist 판정은 모두 그 '매치된 텍스트'에만 적용되므로,
# 값 밖의 주석·산문으로는 아무것도 숨길 수 없다.
# 매치를 하나도 추출하지 못하면(패턴/플래그 불일치 등 예기치 못한 상황) 줄을 남긴다 —
# 제외할 근거를 못 만든 채 조용히 버리지 않는 fail-closed 방향.
filter_line_matches() { # $1=탐지 패턴 $2=제외 regex $3=git grep 플래그
  local pat="$1" excl="$2" flags="$3" line path rest content gi="" m kept n
  case "$flags" in *-i*) gi="-i" ;; esac
  while IFS= read -r line; do
    [ -z "$line" ] && continue
    path="${line%%:*}"; rest="${line#*:}"; content="${rest#*:}"
    kept=0; n=0
    while IFS= read -r m; do
      [ -z "$m" ] && continue
      n=$((n + 1))
      if [ -n "$excl" ] && printf '%s' "$m" | grep -qE $gi "$excl"; then continue; fi
      if allowlisted "$path" "$m"; then continue; fi
      kept=1
    done <<EOF
$(printf '%s\n' "$content" | grep -oE $gi "$pat")
EOF
    [ "$n" -eq 0 ] && kept=1
    [ "$kept" = "1" ] && printf '%s\n' "$line"
  done
}
# git grep: -I(바이너리 제외) -n(줄번호) -E(ERE).
# git grep 종료코드: 0=매치, 1=매치 없음, 2+=오류.
#   - 매치 있으면 FOUND=1.
#   - 오류(2+)는 fail-closed: HARD_FAIL=1로 승격해 마지막에 exit 2. 게이트가 내부 오류를
#     "차단 사유 없음"으로 잘못 통과시키지 않게 한다.
# ${REF[@]+"${REF[@]}"}: --all 모드의 빈 배열 REF=()를 set -u 하에서 안전하게 확장한다.
#   naive한 "${REF[@]}"는 bash 4.4 미만(macOS 기본 /bin/bash 3.2)에서 unbound variable
#   오류를 내고, 그 오류가 명령 치환 서브셸에서 삼켜지면 모든 scan()이 매치 0건처럼 되어
#   전체 fail-open했다(L-11). 안전 확장으로 어떤 bash에서도 정상 동작한다.
scan() { # $1=설명 $2=패턴 $3(opt)=제외 grep -vE 패턴 $4(opt)=git grep 플래그(예: -i) $5(opt)=추가 제외 pathspec
  local out rc extra="${4:-}" xpath="${5:-}"
  # 파일 단위 제외는 없다. 스캐너 본체·회귀 스위트·스킬 문서·allowlist 매니페스트까지
  # 전부 스캔한다 — 어떤 파일이든 통째로 빼면 '보호는 되지만 검사되지 않는' 사각지대가
  # 되어, 거기 실제 자격증명이 들어가도 CI가 초록으로 통과한다. 회귀 스위트는 픽스처
  # 값을 변수로 조립해 소스에 자격증명 형태를 남기지 않는 방식으로 스캔 대상에 남는다.
  # $5가 있으면 추가 제외 pathspec(예: ':!*.go')을 붙인다.
  out=$(git grep ${REF[@]+"${REF[@]}"} -nIE $extra "$2" -- . ${xpath:+"$xpath"} 2>/dev/null)
  rc=$?
  if [ "$rc" -gt 1 ]; then
    echo "❌ 내부 오류: '$1' 스캔의 git grep 실패(exit $rc) — 게이트 fail-closed." >&2
    HARD_FAIL=1
    return
  fi
  out=$(printf '%s\n' "$out" | filter_line_matches "$2" "${3:-}" "$extra")
  out=$(printf '%s\n' "$out" | grep -v '^$')
  if [ -n "$out" ]; then section "$1"; printf '%s\n' "$out"; FOUND=1; fi
}

echo "🔎 opensource-maintainer 스캔 ($SCOPE)"

# 1) 시크릿 — 키워드 뒤에 대입되는 실값. 구분자는 `:`·`=`뿐 아니라 Go의 `:=`도 매칭한다.
#  (A) 따옴표 리터럴(모든 파일): 의도적 하드코딩. 12자+ 값을 후보로 본다.
#  (B) 따옴표 없는 값(비-Go 파일): YAML/env 등 설정의 따옴표 없는 시크릿(`client_secret: 실값`).
#      Go는 문자열 리터럴이 항상 따옴표라 `.go`의 따옴표 없는 RHS는 코드 식별자(예: 구조체
#      필드가 같은 이름의 변수를 받는 형태)일 뿐 시크릿일 수 없으므로 `*.go`를 제외한다. 이렇게
#      하면 식별자 대입 과검출 없이도 따옴표 없는 실제 시크릿을 숫자 유무·위치와 무관하게
#      잡는다(숫자 휴리스틱은 끝자리 숫자·무숫자 토큰을 놓쳐 false-green 위험이라 폐기).
#  과검출은 아래 제외 목록·모델 판단이 흡수한다(놓치는 것보다 과검출이 안전한 방향).
scan "시크릿 의심 (따옴표 리터럴)" \
  '(client_secret|client_id|api[_-]?key|secret_key|access_token|refresh_token|secret|token|password|passwd)["'"'"'`]?[[:space:]]*(:=|[:=])[[:space:]]*["'"'"'`][A-Za-z0-9._/+-]{12,}' \
  'getenv|os\.Getenv|process\.env|example|placeholder|<.*>|YOUR_|xxx+|\$\{' \
  '-i'
scan "시크릿 의심 (따옴표 없는 값, 비-Go)" \
  '(client_secret|client_id|api[_-]?key|secret_key|access_token|refresh_token|secret|token|password|passwd)["'"'"'`]?[[:space:]]*(:=|[:=])[[:space:]]*[A-Za-z0-9._/+-]{12,}' \
  'getenv|os\.Getenv|process\.env|example|placeholder|<.*>|YOUR_|xxx+|\$\{' \
  '-i' \
  ':!*.go'
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
#    개인 도구명(비밀번호 관리자 등)이 커밋되면 서명 키·시크릿 보관 환경이 노출돼 표적
#    정찰 정보가 된다(레포 정책: 환경 의존 내용 금지). 일반 표현으로 바꾼다.
#    아래 첫 스캔만 이 파일(scan.sh)을 제외한다: 패턴 문자열이 곧 그 OS/경로 리터럴이라
#    자기 자신을 무조건 잡는다(다른 방법은 패턴을 조각내 알아볼 수 없게 만드는 것뿐).
#    시크릿·개인키·Bearer·이메일 등 정작 중요한 검사에는 scan.sh도 그대로 포함된다.
scan "환경 의존(OS/머신 특정)" 'darwin/arm64|macOS/arm64|macos/arm64|x86_64-apple|/opt/homebrew|/usr/local/Cellar' \
  '' '' ':!.claude/skills/opensource-maintainer/scripts/scan.sh'
scan "개인 도구 식별자(머신-특정)" '1[Pp]assword'

# 4) 실수로 스테이징된 .env (example 제외)
ENVFILES=$( (git diff --cached --name-only 2>/dev/null; [ "$MODE" = "all" ] && git ls-files) \
  | grep -E '(^|/)\.env($|\.)' | grep -vE '\.env\.(example|sample|template)$|env\.example' | sort -u)
if [ -n "$ENVFILES" ]; then section ".env 파일 추적/스테이징됨"; printf '%s\n' "$ENVFILES"; FOUND=1; fi

# 5) 설정 점검 (레포 내용이 아니라 커밋하는 머신의 git config) — 커밋 이메일 / 서명.
#    --content-only(CI)에서는 건너뛴다: CI 러너는 커밋 주체가 아니고 user.email도 없다.
if [ "$CONFIG_CHECK" = "1" ]; then
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
