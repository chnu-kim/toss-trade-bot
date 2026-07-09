#!/usr/bin/env bash
# scripts/verify-credential-narrowing.sh
#
# ADR-0011 point 5 ② / Consequences 실측 목록 6·7 (i)~(ix)·8 의 capability 실측.
# loop 자격증명 narrowing(#46)이 "됐다"를 오퍼레이터 단언이 아니라 실측으로 증명한다.
# 세 공격 벡터 — (a) admin protection-편집 · (b) check-위조 · (c) 승인-위조 — 가
# 실제로 닫혔는지를 HTTP 상태 코드 기준으로 확인하고, 항목별 PASS/FAIL을 보고한다.
#
# ⚠️ 완료 판정은 실측만이다. 어느 한 항목이라도 FAIL이면 ②는 미완이고 Phase A 진입
#    불가다(비-0 종료). 특히 (7-ix)가 ② 완료의 최종 판정이다.
#
# 보안(public 레포): 토큰·키 값을 출력·저장·로그에 남기지 않는다. 모든 시크릿은
#   환경변수로만 받고, HTTP 상태 코드와 판정만 출력한다.
#
# ── 사용법 ────────────────────────────────────────────────────────────────
#   NEW_PAT=…                    (필수) narrowing 후의 fine-grained PAT
#                                 (Contents:RW + PR:read + Issues:RW + Admin:read)
#   OLD_ADMIN_TOKEN=…            (teardown 확인 시) 폐기했어야 할 구 admin classic PAT
#   APPROVE_TARGET_PR=<n>        (7-vi) APPROVE 프로브 대상 — 반드시 비-chnu-kim 작성
#   MERGE_TARGET_PR=<n>          (7-viii) 게이트-미충족 열린 PR 번호
#   COMMENT_TARGET_PR=<n>        (7-vii) 코멘트 프로브 대상 PR/이슈 번호
#   OWNER_REPO=chnu-kim/toss-trade-bot   (기본값: git remote에서 유도)
#   RUN_PUSH_TESTS=1             (목록 6·8) 실제 git push 거부 테스트 활성(임시 브랜치 생성)
#
#   예:  NEW_PAT=$FG_PAT APPROVE_TARGET_PR=99 MERGE_TARGET_PR=99 \
#        COMMENT_TARGET_PR=99 ./scripts/verify-credential-narrowing.sh
#
# ── 종료 코드 ─────────────────────────────────────────────────────────────
#   0  전 항목 PASS(수행된 항목 기준) — ② 완료 판정 가능
#   1  하나 이상 FAIL 또는 판정불능(INCONCLUSIVE) — ② 미완
#   2  실행 오류(필수 도구·인자 부재)
set -uo pipefail

API="https://api.github.com"
FAILED=0
INCONCLUSIVE=0

# ── 유틸 ──────────────────────────────────────────────────────────────────
need() { command -v "$1" >/dev/null 2>&1 || { echo "❌ 필요한 도구 없음: $1"; exit 2; }; }
need curl

# OWNER_REPO: 명시 없으면 origin remote에서 유도(git이 있을 때만).
if [ -z "${OWNER_REPO:-}" ]; then
  origin=$(git config --get remote.origin.url 2>/dev/null || true)
  OWNER_REPO=$(printf '%s' "$origin" | sed -E 's#(git@github\.com:|https://github\.com/)##; s#\.git$##')
fi
[ -n "${OWNER_REPO:-}" ] || { echo "❌ OWNER_REPO를 유도할 수 없음 — 환경변수로 지정하라"; exit 2; }

section() { printf '\n=== %s ===\n' "$1"; }

# curl_status METHOD PATH TOKEN [BODY]
#   토큰을 헤더로만 전달하고, 응답 바디는 버리고 HTTP 상태 코드만 stdout으로 반환한다.
#   토큰 값은 어디에도 출력되지 않는다.
curl_status() {
  local method="$1" path="$2" token="$3" body="${4:-}"
  local args=(-s -o /dev/null -w '%{http_code}' -X "$method"
    -H "Authorization: Bearer ${token}"
    -H "Accept: application/vnd.github+json"
    -H "X-GitHub-Api-Version: 2022-11-28")
  [ -n "$body" ] && args+=(-H "Content-Type: application/json" -d "$body")
  curl "${args[@]}" "${API}${path}" 2>/dev/null || echo "000"
}

