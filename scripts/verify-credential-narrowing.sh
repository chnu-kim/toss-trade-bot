#!/usr/bin/env bash
# scripts/verify-credential-narrowing.sh
#
# ADR-0011 point 5 ② / Consequences 실측 목록 6·7 (i)~(ix)·8 의 capability 실측.
# loop 자격증명 narrowing(#46)이 "됐다"를 오퍼레이터 단언이 아니라 실측으로 증명한다.
# 세 공격 벡터 — (a) admin protection-편집 · (b) check-위조 · (c) 승인-위조 — 가
# 실제로 닫혔는지를 HTTP 상태 코드 기준으로 확인하고, 항목별 PASS/FAIL을 보고한다.
#
# ⚠️ 완료 판정은 실측만이다. 어느 mandatory 항목이라도 FAIL 또는 판정불능(INCONCLUSIVE)
#    이면 ②는 미완이고 Phase A 진입 불가다(exit 1). 특히 (7-ix)가 ② 완료의 최종
#    판정이다. **prereq(대상 PR·토큰 등)가 없어 mandatory 프로브를 못 돌리면 조용히
#    건너뛰지 않고 INCONCLUSIVE로 fail-closed한다** — 부분 실행이 exit 0(=②완료)으로
#    새면 false-green이 #50 precondition ②를 거쳐 Phase B까지 전파된다(PR#22 실패형).
#
# 파괴성 프로브(admin-write·workflow-write)는 **main을 건드리지 않는다** — 일회용
#    브랜치를 만들어 그 위에서만 시도하고 EXIT trap으로 정리한다. main에 직접 PUT하면
#    토큰이 과권한일 때(=잡으려는 실패 케이스) 보호 정책을 실제로 파괴한다.
#
# 보안(public 레포): 토큰·키 값을 출력·저장·로그에 남기지 않는다. 모든 시크릿은
#   환경변수로만 받고, HTTP 상태 코드와 판정만 출력한다.
#
# ── 사용법 ────────────────────────────────────────────────────────────────
#   NEW_PAT=…                    (필수) narrowing 후의 fine-grained PAT
#                                 (Contents:RW + PR:read + Issues:RW + Admin:read)
#   OLD_ADMIN_TOKEN=…            (mandatory) 폐기했어야 할 구 admin classic PAT
#   APPROVE_TARGET_PR=<n>        (mandatory) APPROVE 프로브 대상 — 반드시 비-chnu-kim 작성
#   MERGE_TARGET_PR=<n>          (mandatory) 게이트-미충족 열린 PR 번호
#   SSH_TEARDOWN_CONFIRMED=1     (mandatory) 목록 8(SSH auth 등록 해제·push 거부)을
#                                 수동 실측·확인했다는 명시 승인. 없으면 INCONCLUSIVE.
#   COMMENT_TARGET_PR=<n>        (관측·비차단) 코멘트 capability 대상 PR/이슈 번호
#   OWNER_REPO=chnu-kim/toss-trade-bot   (기본값: git remote에서 유도)
#
#   loop 실행 컨텍스트에서 실행하라 — (7-ix)는 명시 토큰 없이 ambient gh 자격증명을
#   써서 기존 admin 토큰 잔존을 잡는다. gh CLI 필수.
#
# ── 종료 코드 ─────────────────────────────────────────────────────────────
#   0  mandatory 전 항목 PASS — ② 완료 판정 가능
#   1  하나 이상 FAIL 또는 INCONCLUSIVE — ② 미완(부분 실행 포함)
#   2  실행 오류(필수 도구·인자 부재)
set -uo pipefail

API="https://api.github.com"
FAILED=0
INCONCLUSIVE=0
_PROBE_BRANCH=""   # 일회용 브랜치명(생성되면 채워짐) — EXIT trap이 정리한다.

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
pass()  { printf '  ✅ PASS  [%s] %s (HTTP %s)\n' "$1" "$2" "$3"; }
fail()  { printf '  ❌ FAIL  [%s] %s (HTTP %s)\n' "$1" "$2" "$3"; FAILED=1; }
inconc(){ printf '  ⚠️  판정불능 [%s] %s (HTTP %s)\n' "$1" "$2" "$3"; INCONCLUSIVE=1; }
observe(){ printf '  ℹ️  관측 [%s] %s (HTTP %s)\n' "$1" "$2" "$3"; }

