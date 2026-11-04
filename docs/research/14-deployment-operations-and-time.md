# Deployment, operations, and time handling for a single-process unattended bot (host, restarts, single-instance, NTP, IP allowlist)

> **왜 중요한가** — CLAUDE.md '24시간 무인 실행'; ADR-0001 implies single process; internal/runtime/doc.go defers multi-process exclusion to a future ADR; ADR-0012 sentinel relies on graceful shutdown; env.example expects persistent storage for data/bot.db and data/audit; Toss IP allowlist (403 otherwise) requires a static egress IP; retro noted NTP wall-clock regression. No deployment ADR exists yet — this topic feeds it.

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 7건. 상태 집계: ❌ 미답 3, 🟡 부분 8

## 핵심 결론

- (완전 답변된 질문 없음 — 아래 부분 답변 참조)

## 질문별 답변

### T13.Q1 · Hosting options for a KR-based retail bot: home server vs domestic cloud (Naver Cloud, KT Cloud) vs AWS Seoul/Tokyo vs Oracle free tier — latency to openapi.tossinvest.com, static IP availability for the Toss allowlist, cost, and uptime.

❌ 미답 · 우선순위 must · 확신 low

No external evidence. These remain unverified: how the Toss allowlist works, latency, and static-IP products and prices from the providers. Repo constraint: if the allowlist premise holds, a static egress IP is required, and an IP change would surface as generic non-retryable 403s [repo-toss-client].

### T13.Q2 · systemd unit design for the bot: Restart=on-failure with RestartSec/StartLimit, KillSignal/TimeoutStopSec matching TOSS_BOT_SHUTDOWN_TIMEOUT, WorkingDirectory/StateDirectory, hardening directives (ProtectSystem, PrivateTmp, NoNewPrivileges), and single-instance guarantees.

🟡 부분 · 우선순위 must · 확신 medium

KillSignal=SIGTERM fits the bot's signal.NotifyContext handling. TimeoutStopSec must be greater than TOSS_BOT_SHUTDOWN_TIMEOUT (10s) plus the time for the sentinel fsync and store close. Otherwise SIGKILL makes every restart unclean, and the next boot comes up halted and needs a human [repo-env-example][repo-adr-0005-0006-0012]. Restart=on-failure with a StartLimit is reasonable, because crash-loop boots come up with submits fail-closed (ADR-0004 point 3). StartLimit also bounds token re-issuance (ADR-0001). Single-instance through systemd holds only per host and only for starts through the unit [repo-runtime-doc]. The semantics of the hardening directives are unverified.

### T13.Q3 · Single-instance enforcement options and failure modes (flock lockfile, systemd, cloud lease) given ADR-0001 token invalidation and the sentinel's non-exclusion (internal/runtime/doc.go) — what should the future ADR choose?

🟡 부분 · 우선순위 must · 확신 medium

The sentinel does not exclude [repo-runtime-doc]. A second process means mutual token invalidation (ADR-0001), a second SQLite writer (ADR-0005), and both processes booting unhalted. Recommendation: take a per-host flock on a lockfile next to bot.db before store.Open and before the first token issuance, and exit non-zero if acquisition fails. The kernel releases the lock when the holder crashes, so there is no stale-lease outage. Cross-host operation would be declared unsupported. flock/NFS semantics and the fencing literature are unverified.

### T13.Q4 · Time synchronization: chrony/systemd-timesyncd configuration, step vs slew, detecting clock jumps in-process, choosing KST vs UTC for persisted timestamps, and how market calendars should be evaluated (exchange local time).

🟡 부분 · 우선순위 must · 확신 medium

In-process durations use the monotonic reading and are immune to NTP steps. Persisted times compare by wall clock and can regress [go-time-monotonic-pkgdoc]. Persist timestamps in UTC (audit/writer.go already does). Detect clock jumps by comparing the monotonic t2.Sub(t1) with the wall-clock t2.Round(0).Sub(t1.Round(0)) [go-time-monotonic-pkgdoc]. Evaluate market sessions in the exchange's local zone (Asia/Seoul or America/New_York). chrony vs timesyncd behaviour is unverified.

