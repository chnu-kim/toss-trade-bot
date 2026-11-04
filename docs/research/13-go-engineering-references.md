# Go engineering references: layout, httptest/race testing, modernc sqlite, context/cancellation, clock injection, toolchain

> **왜 중요한가** — CLAUDE.md mandates golang-standards/project-layout, TDD, -race, httptest; go.mod go 1.25 / toolchain go1.26.5; ADR-0005 pure-Go SQLite; retro notes flaky areas (worktree stale refs, background worker loss). Agents dispatched via /dispatch-issue need authoritative, version-current Go references to avoid reinventing idioms.

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 20건. 상태 집계: 🟡 부분 7, ✅ 답변됨 4, ❌ 미답 1

## 핵심 결론

- **Context propagation and cancellation best practices: context.WithoutCancel for must-finish writes (e.g. audit commit after cancel), deadline inheritance, context in structs anti-pattern, and context.AfterFunc (Go 1.21+).** — WithoutCancel keeps the parent's values but drops its deadline and cancellation [pkg-context], so always wrap it in WithTimeout. A child's deadline is never later than its parent's, and cancel must be called on every path (vet lostcancel) [pkg-context]. AfterFunc runs f in its own goroutine. stop() does not wait for f, so teardown needs its own synchronization [pkg-context]. With WithCancelCause, Err stays Canceled and Cause holds the reason [pkg-context]. Do not store a context in a struct [pkg-context]. If ctx is canceled, BeginTx rolls back and Commit returns an error [pkg-database-sql]. A must-finish audit commit therefore needs a context derived from WithoutCancel and bounded by a timeout. For HTTP, use NewRequestWithContext; Client.Timeout also covers reading the body [pkg-net-http].
- **Time/clock injection patterns in Go (interface Clock, benbjohnson/clock, jonboulle/clockwork, testing/synctest), monotonic-time semantics, and testing timers/tickers deterministically for supervisor and reconciler ticks.** — Option 1 is an injected func() or Clock interface. clockwork is maintained; Advance plus BlockUntilContext gives a deterministic handshake [gh-jonboulle-clockwork]. benbjohnson/clock is archived [gh-benbjohnson-clock]. Option 2 is testing/synctest (Go 1.25), whose fake clock advances only when the bubble is durably blocked [pkg-testing-synctest]. Mutex waits, network I/O and syscalls are not durably blocking, so SQLite and httptest paths need an injected clock instead. T.Run and T.Parallel are not allowed inside a bubble. synctest.Sleep needs 1.27 [pkg-testing-synctest]. Monotonic time: Sub/Before/After use the monotonic reading, while AddDate/Round/Truncate/In/UTC and serialization strip it. The monotonic clock may pause during system sleep [pkg-time]. Compute elapsed time and backoff from monotonic differences, and use wall time for market hours and persisted timestamps. Since 1.23, unstopped timers are GC-eligible. NewTicker panics for d<=0 [pkg-time].
- **modernc.org/sqlite usage specifics in Go: database/sql connection pool pitfalls with SQLite (MaxOpenConns=1 for writer, separate read pool), DSN pragmas via _pragma query params, busy_timeout, transactions with BEGIN IMMEDIATE, and prepared statement caching.** — database/sql allows unlimited open connections and 2 idle by default [pkg-database-sql]. Use a writer DB with SetMaxOpenConns(1) and a separate read pool. _pragma values run unvalidated, and text after ';' also runs unless StrictPragmas(true) is set. _busy_timeout is validated and applied before the _pragma values. _txlock=immediate makes BeginTx issue BEGIN IMMEDIATE. PRAGMAs set through Exec leak to the next borrower of the pooled connection, so use the DSN or a connection hook. _time_format=string_no_monotonic keeps the monotonic suffix out of stored times [pkg-modernc-sqlite]. Statements prepared on a DB re-prepare on each connection, while those prepared on a Tx or Conn are bound to it. A Conn must be Closed [pkg-database-sql]. Confidence is medium: only one pass read the Driver.Open section, and sqlite.org was unreachable.
- **Error handling conventions: wrapping with %w, sentinel vs typed errors (transientError, FailClosedError), errors.Is/As across package seams, and avoiding error-string matching for retry classification.** — Wrap errors with %w. Is/As walk the error tree pre-order, including Join's Unwrap() []error. Prefer Is over ==, and keep any custom Is method shallow. AsType (1.26, usable at go1.26.5) is preferred over As. errors.Unwrap does not unwrap Join results [pkg-errors]. Classifying transientError and FailClosedError with typed or sentinel errors follows from these rules. Inference: string matching skips the tree and breaks once an error is wrapped.

