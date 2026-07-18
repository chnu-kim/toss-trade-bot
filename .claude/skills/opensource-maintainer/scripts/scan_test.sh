#!/usr/bin/env bash
# scan.sh 회귀 테스트 (issue #27: M-6 / L-11 / L-12 + 게이트 하드닝).
#
# 각 케이스를 격리된 임시 git 레포에서 재현해 scan.sh의 종료코드·검출을 검증한다.
# 실제 레포는 절대 건드리지 않는다. 모든 케이스 통과 시 exit 0, 하나라도 실패 시 exit 1.
#
# **이 파일도 레포 스캔 대상이다**(제외 경로 없음). 그래서 아래 픽스처 값은 전부 변수로
# hoist 해서 조립한다 — 소스에 `keyword = "값"` 형태나 개인 도구명·이메일 리터럴을 그대로
# 남기면 스캐너가 자기 회귀 스위트를 후보로 잡아 CI가 영구 red가 되고, 그렇다고 이 파일을
# 통째로 제외하면 실제 자격증명이 여기 들어가도 안 보이는 사각지대가 된다.
# 변수명에는 시크릿 키워드를 넣지 않는다(그래야 대입 줄 자체가 패턴에 걸리지 않는다).
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SCAN="$HERE/scan.sh"
PASS=0
FAIL=0

# --- 픽스처 값 (전부 합성/가짜) ---
FV_A="AbCdEf123456789xyz"
FV_B="SyntheticFixtureValue1"
FV_C="DifferentLeakValue999"
FV_D="RealLeakValue123456"
FV_E="RealLeakValue987654"
FV_F="RealLeakValue555555"
FV_G="AllowedFixtureValue1"
FV_H="InlineBypassValue123"
FV_I="syntheticcredentialvalue9"
FV_J="syntheticcredentialvaluexyz"
FV_K="RealLeakInDocs123456"
FV_L="RealLeakInNewFile1234"
FV_M="RealLeakInSuite123456"
FV_N="${FV_B}PLUSREALSECRET"
PH_A="YOUR_CLIENT_SECRET_HERE"
PH_B="example-placeholder-value"
ID_A="clientSecret"                 # 코드 식별자(시크릿 아님)
FAKE_A="fake-token-never-used"
TOOL="1Pass""word"                  # 리터럴을 소스에 남기지 않으려고 쪼갠다
MAIL="personal@""gmail.com"         # 위와 같은 이유
ALLOW="\
.claude/skills/opensource-maintainer/allowlist.txt"

pass() { printf '  ✅ %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  ❌ %s\n' "$1"; FAIL=$((FAIL + 1)); }

# 격리된 임시 레포를 만들고 그 경로를 echo 한다. 인자는 "파일경로|내용" 픽스처.
make_repo() {
  local dir; dir="$(mktemp -d)"
  git -C "$dir" init -q
  git -C "$dir" config user.email "t@users.noreply.github.com"
  git -C "$dir" config user.name "t"
  git -C "$dir" config commit.gpgsign false
  local spec name body
  for spec in "$@"; do
    name="${spec%%|*}"; body="${spec#*|}"   # 첫 '|'만 구분자 — 본문의 '|'는 그대로 유지된다
    mkdir -p "$(dirname "$dir/$name")"
    printf '%s\n' "$body" >"$dir/$name"
  done
  git -C "$dir" add -A >/dev/null 2>&1
  git -C "$dir" commit -q -m fixture --no-gpg-sign >/dev/null 2>&1
  printf '%s' "$dir"
}

# run_scan <bash-bin> <repo-dir> [scan-args...] → stdout=출력, 반환코드=scan 종료코드
run_scan() {
  local bash_bin="$1" dir="$2"; shift 2
  ( cd "$dir" && "$bash_bin" "$SCAN" "$@" )
}

echo "== M-6: :=/따옴표없는 시크릿 검출 =="
D=$(make_repo \
  "walrus.go|clientSecret := \"$FV_A\"" \
  "yaml.yml|client_secret: $FV_A" \
  "eq.go|clientSecret = \"$FV_A\"")