### T13.Q5 · Container vs bare binary deployment: Docker with persistent volumes and fsync semantics (overlayfs vs bind mount), Compose restart policies, and whether containerization adds durability risk for SQLite/WAL.

❌ 미답 · 우선순위 should · 확신 low

No external sources. Repo inference: ADR-0005 and ADR-0006 durability requires data/ on a local filesystem (a bind mount or named volume) held by exactly one container. The stop grace period has the same constraint as TimeoutStopSec [repo-adr-0005-0006-0012].

### T13.Q6 · Deploy pipeline for an always-halted-at-boot bot (ADR-0007): blue/green is impossible with one token — safe rolling restart procedure, pre-deploy checks (govulncheck, tests), post-deploy health, and rollback with store schema migrations.

🟡 부분 · 우선순위 should · 확신 medium

Blue/green is impossible with one token (ADR-0001). Procedure:
1. Run the gates: test -race, vet, govulncheck, scan.sh.
2. Send SIGTERM and confirm the clean sentinel was written.
3. Snapshot bot.db and the audit directory.
4. Start the new binary and check the boot scan, submit state and halt state.
Rollback across a migration fails with ErrSchemaTooNew [repo-store-schema], so it needs the snapshot restored. Orders placed after the deploy then exist only at Toss, and boot reconciliation (ADR-0003) must re-derive them. That risk is unanalysed.

### T13.Q7 · Operator affordances (#80): CLI for clearing halts, inspecting journal/halt/sentinel, triggering reconciliation, and safe remote access (SSH + MFA) — design patterns from other unattended systems (e.g. nomad/consul operator commands).

🟡 부분 · 우선순위 must · 확신 medium

Issue #80 defines a local CLI with status, a confirmed clear, an audit record, and FinalizePendingHalt before clear [repo-issue-80]. A separate process writing bot.db would be a second writer and cannot see the in-memory carrier. The CLI should therefore take the same flock (so it runs only while the bot is stopped) or use a local unix control socket. Nomad/Consul patterns and SSH+MFA are unverified.

### T13.Q8 · Disk/storage planning: audit segment growth rates, retention windows, monitoring free space, and log rotation; what happens to ADR-0012 fail-closed when disk fills.

🟡 부분 · 우선순위 should · 확신 medium

Disk full is fail-closed and escalates to the killswitch, and prune is gated on a durable ack [repo-adr-0005-0006-0012]. The clean sentinel write may also fail, so the next boot is halted. Growth rates and alerting are unmeasured or undecided.

### T13.Q9 · Network egress reliability: DNS failures, proxy/NAT timeouts on long-idle keep-alives, IPv6 vs IPv4 for the allowlist, and detecting 'allowlist mismatch' 403 as a non-retryable config error.

🟡 부분 · 우선순위 should · 확신 low

Only 429 and >= 500 are retried, and 403 is a generic non-retryable error [repo-toss-client]. An allowlist 403 is not retried, but it does not halt either. Proposal: a config-error class that halts, pending confirmation of the Toss error body. DNS, NAT and IPv6 are unverified.

### T13.Q10 · Configuration management: env vars vs config file, validation at boot (fail-fast on zero thresholds as env.example does), secrets separation, and config drift detection between runbook and deployed values.

🟡 부분 · 우선순위 nice · 확신 medium

Configuration is env vars with fail-fast validation: thresholds must be > 0, https is required except loopback, the account seq is numeric, and the store path rejects #, ? and % [repo-env-example]. Secrets are redacted through config.Secret. Drift detection (emitting the effective non-secret config to the audit sink at boot) is unaddressed.

### T13.Q11 · Cost and power considerations for 24/7 home operation (UPS, ISP IP changes breaking the allowlist, DDNS) vs cloud; failure-mode comparison for unattended money-moving software.

❌ 미답 · 우선순위 nice · 확신 low

No sources. Repo inference: an ISP IP change appears as 403s, an availability loss rather than a money-safety loss. Power loss without a UPS is an unclean exit, so the next boot is halted and needs a human.

## 프로젝트 함의