## 질문별 답변

### T12.Q1 · Current authoritative guidance on Go project layout (golang-standards/project-layout vs Go team's 'Organizing a Go module' and its criticism of the 'standard' layout), internal/ semantics, and cmd/ thinness — reconcile with CLAUDE.md rules.

🟡 부분 · 우선순위 should · 확신 medium

golang-standards/project-layout states that it is 'NOT an official standard defined by the core Go dev team' and points readers to the Go team's 'Organizing a Go module' guide [gh-project-layout]. That official page could not be reached (go.dev was blocked), so its specific criticisms are not cited here. The README supports the conventions CLAUDE.md uses: /cmd is thin and has one directory per executable; /internal is privacy enforced by the compiler; /pkg is optional and disputed [gh-project-layout]. The repo already follows this: cmd/{bot,verdict-gate,presence-check}, internal/ holds the code, and there is no pkg/. The substance needs no reconciliation. CLAUDE.md should describe the layout as a convention it follows rather than 'a standard'.

### T12.Q2 · Testing idioms: httptest.Server/NewUnstartedServer for HTTP client tests, testing table-driven patterns, t.Cleanup, t.Setenv, subtests parallelism, golden files, testcontainers avoidance, -race limitations (what the race detector cannot detect), and -count/-shuffle for flake hunting.

🟡 부분 · 우선순위 must · 확신 high

Use httptest.NewServer, or NewUnstartedServer when Config/TLS must change before Start, and register t.Cleanup(srv.Close); Close blocks until outstanding requests finish. Server.Client() trusts the test certificate [pkg-httptest]. NewTestServer requires go1.27.0, so it is not available at go1.26.5 [pkg-httptest]. Cleanup runs in LIFO order. Setenv and Chdir cannot be combined with Parallel. T.Context is canceled before cleanups run. Table-driven tests go through T.Run [pkg-testing]. For flake hunting, -count=1 disables the test cache [pkg-cmd-go]. Repeated instances from -count/-cpu never overlap [pkg-testing]. Do not run httptest inside a synctest bubble, because network I/O is not durably blocking [pkg-testing-synctest]. Race platform support is listed in [pkg-cmd-go]. Not covered by any source: -shuffle, race-detector limitations (uncited general knowledge: it only finds races on paths that actually execute, and it does not detect deadlocks or logic races), golden files, and testcontainers.

### T12.Q3 · Context propagation and cancellation best practices: context.WithoutCancel for must-finish writes (e.g. audit commit after cancel), deadline inheritance, context in structs anti-pattern, and context.AfterFunc (Go 1.21+).

✅ 답변됨 · 우선순위 must · 확신 high

WithoutCancel keeps the parent's values but drops its deadline and cancellation [pkg-context], so always wrap it in WithTimeout. A child's deadline is never later than its parent's, and cancel must be called on every path (vet lostcancel) [pkg-context]. AfterFunc runs f in its own goroutine. stop() does not wait for f, so teardown needs its own synchronization [pkg-context]. With WithCancelCause, Err stays Canceled and Cause holds the reason [pkg-context]. Do not store a context in a struct [pkg-context]. If ctx is canceled, BeginTx rolls back and Commit returns an error [pkg-database-sql]. A must-finish audit commit therefore needs a context derived from WithoutCancel and bounded by a timeout. For HTTP, use NewRequestWithContext; Client.Timeout also covers reading the body [pkg-net-http].

### T12.Q4 · Time/clock injection patterns in Go (interface Clock, benbjohnson/clock, jonboulle/clockwork, testing/synctest), monotonic-time semantics, and testing timers/tickers deterministically for supervisor and reconciler ticks.