OUT=$(run_scan bash "$D" --all); RC=$?
grep -q 'walrus.go' <<<"$OUT" && pass "Go := 대입 검출" || fail ":= 케이스 미검출"
grep -q 'yaml.yml'  <<<"$OUT" && pass "따옴표 없는 YAML 값 검출" || fail "따옴표 없는 YAML 미검출"
grep -q 'eq.go'     <<<"$OUT" && pass "= 대입 검출(회귀 보존)" || fail "기존 = 케이스 회귀"
[ "$RC" -eq 1 ] && pass "시크릿 존재 시 exit 1(차단)" || fail "시크릿 존재하는데 exit=$RC (fail-open 위험)"
rm -rf "$D"

echo "== M-6b: .go의 따옴표 없는 식별자 대입은 과검출 안 함 (codex P2) =="
# Go는 문자열이 항상 따옴표 → .go의 따옴표 없는 RHS는 코드 식별자일 뿐 시크릿 아님.
D=$(make_repo \
  "field.go|clientSecret: $ID_A," \
  "fake.go|GH_TOKEN=$FAKE_A")
OUT=$(run_scan bash "$D" --all); RC=$?
{ ! grep -q 'field.go' <<<"$OUT"; } && pass ".go 구조체 필드의 식별자 대입 미검출" || fail "식별자 대입 과검출(field.go)"
{ ! grep -q 'fake.go'  <<<"$OUT"; } && pass ".go 따옴표없는 값 미검출" || fail "과검출(fake.go)"
[ "$RC" -eq 0 ] && pass ".go 식별자만 있는 레포 exit 0(거짓 양성 없음)" || { fail ".go 식별자 대입인데 exit=$RC"; printf '%s\n' "$OUT"; }
rm -rf "$D"

echo "== M-6c: 비-Go 따옴표 없는 시크릿은 숫자 유무·위치 무관 검출 (adversarial [high]) =="
# 숫자 휴리스틱이 놓치던 케이스: 끝자리 숫자 토큰·무숫자 토큰.
# (합성값 — 프로바이더 시크릿 프리픽스는 피한다. GitHub push protection이 실키로 오인해 push를 막음)
D=$(make_repo \
  "digit_end.yml|api_key: $FV_I" \
  "all_alpha.yml|api_key: $FV_J")
OUT=$(run_scan bash "$D" --all); RC=$?
grep -q 'digit_end.yml' <<<"$OUT" && pass "끝자리 숫자 토큰 검출(false-green 아님)" || fail "끝자리 숫자 토큰 미검출(false-green)"
grep -q 'all_alpha.yml' <<<"$OUT" && pass "무숫자 토큰 검출(false-green 아님)" || fail "무숫자 토큰 미검출(false-green)"
[ "$RC" -eq 1 ] && pass "따옴표 없는 시크릿 존재 시 exit 1" || fail "exit=$RC (1 기대)"
rm -rf "$D"

echo "== L-12: 개인 도구 식별자 검출 =="
D=$(make_repo "doc.md|서명 키는 $TOOL agent에 있다")
OUT=$(run_scan bash "$D" --all); RC=$?
{ grep -q 'doc.md' <<<"$OUT" && [ "$RC" -eq 1 ]; } && pass "개인 도구명 검출·exit 1" || fail "개인 도구명 미검출 (RC=$RC)"
rm -rf "$D"

echo "== L-11: bash 3.2(<4.4) --all fail-open 안 함 =="
if [ -x /bin/bash ]; then
  BV="$(/bin/bash -c 'echo $((BASH_VERSINFO[0]*100 + BASH_VERSINFO[1]))' 2>/dev/null || echo 999)"
  if [ "$BV" -lt 404 ]; then
    D=$(make_repo "leak.go|clientSecret = \"$FV_A\"")
    OUT=$(run_scan /bin/bash "$D" --all); RC=$?
    { [ "$RC" -ne 0 ] && grep -q 'leak.go' <<<"$OUT"; } \
      && pass "bash $BV(/bin/bash) --all: 시크릿 검출·exit≠0 (fail-open 아님)" \
      || fail "bash $BV --all fail-open: RC=$RC, 검출=$(grep -c 'leak.go' <<<"$OUT")"
    rm -rf "$D"
  else
    pass "SKIP: /bin/bash가 $BV(≥4.4) — bash 3.2 미보유 환경"
  fi