# 판정 헬퍼. 실제 상태 코드만 찍고 토큰은 절대 안 찍는다.
pass() { printf '  ✅ PASS  [%s] %s (HTTP %s)\n' "$1" "$2" "$3"; }
fail() { printf '  ❌ FAIL  [%s] %s (HTTP %s)\n' "$1" "$2" "$3"; FAILED=1; }
inconc(){ printf '  ⚠️  판정불능 [%s] %s (HTTP %s)\n' "$1" "$2" "$3"; INCONCLUSIVE=1; }
skip()  { printf '  ⏭️  SKIP  [%s] %s\n' "$1" "$2"; }

# expect_status ID DESC TOKEN METHOD PATH WANT [BODY]
#   WANT 코드와 일치하면 PASS, 아니면 FAIL.
expect_status() {
  local id="$1" desc="$2" token="$3" method="$4" path="$5" want="$6" body="${7:-}"
  local got; got=$(curl_status "$method" "$path" "$token" "$body")
  if [ "$got" = "$want" ]; then pass "$id" "$desc" "$got"; else fail "$id" "$desc(기대 $want)" "$got"; fi
}

# expect_denied ID DESC TOKEN METHOD PATH [BODY]
#   권한-부재 거부(403/404)면 PASS. 성공(2xx)이면 FAIL(벡터가 열려 있다는 직접 증거).
#   GitHub은 fine-grained PAT의 권한 부재를 403(Resource not accessible) 또는 404로 반환한다.
expect_denied() {
  local id="$1" desc="$2" token="$3" method="$4" path="$5" body="${6:-}"
  local got; got=$(curl_status "$method" "$path" "$token" "$body")
  case "$got" in
    403|404) pass "$id" "$desc — 권한-부재 거부" "$got" ;;
    2*)      fail "$id" "$desc — 성공(=권한 잔존, 벡터 열림)" "$got" ;;
    *)       inconc "$id" "$desc — 예상외 응답" "$got" ;;
  esac
}

# ── 필수 인자 ─────────────────────────────────────────────────────────────
[ -n "${NEW_PAT:-}" ] || { echo "❌ NEW_PAT(narrowing 후 fine-grained PAT) 환경변수 필수"; exit 2; }

echo "🔎 자격증명 narrowing capability 실측 — ${OWNER_REPO}"
echo "   (토큰 값은 출력되지 않습니다. HTTP 상태 코드와 판정만 표시)"

# ══════════════════════════════════════════════════════════════════════════
# A. 새 PAT capability (ADR-0011 목록 7 (i)~(viii)) — '같은 PAT로'의 확인.
#    새 PAT의 권한 상한만 증명한다(기존 admin 토큰 잔존은 (7-ix)가 별도 확인).
# ══════════════════════════════════════════════════════════════════════════
section "A. 새 PAT capability (목록 7 i~viii)"

# (7-i) repository_dispatch 성공(204) — PR-생성 workflow 트리거 능력(Contents:write).
expect_status "7-i" "repository_dispatch 트리거" "$NEW_PAT" POST \
  "/repos/${OWNER_REPO}/dispatches" "204" '{"event_type":"verify-narrowing-probe"}'

# (7-ii) workflow_dispatch API는 Actions:write 부재로 거부돼야 한다.
#   대상 워크플로 파일명은 고정(ci.yml). 권한 부재면 403/404.
expect_denied "7-ii" "workflow_dispatch(Actions:write 부재)" "$NEW_PAT" POST \
  "/repos/${OWNER_REPO}/actions/workflows/ci.yml/dispatches" '{"ref":"main"}'

# (7-iii) Issues RW — 생성·라벨·close. 실패하면 issue 파이프라인이 죽는다.
#   생성만 실측(라벨/close는 생성된 번호에 이어서). 201이면 PASS.
iss_status=$(curl -s -w '\n%{http_code}' -X POST \
  -H "Authorization: Bearer ${NEW_PAT}" -H "Accept: application/vnd.github+json" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  -d '{"title":"[probe] narrowing verify — auto-close","body":"자동 생성된 실측 프로브. 즉시 close됨."}' \
  "${API}/repos/${OWNER_REPO}/issues" 2>/dev/null)
