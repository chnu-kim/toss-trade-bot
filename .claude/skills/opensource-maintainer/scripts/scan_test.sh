#!/usr/bin/env bash
# scan.sh 회귀 테스트 (issue #27: M-6 / L-11 / L-12).
#
# 각 케이스를 격리된 임시 git 레포에서 재현해 scan.sh의 종료코드·검출을 검증한다.
# 실제 레포는 절대 건드리지 않는다. 모든 케이스 통과 시 exit 0, 하나라도 실패 시 exit 1.
#
# 이 파일은 .claude/skills/opensource-maintainer/ 아래(scan.sh 자기-제외 경로)에 있어
# 아래 픽스처 문자열이 실제 게이트를 self-trip하지 않는다. 픽스처 값은 합성(가짜)이다.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
SCAN="$HERE/scan.sh"
PASS=0
FAIL=0

pass() { printf '  ✅ %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf '  ❌ %s\n' "$1"; FAIL=$((FAIL + 1)); }

# 격리된 임시 레포를 만들고 그 경로를 echo 한다. $1 이후 인자는 "파일명|내용" 픽스처.
make_repo() {
  local dir; dir="$(mktemp -d)"
  git -C "$dir" init -q
  git -C "$dir" config user.email "t@users.noreply.github.com"
  git -C "$dir" config user.name "t"
  git -C "$dir" config commit.gpgsign false
  local spec name body
  for spec in "$@"; do
    name="${spec%%|*}"; body="${spec#*|}"
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
  'walrus.go|clientSecret := "AbCdEf123456789xyz"' \
  'yaml.yml|client_secret: AbCdEf123456789xyz' \
  'eq.go|clientSecret = "AbCdEf123456789xyz"')
OUT=$(run_scan bash "$D" --all); RC=$?
grep -q 'walrus.go' <<<"$OUT" && pass "clientSecret := \"…\" (Go :=) 검출" || fail ":= 케이스 미검출"
grep -q 'yaml.yml'  <<<"$OUT" && pass "client_secret: … (따옴표 없음) 검출" || fail "따옴표 없는 YAML 미검출"
grep -q 'eq.go'     <<<"$OUT" && pass "clientSecret = \"…\" (회귀 보존) 검출" || fail "기존 = 케이스 회귀"
[ "$RC" -eq 1 ] && pass "시크릿 존재 시 exit 1(차단)" || fail "시크릿 존재하는데 exit=$RC (fail-open 위험)"
rm -rf "$D"

echo "== M-6b: .go의 따옴표 없는 식별자 대입은 과검출 안 함 (codex P2) =="
# Go는 문자열이 항상 따옴표 → .go의 따옴표 없는 RHS는 코드 식별자일 뿐 시크릿 아님.
D=$(make_repo \
  'field.go|clientSecret: clientSecret,' \
  'fake.go|GH_TOKEN=fake-token-never-used')
OUT=$(run_scan bash "$D" --all); RC=$?
{ ! grep -q 'field.go' <<<"$OUT"; } && pass ".go 구조체 필드 clientSecret: clientSecret 미검출" || fail "식별자 대입 과검출(field.go)"
{ ! grep -q 'fake.go'  <<<"$OUT"; } && pass ".go 따옴표없는 값 미검출(fake-token)" || fail "과검출(fake.go)"
[ "$RC" -eq 0 ] && pass ".go 식별자만 있는 레포 exit 0(거짓 양성 없음)" || { fail ".go 식별자 대입인데 exit=$RC"; printf '%s\n' "$OUT"; }
rm -rf "$D"

echo "== M-6c: 비-Go 따옴표 없는 시크릿은 숫자 유무·위치 무관 검출 (adversarial [high]) =="
# 숫자 휴리스틱이 놓치던 케이스: 끝자리 숫자 토큰·무숫자 토큰. 파일-타입 판별이 이를 fail-closed로 잡는다.
# (합성값 — 프로바이더 시크릿 프리픽스는 피한다. GitHub push protection이 실키로 오인해 push를 막음)
D=$(make_repo \
  'digit_end.yml|api_key: syntheticcredentialvalue9' \
  'all_alpha.yml|api_key: syntheticcredentialvaluexyz')
OUT=$(run_scan bash "$D" --all); RC=$?
grep -q 'digit_end.yml' <<<"$OUT" && pass "끝자리 숫자 토큰 검출(false-green 아님)" || fail "끝자리 숫자 토큰 미검출(false-green)"
grep -q 'all_alpha.yml' <<<"$OUT" && pass "무숫자 토큰 검출(false-green 아님)" || fail "무숫자 토큰 미검출(false-green)"
[ "$RC" -eq 1 ] && pass "따옴표 없는 시크릿 존재 시 exit 1" || fail "exit=$RC (1 기대)"
rm -rf "$D"

echo "== L-12: 개인 도구 식별자(1Password) 검출 =="
D=$(make_repo 'doc.md|서명 키는 1Password agent에 있다')
OUT=$(run_scan bash "$D" --all); RC=$?
{ grep -q 'doc.md' <<<"$OUT" && [ "$RC" -eq 1 ]; } && pass "1Password 문구 검출·exit 1" || fail "1Password 미검출 (RC=$RC)"
rm -rf "$D"

echo "== L-11: bash 3.2(<4.4) --all fail-open 안 함 =="
if [ -x /bin/bash ]; then
  BV="$(/bin/bash -c 'echo $((BASH_VERSINFO[0]*100 + BASH_VERSINFO[1]))' 2>/dev/null || echo 999)"
  if [ "$BV" -lt 404 ]; then
    D=$(make_repo 'leak.go|clientSecret = "AbCdEf123456789xyz"')
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

echo "== allowlist: scan-allow 마커는 '그 줄만' 허용한다 =="
# 핵심 안전 속성: 마커가 파일/디렉토리 전체를 삼키면 그게 새 false-green이다.
# 같은 파일에 마커 있는 줄과 없는 줄을 두고, 없는 줄은 반드시 계속 검출돼야 한다.
D=$(make_repo \
  'mixed.go|const allowedSecret = "AbCdEf123456789xyz" // scan-allow: 합성 픽스처
const leakedSecret = "ZyXwVu987654321qrs"')
OUT=$(run_scan bash "$D" --all); RC=$?
{ ! grep -q 'AbCdEf123456789xyz' <<<"$OUT"; } && pass "마커 있는 줄은 제외됨" || fail "마커가 동작 안 함"
grep -q 'ZyXwVu987654321qrs' <<<"$OUT" && pass "같은 파일의 마커 없는 시크릿은 여전히 검출(파일 전체를 삼키지 않음)" \
  || fail "allowlist가 같은 파일의 진짜 시크릿까지 삼킴(false-green)"
[ "$RC" -eq 1 ] && pass "마커 없는 시크릿 존재 시 exit 1" || fail "exit=$RC (1 기대)"
rm -rf "$D"

D=$(make_repo 'only_allowed.go|const allowedSecret = "AbCdEf123456789xyz" // scan-allow: 합성 픽스처')
OUT=$(run_scan bash "$D" --all); RC=$?
[ "$RC" -eq 0 ] && pass "마커로 전부 허용되면 exit 0" || { fail "exit=$RC (0 기대)"; printf '%s\n' "$OUT"; }
rm -rf "$D"

echo "== --content-only: 커밋 설정 점검만 건너뛰고 내용 스캔은 유지 =="
# CI 러너엔 user.email이 없어 설정 점검이 항상 실패한다 → --content-only로 그 절만 건너뛴다.
# 단, 내용(시크릿) 스캔이 함께 약해지면 안 된다.
D=$(make_repo 'clean.md|이 레포는 Toss Open API를 사용한다.')
git -C "$D" config user.email "personal@gmail.com"   # noreply 아님 → 설정 점검 실패 유발
OUT=$(run_scan bash "$D" --all); RC=$?
[ "$RC" -eq 1 ] && pass "비-noreply 이메일은 기본 모드에서 후보(exit 1)" || fail "설정 점검이 동작 안 함(exit=$RC)"
OUT=$(run_scan bash "$D" --all --content-only); RC=$?
[ "$RC" -eq 0 ] && pass "--content-only는 설정 점검을 건너뜀(exit 0)" || { fail "--content-only exit=$RC (0 기대)"; printf '%s\n' "$OUT"; }
rm -rf "$D"

D=$(make_repo 'leak.yml|client_secret: AbCdEf123456789xyz')
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