- The deployment ADR must pick explicit exclusion, because internal/runtime/doc.go says the sentinel is not one. Recommended: a per-host flock next to bot.db, taken before store.Open and the first token issuance (ADR-0001). Cross-host operation is unsupported.
- TimeoutStopSec, or the container stop grace period, must be greater than TOSS_BOT_SHUTDOWN_TIMEOUT plus the sentinel fsync. Otherwise ADR-0012 1(c) makes every restart a halted boot.
- ErrSchemaTooNew means any deploy that carries a migration needs a bot.db and audit snapshot after a clean stop. The ADR must analyse whether restoring it is safe given orders that exist only at Toss (ADR-0003).
- Issue #80 CLI: take the same flock, or use a control socket, to preserve ADR-0005's single writer.
- Add a 403 config-error class in internal/toss/client.go that trips the killswitch, after checking the Toss error schema.
- ADR-0005/0006 durability rules out network filesystems and multiple containers sharing data/. This is an inference that still needs verification.
- Time rule: monotonic durations in-process, UTC persisted, and no ordering of persisted wall times across a possible clock step without tolerance.
- StartLimit bounds the rate of token re-issuance during a crash loop.

## 출처 간 상충

- Only one external source was verified, so no external sources disagree with each other.
- Issue #80 assumes a local CLI is enough, but a separate CLI process writing the store breaks ADR-0005's single-writer premise.
- Rolling back by restoring a pre-deploy bot.db conflicts with CLAUDE.md's rule not to trust local state, unless ADR-0003 reconciliation re-derives the post-snapshot orders from the API.

## 남은 공백

- **T13.Q1** — Toss allowlist docs; static IP pricing; latency (추가 검색: 토스증권 Open API 허용 IP 등록; Naver Cloud 공인 IP 요금; AWS Elastic IP ap-northeast-2 pricing)
- **T13.Q2** — systemd directive semantics (추가 검색: systemd.service StartLimitBurst StartLimitIntervalSec; systemd.kill TimeoutStopSec; systemd.exec StateDirectory ProtectSystem strict)
- **T13.Q3** — flock(2) semantics; fencing literature (추가 검색: man 2 flock; Kleppmann how to do distributed locking)
- **T13.Q4** — chrony makestep vs timesyncd (추가 검색: chrony.conf makestep; systemd-timesyncd step slew)
- **T13.Q5** — SQLite WAL on network filesystems / Docker volumes (추가 검색: sqlite wal.html network filesystem; sqlite howtocorrupt)
- **T13.Q7** — Operator CLI patterns; SSH MFA (추가 검색: nomad operator command; OpenSSH AuthenticationMethods)
- **T13.Q9** — Toss 403 body; NAT idle timeouts (추가 검색: openapi.tossinvest.com openapi.json 403; Go http.Transport IdleConnTimeout NAT)
- **T13.Q11** — KR ISP dynamic IP; UPS practice (추가 검색: KT 인터넷 유동 IP 변경 주기; NUT UPS systemd shutdown)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|
| `repo-runtime-doc` | internal/runtime/doc.go: the sentinel is NOT a mutual-exclusion primitive | toss-trade-bot maintainers | 2026 | code | <internal/runtime/doc.go> | 5 | 5 | Two processes on one store after a clean shutdown both read clean, both write running, and neither boots halted.; A stale lease that blocks every restart would be a new outage mode. |
| `repo-env-example` | configs/env.example (durable state paths, thresholds, shutdown timeout) | toss-trade-bot maintainers | 2026 | code | <configs/env.example> | 5 | 5 | Losing bot.db or the audit directory makes every boot look like a first boot, which comes up halted.; A zero threshold is refused at boot.; TOSS_BOT_SHUTDOWN_TIMEOUT=10s; a drain timeout is unclean, so the next boot is halted. |
| `repo-adr-0005-0006-0012` | ADR-0005 (store), ADR-0006 (audit sink), ADR-0012 (killswitch durability ordering), ADR-0004 (submit guard) | toss-trade-bot maintainers | 2026 | official-doc | <docs/adr/> | 5 | 5 | synchronous=FULL with WAL is fixed (ADR-0005).; Disk full leads to fail-closed / killswitch (ADR-0005, ADR-0006).; clean is written only on a graceful exit (ADR-0012 1(c)). |
| `repo-store-schema` | internal/store/schema.go: ErrSchemaTooNew and the V1–V5 forward-only migrations | toss-trade-bot maintainers | 2026 | code | <internal/store/schema.go> | 5 | 5 | An old binary opening a newer schema fails with ErrSchemaTooNew.; There are no down-migrations. |
| `repo-issue-80` | Issue #80: operator clear affordance (CLI) | toss-trade-bot maintainers | 2026 | official-doc | <GitHub issue #80 (repo-internal)> | 5 | 5 | Every first deploy boots halted; without ClearHalt it stays halted.; Clear must finalize the pending halt first and refuse on failure. |
| `go-time-monotonic-pkgdoc` | Package time: Monotonic Clocks | The Go Authors | 2026 | official-doc | <https://pkg.go.dev/time#hdr-Monotonic_Clocks> | 4 | 5 | "the wall clock is for telling time and the monotonic clock is for measuring time."; Comparisons and subtractions use monotonic readings when both times have one; otherwise they fall back to wall clock.; t.Round(0) strips the monotonic reading; AddDate, Round and Truncate also strip it. |
| `repo-toss-client` | internal/toss/client.go: retry classification | toss-trade-bot maintainers | 2026 | code | <internal/toss/client.go> | 4 | 5 | Retryable means 429 or >= 500 only.; There is no 403 (allowlist) config-error class. |