✅ 답변됨 · 우선순위 must · 확신 high

Option 1 is an injected func() or Clock interface. clockwork is maintained; Advance plus BlockUntilContext gives a deterministic handshake [gh-jonboulle-clockwork]. benbjohnson/clock is archived [gh-benbjohnson-clock]. Option 2 is testing/synctest (Go 1.25), whose fake clock advances only when the bubble is durably blocked [pkg-testing-synctest]. Mutex waits, network I/O and syscalls are not durably blocking, so SQLite and httptest paths need an injected clock instead. T.Run and T.Parallel are not allowed inside a bubble. synctest.Sleep needs 1.27 [pkg-testing-synctest]. Monotonic time: Sub/Before/After use the monotonic reading, while AddDate/Round/Truncate/In/UTC and serialization strip it. The monotonic clock may pause during system sleep [pkg-time]. Compute elapsed time and backoff from monotonic differences, and use wall time for market hours and persisted timestamps. Since 1.23, unstopped timers are GC-eligible. NewTicker panics for d<=0 [pkg-time].

### T12.Q5 · modernc.org/sqlite usage specifics in Go: database/sql connection pool pitfalls with SQLite (MaxOpenConns=1 for writer, separate read pool), DSN pragmas via _pragma query params, busy_timeout, transactions with BEGIN IMMEDIATE, and prepared statement caching.

✅ 답변됨 · 우선순위 must · 확신 medium

database/sql allows unlimited open connections and 2 idle by default [pkg-database-sql]. Use a writer DB with SetMaxOpenConns(1) and a separate read pool. _pragma values run unvalidated, and text after ';' also runs unless StrictPragmas(true) is set. _busy_timeout is validated and applied before the _pragma values. _txlock=immediate makes BeginTx issue BEGIN IMMEDIATE. PRAGMAs set through Exec leak to the next borrower of the pooled connection, so use the DSN or a connection hook. _time_format=string_no_monotonic keeps the monotonic suffix out of stored times [pkg-modernc-sqlite]. Statements prepared on a DB re-prepare on each connection, while those prepared on a Tx or Conn are bound to it. A Conn must be Closed [pkg-database-sql]. Confidence is medium: only one pass read the Driver.Open section, and sqlite.org was unreachable.

### T12.Q6 · Go release notes relevant to this repo: Go 1.22 loopvar, 1.23 iterators/timers GC change, 1.24 testing/synctest + generic aliases + weak pointers, 1.25 (container-aware GOMAXPROCS, testing/synctest GA, json/v2 experiment), 1.26 changes — and toolchain/GOTOOLCHAIN behaviour in CI.

🟡 부분 · 우선순위 should · 확신 medium

No release notes could be fetched. From the package docs: 1.23 made timers GC-eligible [pkg-time] and added atomic And/Or [pkg-sync-atomic]. 1.25 added synctest.Test [pkg-testing-synctest], WaitGroup.Go [pkg-sync] and json.Number's v2 interfaces [pkg-encoding-json]. 1.26 added errors.AsType [pkg-errors]. 1.27 added httptest.NewTestServer and synctest.Sleep [pkg-httptest][pkg-testing-synctest]. pkg.go.dev shows go1.27.3, while the repo uses go 1.25.0 with toolchain go1.26.5. go mod edit -toolchain sets the toolchain directive [pkg-cmd-go]. Unverified: GOTOOLCHAIN behavior in CI, 1.22 loopvar, 1.24 aliases and weak pointers, container-aware GOMAXPROCS, and whether json/v2 is still a GOEXPERIMENT.

### T12.Q7 · Static analysis and lint config: golangci-lint v2 config, staticcheck, govet copylocks/lostcancel, errcheck, gosec, exhaustive for enums (OrderStatus unknown-preserving), and nilaway — what the CI should run (ADR-0008 point 7(a)).

🟡 부분 · 우선순위 should · 확신 low