# mandatory 프로브의 prereq가 없을 때: 조용한 skip이 아니라 fail-closed(INCONCLUSIVE).
mandatory_missing() { printf '  ⚠️  판정불능 [%s] %s\n' "$1" "$2 — mandatory prereq 부재, ② 완료 판정 불가"; INCONCLUSIVE=1; }

# expect_status ID DESC TOKEN METHOD PATH WANT [BODY] — WANT면 PASS, 아니면 FAIL.
expect_status() {
  local id="$1" desc="$2" token="$3" method="$4" path="$5" want="$6" body="${7:-}"
  local got; got=$(curl_status "$method" "$path" "$token" "$body")
  if [ "$got" = "$want" ]; then pass "$id" "$desc" "$got"; else fail "$id" "$desc(기대 $want)" "$got"; fi
}

# expect_denied ID DESC TOKEN METHOD PATH [BODY]
#   권한-부재 거부(403/404)면 PASS. 성공(2xx)이면 FAIL(벡터가 열려 있다는 직접 증거).
expect_denied() {
  local id="$1" desc="$2" token="$3" method="$4" path="$5" body="${6:-}"
  local got; got=$(curl_status "$method" "$path" "$token" "$body")
  case "$got" in
    403|404) pass "$id" "$desc — 권한-부재 거부" "$got" ;;
    2*)      fail "$id" "$desc — 성공(=권한 잔존, 벡터 열림)" "$got" ;;
    *)       inconc "$id" "$desc — 예상외 응답" "$got" ;;
  esac
}

# ── 일회용 브랜치: 파괴성 프로브(admin-write·workflow-write) 격리 ─────────────
# shellcheck disable=SC2329  # EXIT trap으로 간접 호출된다(아래 trap 등록).
cleanup_probe_branch() {
  [ -n "$_PROBE_BRANCH" ] || return 0
  # 보호가 걸렸을 수 있으니 먼저 해제 시도(성공 시 admin-write가 있었다는 뜻 — 이미 FAIL),
  # 그다음 브랜치 자체를 삭제(Contents:write). 실패해도 무해(일회용 브랜치일 뿐).
  curl -s -o /dev/null -X DELETE -H "Authorization: Bearer ${NEW_PAT:-}" \
    -H "Accept: application/vnd.github+json" \
    "${API}/repos/${OWNER_REPO}/branches/${_PROBE_BRANCH}/protection" 2>/dev/null || true
  curl -s -o /dev/null -X DELETE -H "Authorization: Bearer ${NEW_PAT:-}" \
    -H "Accept: application/vnd.github+json" \
    "${API}/repos/${OWNER_REPO}/git/refs/heads/${_PROBE_BRANCH}" 2>/dev/null || true
}
trap cleanup_probe_branch EXIT

# ── 필수 인자 ─────────────────────────────────────────────────────────────
[ -n "${NEW_PAT:-}" ] || { echo "❌ NEW_PAT(narrowing 후 fine-grained PAT) 환경변수 필수"; exit 2; }

echo "🔎 자격증명 narrowing capability 실측 — ${OWNER_REPO}"
echo "   (토큰 값은 출력되지 않습니다. HTTP 상태 코드와 판정만 표시)"

# 일회용 브랜치 생성(Contents:write). main SHA에서 분기. 이후 파괴성 프로브는 전부 이 위에서.
main_sha=$(curl -s -H "Authorization: Bearer ${NEW_PAT}" -H "Accept: application/vnd.github+json" \
  "${API}/repos/${OWNER_REPO}/git/ref/heads/main" 2>/dev/null \
  | grep -o '"sha"[[:space:]]*:[[:space:]]*"[0-9a-f]*"' | head -1 | grep -o '[0-9a-f]\{7,\}')
if [ -n "$main_sha" ]; then
  cand="probe/narrowing-verify-$$"
  crc=$(curl_status POST "/repos/${OWNER_REPO}/git/refs" "$NEW_PAT" \
    "{\"ref\":\"refs/heads/${cand}\",\"sha\":\"${main_sha}\"}")
  if [ "$crc" = "201" ]; then _PROBE_BRANCH="$cand"; else
    printf '  ℹ️  일회용 브랜치 생성 실패(HTTP %s) — 파괴성 프로브(7-v·목록6·7-ix PUT)는 INCONCLUSIVE 처리\n' "$crc"
  fi