else
  pass "SKIP: /bin/bash 없음"
fi

echo "== allowlist: 매니페스트는 (경로 + 완전일치 리터럴)로 좁게 pin 한다 =="
# 핵심 안전 속성: allowlist가 파일 전체나 값 하나를 통째로 열면 그게 새 false-green이다.
# 같은 파일의 '다른 값'과, 다른 파일의 '같은 값'은 반드시 계속 검출돼야 한다.
D=$(make_repo \
  "$ALLOW|fixtures.go|Secret = \"$FV_B|합성 픽스처" \
  "fixtures.go|const fixtureSecret = \"$FV_B\"
const otherSecret = \"$FV_C\"" \
  "elsewhere.go|const copiedSecret = \"$FV_B\"")
OUT=$(run_scan bash "$D" --all); RC=$?
{ ! grep -q 'fixtures.go:1:' <<<"$OUT"; } && pass "(경로+완전일치) 매치는 제외됨" || fail "매니페스트가 동작 안 함"
grep -q "$FV_C" <<<"$OUT" && pass "같은 파일의 '다른 값'은 계속 검출(리터럴 pin)" \
  || fail "allowlist가 파일 전체를 열어버림(false-green)"
grep -q 'elsewhere.go' <<<"$OUT" && pass "'다른 파일'의 같은 값은 계속 검출(경로 pin)" \
  || fail "allowlist가 값만으로 열림(false-green)"
[ "$RC" -eq 1 ] && pass "허용되지 않은 시크릿 존재 시 exit 1" || fail "exit=$RC (1 기대)"
rm -rf "$D"

# 매니페스트 자신도 스캔 대상이라, 항목의 리터럴이 매니페스트 줄에서도 매치된다.
# 실제 레포와 동일하게 그 자기참조 줄에 대한 항목을 함께 둔다(둘 다 완전일치로 허용).
D=$(make_repo \
  "$ALLOW|only.go|Secret = \"$FV_B|합성 픽스처
$ALLOW|Secret = \"$FV_B|자기참조 줄" \
  "only.go|const fixtureSecret = \"$FV_B\"")
OUT=$(run_scan bash "$D" --all); RC=$?
[ "$RC" -eq 0 ] && pass "매니페스트로 전부 허용되면 exit 0" || { fail "exit=$RC (0 기대)"; printf '%s\n' "$OUT"; }
rm -rf "$D"

# 완전일치여야 한다: 부분일치면 허용 리터럴을 접두/접미로 품은 더 긴 값이 그대로 통과한다.
D=$(make_repo \
  "$ALLOW|ext.go|Secret = \"$FV_B|합성 픽스처" \
  "ext.go|const fixtureSecret = \"$FV_B\"
const sneakySecret = \"$FV_N\"")
OUT=$(run_scan bash "$D" --all); RC=$?
{ ! grep -q 'ext.go:1:' <<<"$OUT"; } && pass "완전일치하는 픽스처는 제외" || fail "완전일치 제외가 동작 안 함"
grep -q 'PLUSREALSECRET' <<<"$OUT" && pass "허용 리터럴을 접두로 품은 더 긴 값은 검출(부분일치 우회 차단)" \
  || fail "접두 확장으로 allowlist 우회 가능(false-green)"
[ "$RC" -eq 1 ] && pass "확장 값 존재 시 exit 1" || fail "exit=$RC (1 기대)"
rm -rf "$D"

# 인라인 주석 마커는 우회 수단이 아니다: 인정하면 PR이 진짜 자격증명 옆에 주석만 달아
# CI 게이트를 통과할 수 있다(우회 수단이 스캔 대상과 같은 신뢰 경계에 놓임).
D=$(make_repo "inline.go|const someSecret = \"$FV_H\" // scan-allow: 우회 시도")
OUT=$(run_scan bash "$D" --all); RC=$?
{ [ "$RC" -eq 1 ] && grep -q 'inline.go' <<<"$OUT"; } \
  && pass "인라인 주석 마커로는 우회 불가(매니페스트만 유효)" \
  || fail "인라인 마커로 게이트 우회 가능(false-green): RC=$RC"