golangci-lint bundles more than 100 linters [gh-golangci-lint]; the v2 schema is unverified. exhaustive checks enum switches across packages, but the README does not document how it treats a default case [gh-exhaustive]. nilaway only runs in golangci-lint as a custom plugin and can produce false positives, so use it as advisory and scope it with -include-pkgs [gh-nilaway]. vet copylocks [pkg-sync] and lostcancel [pkg-context] already run as a subset during go test [pkg-cmd-go]. staticcheck, gosec and errcheck have no fetched source.

### T12.Q8 · Error handling conventions: wrapping with %w, sentinel vs typed errors (transientError, FailClosedError), errors.Is/As across package seams, and avoiding error-string matching for retry classification.

✅ 답변됨 · 우선순위 should · 확신 high

Wrap errors with %w. Is/As walk the error tree pre-order, including Join's Unwrap() []error. Prefer Is over ==, and keep any custom Is method shallow. AsType (1.26, usable at go1.26.5) is preferred over As. errors.Unwrap does not unwrap Join results [pkg-errors]. Classifying transientError and FailClosedError with typed or sentinel errors follows from these rules. Inference: string matching skips the tree and breaks once an error is wrapped.

### T12.Q9 · Concurrency primitives and patterns: sync.Mutex vs RWMutex tradeoffs, atomic types (Go 1.19+), channels vs mutex for state machines, worker pools, bounded queues, and 'Go memory model' guarantees relevant to ADR-0013 single-snapshot predicate.

🟡 부분 · 우선순위 should · 확신 medium

The nth Unlock happens before the mth Lock. A failed TryLock establishes no ordering. RWMutex has no recursive read locking and no upgrade or downgrade [pkg-sync]. A predicate over several fields therefore needs one lock acquisition. Separate atomics are each sequentially consistent, but reading two of them does not give a snapshot. For immutable snapshots, use atomic.Pointer[T]/Value copy-on-write [pkg-sync-atomic]. The memory model page and guidance on worker pools and bounded queues were not fetched.

### T12.Q10 · JSON decoding hardening: decoder.DisallowUnknownFields tradeoffs, json.Number/string passthrough for decimals, size limits (http.MaxBytesReader), and Go 1.25 encoding/json/v2 implications.

🟡 부분 · 우선순위 nice · 확신 medium

In v1, DisallowUnknownFields rejects unknown keys and UseNumber preserves decimal literals [pkg-encoding-json]. v2 ignores unknown members by default; RejectUnknownMembers opts in. It also rejects duplicate names and invalid UTF-8 and matches names case-sensitively [pkg-encoding-json-v2]. It may still be gated behind GOEXPERIMENT at go1.26 (unverified). MaxBytesReader is for server-side bodies [pkg-net-http], so cap client responses with io.LimitedReader, which is an inference. Tradeoff, also an inference: DisallowUnknownFields on Toss responses fails closed whenever the API adds a field.

### T12.Q11 · Build/release for an unattended binary: CGO_ENABLED=0 static builds, -trimpath, ldflags version stamping, goreleaser, cross-compiling for the deployment host, and minimal container images.

🟡 부분 · 우선순위 nice · 확신 low

-trimpath strips file paths from the binary [pkg-cmd-go]. modernc is pure Go, so builds with CGO_ENABLED=0 work. Pin libc to the driver's version [pkg-modernc-sqlite]. -race only works on some platforms [pkg-cmd-go] and needs cgo (uncited general knowledge). Not covered: goreleaser, ldflags vs ReadBuildInfo, distroless images.

### T12.Q12 · Git worktree workflows for parallel agent dispatch (retro item 1: stale local main), safe baselines for destructive 'Red' demonstrations, and tooling to keep worktrees synced with origin/main.

❌ 미답 · 우선순위 nice · 확신 low

No external source was fetched. The repo retro (docs/retros/2026-07-19-loop-engineering-campaign.md, process item 1) records a review against a stale local main inside a worktree. CLAUDE.md now pins review bases to origin/main. Git worktree and baseline guidance remains uncited.

## 프로젝트 함의