else
  printf '  ℹ️  main SHA 조회 실패 — 파괴성 프로브는 INCONCLUSIVE 처리\n'
fi

# ══════════════════════════════════════════════════════════════════════════
# A. 새 PAT capability (ADR-0011 목록 7 (i)~(viii)) — '같은 PAT로'의 확인.
# ══════════════════════════════════════════════════════════════════════════
section "A. 새 PAT capability (목록 7 i~viii)"

# (7-i) repository_dispatch 성공(204). event_type는 create-loop-pr가 아니라 프로브 전용
#   값을 써서 pr-creation.yml이 실제로 트리거되지 않게 한다(트리거 조건 불일치).
expect_status "7-i" "repository_dispatch 트리거" "$NEW_PAT" POST \
  "/repos/${OWNER_REPO}/dispatches" "204" '{"event_type":"verify-narrowing-probe"}'

# (7-ii) workflow_dispatch API는 Actions:write 부재로 거부돼야 한다.
expect_denied "7-ii" "workflow_dispatch(Actions:write 부재)" "$NEW_PAT" POST \
  "/repos/${OWNER_REPO}/actions/workflows/ci.yml/dispatches" '{"ref":"main"}'

# (7-iii) Issues RW — 생성·라벨·close.
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

# (7-iv) Administration:read — protection 조회 200.
expect_status "7-iv" "protection 조회(Administration:read)" "$NEW_PAT" GET \
  "/repos/${OWNER_REPO}/branches/main/protection" "200"

# (7-v) Administration:write 부재 — 거부돼야 한다. (a) admin 벡터 폐쇄 증거.
#   ★ main이 아니라 일회용 브랜치에 PUT한다(실패 시 main 파괴 방지 — 리뷰 P1).
if [ -n "$_PROBE_BRANCH" ]; then
  pc=$(curl_status PUT "/repos/${OWNER_REPO}/branches/${_PROBE_BRANCH}/protection" "$NEW_PAT" \
    '{"required_status_checks":null,"enforce_admins":true,"required_pull_request_reviews":null,"restrictions":null}')
  case "$pc" in
    403|404) pass "7-v" "PUT protection 거부(Administration:write 부재)" "$pc" ;;
    2*)      fail "7-v" "PUT protection 성공(=Administration:write 잔존, (a) 벡터 열림)" "$pc" ;;
    *)       inconc "7-v" "예상외 응답" "$pc" ;;
  esac
else
  mandatory_missing "7-v" "일회용 브랜치 없음 → admin-write 프로브 불가"
fi

# (목록 6) Workflows 권한 부재 — 일회용 브랜치에 .github/workflows/ 파일 생성 시도가
#   거부돼야 한다. (b) check-위조 벡터 폐쇄 증거. main이 아니라 일회용 브랜치라 branch
#   protection이 아닌 Workflows 권한 계층에서 거부가 일어난다(정확한 벡터 측정).
if [ -n "$_PROBE_BRANCH" ]; then
  wf_b64=$(printf 'name: probe\non: workflow_dispatch\njobs: {}\n' | base64 | tr -d '\n')
  wc=$(curl_status PUT "/repos/${OWNER_REPO}/contents/.github/workflows/zzz-narrowing-probe.yml" "$NEW_PAT" \
    "{\"message\":\"probe\",\"content\":\"${wf_b64}\",\"branch\":\"${_PROBE_BRANCH}\"}")
  case "$wc" in
    403|404) pass "목록6" "workflow 파일 push 거부(Workflows 권한 부재)" "$wc" ;;
    2*)      fail "목록6" "workflow 파일 생성 성공(=Workflows 권한 잔존, (b) 벡터 열림)" "$wc" ;;
    *)       inconc "목록6" "예상외 응답" "$wc" ;;
  esac
else
  mandatory_missing "목록6" "일회용 브랜치 없음 → workflow-write 프로브 불가"
fi