rm -rf "$D"

echo "== 제외 목록은 '값'에만 적용된다 (같은 줄 산문으로 우회 불가) =="
# 줄 전체에 제외 regex를 걸면 진짜 자격증명 옆에 'example' 한 단어만 적어도 숨길 수 있다.
D=$(make_repo \
  "prose1.yml|client_secret: $FV_D # example value copied from docs" \
  "prose2.yml|api_key: $FV_E (placeholder for now)" \
  "prose3.yml|access_token: $FV_F # YOUR_TEAM should rotate this")
OUT=$(run_scan bash "$D" --all); RC=$?
grep -q 'prose1.yml' <<<"$OUT" && pass "값 뒤 'example' 주석으로 우회 불가" || fail "example 산문으로 시크릿 은닉(false-green)"
grep -q 'prose2.yml' <<<"$OUT" && pass "값 뒤 'placeholder' 산문으로 우회 불가" || fail "placeholder 산문으로 은닉(false-green)"
grep -q 'prose3.yml' <<<"$OUT" && pass "값 뒤 'YOUR_' 산문으로 우회 불가" || fail "YOUR_ 산문으로 은닉(false-green)"
[ "$RC" -eq 1 ] && pass "산문 우회 시도에도 exit 1" || fail "exit=$RC (1 기대)"
rm -rf "$D"

# 반대 방향: 값 자체가 진짜 placeholder면 계속 제외돼야 한다(과검출 방지 유지).
D=$(make_repo \
  "ph1.go|const clientSecret = \"$PH_A\"" \
  "ph2.yml|client_secret: $PH_B")
OUT=$(run_scan bash "$D" --all); RC=$?
[ "$RC" -eq 0 ] && pass "값 자체가 placeholder면 제외 유지(exit 0)" || { fail "placeholder 값 과검출(exit=$RC)"; printf '%s\n' "$OUT"; }
rm -rf "$D"

echo "== 한 줄의 매치를 전부 평가한다 (앞 매치로 뒤의 실제 키를 숨길 수 없음) =="
# 압축된 JSON/YAML은 한 줄에 여러 키/값이 온다. '첫 매치'나 '줄 전체'로 판정하면
# 앞의 example/allowlist 매치 하나가 뒤의 진짜 자격증명을 통째로 가린다(false-green).
D=$(make_repo \
  "$ALLOW|conf.json|client_secret\":\"$FV_G|합성 픽스처" \
  "json1.json|{\"client_secret\":\"$PH_B\",\"api_key\":\"$FV_D\"}" \
  "conf.json|{\"client_secret\":\"$FV_G\",\"api_key\":\"$FV_E\"}")
OUT=$(run_scan bash "$D" --all); RC=$?
grep -q 'json1.json' <<<"$OUT" && pass "앞 매치가 placeholder여도 뒤의 실제 키 검출" \
  || fail "첫 매치 placeholder가 줄 전체를 은닉(false-green)"
grep -q 'conf.json' <<<"$OUT" && pass "앞 매치가 allowlist여도 뒤의 실제 키 검출" \
  || fail "allowlist 매치가 줄 전체를 은닉(false-green)"
[ "$RC" -eq 1 ] && pass "동일 줄 다중 매치 시 exit 1" || fail "exit=$RC (1 기대)"
rm -rf "$D"

echo "== 스캐너 디렉토리에 사각지대가 없다 (회귀 스위트 포함 전부 스캔) =="
# 어떤 파일이든 통째로 빼면 '보호는 되지만 검사되지 않는' 사각지대가 되어, 거기 실제
# 자격증명이 들어가도 CI가 초록으로 통과한다. 이 스위트 파일 자신도 예외가 아니다.
D=$(make_repo \
  ".claude/skills/opensource-maintainer/SKILL.md|client_secret: $FV_K" \
  "$ALLOW|# 주석뿐인 매니페스트" \
  ".claude/skills/opensource-maintainer/newfile.md|client_secret: $FV_L" \
  ".claude/skills/opensource-maintainer/scripts/scan_test.sh|client_secret: $FV_M")