- ADR-0005 / internal/store/store.go (lines 106, 128-133) already follows the modernc guidance: writer SetMaxOpenConns(1), _txlock=immediate, a separate reader, and PRAGMAs set in the DSN [pkg-modernc-sqlite][pkg-database-sql]. Optional: switch _pragma=busy_timeout(5000) to the validated _busy_timeout param, which is applied before the other pragmas. StrictPragmas is low risk because the values are constants.
- ADR-0005 assumes CGO_ENABLED=0. CI (.github/workflows/ci.yml) runs vet, build, test -race, govulncheck and scan, but it has no CGO_ENABLED=0 build step to prove the static build claim, and race testing needs cgo. Add that step and document that the race build and the release build use different configurations.
- Toolchain skew: go.mod says go 1.25.0 with toolchain go1.26.5, while pkg.go.dev shows go1.27.3. httptest.NewTestServer and synctest.Sleep (both 1.27) will not compile in this repo. go-tdd-implementer should check the 'added in' annotations. errors.AsType (1.26) works under the toolchain but goes beyond the go 1.25.0 directive.
- ADR-0006 / ADR-0001: the WithTimeout(WithoutCancel(ctx)) pattern for audit, ack and trip writes matches [pkg-context].
- Clock: internal/store calls time.Now() directly while the reconciler and order code inject now(). Use synctest only for tick tests that are purely channel-based. SQLite and httptest paths need an injected clock [pkg-testing-synctest]. Prefer clockwork over benbjohnson/clock.
- ADR-0003/0014: windows computed from stored UnixNano values lose monotonic protection [pkg-time]. Prefer in-process monotonic differences where possible.
- ADR-0013 point 3 (a single mu snapshot) is consistent with [pkg-sync][pkg-sync-atomic].
- ADR-0008 point 7(a): there is no lint job yet. Adding pinned golangci-lint (staticcheck, errcheck, exhaustive) plus an advisory nilaway creates gate evidence, so under the CLAUDE.md gate-promotion rule the config file must be registered as sacred (CODEOWNERS + sacredRequiredPaths + a fail-closed test) in the same PR.
- The internal/toss DecodeJSON LimitedReader cap is the correct client-side analogue [pkg-net-http]. Hold off on json/v2 until its status at go1.26 is verified.

## 출처 간 상충

- benbjohnson/clock: pkg.go.dev does not show an archive flag, but GitHub shows the repository archived on 2023-05-18. Treat it as archived.
- httptest.NewTestServer is the documented recommendation but requires go1.27.0. At go1.26.5, use NewServer + t.Cleanup.
- project-layout describes /pkg both as being for external library code and as optional and disputed. CLAUDE.md's 'no pkg/' rule satisfies both readings.
- encoding/json v1 says to use v2, but the v2 page shows a jsonv2 build tag. Whether v2 is GOEXPERIMENT-gated at go1.26 is unresolved.
- The gap-filling rounds that returned zero sources reflect access limits. They are not contrary evidence.

## 남은 공백