# (7-vi) PR:write 부재 — APPROVE 거부. (c) 승인-위조 폐쇄 증거.
#   ★ 403(권한-부재)만 PASS. 422(self-approval 차단)는 판정불능 → 비-chnu-kim PR 재실측.
if [ -n "${APPROVE_TARGET_PR:-}" ]; then
  ac=$(curl_status POST "/repos/${OWNER_REPO}/pulls/${APPROVE_TARGET_PR}/reviews" "$NEW_PAT" '{"event":"APPROVE"}')
  case "$ac" in
    403|404) pass "7-vi" "APPROVE 거부(PR:write 부재)" "$ac" ;;
    422)     inconc "7-vi" "self-approval 차단(대상이 chnu-kim 작성?) — 비-chnu-kim PR로 재실측" "$ac" ;;
    2*)      fail "7-vi" "APPROVE 성공(=PR:write 잔존, (c) 벡터 열림)" "$ac" ;;
    *)       inconc "7-vi" "예상외 응답" "$ac" ;;
  esac
else
  mandatory_missing "7-vi" "APPROVE_TARGET_PR 미지정(비-chnu-kim 작성 PR 필요)"
fi

# (7-vii) 코멘트 capability — ADR상 판정이 아니라 '관측·기록'(실패 시 후속 이슈). 비차단.
if [ -n "${COMMENT_TARGET_PR:-}" ]; then
  cc=$(curl_status POST "/repos/${OWNER_REPO}/issues/${COMMENT_TARGET_PR}/comments" "$NEW_PAT" \
    '{"body":"[probe] narrowing verify — 코멘트 capability 관측(무시 가능)."}')
  if [ "$cc" = "201" ]; then
    observe "7-vii" "PR 코멘트(Issues:write) 성공" "$cc"
  else
    observe "7-vii" "PR 코멘트 실패 — 코멘트 동작 App 이관 + ADR amend 후속 필요" "$cc"
  fi
else
  observe "7-vii" "COMMENT_TARGET_PR 미지정 — 관측 생략(비차단)" "n/a"
fi

# (7-viii) 게이트-미충족 PR에 merge 호출 시 branch protection이 거부.
#   ★ enforce_admins=false면 admin-user 토큰이 우회해 merge가 성공할 수 있어 유효 판정 불가.
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
  mandatory_missing "7-viii" "MERGE_TARGET_PR 미지정(게이트-미충족 열린 PR 필요)"
fi

# ══════════════════════════════════════════════════════════════════════════
# B. (7-ix) ② 완료 판정 — loop가 실제 resolve하는 ambient 자격증명(명시 토큰 없이)으로
#    PUT protection·APPROVE를 시도해 둘 다 거부되는지. (i)~(viii)는 새 PAT 상한만
#    증명하고, 기존 admin 토큰 잔존은 이 항목만이 증명한다.
# ══════════════════════════════════════════════════════════════════════════
section "B. (7-ix) ② 완료 판정 — ambient loop 자격증명"

printf '  자격증명 표면 점검(값 미출력):\n'
printf '    - GH_TOKEN 설정됨: %s\n'     "$([ -n "${GH_TOKEN:-}" ] && echo yes || echo no)"
printf '    - GITHUB_TOKEN 설정됨: %s\n' "$([ -n "${GITHUB_TOKEN:-}" ] && echo yes || echo no)"

if ! command -v gh >/dev/null 2>&1; then
  mandatory_missing "7-ix" "gh CLI 없음 → ambient 자격증명 프로브 불가(loop 컨텍스트에서 실행하라)"
