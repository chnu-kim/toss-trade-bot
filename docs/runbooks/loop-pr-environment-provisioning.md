# Runbook: `loop-pr` environment 프로비저닝 + PR-생성 workflow 실측 절차

이 문서는 [ADR-0011](../adr/0011-loop-pr-credential-flow.md)이 사람 액션으로 남긴 절차를
실행 가능한 체크리스트로 정리한 것이다(이슈 #47 작업 2·3). **여기 나열된 단계 중 environment
생성·시크릿 등재·App key rotate·branch protection 변경은 전부 admin 권한이 필요한 사람 액션이다
— 에이전트가 대행할 수 없다.** 에이전트가 실행 가능한 실측(사전 스테이징한 변조 브랜치 확인 등)은
각 단계에 명시했다.

## 확정된 값 (SSOT — #49·#50이 참조)

| 항목 | 값 | 근거 |
|---|---|---|
| workflow 파일 | `.github/workflows/pr-creation.yml` | 이슈 #47 |
| `event_type` | `create-loop-pr` | 이슈 #47 |
| environment 이름 | `loop-pr` | 이슈 #47 |
| environment 시크릿 | `MECHANU_APP_ID`, `MECHANU_APP_PRIVATE_KEY` | 이슈 #47 |
| `client_payload` 필드 | `head_branch`, `pr_title`, `pr_body` | 이슈 #47 |
| `client_payload` 크기 상한 | 64KB(JSON 직렬화 전체 — GitHub REST API 문서, "Create a repository dispatch event") | 이슈 #47 미정 사항 해소 |

## 순서(안전 메커니즘의 일부 — 뒤집지 않는다)

ADR-0011 Consequences 사람 액션 순서 그대로: **(3) → (1) → (2)**.

1. **(3) 자격증명 narrowing 완료 확인** — [#46](https://github.com/chnu-kim/toss-trade-bot/issues/46)의
   실측 목록 7 (ix)(loop 실행 컨텍스트가 실제로 resolve하는 자격증명으로 `PUT .../branches/main/protection`과
   `POST .../pulls/{n}/reviews`(APPROVE) 둘 다 거부)가 **통과로 기록**돼 있는지 사람이 직접 확인한다.
   오퍼레이터 단언이 아니라 #46 이슈/PR에 남은 실측 기록을 읽고 확인한다.
2. **(1) App private key rotate + environment 프로비저닝** — 아래 "1. App key rotate"·"2. environment
   생성" 섹션. **(3) 확인 전에는 절대 하지 않는다** — admin/workflow 자격증명이 아직 loop 컨텍스트에
   남아 있는 동안 key를 environment에 두면, `PUT` deployment-branch-policy 편집 능력(admin)과 브랜치
   workflow push 능력(workflow scope)의 조합으로 key가 탈취될 수 있고 그 탈취는 narrowing을 통과해
   Phase B까지 durable하게 잔존한다(ADR-0011 point 5 라운드 7).
3. **(2) 이 PR(#47) 머지** — (1)이 끝난 뒤에만. 머지 직후 이 workflow가 main에서 가동된다.

## 1. App private key rotate (사람 액션)

기존 App private key는 오늘 이전에 로컬 materialize 시도(차단됨)·presence-check 구버전의 로컬 key
사용 등으로 잠재 노출 이력이 있다고 간주한다 — **새로 생성한 key만 사용하고, 기존 key는 전부 revoke한다.**

1. GitHub App(Mechanu) 설정 페이지 → "Generate a private key"로 **새 key**를 생성해 안전하게 보관한다
   (레포 밖 — 1Password 등. 이 레포에 원문을 두지 않는다).
2. 같은 페이지에서 **기존에 생성돼 있던 private key를 전부 삭제(revoke)**한다.
3. **teardown 실측(ADR-0011 실측 목록 12 — 완료 판정은 단언이 아니라 capability 실측)**: 삭제된
   구 key로 서명한 App JWT로 `GET /app`을 호출해 **401**이 반환되는지 확인한다.

   ```bash
   # $OLD_APP_ID, $OLD_KEY_PATH(revoke된 구 key 원문 경로)는 이 세션 밖에서 사람이 직접 채운다.
   # 이 스크립트는 JWT를 표준출력에 남기지 않는다.
   now=$(date +%s)
   header='{"alg":"RS256","typ":"JWT"}'
   payload=$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' "$((now-60))" "$((now+300))" "$OLD_APP_ID")
   b64() { openssl base64 -e -A | tr '+/' '-_' | tr -d '='; }
   signing_input="$(printf '%s' "$header" | b64).$(printf '%s' "$payload" | b64)"
   sig=$(printf '%s' "$signing_input" | openssl dgst -sha256 -sign "$OLD_KEY_PATH" | b64)
   jwt="$signing_input.$sig"
   status=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $jwt" \
     -H 'Accept: application/vnd.github+json' https://api.github.com/app)
   echo "GET /app with revoked key -> HTTP $status (expect 401)"
   unset jwt sig signing_input
   ```

   **결과(HTTP 상태 코드만, JWT/key 원문 없이)를 이 PR(#47) 코멘트로 기록한다.** 401이 아니면 rotate가
   미완이므로 다음 단계로 진행하지 않는다.

## 2. environment 생성 (사람 액션)

1. 레포 Settings → Environments → New environment → 이름 `loop-pr`.
2. **Deployment branch policy를 "Selected branches"로 바꾸고 `main` 하나만 추가한다.** 와일드카드·다른
   브랜치 추가 금지 — main-제한이 이 environment가 시크릿 격리를 제공하는 유일한 근거다(ADR-0011 point 3).
3. Environment secrets에 다음 두 값을 등록한다(레포-레벨 Secrets에는 등록하지 않는다 — point 3):
   - `MECHANU_APP_ID`: App ID.
   - `MECHANU_APP_PRIVATE_KEY`: 위 1단계에서 새로 생성한 key의 PEM 원문.
4. Required reviewers·wait timer 등 다른 보호 규칙은 이 workflow의 목적(무인 dispatch 즉시 실행)과
   충돌하므로 추가하지 않는다 — 격리는 branch policy만으로 충분하다(point 3).

## 3. 실측 목록 3 — 비-main ref의 environment 시크릿 접근 거부 (프로비저닝 직후, 사람 액션)

더미 workflow(또는 이 workflow를 임시로 비-main 브랜치에 push한 사본 — **narrowing(#46) 완료 후에는
loop 자격증명으로 이 push 자체가 거부되므로, 이 실측은 사람이 직접 push해 수행한다**)를 main이 아닌
브랜치에 두고 `environment: loop-pr`을 참조하는 잡을 그 브랜치의 `workflow_dispatch --ref <그 브랜치>`
(또는 사람이 트리거 가능한 다른 방식)로 실행한다. 기대 결과: **잡이 시작되지 못하고 "environment
protection rules"로 대기/차단되거나, environment 자체를 resolve하지 못해 시크릿이 빈 문자열로
주입된다.** 실측 결과(스크린샷 대신 실행 로그 URL과 관측된 차단 사유)를 이 PR(#47) 코멘트로 기록한다.

## 4. 실측 목록 4 — 비-main 브랜치의 변조 정의는 실행되지 않는다

이 PR은 변조된 `pr-creation.yml` 사본을 담은 프로브 브랜치를 **이미 선-push**해 두었다(narrowing(#46)
전 — 이 시점엔 아직 loop가 workflow push 능력을 보유하므로 에이전트가 직접 준비할 수 있었다).

- 브랜치: `probe/pr-creation-tamper-47`
- 차이점: 잡의 첫 스텝이 `echo "TAMPERED DEFINITION EXECUTED — ADR-0011 실측 목록 4 위반 신호"`를
  출력하도록 마커를 추가했다(트리거·가드·권한은 동일 — 이 브랜치 자체가 별도 위험을 열지 않는다;
  `repository_dispatch`는 애초에 이 브랜치의 정의를 실행할 수 없다는 것이 검증 대상인 플랫폼 사실이다).

**실측 절차(#46 완료 확인 + 이 PR 머지 + environment 프로비저닝 후, 사람 또는 loop가 dispatch)**:

```bash
gh api --method POST "repos/chnu-kim/toss-trade-bot/dispatches" \
  -f event_type='create-loop-pr' \
  -f 'client_payload[head_branch]=probe/pr-creation-tamper-47' \
  -f 'client_payload[pr_title]=ADR-0011 실측 목록 4 프로브 — 머지 금지' \
  -f 'client_payload[pr_body]=main 정의 실행 확인용. 이 PR은 검증 후 close한다.'
```

기대 결과: 실행된 워크플로 run의 로그 어디에도 `TAMPERED DEFINITION EXECUTED` 마커가 **없어야** 한다
(main의 untampered 정의가 실행됐다는 뜻). 생성된 PR을 확인 후 close하고(머지 금지), run 로그 URL +
마커 부재 확인을 이 PR(#47) 코멘트로 기록한다.

## 5. 활성화 스모크 테스트

위 4번과 별개로, 정상 `head_branch`(예: 사소한 문서 브랜치)로 같은 방식의 dispatch를 보내 다음을 확인한다:

- 생성된 PR의 **작성자가 `mechanu[bot]`**인지 (`gh pr view <PR> --json author`).
- 그 PR에 기존 required check(`ci.yml`의 "build · vet · gofmt · test-race")가 **정상 트리거**되는지
  (`gh pr checks <PR>`).

결과를 이 PR(#47) 코멘트로 기록한다.

## 6. 머지 게이트 기록

이 PR(#47)의 머지는 위 (3)(=#46 목록 7 (ix) 통과 확인)이 **먼저** 기록된 뒤에만 진행한다. 머지 직전
PR 타임라인에 "#46 목록 7 (ix) 통과 확인함 — 이 PR을 머지함" 코멘트를 남겨 순서 준수를 기록으로
남긴다(ADR-0011이 요구하는 감사 흔적).
