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
OUT=$(run_scan bash "$D"); RC=$?
[ "$RC" -eq 2 ] && pass "비-git 디렉토리 exit 2(fail-closed)" || fail "비-git 디렉토리 exit=$RC (2 기대)"
rm -rf "$D"

echo
echo "결과: PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