else
  gh auth status >/dev/null 2>&1 && printf '    - gh 캐시 세션: 있음\n' || printf '    - gh 캐시 세션: 없음\n'

  # ambient PUT protection — ★ main이 아니라 일회용 브랜치 대상(파괴 방지).
  if [ -n "$_PROBE_BRANCH" ]; then
    perr=$(gh api --method PUT "repos/${OWNER_REPO}/branches/${_PROBE_BRANCH}/protection" \
      -f 'enforce_admins=true' 2>&1 >/dev/null); prc=$?
    pcode=$(printf '%s' "$perr" | grep -oE 'HTTP [0-9]+' | head -1)
    if [ $prc -ne 0 ] && printf '%s' "$perr" | grep -qE 'HTTP (403|404)'; then
      pass "7-ix" "ambient PUT protection 거부" "${pcode:-n/a}"
    elif [ $prc -eq 0 ]; then
      fail "7-ix" "ambient PUT protection 성공(=admin 잔존 자격증명)" "2xx"
    else
      inconc "7-ix" "ambient PUT protection — 예상외" "${pcode:-n/a}"
    fi
  else
    mandatory_missing "7-ix" "일회용 브랜치 없음 → ambient PUT protection 프로브 불가"
  fi

  # ambient APPROVE — 대상 = 비-chnu-kim PR. 403만 PASS, 422는 판정불능.
  if [ -n "${APPROVE_TARGET_PR:-}" ]; then
    aerr=$(gh api --method POST "repos/${OWNER_REPO}/pulls/${APPROVE_TARGET_PR}/reviews" \
      -f 'event=APPROVE' 2>&1 >/dev/null); arc=$?
    acode=$(printf '%s' "$aerr" | grep -oE 'HTTP [0-9]+' | head -1)
    if [ $arc -ne 0 ] && printf '%s' "$aerr" | grep -qE 'HTTP (403|404)'; then
      pass "7-ix" "ambient APPROVE 거부(권한-부재)" "${acode:-n/a}"
    elif printf '%s' "$aerr" | grep -qE 'HTTP 422'; then
      inconc "7-ix" "ambient APPROVE 422(self-approval?) — 비-chnu-kim PR로 재실측" "${acode:-n/a}"
    elif [ $arc -eq 0 ]; then
      fail "7-ix" "ambient APPROVE 성공(=approve-capable 잔존)" "2xx"
    else
      inconc "7-ix" "ambient APPROVE — 예상외" "${acode:-n/a}"
    fi
  else
    mandatory_missing "7-ix" "APPROVE_TARGET_PR 미지정 → ambient APPROVE 프로브 불가"
  fi
fi

# ══════════════════════════════════════════════════════════════════════════
# C. teardown 실측 — 폐기가 '단언'이 아니라 실측인지.
# ══════════════════════════════════════════════════════════════════════════
section "C. teardown 실측"

# 구 admin 토큰 revoke — 그 토큰으로 GET /user가 401이면 폐기됨.
if [ -n "${OLD_ADMIN_TOKEN:-}" ]; then
  oc=$(curl_status GET "/user" "$OLD_ADMIN_TOKEN")
  if [ "$oc" = "401" ]; then
    pass "teardown" "구 admin 토큰 revoke(GET /user 401)" "$oc"
  else
    fail "teardown" "구 admin 토큰이 아직 유효(revoke 안 됨)" "$oc"
  fi
else
  mandatory_missing "teardown" "OLD_ADMIN_TOKEN 미지정 → 구 admin 토큰 401 확인 불가"
fi

# (목록 8) SSH authentication 등록 해제·push 거부 — 이 스크립트는 SSH key auth 상태를
#   신뢰성 있게 검증할 수 없다(오퍼레이터 계정 설정 소관). 수동 실측 후 명시 승인
#   (SSH_TEARDOWN_CONFIRMED=1) 없이는 fail-closed(INCONCLUSIVE)로 둔다.
if [ "${SSH_TEARDOWN_CONFIRMED:-}" = "1" ]; then
  pass "목록8" "SSH auth 등록 해제·push 거부 — 오퍼레이터 수동 실측 확인됨" "manual"
else
  mandatory_missing "목록8" "SSH_TEARDOWN_CONFIRMED 미설정 — 아래를 수동 실측 후 승인 필요:
       1) GitHub 계정 authentication key 목록에 loop push key 부재(signing-only만 허용)
       2) 해당 SSH key로 git push 시도가 거부됨"
fi

# ══════════════════════════════════════════════════════════════════════════
section "요약"
if [ "$FAILED" = "1" ]; then
  echo "❌ FAIL 항목 있음 — ②(narrowing) 미완. Phase A 진입 불가. 위 FAIL을 해소 후 재실측하라."
  exit 1
elif [ "$INCONCLUSIVE" = "1" ]; then
  echo "⚠️  판정불능/부분 실행 — ② 완료 판정 아님(exit 1)."
  echo "    원인(대상 PR 미지정·chnu-kim 작성·enforce_admins=false·토큰/일회용 브랜치 부재 등)을"
  echo "    해소 후 재실측하라. 판정불능을 남긴 채 ② 완료로 간주하지 말 것"
  echo "    (false-green이 #50 precondition ②로 전파된다 — PR#22 실패형)."
  exit 1
else
  echo "✅ mandatory 전 항목 PASS — ②(narrowing) 완료 판정 가능."
  echo "   결과 요약(항목별 HTTP 상태·판정, 시크릿 원문 제외)을 이슈 #46에 코멘트로 기록하라."
  exit 0
fi