iss_code=$(printf '%s' "$iss_status" | tail -n1)
if [ "$iss_code" = "201" ]; then
  iss_num=$(printf '%s' "$iss_status" | sed '$d' | grep -o '"number"[[:space:]]*:[[:space:]]*[0-9]*' | head -1 | grep -o '[0-9]*')
  pass "7-iii" "이슈 생성(Issues:write)" "$iss_code"
  if [ -n "${iss_num:-}" ]; then
    lc=$(curl_status POST "/repos/${OWNER_REPO}/issues/${iss_num}/labels" "$NEW_PAT" '{"labels":["agent:ready"]}')
    if [ "$lc" = "200" ]; then pass "7-iii" "라벨 부여" "$lc"; else fail "7-iii" "라벨 부여" "$lc"; fi
    cc=$(curl_status PATCH "/repos/${OWNER_REPO}/issues/${iss_num}" "$NEW_PAT" '{"state":"closed"}')
    if [ "$cc" = "200" ]; then pass "7-iii" "이슈 close(#${iss_num})" "$cc"; else fail "7-iii" "이슈 close(#${iss_num})" "$cc"; fi
  fi
else
  fail "7-iii" "이슈 생성(Issues:write)" "$iss_code"
fi

# (7-iv) Administration:read — protection 조회 200(presence-check (b) 통과 가능성).
expect_status "7-iv" "protection 조회(Administration:read)" "$NEW_PAT" GET \
  "/repos/${OWNER_REPO}/branches/main/protection" "200"

# (7-v) Administration:write 부재 — PUT protection 거부. (a) admin 벡터 폐쇄 증거.
#   유효한 최소 바디를 보내되, 거부는 권한 계층에서 일어나므로 바디 내용과 무관.
expect_denied "7-v" "PUT protection(Administration:write 부재)" "$NEW_PAT" PUT \
  "/repos/${OWNER_REPO}/branches/main/protection" \
  '{"required_status_checks":null,"enforce_admins":true,"required_pull_request_reviews":null,"restrictions":null}'

# (7-vi) PR:write 부재 — APPROVE 거부. (c) 승인-위조 폐쇄 증거.
#   ★ 403(권한-부재)만 PASS. 422(self-approval 차단)는 approve-capable 자격증명에도
#     발생하므로 판정불능 → 비-chnu-kim 작성 PR로 재실측하라.
if [ -n "${APPROVE_TARGET_PR:-}" ]; then
  ac=$(curl_status POST "/repos/${OWNER_REPO}/pulls/${APPROVE_TARGET_PR}/reviews" "$NEW_PAT" '{"event":"APPROVE"}')
  case "$ac" in
    403|404) pass "7-vi" "APPROVE 거부(PR:write 부재)" "$ac" ;;
    422)     inconc "7-vi" "self-approval 차단(대상이 chnu-kim 작성?) — 비-chnu-kim PR로 재실측" "$ac" ;;
    2*)      fail "7-vi" "APPROVE 성공(=PR:write 잔존, (c) 벡터 열림)" "$ac" ;;
    *)       inconc "7-vi" "예상외 응답" "$ac" ;;
  esac
else
  skip "7-vi" "APPROVE_TARGET_PR 미지정(비-chnu-kim 작성 PR 번호 필요)"
fi

# (7-vii) Issues:write로 PR conversation 코멘트 성공 여부 — 실측·기록(판정이 아니라 관측).
if [ -n "${COMMENT_TARGET_PR:-}" ]; then
  cc=$(curl_status POST "/repos/${OWNER_REPO}/issues/${COMMENT_TARGET_PR}/comments" "$NEW_PAT" \
    '{"body":"[probe] narrowing verify — 코멘트 capability 관측(무시 가능)."}')
  if [ "$cc" = "201" ]; then
    pass "7-vii" "PR 코멘트(Issues:write) 성공" "$cc"
  else
    printf '  ℹ️  관측 [7-vii] PR 코멘트 실패 — 코멘트 동작 App 이관 + ADR amend 후속 필요 (HTTP %s)\n' "$cc"
  fi
else
  skip "7-vii" "COMMENT_TARGET_PR 미지정"
fi