- **T12.Q1** — Official 'Organizing a Go module' page (추가 검색: go.dev/doc/modules/layout; golang-standards project-layout issue 117)
- **T12.Q2** — Race detector limitations, -shuffle, golden-file -update idiom (추가 검색: go.dev/doc/articles/race_detector; go help testflag -shuffle)
- **T12.Q5** — sqlite.org WAL, BEGIN IMMEDIATE, synchronous docs (추가 검색: sqlite.org wal.html; sqlite.org lang_transaction.html; sqlite.org pragma.html synchronous)
- **T12.Q6** — Release notes 1.22-1.27, toolchain doc, jsonv2 experiment status (추가 검색: go.dev/doc/go1.26; go.dev/doc/toolchain GOTOOLCHAIN; go.dev/doc/go1.25 jsonv2 GOMAXPROCS)
- **T12.Q7** — golangci-lint v2 schema, staticcheck, gosec, exhaustive default semantics (추가 검색: golangci-lint.run configuration v2; staticcheck.dev/docs/checks; exhaustive -default-signifies-exhaustive)
- **T12.Q9** — Go memory model text (추가 검색: go.dev/ref/mem)
- **T12.Q11** — goreleaser, vcs stamping, distroless (추가 검색: goreleaser.com builds; runtime/debug ReadBuildInfo vcs.revision; distroless static Go)
- **T12.Q12** — git-worktree docs and origin/main sync practice (추가 검색: git-scm.com/docs/git-worktree)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|
| `pkg-modernc-sqlite` | modernc.org/sqlite package documentation | modernc.org / cznic | 2026 | official-doc | <https://pkg.go.dev/modernc.org/sqlite> | 5 | 5 | Driver name 'sqlite' (blank import); NewConnector+sql.OpenDB is equivalent; _pragma values run as 'PRAGMA <value>' without validation; text after ';' also runs unless StrictPragmas(true); _busy_timeout (alias _timeout) is validated and applied first, before _pragma values |
| `pkg-database-sql` | database/sql package documentation | Go Authors | 2026 | official-doc | <https://pkg.go.dev/database/sql> | 5 | 5 | SetMaxOpenConns n<=0 = unlimited (default); Default max idle connections is 2; BeginTx: if ctx is canceled the tx is rolled back and Commit returns an error |
| `pkg-context` | context package documentation | Go Authors | 2026 | official-doc | <https://pkg.go.dev/context> | 5 | 5 | WithoutCancel: not canceled with parent; no deadline, Done nil, Err nil; values preserved; AfterFunc runs f in its own goroutine after ctx is done; stop() returns true only if it prevented f and does not wait for f; WithCancelCause: Err() still Canceled, Cause returns supplied error; first cause wins |
| `pkg-testing-synctest` | testing/synctest package documentation | Go Authors | 2026 | official-doc | <https://pkg.go.dev/testing/synctest> | 5 | 5 | synctest.Test(t, f) (Go 1.25) runs f in an isolated bubble; fails on bubble deadlock; Bubble clock starts 2000-01-01 00:00 UTC; advances only when all bubble goroutines are durably blocked; synctest.Wait blocks until all other bubble goroutines are durably blocked |
| `pkg-time` | time package documentation (Monotonic Clocks, Timer/Ticker) | Go Authors | 2026 | official-doc | <https://pkg.go.dev/time> | 5 | 5 | Wall clock tells time, monotonic clock measures time; AddDate, Round, Truncate, In, Local, UTC strip monotonic; t.Round(0) strips; Sub/Before/After/Equal/Compare use monotonic when both values have it |
| `pkg-testing` | testing package documentation | Go Authors | 2026 | official-doc | <https://pkg.go.dev/testing> | 5 | 5 | Cleanup runs last-added-first after the test and all subtests complete; T.Setenv/T.Chdir cannot be used in parallel tests or tests with parallel ancestors; T.Context is canceled just before Cleanup functions run |
| `pkg-httptest` | net/http/httptest package documentation | Go Authors | 2026 | official-doc | <https://pkg.go.dev/net/http/httptest> | 5 | 5 | NewServer = NewUnstartedServer + Start on loopback; caller must Close (blocks until outstanding requests finish); NewUnstartedServer lets tests change Config/TLS before Start/StartTLS; Server.Client() trusts the test cert and closes idle connections on Close |
| `pkg-net-http` | net/http package documentation (Client, MaxBytesReader) | Go Authors | 2026 | official-doc | <https://pkg.go.dev/net/http> | 4 | 5 | Client.Timeout covers connect, redirects and body read; zero = no timeout; Clients/Transports are concurrency-safe and should be reused; Body must be read to EOF and closed for keep-alive reuse |
| `pkg-sync` | sync package documentation | Go Authors | 2026 | official-doc | <https://pkg.go.dev/sync> | 4 | 5 | Mutex: nth Unlock happens before mth Lock (n<m); failed TryLock gives no ordering; RWMutex: no recursive read locking; blocked Lock excludes new readers; no upgrade/downgrade; WaitGroup.Go (Go 1.25) runs f in a tracked goroutine; f must not panic |
| `pkg-sync-atomic` | sync/atomic package documentation | Go Authors | 2026 | official-doc | <https://pkg.go.dev/sync/atomic> | 4 | 5 | If B observes A's effect, A synchronizes before B; one sequentially consistent order; Typed atomics preferred over function forms; And/Or added in Go 1.23 |
| `pkg-errors` | errors package documentation (Is, As, AsType, Join) | Go Authors | 2026 | official-doc | <https://pkg.go.dev/errors> | 4 | 5 | Is/As traverse the tree pre-order depth-first via Unwrap() error or Unwrap() []error; errors.Is preferred over ==; an Is method should compare shallowly; errors.AsType[E](err) (E, bool) added in Go 1.26.0; preferred over As |
| `pkg-encoding-json` | encoding/json package documentation (v1) | Go Authors | 2026 | official-doc | <https://pkg.go.dev/encoding/json> | 3 | 5 | DisallowUnknownFields errors on unknown keys; UseNumber decodes numbers as json.Number; json.Number gained MarshalJSONTo/UnmarshalJSONFrom (Go 1.25) |
| `pkg-encoding-json-v2` | encoding/json/v2 package documentation | Go Authors | 2026 | official-doc | <https://pkg.go.dev/encoding/json/v2> | 3 | 5 | Unknown members ignored by default; RejectUnknownMembers -> ErrUnknownName; Case-sensitive name matching by default; Rejects duplicate member names and invalid UTF-8 by default |
| `pkg-cmd-go` | cmd/go command documentation | Go Authors | 2026 | official-doc | <https://pkg.go.dev/cmd/go> | 3 | 5 | go test caches successful package results; -count=1 disables caching; go vet runs a high-confidence subset during go test; -trimpath removes file system paths from the executable |
| `gh-jonboulle-clockwork` | jonboulle/clockwork: a fake clock for Go | Jonathan Boulle and contributors | 2026 | code | <https://github.com/jonboulle/clockwork> | 4 | 4 | NewRealClock() for production; NewFakeClock()/NewFakeClockAt(t) for tests; Advance(d) notifies waiters before returning; BlockUntilContext(ctx, n) waits for n waiters; BlockUntil deprecated |
| `gh-benbjohnson-clock` | benbjohnson/clock: Clock library for mocking time | Ben Johnson | 2023 | code | <https://github.com/benbjohnson/clock> | 3 | 4 | Archived 2023-05-18; 'This module is no longer maintained'; Mock starts at Unix epoch; moves only via Add; Last release v1.3.5 (May 2023) |
| `gh-exhaustive` | nishanths/exhaustive: enum switch exhaustiveness analyzer | Nishanth Shanmugham | 2026 | code | <https://github.com/nishanths/exhaustive> | 3 | 4 | Checks expression switches on enum-like named types; -check=typeswitch for type switches; Flags include -default-signifies-exhaustive style, -defrequire, -constignore, -typeignore |
| `gh-nilaway` | uber-go/nilaway: static nil panic detector | Uber | 2026 | code | <https://github.com/uber-go/nilaway> | 3 | 4 | Cross-package nil-flow analysis with no annotations; Scope with -include-pkgs; golangci-lint integration only via custom plugin build |
| `gh-golangci-lint` | golangci/golangci-lint repository | golangci-lint maintainers | 2026 | code | <https://github.com/golangci/golangci-lint> | 3 | 4 | Parallel runner with caching, YAML config, 100+ linters; Repo ships .golangci.reference.yml |
| `gh-project-layout` | golang-standards/project-layout README | golang-standards community | 2026 | code | <https://github.com/golang-standards/project-layout> | 5 | 3 | 'NOT an official standard defined by the core Go dev team'; /cmd: one dir per executable; keep minimal; /internal: compiler-enforced privacy; may appear at multiple levels |