OUT=$(run_scan bash "$D" --all); RC=$?
grep -q 'SKILL.md' <<<"$OUT" && pass "스킬 문서의 시크릿 검출" || fail "스킬 문서가 사각지대(false-green)"
grep -q 'newfile.md' <<<"$OUT" && pass "디렉토리에 새로 생긴 파일도 검출" || fail "새 파일이 사각지대(false-green)"
grep -q 'scan_test.sh' <<<"$OUT" && pass "회귀 스위트 파일의 시크릿도 검출(제외 없음)" \
  || fail "회귀 스위트가 사각지대(false-green)"
[ "$RC" -eq 1 ] && pass "스캐너 디렉토리 유출 시 exit 1" || fail "exit=$RC (1 기대)"
rm -rf "$D"

echo "== --content-only: 커밋 설정 점검만 건너뛰고 내용 스캔은 유지 =="
# CI 러너엔 user.email이 없어 설정 점검이 항상 실패한다 → --content-only로 그 절만 건너뛴다.
# 단, 내용(시크릿) 스캔이 함께 약해지면 안 된다.
D=$(make_repo 'clean.md|이 레포는 Toss Open API를 사용한다.')
git -C "$D" config user.email "$MAIL"   # noreply 아님 → 설정 점검 실패 유발
OUT=$(run_scan bash "$D" --all); RC=$?
[ "$RC" -eq 1 ] && pass "비-noreply 이메일은 기본 모드에서 후보(exit 1)" || fail "설정 점검이 동작 안 함(exit=$RC)"
OUT=$(run_scan bash "$D" --all --content-only); RC=$?
[ "$RC" -eq 0 ] && pass "--content-only는 설정 점검을 건너뜀(exit 0)" || { fail "--content-only exit=$RC (0 기대)"; printf '%s\n' "$OUT"; }
rm -rf "$D"

D=$(make_repo "leak.yml|client_secret: $FV_A")
OUT=$(run_scan bash "$D" --all --content-only); RC=$?
{ [ "$RC" -eq 1 ] && grep -q 'leak.yml' <<<"$OUT"; } && pass "--content-only에서도 시크릿은 그대로 검출" \
  || fail "--content-only가 내용 스캔을 약화시킴(RC=$RC)"
rm -rf "$D"

echo "== fail-closed: 알 수 없는 인자 → exit 2 =="
D=$(make_repo 'clean.md|정상 파일')
OUT=$(run_scan bash "$D" --bogus-flag); RC=$?
[ "$RC" -eq 2 ] && pass "오타/미지원 플래그는 exit 2(게이트 약화 방지)" || fail "미지원 플래그 exit=$RC (2 기대)"
rm -rf "$D"

echo "== 정상: 깨끗한 레포는 exit 0 (거짓 양성 없음) =="
D=$(make_repo \
  'main.go|package main

func main() { secretHandler() }' \
  'readme.md|이 봇은 Toss Open API를 사용한다.')
OUT=$(run_scan bash "$D" --all); RC=$?
[ "$RC" -eq 0 ] && pass "깨끗한 레포 exit 0" || { fail "깨끗한 레포인데 exit=$RC"; printf '%s\n' "$OUT"; }
rm -rf "$D"

echo "== fail-closed: git 레포 아님 → exit 2 =="
D=$(mktemp -d)
# GIT_CEILING_DIRECTORIES로 상위 탐색을 막아, 임시 디렉토리의 조상이 우연히 git 레포여도
# (예: 일부 CI runner의 TMPDIR 배치) 확실히 "레포 아님"으로 판정되게 한다(테스트 hermetic).
export GIT_CEILING_DIRECTORIES="$(dirname "$D")"
OUT=$(run_scan bash "$D"); RC=$?
unset GIT_CEILING_DIRECTORIES
[ "$RC" -eq 2 ] && pass "비-git 디렉토리 exit 2(fail-closed)" || fail "비-git 디렉토리 exit=$RC (2 기대)"
rm -rf "$D"

echo
echo "결과: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