# (7-viii) 게이트-미충족 PR에 merge 호출 시 branch protection이 거부.
#   ★ 이 항목은 enforce_admins=true 라야 유효하다 — false면 admin-user 토큰이 우회해
#     merge가 성공(=거부 관측 불가)한다. enforce_admins 상태를 먼저 확인해 안내한다.
if [ -n "${MERGE_TARGET_PR:-}" ]; then
  ea=$(curl -s -H "Authorization: Bearer ${NEW_PAT}" -H "Accept: application/vnd.github+json" \
    "${API}/repos/${OWNER_REPO}/branches/main/protection/enforce_admins" 2>/dev/null \
    | grep -o '"enabled"[[:space:]]*:[[:space:]]*\(true\|false\)' | grep -o 'true\|false')
  if [ "${ea:-}" = "false" ]; then
    inconc "7-viii" "enforce_admins=false — 유효한 판정 불가(먼저 활성화 후 재실측)" "n/a"
  else
    mc=$(curl_status PUT "/repos/${OWNER_REPO}/pulls/${MERGE_TARGET_PR}/merge" "$NEW_PAT" '{}')
    case "$mc" in
      405|409|403|422) pass "7-viii" "게이트-미충족 merge 거부(branch protection)" "$mc" ;;
      200)             fail "7-viii" "merge 성공(=게이트 우회)" "$mc" ;;
      *)               inconc "7-viii" "예상외 응답" "$mc" ;;
    esac
  fi
else
  skip "7-viii" "MERGE_TARGET_PR 미지정"
fi

# ══════════════════════════════════════════════════════════════════════════
# B. (7-ix) ② 완료 판정 — loop가 실제 resolve하는 자격증명(명시 토큰 없이)으로
#    PUT protection·APPROVE를 시도해 둘 다 거부되는지. (i)~(viii)는 새 PAT의 상한만
#    증명하고, 기존 admin 토큰이 loop 컨텍스트에서 더 이상 resolve되지 않는다는 것은
#    이 항목만이 증명한다. 이 스크립트는 실행 컨텍스트의 ambient gh/git 자격증명을 쓴다.
# ══════════════════════════════════════════════════════════════════════════
section "B. (7-ix) ② 완료 판정 — ambient loop 자격증명"

# 자격증명 표면 인벤토리(값은 출력하지 않고 존재 여부만).
printf '  자격증명 표면 점검(값 미출력):\n'
printf '    - GH_TOKEN 설정됨: %s\n'     "$([ -n "${GH_TOKEN:-}" ] && echo yes || echo no)"
printf '    - GITHUB_TOKEN 설정됨: %s\n' "$([ -n "${GITHUB_TOKEN:-}" ] && echo yes || echo no)"
if command -v gh >/dev/null 2>&1; then
  gh auth status >/dev/null 2>&1 && printf '    - gh 캐시 세션: 있음\n' || printf '    - gh 캐시 세션: 없음\n'
fi

if command -v gh >/dev/null 2>&1; then
  # ambient gh로 PUT protection 시도 — 거부(비-0 + HTTP 4xx)면 PASS.
  perr=$(gh api --method PUT "repos/${OWNER_REPO}/branches/main/protection" \
    -f 'enforce_admins=true' 2>&1 >/dev/null); prc=$?
  if [ $prc -ne 0 ] && printf '%s' "$perr" | grep -qE 'HTTP (403|404)'; then
    pass "7-ix" "ambient PUT protection 거부" "$(printf '%s' "$perr" | grep -oE 'HTTP [0-9]+' | head -1)"
  elif [ $prc -eq 0 ]; then
    fail "7-ix" "ambient PUT protection 성공(=admin 잔존 자격증명)" "2xx"
  else
    inconc "7-ix" "ambient PUT protection — 예상외" "$(printf '%s' "$perr" | grep -oE 'HTTP [0-9]+' | head -1)"
  fi

  # ambient gh로 APPROVE 시도(대상 = 비-chnu-kim PR). 403만 PASS, 422는 판정불능.
  if [ -n "${APPROVE_TARGET_PR:-}" ]; then
    aerr=$(gh api --method POST "repos/${OWNER_REPO}/pulls/${APPROVE_TARGET_PR}/reviews" \
      -f 'event=APPROVE' 2>&1 >/dev/null); arc=$?
    acode=$(printf '%s' "$aerr" | grep -oE 'HTTP [0-9]+' | head -1)
    if [ $arc -ne 0 ] && printf '%s' "$aerr" | grep -qE 'HTTP (403|404)'; then
      pass "7-ix" "ambient APPROVE 거부(권한-부재)" "$acode"
    elif printf '%s' "$aerr" | grep -qE 'HTTP 422'; then
      inconc "7-ix" "ambient APPROVE 422(self-approval?) — 비-chnu-kim PR로 재실측" "$acode"
    elif [ $arc -eq 0 ]; then
      fail "7-ix" "ambient APPROVE 성공(=approve-capable 잔존)" "2xx"
    else
      inconc "7-ix" "ambient APPROVE — 예상외" "$acode"
    fi
  else
    skip "7-ix" "APPROVE_TARGET_PR 미지정 — ambient APPROVE 프로브 생략"
  fi