### 출처 요약

- **`pkg-modernc-sqlite`** — Official docs for the pure-Go SQLite driver chosen in ADR-0005. They cover the driver name, file: URI DSNs, the _pragma/_busy_timeout/_txlock/_time_format/_timezone parameters and the order they are applied in, the multi-statement risk of _pragma (StrictPragmas), and the fact that connection-scoped state persists across pool borrowers. They also report a CPU-bound slowdown compared with C SQLite and say to pin modernc.org/libc. _(접근: open access; only one collector pass read the _busy_timeout/_txlock section)_
- **`pkg-database-sql`** — Standard library reference for pool limits, BeginTx context semantics, dedicated Conn, and how long prepared statements live. It is the basis for the single-writer plus read-pool design in internal/store. _(접근: open access; read only to 100k chars)_
- **`pkg-context`** — Official context reference for WithoutCancel, AfterFunc, WithCancelCause and WithDeadline, plus the rules against storing a context in a struct and for always calling cancel. WithoutCancel is the primitive for writes that must finish after cancellation. _(접근: open access)_
- **`pkg-testing-synctest`** — Synctest bubbles (GA in Go 1.25) run a fake clock that advances only when every goroutine in the bubble is durably blocked. Mutexes, network I/O and syscalls are not durably blocking. The page reflects go1.27.3, and synctest.Sleep is marked 1.27. _(접근: open access; page shows go1.27.3)_
- **`pkg-time`** — Defines the split between the wall clock and the monotonic clock, lists which operations strip the monotonic reading, and describes the Go 1.23 change that made timers and tickers GC-eligible. _(접근: open access; partially read)_
- **`pkg-testing`** — Authoritative semantics for T.Cleanup, T.Setenv, T.Chdir, T.Context, T.Parallel, subtests, B.Loop and ArtifactDir. _(접근: open access; -shuffle not covered; version annotations of T.Context/ArtifactDir not verified against go1.26)_
- **`pkg-httptest`** — Reference for httptest.Server, NewServer, NewUnstartedServer, NewTLSServer, Client and ResponseRecorder. The page recommends NewTestServer, but that function is annotated 'added in go1.27.0' and so does not exist at the repo's go1.26.5. _(접근: open access; page shows go1.27.3)_
- **`pkg-net-http`** — Client-side rules: reuse one Client/Transport, set Client.Timeout or pass a context, and read the response body to EOF before closing it. MaxBytesReader is meant for server-side request bodies. _(접근: open access; read only to 100k chars)_
- **`pkg-sync`** — Covers Mutex, RWMutex, Once and WaitGroup (including WaitGroup.Go from 1.25) and the happens-before edges each one provides. _(접근: open access)_
- **`pkg-sync-atomic`** — States that atomics are sequentially consistent and form synchronizes-before edges, recommends the typed atomics, and gives a copy-on-write Value example for snapshots. _(접근: open access)_
- **`pkg-errors`** — Defines how Is, As and Join traverse an error tree, and documents the generic AsType added in Go 1.26. _(접근: open access)_
- **`pkg-encoding-json`** — Covers DisallowUnknownFields, UseNumber and json.Number. The page (go1.27.3) says v1 is built on top of v2 and that new code should use v2. _(접근: open access; shown at go1.27.3)_
- **`pkg-encoding-json-v2`** — Describes v2's stricter defaults. Unknown members are still ignored by default unless RejectUnknownMembers is set. The page does not say whether v2 needs a GOEXPERIMENT flag at go1.26. _(접근: open access; experiment status unconfirmed)_
- **`pkg-cmd-go`** — Reference for the go command: the test cache and -count=1, the vet subset that runs during go test, -trimpath, -race platform support, and go mod edit -toolchain. _(접근: open access; truncated; GOTOOLCHAIN/-shuffle not seen)_
- **`gh-project-layout`** — The community layout that CLAUDE.md cites. The README says it is NOT an official Go-team standard and points readers to 'Organizing a Go module'. _(접근: open access; official go.dev page unreachable)_
- **`gh-jonboulle-clockwork`** — A maintained Clock interface with real and fake implementations. Advance and BlockUntilContext give tests a deterministic handshake. _(접근: open access)_
- **`gh-benbjohnson-clock`** — The historic reference design for an injected clock. The repository was archived on 2023-05-18 and is no longer maintained, so do not add it as a dependency. _(접근: open access)_
- **`gh-exhaustive`** — Reports enum constants missing from switch statements, including across packages. The README does not explain what its flags mean. _(접근: open access; flag semantics not on README)_
- **`gh-nilaway`** — An interprocedural analyzer that tracks nil flows across functions. It only works with golangci-lint as a custom plugin and reports false positives, so it should start as advisory. _(접근: open access)_
- **`gh-golangci-lint`** — A linter runner that bundles more than 100 linters. Only the repository is cited here. The v2 config schema is unverified because golangci-lint.run was unreachable. _(접근: open access; v2 specifics unverified)_