### 출처 요약

- **`go-time-monotonic-pkgdoc`** — Official Go documentation. A time.Time holds a wall clock reading and a monotonic one. When both operands have a monotonic reading, comparison and subtraction use it, so elapsed time measured inside one process is not affected by NTP steps. Times that are serialized or reconstructed (Parse, Unix, Date, values read back from storage) have no monotonic reading and compare by wall clock only. This is the only external source verified for this topic. _(접근: open access; verified by an earlier collector (first 100000 of 114382 chars read))_
- **`repo-runtime-doc`** — Repo-internal primary source. The sentinel's boot transition is an unconditional read-then-write, not a compare-and-set or a lease. Two cmd/bot processes started after a clean shutdown would both boot unhalted. Single-process operation rests on ADR-0001 and ADR-0005. Lease design is deferred to /architect. _(접근: repo-internal, read directly)_
- **`repo-env-example`** — Repo-internal. Defines TOSS_BOT_STORE_PATH (default data/bot.db) and TOSS_BOT_AUDIT_DIR (default data/audit), both of which must be on persistent storage. Escalation thresholds must be > 0 or boot is refused. TOSS_BOT_SHUTDOWN_TIMEOUT defaults to 10s, and a drain timeout counts as an unclean exit, so the next boot is halted. _(접근: repo-internal, re-verified 2026-11-04)_
- **`repo-adr-0005-0006-0012`** — Repo-internal decisions. ADR-0005 fixes SQLite at synchronous=FULL with WAL, and a disk-full durable append failure is fail-closed. ADR-0006 requires a content fsync, an atomic rename and a parent directory fsync, escalates disk full to the killswitch, and gates prune on a durable ack. ADR-0012 1(c) writes clean only on a graceful exit with no unpersisted pending halt. ADR-0004 point 3 keeps submits blocked at boot until the scan completes. _(접근: repo-internal)_
- **`repo-store-schema`** — Open fails closed when the database schema is newer than the binary knows. Migrations are forward-only, so a rollback across a migration needs a restored pre-migration bot.db. _(접근: repo-internal)_
- **`repo-toss-client`** — isRetryableStatus returns true only for 429 and >= 500, and a 401 triggers one token refresh. A 403 is a generic non-retryable error with no distinct class. _(접근: repo-internal, re-verified 2026-11-04)_
- **`repo-issue-80`** — Asks for a local CLI that shows halt state (the durable phase plus the in-memory carrier) and clears the global halt behind a confirmation, with an audit record of who, when and why. It must run FinalizePendingHalt before clearing and refuse if that fails. A remote API is out of scope, and auto-clear is forbidden. _(접근: repo-internal, read by an earlier collector)_