else
  skip "7-ix" "gh CLI 없음 — ambient 자격증명 프로브 불가(loop 컨텍스트에서 실행하라)"
fi

# ══════════════════════════════════════════════════════════════════════════
# C. teardown 실측 — 폐기가 '단언'이 아니라 실측인지.
# ══════════════════════════════════════════════════════════════════════════
section "C. teardown 실측"

# 구 admin 토큰 revoke 확인 — 그 토큰으로 GET /user가 401이면 폐기됨.
if [ -n "${OLD_ADMIN_TOKEN:-}" ]; then
  oc=$(curl_status GET "/user" "$OLD_ADMIN_TOKEN")
  if [ "$oc" = "401" ]; then
    pass "teardown" "구 admin 토큰 revoke(GET /user 401)" "$oc"
  else
    fail "teardown" "구 admin 토큰이 아직 유효(revoke 안 됨)" "$oc"
  fi
else
  skip "teardown" "OLD_ADMIN_TOKEN 미지정 — 구 admin 토큰 401 확인 생략"
fi

# SSH authentication key 등록 해제 — 계정 authentication key 목록에 loop push key 없음.
#   (fine-grained PAT엔 이 조회 권한이 없을 수 있어 ambient gh로 시도)
if command -v gh >/dev/null 2>&1; then
  if gh api "user/keys" >/dev/null 2>&1; then
    kcount=$(gh api "user/keys" --jq 'length' 2>/dev/null || echo "?")
    printf '  ℹ️  관측 [teardown] 계정 authentication key 수: %s — loop push key 부재를 육안 확인하라\n' "$kcount"
  else
    skip "teardown" "user/keys 조회 불가(권한/컨텍스트) — 웹 UI로 authentication key 목록 확인"
  fi
fi

# (목록 6·8) 실제 git push 거부 테스트 — 임시 브랜치 생성이 필요해 기본 비활성.
if [ "${RUN_PUSH_TESTS:-}" = "1" ]; then
  section "D. git push 거부 (목록 6·8) — 실측 활성"
  echo "  ⚠️  이 섹션은 임시 브랜치를 만들어 push 거부를 확인한다. 수동 절차 권장:"
  echo "     - (목록 6) 새 PAT(HTTPS)로 .github/workflows/*.yml 수정 커밋 push → 거부 확인"
  echo "     - (목록 8) authentication 해제한 SSH key로 push 시도 → 거부 확인"
  echo "     자동화는 후속 커밋에서 임시 브랜치 생성·정리 로직과 함께 추가한다."
else
  skip "목록6·8" "git push 거부 테스트 비활성(RUN_PUSH_TESTS=1로 활성) — 수동 실측 권장"
fi

# ══════════════════════════════════════════════════════════════════════════
section "요약"
if [ "$FAILED" = "1" ]; then
  echo "❌ FAIL 항목 있음 — ②(narrowing) 미완. Phase A 진입 불가. 위 FAIL을 해소 후 재실측하라."
  exit 1
elif [ "$INCONCLUSIVE" = "1" ]; then
  echo "⚠️  판정불능 항목 있음 — 원인(대상 PR이 chnu-kim 작성 / enforce_admins=false 등)을 해소 후 재실측하라."
  echo "    판정불능을 남긴 채 ② 완료로 간주하지 말 것(false-green이 #50 precondition ②로 전파된다)."
  exit 1
else
  echo "✅ 수행된 전 항목 PASS — ②(narrowing) 완료 판정 가능."
  echo "   결과 요약(항목별 HTTP 상태·판정, 시크릿 원문 제외)을 이슈 #46에 코멘트로 기록하라."
  exit 0
fi
