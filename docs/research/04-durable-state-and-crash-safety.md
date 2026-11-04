# Durable state and crash safety: SQLite durability, outbox/WAL, sentinels, retention

> **왜 중요한가** — ADR-0005 (modernc.org/sqlite, synchronous=FULL + WAL, event-per-transaction, single writer), ADR-0006 (append-only segments, dir fsync, torn tail, crc32c), ADR-0012 (clean-shutdown sentinel, durable-before-visible), store V3-V5 migrations (#20/#29/#14 prune), env.example DSN restrictions. Gaps: fsync guarantees of the pure-Go driver, filesystem/VM semantics on the deployment host, backup/restore of the store, and what 'durable' means on cloud block storage.

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 38건. 상태 집계: 🟡 부분 10, ✅ 답변됨 2

## 핵심 결론

- **modernc.org/sqlite specifics: does its VFS call fsync/fdatasync identically to C SQLite; known issues with WAL, locking, busy_timeout, multiple connections, memory, and performance; version/CVE history relevant to v1.53.0.** — fsync parity: the Linux build always calls full fsync() and never fdatasync() (about 1010 vs 0 for 1000 FULL commits). The v1.53.0 VFS calls libc.Xfsync, which issues SYS_fsync. That is at least as strong as C SQLite, which uses fdatasync, at about 2x the commit latency (4.5 vs 2.25 ms p50). An opt-in, process-wide Fdatasync() switch is pending [modernc-sqlite-issue10-fdatasync].

Version: v1.53.0 bundles SQLite 3.53.2 [modernc-sqlite-changelog]. That is past the 3.51.3 WAL-reset fix for the corruption bug in 3.7.0 to 3.51.2, which needs two connections writing or checkpointing concurrently [sqlite-wal-reset-bug-techzine]. No CVE specific to modernc was found. The latest release is v1.61.1 [modernc-sqlite-pkgdoc].

Pitfalls:
- Only _pragma, _time_format and _txlock are honoured in the DSN [modernc-dsn-pragma-issue].
- _pragma text is executed as SQL unless StrictPragmas is set [modernc-sqlite-pkgdoc].
- Connection state persists across pool borrowers, and the pool is unbounded [modernc-sqlite-pkgdoc].
- busy and setlk timeouts are per connection [sqlite-setlk-timeout].
- OFD locks are opt-in, while -shm uses POSIX locks [modernc-sqlite-pkgdoc].
- An unclosed *sql.Rows pins a read transaction and starves the checkpoint [turso-go-sqlite-wal].
- libc must match exactly [modernc-sqlite-changelog].
- CPU-bound work is about 1.3x to 2x slower than C SQLite, but fsync latency dominates for this bot [modernc-sqlite-pkgdoc].
- **Write-ahead intent / outbox pattern literature (Helland, Kleppmann DDIA ch.7/11, Debezium outbox) — conditions under which a WAL marker written before an external call gives at-most-once vs at-least-once, matching ADR-0002's prepared/submit-attempted/acked design.** — Atomicity holds only within one entity, and messages between entities may repeat [helland-life-beyond-dt]. Deduplication and crash recovery must be completed at the endpoints [saltzer-end-to-end-1984]. An outbox commits intent together with state, but its relay is at-least-once [microservices-io-outbox][debezium-outbox-release-1-8].

For a marker written before an external call:
- At-least-once results if a restart replays a call whose marker shows intent but no ack. That is safe only with server-side dedup.
- At-most-once results if `submit-attempted` is made durable before the POST and a restart never resubmits blindly. It reconciles through GET /orders/{orderId} and otherwise halts as ambiguous.

ADR-0002/0003 take the at-most-once path with orderId as the truth key. Per the spec, clientOrderId is only a server-side duplicate guard and is not queryable afterwards.

A third-party guide claims resending the same clientOrderId returns the earlier order within about 10 minutes [toss-clientorderid-algolab]. ADR-0002 records that duplicate behaviour is undefined in the spec. Until the spec confirms the claim, it must not be used to justify a resend.

## 질문별 답변

### T4.Q1 · SQLite durability semantics in WAL mode with synchronous=FULL vs NORMAL: exact fsync points, checkpoint behaviour, 'power-safe overwrite', and the documented 'How To Corrupt An SQLite Database File' failure list.

🟡 부분 · 우선순위 must · 확신 medium

Under WAL with synchronous=FULL the WAL is synced after every commit, so commits survive power loss or an OS crash. Under NORMAL the WAL is synced only before a checkpoint and the DB file after it: the database stays consistent, but the latest commits can roll back. An application-only crash is safe at any level. EXTRA differs from FULL only in DELETE journal mode (a directory sync after journal unlink) [sqlite-pragma-synchronous-psow]. A checkpoint of FULL or higher makes earlier NORMAL commits durable; PASSIVE does not [sqlite-forum-sync-durability]. Auto-checkpoint runs at 1000 pages and on last close, and a checkpoint cannot pass the oldest reader [sqlite-wal-doc]. PSOW is assumed by default [sqlite-pragma-synchronous-psow].

howtocorrupt lists:
- external overwrites
- a hot journal or WAL separated or deleted
- a raw copy without the WAL or taken mid-transaction
- fd reuse and fork
- broken POSIX or network-filesystem locking
- drives that do not honour sync
- sync disabled by PRAGMA [sqlite-howtocorrupt]

Repo: openConn sets busy_timeout, WAL, FULL and foreign_keys through _pragma, the only DSN form modernc honours [modernc-dsn-pragma-issue].

Still unread: the canonical pragma.html wording, checkpoint_fullfsync, and the text of the sync-lies section.

### T4.Q2 · modernc.org/sqlite specifics: does its VFS call fsync/fdatasync identically to C SQLite; known issues with WAL, locking, busy_timeout, multiple connections, memory, and performance; version/CVE history relevant to v1.53.0.

✅ 답변됨 · 우선순위 must · 확신 high

fsync parity: the Linux build always calls full fsync() and never fdatasync() (about 1010 vs 0 for 1000 FULL commits). The v1.53.0 VFS calls libc.Xfsync, which issues SYS_fsync. That is at least as strong as C SQLite, which uses fdatasync, at about 2x the commit latency (4.5 vs 2.25 ms p50). An opt-in, process-wide Fdatasync() switch is pending [modernc-sqlite-issue10-fdatasync].

Version: v1.53.0 bundles SQLite 3.53.2 [modernc-sqlite-changelog]. That is past the 3.51.3 WAL-reset fix for the corruption bug in 3.7.0 to 3.51.2, which needs two connections writing or checkpointing concurrently [sqlite-wal-reset-bug-techzine]. No CVE specific to modernc was found. The latest release is v1.61.1 [modernc-sqlite-pkgdoc].

Pitfalls:
- Only _pragma, _time_format and _txlock are honoured in the DSN [modernc-dsn-pragma-issue].
- _pragma text is executed as SQL unless StrictPragmas is set [modernc-sqlite-pkgdoc].
- Connection state persists across pool borrowers, and the pool is unbounded [modernc-sqlite-pkgdoc].
- busy and setlk timeouts are per connection [sqlite-setlk-timeout].
- OFD locks are opt-in, while -shm uses POSIX locks [modernc-sqlite-pkgdoc].
- An unclosed *sql.Rows pins a read transaction and starves the checkpoint [turso-go-sqlite-wal].
- libc must match exactly [modernc-sqlite-changelog].
- CPU-bound work is about 1.3x to 2x slower than C SQLite, but fsync latency dominates for this bot [modernc-sqlite-pkgdoc].

### T4.Q3 · POSIX durability traps: directory fsync after rename/create, fsync error semantics (fsyncgate, PostgreSQL 2018), ext4 vs xfs vs btrfs data=ordered, O_DSYNC, and cloud block storage (EBS, GCE PD) durability claims — which apply to the audit segment protocol (ADR-0006 point 4)?

🟡 부분 · 우선순위 must · 확신 medium

Directory fsync: fsync of a file does not persist its directory entry. A create, rename or unlink needs a parent-directory fsync, and durable_rename is fsync tmp, rename, fsync new, fsync dir [pg-durable-rename-commit][pillai-alice-osdi14][lwn-fsync-posix-ext4]. ext4's auto_da_alloc rename heuristics are not portable [lwn-fsync-posix-ext4].

fsync errors: after a failed fsync, ext4, XFS and Btrfs can mark the pages clean, so a retry can falsely succeed. Another caller can consume the error, and Linux 4.13+ reports only errors that occur after open() [rebello-fsync-atc20][lwn-pg-fsync-surprise][pg-wiki-fsync-errors]. PostgreSQL PANICs and replays its WAL (data_sync_retry=off) [postgres-fd-c-fsync-retry].

Cloud block storage: the EBS and PD figures describe volume loss, not when a guest fsync reaches stable media [aws-ebs-pg-whitepaper][gcp-pd-durability].

Applied to ADR-0006 point 4, all of these hold:
- directory fsync on segment create, rotate and unlink
- fsync failure is terminal and never retried
- no reliance on ext4 heuristics
- crc32c framing for torn tails, because appends are not atomic
- recovery reads only on-disk state, never the page cache [rebello-fsync-atc20]

Not sourced: O_DSYNC, data=ordered vs data=journal, and XFS/Btrfs specifics.

### T4.Q4 · Crash-consistency testing techniques: ALICE/CrashMonkey-style file-system crash simulation, SQLite's own test suite approach, Go-level fault injection of fsync, and how to test 'torn tail' recovery deterministically.

🟡 부분 · 우선순위 should · 확신 medium

Techniques found:
- ALICE: replay prefixes and reorderings of recorded syscalls [pillai-alice-osdi14].
- CrashMonkey and ACE: bounded exhaustive workloads with a crash after each fsync-class call; most bugs need three or fewer operations [mohan-crashmonkey-osdi18].
- CuttleFS: injects fsync failures [rebello-fsync-atc20].
- LazyFS: deterministic lost writes and torn writes (torn-op, torn-seq) driven by a FIFO [ramos-lazyfs-vldb24].
- SQLite's own test VFS: fail the Nth I/O operation once or persistently; a crash VFS snapshots after N operations, applies power-loss damage, then runs integrity_check [sqlite-testing-doc].

Applied here:
- (i) Through the audit writer's fs seam, enumerate a fail or crash at operation N over write, fsync-tmp, rename and fsync-dir, in single and persistent modes.
- (ii) Truncate a real segment at every byte of the last [len][crc32c][payload] frame and assert that exactly the committed prefix is recovered.
- (iii) Optionally run LazyFS torn-op in Linux CI.

No Go-specific fsync fault-injection library was found.

### T4.Q5 · Write-ahead intent / outbox pattern literature (Helland, Kleppmann DDIA ch.7/11, Debezium outbox) — conditions under which a WAL marker written before an external call gives at-most-once vs at-least-once, matching ADR-0002's prepared/submit-attempted/acked design.

✅ 답변됨 · 우선순위 should · 확신 medium

Atomicity holds only within one entity, and messages between entities may repeat [helland-life-beyond-dt]. Deduplication and crash recovery must be completed at the endpoints [saltzer-end-to-end-1984]. An outbox commits intent together with state, but its relay is at-least-once [microservices-io-outbox][debezium-outbox-release-1-8].

For a marker written before an external call:
- At-least-once results if a restart replays a call whose marker shows intent but no ack. That is safe only with server-side dedup.
- At-most-once results if `submit-attempted` is made durable before the POST and a restart never resubmits blindly. It reconciles through GET /orders/{orderId} and otherwise halts as ambiguous.

ADR-0002/0003 take the at-most-once path with orderId as the truth key. Per the spec, clientOrderId is only a server-side duplicate guard and is not queryable afterwards.

A third-party guide claims resending the same clientOrderId returns the earlier order within about 10 minutes [toss-clientorderid-algolab]. ADR-0002 records that duplicate behaviour is undefined in the spec. Until the spec confirms the claim, it must not be used to justify a resend.

### T4.Q6 · Clean-shutdown sentinel / 'dirty flag' prior art (journaling file systems' dirty bit, PostgreSQL pg_control state, MySQL InnoDB recovery flags, systemd unit state) and the known pitfalls when sentinel and data are in different durability domains.

🟡 부분 · 우선순위 should · 확신 medium

pg_control stores a versioned 7-state DBState that includes an intermediate SHUTDOWNING state. A format change requires a PG_CONTROL_VERSION bump [postgres-pg-control-h]. Clean is written only after the shutdown checkpoint is durable, and any other state at boot triggers recovery. A crash during the end-of-recovery checkpoint can leave a misleading state [pg-control-state-thread].

ADR-0012's sentinel lives in the store, the same durability domain as the data, which matches pg_control.

The pitfall with separate domains: one side becomes durable without the other. Examples are a sentinel file fsynced without its directory, or a data fsync that failed while the sentinel's succeeded. The next boot then trusts a stale clean [pillai-alice-osdi14][rebello-fsync-atc20][pg-durable-rename-commit]. The audit segments are in a different domain from the store sentinel, so recovery must not infer audit completeness from a clean sentinel.

Not sourced: the ext4 s_state, InnoDB and systemd analogues.

### T4.Q7 · Retention and prune for bounded stores: SQLite VACUUM/auto_vacuum behaviour, WAL growth with long readers (checkpoint starvation), index strategy for time-cutoff deletes (V5 index), and file-size monitoring.

🟡 부분 · 우선순위 should · 확신 medium

Long or overlapping readers starve the checkpoint and the WAL grows without bound [sqlite-wal-doc][syncthing-checkpoint-starvation]. In Go, a forgotten rows.Close() is the typical cause [turso-go-sqlite-wal]. Litestream deliberately holds such a reader [litestream-how-it-works].

Remedies:
- short read transactions, the prune scan in particular
- wal_checkpoint(TRUNCATE) when idle
- monitor the -wal and DB file sizes [syncthing-checkpoint-starvation][sqlite-wal-doc]

VACUUM is blocked by open readers too [turso-go-sqlite-wal]. VACUUM INTO gives a compact copy [sqlite-lang-vacuum].

Not sourced; general knowledge, not cited:
- auto_vacuum and incremental_vacuum: freed pages go to the freelist and the file does not shrink without VACUUM.
- Indexing for the cutoff delete: an index on (terminal state, cutoff timestamp) makes the V5 prune a range delete.

### T4.Q8 · Backup and restore of a live SQLite store (sqlite3 .backup API, litestream, VACUUM INTO), and consistency between store backup and audit segments — and ADR-0007 point 7 implication that a restored store boots halted.

🟡 부분 · 우선순위 should · 확신 medium

A raw cp of a live database is unsafe [sqlite-howtocorrupt]. Safe options:
- The backup API, which modernc wraps since v1.52.0 [sqlite-backup-doc][modernc-sqlite-changelog].
- VACUUM INTO. SQLite does not fsync its output, so the tooling must fsync the file and its directory [sqlite-lang-vacuum].
- Litestream: asynchronous, so RPO > 0. It is a second process holding a long reader, it adds a _litestream_lock table, and it is beta. It does not cover audit segments [litestream-how-it-works].

A store snapshot is taken at one point in time and cannot be consistent with audit segments written after it.

ADR-0007 point 7 and ADR-0012: a backup taken while the bot runs carries `running` and boots halted. A backup taken after a clean shutdown carries `clean` and would boot unhalted, losing later halt history. The restore procedure must therefore force the sentinel to `running`, and reconciliation (ADR-0003) then rebuilds truth from the API.

No source covered joint snapshots of the store and the audit directory.

### T4.Q9 · Schema migration practices for embedded SQLite in Go (versioned migrations, user_version pragma, forward-only, migration tests on real on-disk fixtures) and how to guard against migration-version drift (retro item 4).

🟡 부분 · 우선순위 nice · 확신 low

Prior art: migrations versioned through user_version, and goose's go:embed migrations with a version table [sqlitemigrate-zerokspot]. External tools such as Litestream can add tables, so schema checks should tolerate or reject them explicitly [litestream-how-it-works].

The repo's per-step transaction with user_version advanced inside it is the forward-only pattern.

Inference, not sourced: commit on-disk V1 to V4 fixtures, migrate each to head, and assert that a user_version above the binary's maximum refuses to open and boots halted. Pin the head version in a test so retro item 4 drift fails CI.

### T4.Q10 · Single-process exclusion for the store: SQLite locking is not a lease; options are flock on a lock file, lease rows with TTL, systemd single-instance; what the failure modes (stale lock blocking restart) are — needed before any multi-instance deployment (internal/runtime/doc.go).

🟡 부분 · 우선순위 should · 확신 medium

SQLite locking is not ownership. POSIX locks drop on any fd close, and WAL needs a single host [sqlite-howtocorrupt][sqlite-wal-doc]. A second process on the store recreates the concurrent write and checkpoint pattern behind the WAL-reset bug [sqlite-wal-reset-bug-techzine].

Options:
- flock with TryLock on a sidecar file. The kernel releases it on exit, so no stale lock blocks a restart. It is advisory and unreliable on NFS [gofrs-flock].
- A TTL lease row. It needs renewal and fencing; without fencing tokens a paused zombie can write after expiry, and a stale lease blocks restart until the TTL runs out [kleppmann-distributed-locking].
- systemd single-instance supervision. Not sourced.

For a single host, flock on the data directory, taken before the sentinel flip, is the lowest-risk option.

### T4.Q11 · Disk-full and ENOSPC handling: SQLite behaviour on full disk during WAL append, how to pre-reserve space, and how to make 'store cannot write' a fail-closed halt without losing the halt itself (ADR-0012 arm (a)).

🟡 부분 · 우선순위 should · 확신 low

After SQLITE_FULL, IOERR or BUSY, SQLite may roll back automatically with a version-dependent scope, so issue an explicit ROLLBACK. SQLITE_FULL can also come from max_page_count, and retrying unchanged fails. Old WAL builds raised SIGBUS when the disk filled during shm mapping (fixed with fallocate) [sqlite-forum-sqlite-full].

fsync failure is terminal; PostgreSQL crashes and replays from its WAL [pg-wiki-fsync-errors][postgres-fd-c-fsync-retry][rebello-fsync-atc20]. SQLite's own test suite exercises persistent I/O failure [sqlite-testing-doc].

ADR-0012 arm (a): a failed `prepared` write blocks submission, so it fails closed. A halt that cannot be persisted is covered because the sentinel stays `running`, so the next boot is halted.

Not sourced: the state of WAL frames after ENOSPC mid-append, and space reservation. Practice: a free-space watermark that halts before the disk is full, plus a reserve file.

### T4.Q12 · File permission and integrity hardening for the store and audit files (0600, immutable segments, checksums, tamper evidence) in an OSS-quality unattended deployment (#28 L-1 store hardening).

🟡 부분 · 우선순위 nice · 확신 low

SQLite cannot defend against any process that can open its file, so single-owner 0600 permissions on the DB, -wal and -shm, on a local filesystem, are the control [sqlite-howtocorrupt][sqlite-wal-doc].

Repo: secureDBFile and the '#', '?' and '%' path restriction in the store; 0600 segments with [len][crc32c][payload] framing in the audit writer.

crc32c detects torn writes and corruption but is not tamper evidence; that needs a hash chain or a MAC. No source was found for tamper-evident logs or chattr +a/+i.

## 프로젝트 함의

- ADR-0005 point 4 (FULL+WAL, disabling it forbidden) is confirmed, and modernc v1.53.0 is stricter than C SQLite because it uses full fsync [modernc-sqlite-issue10-fdatasync]. A future Fdatasync(true) opt-in is still a durability-relevant change and should need ADR-0005 review. Read back journal_mode, synchronous and busy_timeout on both handles at open and fail closed on any mismatch [modernc-dsn-pragma-issue].
- go.mod pins v1.53.0, which bundles SQLite 3.53.2 and is past the 3.51.3 WAL-reset fix. Add a test that sqlite_version() >= 3.51.3 so a downgrade cannot reintroduce the bug, and keep libc in lockstep [modernc-sqlite-changelog][sqlite-wal-reset-bug-techzine].
- Bound readDB in internal/store, keep the V5 prune read transaction short with rows closed, and monitor -wal size [turso-go-sqlite-wal][sqlite-wal-doc].
- ADR-0006 point 4 (directory fsync, no fsync retry, crc32c torn tail) matches durable_rename, data_sync_retry=off and the ATC'20 findings [pg-durable-rename-commit][postgres-fd-c-fsync-retry][rebello-fsync-atc20]. Extend the no-retry rule to the store's SQLITE_IOERR_FSYNC and SQLITE_FULL, with an explicit ROLLBACK [sqlite-forum-sqlite-full].
- ADR-0006 crash tests: enumerate operation-N fail and crash points through the fs seam, as SQLite and CrashMonkey do, and truncate the tail at every byte [sqlite-testing-doc][mohan-crashmonkey-osdi18].
- ADR-0012: the sentinel in the store matches pg_control. Consider a 'shutting-down' state and a sentinel format version [postgres-pg-control-h]. A clean sentinel says nothing about the audit domain.
- ADR-0002: the spec leaves duplicate clientOrderId behaviour undefined, while a third-party guide claims replay within about 10 minutes [toss-clientorderid-algolab]. Keep the no-blind-resend rule; do not use the claim to justify a resend without spec confirmation.
- Backup is not covered by any ADR. Add a runbook: backup API or VACUUM INTO with an explicit fsync of the output, never cp, and force the sentinel to running on restore (ADR-0007 point 7). Litestream conflicts with ADR-0005's single-process premise [sqlite-lang-vacuum][litestream-how-it-works].
- internal/runtime/doc.go: prefer a kernel-released flock on the data directory over a TTL lease row, which needs fencing [gofrs-flock][kleppmann-distributed-locking].
- configs/env.example: require the store and audit directories to be on a local, non-network filesystem. Cloud volume-durability figures are a backup concern, not an fsync guarantee [aws-ebs-pg-whitepaper].

## 출처 간 상충

- Default synchronous level in WAL mode: the docs say FULL in general, while blogs say NORMAL in WAL builds or that drivers override it. This does not matter for the repo, which sets FULL explicitly.
- EXTRA: older doc text says it matters only in DELETE mode, and newer text may differ. Verify against pragma.html.
- Bundled SQLite version on pkg.go.dev (3.54.1 vs a 3.53.4 benchmark). For the pin, trust the v1.53.0 lib constant (3.53.2).
- modernc vs mattn performance ranges from 20% slower to 2.8x slower, and one source has modernc faster on reads. It depends on the workload and is irrelevant here.
- Outbox semantics: most sources say at-least-once, while one OneUptime table and one RisingWave post say Debezium is exactly-once. The Debezium post itself says at-least-once.
- clientOrderId duplicate behaviour: ADR-0002 reads the spec as undefined, while AlgoLab claims a 10-minute replay. The spec wins until it is verified.
- The durable_rename commit date differs between mirrors (2015/2016 vs 2019); they may be separate commits.
- The ALICE breakdown varies between summaries (about 30 vs roughly half exposed on current filesystems).

## 남은 공백

- **T4.Q1** — Canonical sqlite.org pragma.html, psow.html, atomiccommit.html and the text of howtocorrupt's sync section; sqlite.org could not be fetched. (추가 검색: site:sqlite.org pragma synchronous EXTRA WAL; site:sqlite.org howtocorrupt "honor sync"; site:sqlite.org checkpoint_fullfsync)
- **T4.Q3** — EBS/PD flush and FUA semantics; O_DSYNC vs fsync; ext4 data=ordered vs XFS for append and rename. (추가 검색: EBS NVMe flush FUA acknowledged write durable; GCE persistent disk write cache flush; O_DSYNC vs fdatasync append-only log Linux)
- **T4.Q4** — Go fsync fault-injection libraries; LazyFS usage from Go CI. (추가 검색: golang filesystem fault injection fsync error test; LazyFS CI torn-op example)
- **T4.Q5** — Toss OpenAPI spec text on duplicate clientOrderId behaviour; DDIA ch.7/11 text. (추가 검색: openapi.tossinvest.com clientOrderId duplicate; Kleppmann DDIA idempotence end-to-end exactly-once)
- **T4.Q6** — ext4 s_state, InnoDB shutdown flags, systemd unit state. (추가 검색: ext4 s_state EXT4_VALID_FS; InnoDB innodb_fast_shutdown crash recovery)
- **T4.Q7** — Official auto_vacuum/incremental_vacuum and range-delete indexing. (추가 검색: site:sqlite.org pragma auto_vacuum incremental_vacuum)
- **T4.Q8** — Joint consistent backup of a SQLite store plus sibling append-only logs. (추가 검색: consistent backup sqlite plus append-only log ordering; filesystem snapshot sqlite WAL consistent backup)
- **T4.Q9** — Fixture-based migration tests; refusing a newer schema version. (추가 검색: golang sqlite migration golden fixture user_version test)
- **T4.Q10** — systemd single-instance patterns. (추가 검색: systemd single instance flock ExecStart)
- **T4.Q11** — WAL state after ENOSPC mid-append; space reservation. (추가 검색: sqlite WAL disk full SQLITE_FULL wal file recovery; fallocate reserve file daemon disk full)
- **T4.Q12** — Tamper-evident log designs; append-only file flags. (추가 검색: Crosby Wallach tamper-evident logging; chattr +a append-only audit log)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|
| `sqlite-wal-doc` | Write-Ahead Logging (SQLite documentation) | SQLite project | 2024 | official-doc | <https://sqlite.org/wal.html> | 5 | 5 | Auto-checkpoint on COMMIT at >=1000 WAL pages and when the last connection closes; A checkpoint cannot pass an active reader's mark, so long readers cause starvation and WAL growth; PASSIVE/FULL/RESTART/TRUNCATE differ in waiting; PASSIVE may not finish; TRUNCATE resets the file to zero |
| `sqlite-howtocorrupt` | How To Corrupt An SQLite Database File | SQLite project | 2024 | official-doc | <https://sqlite.org/howtocorrupt.html> | 5 | 5 | Database state is the DB file plus its journal/WAL; separating them corrupts it; A raw copy taken mid-transaction mixes old and new content; Any process that can open the file can overwrite it |
| `modernc-sqlite-pkgdoc` | modernc.org/sqlite package documentation | modernc.org / cznic | 2026 | official-doc | <https://pkg.go.dev/modernc.org/sqlite> | 5 | 5 | _pragma values are executed as SQL; StrictPragmas(true) rejects multi-statement values (process-wide); Connection state persists across borrowers; the pool is unbounded by default; OFD locking is opt-in; WAL -shm still uses POSIX locks |
| `modernc-sqlite-changelog` | modernc.org/sqlite CHANGELOG / compare v1.50.0...v1.53.0 | modernc.org / cznic | 2026 | code | <https://gitlab.com/cznic/sqlite/compare/v1.50.0...v1.53.0> | 5 | 5 | v1.53.0 bundles SQLite 3.53.2; v1.46.2 is the first release on 3.51.3; v1.52.0 adds Backup wrappers |
| `rebello-fsync-atc20` | Can Applications Recover from fsync Failures? | Anthony Rebello, Yuvraj Patel, Ramnatthan Alagappan, Andrea C. Arpaci-Dusseau, Remzi H. Arpaci-Dusseau | 2020 | paper | <https://www.usenix.org/conference/atc20/presentation/rebello> | 5 | 5 | Pages can be marked clean after an fsync failure, so a retry can falsely succeed; No strategy in the five applications, SQLite included, was sufficient; Recover from on-disk state only |
| `pg-wiki-fsync-errors` | Fsync Errors (PostgreSQL wiki) | PostgreSQL community | 2019 | official-doc | <https://wiki.postgresql.org/wiki/Fsync_Errors> | 5 | 5 | A retried fsync can succeed after the data was lost; PANIC on fsync failure, backpatched to 9.4; Linux 4.13 improved reporting, but only for errors after open() |
| `pillai-alice-osdi14` | All File Systems Are Not Created Equal: On the Complexity of Crafting Crash-Consistent Applications | T. S. Pillai, V. Chidambaram, R. Alagappan, S. Al-Kiswany, A. C. Arpaci-Dusseau, R. H. Arpaci-Dusseau | 2014 | paper | <https://www.usenix.org/conference/osdi14/technical-sessions/presentation/pillai> | 5 | 5 | 60 vulnerabilities across 11 applications; Persistence properties vary between filesystems; testing on one is not enough; Creating a file without a parent-directory fsync may lose the file |
| `sqlite-testing-doc` | How SQLite Is Tested | SQLite project | 2026 | official-doc | <https://www.sqlite.org/testing.html> | 4 | 5 | Inject an I/O error at operation N, as a single or a persistent failure; Snapshot the filesystem after N operations, apply power-loss damage, verify atomic commit held; Verify with PRAGMA integrity_check |
| `sqlite-backup-doc` | Using the SQLite Online Backup API | SQLite project | 2024 | official-doc | <https://sqlite.org/backup.html> | 4 | 5 | The backup API snapshots a live DB as of when the copy started; VACUUM INTO writes a consistent copy; the target must be absent or empty; A raw cp can miss WAL contents |
| `sqlite-lang-vacuum` | VACUUM (including VACUUM INTO) | SQLite project | 2026 | official-doc | <https://sqlite.org/lang_vacuum.html> | 4 | 5 | The VACUUM INTO target must not exist or must be empty; The output is not fsynced by SQLite; power loss can corrupt it; The backup API uses less CPU and is incremental; VACUUM INTO gives the smallest output |
| `postgres-fd-c-fsync-retry` | PostgreSQL src/backend/storage/file/fd.c (data_sync_retry) | PostgreSQL Global Development Group | 2026 | code | <https://raw.githubusercontent.com/postgres/postgres/master/src/backend/storage/file/fd.c> | 4 | 5 | data_sync_retry: 'Whether it is safe to continue running after fsync() fails.'; fsync failure leads to a PANIC and WAL replay |
| `mohan-crashmonkey-osdi18` | Finding Crash-Consistency Bugs with Bounded Black-Box Crash Testing | Jayashree Mohan, Ashlie Martinez, Soujanya Ponnapalli, Pandian Raju, Vijay Chidambaram | 2018 | paper | <https://www.usenix.org/conference/osdi18/presentation/mohan> | 4 | 5 | Most bugs reproduce in 3 or fewer operations, with a crash after an fsync-class call; 10 new bugs and 24 of 26 known ones, including broken rename atomicity |
| `helland-life-beyond-dt` | Life beyond Distributed Transactions: an Apostate's Opinion | Pat Helland | 2007 | paper | <https://queue.acm.org/detail.cfm?id=3025012> | 4 | 5 | Atomicity is per entity only; At-least-once delivery requires idempotence or dedup state |
| `saltzer-end-to-end-1984` | End-to-End Arguments in System Design | J. H. Saltzer, D. P. Reed, D. D. Clark | 1984 | paper | <https://web.mit.edu/saltzer/www/publications/endtoend/endtoend.txt> | 4 | 5 | Duplicate suppression and crash recovery are end-to-end concerns; Lower-level reliability helps performance but does not replace end-to-end checks |
| `postgres-pg-control-h` | PostgreSQL src/include/catalog/pg_control.h (DBState enum) | PostgreSQL Global Development Group | 2026 | code | <https://raw.githubusercontent.com/postgres/postgres/master/src/include/catalog/pg_control.h> | 4 | 5 | 7 DBState values, including DB_SHUTDOWNING; Format change requires a PG_CONTROL_VERSION bump |
| `sqlite-setlk-timeout` | sqlite3_setlk_timeout() | SQLite project | 2025 | official-doc | <https://www.sqlite.org/c3ref/setlk_timeout.html> | 3 | 5 | busy and setlk timeouts are per connection; busy_timeout sets both |
| `sqlite-pragma-synchronous-psow` | PRAGMA synchronous / Powersafe Overwrite (SQLite docsrc check-ins) | SQLite project | 2024 | official-doc | <https://www.sqlite.org/docsrc/info/bcf687da9c172292> | 5 | 4 | WAL+FULL: WAL synced after every commit; WAL+NORMAL: consistent, but recent commits may roll back after power loss or OS crash; EXTRA adds a directory sync after journal unlink in DELETE mode only; no stated WAL effect |
| `modernc-sqlite-issue10-fdatasync` | Linux build: define HAVE_FDATASYNC (modernc-org/sqlite issue #10) | aibeshkov; j-modernc-org | 2025 | code | <https://github.com/modernc-org/sqlite/issues/10> | 5 | 4 | Always fsync, never fdatasync (~1010 vs 0 for 1000 FULL commits); Fdatasync(on) is opt-in, process-wide and off by default; p50 commit latency 4.5 ms with fsync vs 2.25 ms with fdatasync |
| `pg-durable-rename-commit` | pgsql: Avoid unlikely data-loss scenarios due to rename() without fsync (durable_rename) | PostgreSQL committers | 2016 | code | <https://hackorum.dev/topics/199739> | 5 | 4 | Durable replace: fsync tmp, rename, fsync new, fsync parent dir; POSIX does not guarantee rename durability without a directory fsync |
| `lwn-pg-fsync-surprise` | PostgreSQL's fsync() surprise | Jonathan Corbet | 2018 | blog | <https://lwn.net/Articles/752063/> | 4 | 4 | A later fsync success does not cover an earlier failed writeback; Another caller can consume the error; Not Linux-only (OpenBSD, NetBSD) |
| `ramos-lazyfs-vldb24` | When Amnesia Strikes: Understanding and Reproducing Data Loss Bugs with Fault Injection (LazyFS) | Maria Ramos et al. | 2024 | paper | <https://www.vldb.org/pvldb/vol17/p3017-ramos.pdf> | 4 | 4 | Drops or tears unsynced writes at a chosen point (torn-op, torn-seq); Configured via TOML or a FIFO, optionally followed by a crash |
| `microservices-io-outbox` | Pattern: Transactional outbox | Chris Richardson | 2024 | blog | <https://microservices.io/patterns/data/transactional-outbox> | 4 | 4 | The message exists if and only if the business transaction committed; At-least-once delivery; consumers must be idempotent |
| `debezium-outbox-release-1-8` | Debezium 1.8.0.Beta1 released (outbox event router notes) | Debezium project | 2021 | official-doc | <https://debezium.io/blog/2021/11/30/debezium-1.8-beta1-released> | 3 | 4 | Outbox rows are propagated at-least-once |
| `pg-control-state-thread` | Is it correct to update db state in control file as 'shutting down' during end-of-recovery checkpoint? (pgsql-hackers) | PostgreSQL hackers | 2021 | blog | <https://www.postgresql.org/message-id/CALj2ACVn5M8xgQ3RD%3D6rSTbbXRBdBWZ%3DTTOBOY_5%2BedMCkWjHA%40mail.gmail.com> | 3 | 4 | Unclean state at boot triggers recovery; Clean is written only after a durable checkpoint; An intermediate crash can mislabel the state |
| `litestream-how-it-works` | Litestream: How it works / README / WAL truncate guide | Ben Johnson / Litestream | 2026 | official-doc | <https://litestream.io/how-it-works/> | 3 | 4 | A long-lived reader deliberately blocks other checkpointers; Asynchronous; recent writes can be lost; Adds a _litestream_lock table; restored databases include it |
| `kleppmann-distributed-locking` | How to do distributed locking | Martin Kleppmann | 2016 | blog | <https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html> | 3 | 4 | Lease expiry plus a process pause gives two holders; Fencing tokens increase per grant and the resource rejects older ones |
| `aws-ebs-pg-whitepaper` | Amazon EBS volume types (Optimizing PostgreSQL on EC2 using EBS) | Amazon Web Services | 2024 | official-doc | <https://docs.aws.amazon.com/whitepapers/latest/optimizing-postgresql-on-ec2-using-ebs/ebs-volume-types.html> | 3 | 4 | io2 99.999%; gp 99.8% to 99.9%; Nothing on fsync or write-cache semantics |
| `gcp-pd-durability` | High-durability Persistent Disk | Google Cloud | 2023 | official-doc | <https://cloud.google.com/blog/products/compute/high-durability-persistent-disk> | 2 | 4 | 3+ copies; regional PD synchronous across 2 zones; No fsync statement |
| `sqlite-wal-reset-bug-techzine` | Old SQLite bug caused months of outages at Tailscale | Techzine | 2026 | blog | <https://www.techzine.eu/news/infrastructure/143585/old-sqlite-bug-caused-months-of-outages-at-tailscale/> | 4 | 3 | Affected 3.7.0 to 3.51.2; fixed in 3.51.3, 3.44.6 and 3.50.7; Trigger: concurrent write or checkpoint from two or more connections; 3.52.0 was withdrawn |
| `modernc-dsn-pragma-issue` | modernc ignores mattn-style DSN params (CoreScope #576) | dborup/CoreScope | 2026 | code | <https://github.com/dborup/CoreScope/issues/576> | 4 | 3 | Only _pragma, _time_format and _txlock are applied; Verify effective PRAGMAs at startup |
| `turso-go-sqlite-wal` | Something you probably want to know about if you're using SQLite in Golang | Turso blog | n.d. | blog | <https://blog.turso.tech/something-you-probably-want-to-know-about-if-youre-using-sqlite-in-golang-72547ad625f1> | 4 | 3 | An unclosed rows keeps a read transaction open and starves the checkpoint; Example: 4.1 kB DB with a 42 MB WAL |
| `gofrs-flock` | gofrs/flock | gofrs contributors | 2025 | code | <https://github.com/gofrs/flock/wiki> | 4 | 3 | Non-blocking TryLock; the kernel releases it on exit; Advisory; unreliable on NFS |
| `sqlite-forum-sync-durability` | Switching pragma synchronous to upgrade durability (SQLite Forum) | SQLite Forum participants | 2023 | blog | <https://sqlite.org/forum/forumpost/1046c87e75> | 3 | 3 | A FULL+ checkpoint makes earlier NORMAL commits durable; PASSIVE may not |
| `lwn-fsync-posix-ext4` | LWN on fsync, rename and ext4 (359997, 322875) / writeback errors (718943) | LWN.net | 2009 | blog | <https://lwn.net/Articles/359997/> | 3 | 3 | No POSIX guarantee for rename or create durability without a directory fsync; ext4 rename heuristics are not portable; Writeback errors may be reported once |
| `sqlite-forum-sqlite-full` | SQLITE_FULL rollback behaviour / distinguishing fatal errors (SQLite Forum) | SQLite Forum participants (incl. D. R. Hipp) | 2022 | blog | <https://sqlite.org/forum/forumpost/1743f70857?t=h> | 3 | 3 | Issue an explicit ROLLBACK after SQLITE_FULL; scope varies by version; SQLITE_FULL can come from max_page_count; Historical SIGBUS on disk-full in WAL shm, fixed with fallocate |
| `toss-clientorderid-algolab` | AlgoLab: Toss Securities Open API guide 2026 (clientOrderId) | AlgoLab | 2026 | blog | <https://algolab.co.kr/blog/toss-securities-open-api-guide-2026> | 4 | 2 | Same clientOrderId returns the earlier order (claimed); Max 36 characters, ^[a-zA-Z0-9\-_]+$; Claimed 10-minute validity, after which reuse creates a new order |
| `syncthing-checkpoint-starvation` | Dealing with checkpoint starvation (Syncthing forum) | Syncthing developers | 2025 | postmortem | <https://forum.syncthing.net/t/dealing-with-checkpoint-starvation/26121> | 3 | 2 | The WAL grows until a checkpoint completes; Remedies: short reads and wal_checkpoint(TRUNCATE) |
| `sqlitemigrate-zerokspot` | sqlitemigrate v1 (user_version-based Go migrations); goose embedded migrations | zerokspot; pressly/goose | 2021 | blog | <https://zerokspot.com/weblog/2021/01/30/sqlitemigrate-v1/> | 2 | 2 | user_version can hold the schema version; goose tracks versions in a table |

### 출처 요약

- **`sqlite-wal-doc`** — Official WAL page. Readers and the single writer do not block each other. A checkpoint cannot pass the oldest active reader's read mark, so long or overlapping readers starve it and the -wal file grows. Auto-checkpoint runs at 1000 pages and when the last connection closes. WAL requires all connections on one host, so network filesystems are ruled out. _(접근: Open access; sqlite.org could not be fetched, so read from snippets and mirrors.)_
- **`sqlite-pragma-synchronous-psow`** — In WAL mode FULL syncs the WAL after every commit, so commits survive power loss. NORMAL syncs the WAL only before checkpoints and stays consistent, but recent commits can roll back. EXTRA differs from FULL only in DELETE journal mode, where it adds a directory sync after journal unlink. An application-only crash is safe at any level. SQLite assumes powersafe overwrite (PSOW) by default. _(접근: docsrc check-ins seen through snippets; verify against pragma.html.)_
- **`sqlite-forum-sync-durability`** — Forum answer: a FULL or stronger wal_checkpoint makes earlier NORMAL commits durable; PASSIVE does not guarantee it. This is the alternative ADR-0005 rejected. _(접근: Snippet only.)_
- **`sqlite-howtocorrupt`** — SQLite's official list of corruption causes: external overwrites; a hot journal or WAL separated, deleted or mismatched; copying a database without its WAL or mid-transaction; reused file descriptors and fork; broken locking, including POSIX locks dropped on any fd close and network filesystems; drives that do not honour sync; and sync disabled by PRAGMA. It is the checklist for backup, hardening and single ownership of the store. _(접근: Read from the mirror's table of contents and snippets; the section text was not read.)_
- **`sqlite-testing-doc`** — SQLite's own I/O-error and crash testing. A test VFS fails the Nth I/O operation, either once or persistently. A crash VFS snapshots the filesystem after N operations and applies power-loss-style damage before recovery. PRAGMA integrity_check then validates the result. This is the model for deterministic crash-point enumeration. _(접근: Snippet only.)_
- **`sqlite-backup-doc`** — The online backup API snapshots a live database while other connections keep writing. VACUUM INTO (3.27.0 and later) writes a consistent, compacted copy. Both are the safe alternatives to a raw file copy. _(접근: Snippets only.)_
- **`sqlite-lang-vacuum`** — VACUUM INTO produces a transactionally consistent snapshot. SQLite does not fsync the output, so power loss mid-operation can leave the backup corrupt. The backup API uses less CPU and can run incrementally. Backup tooling must fsync the output file and its directory itself. _(접근: Snippet only.)_
- **`modernc-sqlite-pkgdoc`** — Driver documentation, about 100k characters read. _pragma DSN values are executed as SQL unless StrictPragmas is set. Connection state persists across pool borrowers, and the pool is unbounded by default. OFD locks are opt-in and -shm still uses POSIX locks. CPU-bound work is about 1.3x to 2x slower than C SQLite. libc must be pinned to match. The latest release is v1.61.1. The page says nothing about fsync. _(접근: Fetched; the last ~44k characters were not read.)_
- **`modernc-sqlite-issue10-fdatasync`** — Fetched. The Linux build always calls full fsync and never fdatasync: about 1010 fsync and 0 fdatasync for 1000 commits at FULL. The maintainer kept that default; an opt-in, process-wide Fdatasync(bool) is pending. A local check of v1.53.0 agreed: the VFS calls libc.Xfsync, which issues SYS_fsync in libc v1.73.4. _(접근: Fetched, and checked against the local module cache in the prior round.)_
- **`modernc-sqlite-changelog`** — v1.53.0 bundles SQLite 3.53.2. v1.46.2 was the first release on 3.51.3, which carries the WAL-reset fix. v1.52.0 added the Backup.Remaining and Backup.PageCount wrappers. No CVE specific to modernc was found. _(접근: Read from the local GOMODCACHE in the prior round.)_
- **`sqlite-wal-reset-bug-techzine`** — News coverage of the WAL-reset corruption bug. It affects SQLite 3.7.0 through 3.51.2 and is triggered by two or more connections writing or checkpointing at the same instant. It is fixed in 3.51.3, with backports in 3.44.6 and 3.50.7. SQLite 3.52.0 was withdrawn. _(접근: Secondary source; snippet only.)_
- **`modernc-dsn-pragma-issue`** — Downstream report that modernc honours only the _pragma, _time_format and _txlock DSN parameters. mattn-style parameters are silently ignored, which left busy_timeout at 0 and the database outside WAL. _(접근: Snippet only.)_
- **`sqlite-setlk-timeout`** — Each connection has a busy timeout and a separate WAL setlk timeout; busy_timeout sets both. Both are per connection and default to 0. _(접근: Snippet only.)_
- **`turso-go-sqlite-wal`** — Go-specific trap: a *sql.Rows that is never closed keeps a read transaction open, which blocks checkpoints. The post's example was a 4.1 kB database with a 42 MB WAL. VACUUM is blocked the same way. _(접근: Snippet only.)_
- **`rebello-fsync-atc20`** — Uses CuttleFS fault injection on ext4, XFS and Btrfs. After a failed fsync the dirty pages can be marked clean, so a retried fsync can falsely succeed. No strategy in PostgreSQL, LMDB, LevelDB, SQLite or Redis was sufficient. Recovery must rely only on on-disk state, not the page cache. _(접근: Open access; abstract only.)_
- **`pg-wiki-fsync-errors`** — The project's account of fsyncgate. PostgreSQL now PANICs on fsync failure (data_sync_retry=off), backpatched to 9.4. Linux 4.13 improved error reporting but only for errors after open(). InnoDB and WiredTiger made similar changes. _(접근: Snippet only.)_
- **`postgres-fd-c-fsync-retry`** — Fetched source showing fsyncgate's outcome in code. data_sync_retry is documented as 'Whether it is safe to continue running after fsync() fails'. The policy is to crash and replay from the WAL, not to retry fsync in process. _(접근: Fetched in the prior round.)_
- **`lwn-pg-fsync-surprise`** — LWN's narrative of fsyncgate. A later successful fsync does not cover an earlier failed writeback, and another caller can consume the error. OpenBSD and NetBSD have similar gaps. The primary thread is archived at danluu.com/fsyncgate and reprinted at lwn.net/Articles/752093. _(접근: Snippet only.)_
- **`pg-durable-rename-commit`** — rename(2) alone is not durable across a crash. durable_rename does: fsync the source, rename, fsync the new name, fsync the parent directory. This is the recipe behind ADR-0006 point 4. _(접근: Mirror snippets; the commit date differs between mirrors.)_
- **`lwn-fsync-posix-ext4`** — POSIX gives few fsync guarantees, and ext4's rename heuristics (auto_da_alloc) are not portable and can fail in edge cases. Writeback errors may be reported only once. _(접근: Snippets only.)_
- **`pillai-alice-osdi14`** — BOB probes filesystem persistence properties, and ALICE replays prefixes and reorderings of recorded syscalls to find crash vulnerabilities. ALICE found 60 across 11 applications. Recurring errors are assuming syscall ordering, assuming atomic append or rename, and skipping the directory fsync. _(접근: Abstract and summaries only.)_
- **`mohan-crashmonkey-osdi18`** — Bounded black-box crash testing (B3) with ACE and CrashMonkey enumerates every workload within a bound and crashes after each fsync-class call. Most bugs reproduce in three or fewer operations. It found 10 new bugs, including broken rename atomicity, and reproduced 24 of 26 known ones. _(접근: Open access.)_
- **`ramos-lazyfs-vldb24`** — LazyFS is a FUSE filesystem with its own page cache. On command it drops unsynced writes, or tears them (torn-op splits one write; torn-seq covers writes with no fsync between them). Faults are set through a TOML file or a FIFO. That makes torn-tail recovery reproducible without a power cut. It found 8 new bugs, and Jepsen uses it. _(접근: Snippets only; the full author list is unverified.)_
- **`helland-life-beyond-dt`** — Atomicity holds only within one entity. Cross-entity messages may be delivered more than once, so receivers must be idempotent or remember what they processed. Uncertainty is managed in the application. _(접근: Summaries only.)_
- **`saltzer-end-to-end-1984`** — Duplicate suppression and crash recovery can be implemented completely only at the endpoints. A local WAL marker cannot by itself give exactly-once order submission. That needs an endpoint check: server-side dedup or a post-hoc status query. _(접근: Open access; summary only.)_
- **`microservices-io-outbox`** — The business write and the outgoing message share one local transaction, and a relay publishes by tailing the log or polling. A crash between publishing and marking the message sent causes a duplicate, so delivery is at-least-once. Debezium's release notes and secondary write-ups (Conduktor, OneUptime, WikiDocs) agree. _(접근: Snippets only.)_
- **`debezium-outbox-release-1-8`** — Official Debezium post: once a row is in the outbox, Debezium propagates it with the usual at-least-once semantics. _(접근: Snippet only.)_
- **`toss-clientorderid-algolab`** — Third-party Korean guide. It says a resend with the same clientOrderId returns the earlier result within about 10 minutes, with a 36-character limit and the charset [a-zA-Z0-9-_]. This conflicts with ADR-0002's spec reading that duplicate behaviour is undefined. Not authoritative. _(접근: Korean-only, third-party; verify against the OpenAPI spec.)_
- **`postgres-pg-control-h`** — Fetched. pg_control stores a 7-value DBState enum that includes an intermediate DB_SHUTDOWNING state. Changing the format requires a PG_CONTROL_VERSION bump. It is a multi-state, versioned lifecycle marker rather than a boolean. _(접근: Fetched in the prior round.)_
- **`pg-control-state-thread`** — Any state other than a clean shutdown triggers crash recovery at boot, and the clean value is written only after a durable checkpoint. A crash during the end-of-recovery checkpoint can leave a misleading state. _(접근: Snippet only.)_
- **`litestream-how-it-works`** — Litestream is a separate process. It holds a long read transaction to block other checkpointers, copies WAL pages into LTX files (v0.5) and checkpoints itself. Replication is asynchronous, so RPO > 0. Per the fetched README it creates a _litestream_lock table in the source DB, has a 30 s shutdown timeout that can leave the last sync incomplete, and is beta. It does not capture sibling files such as audit segments. _(접근: README fetched; other pages snippet only.)_
- **`syncthing-checkpoint-starvation`** — A production report of the WAL growing without bound while overlapping readers starved the checkpoint. The remedies were short read transactions and explicit TRUNCATE checkpoints. _(접근: Snippet only.)_
- **`sqlite-forum-sqlite-full`** — After SQLITE_FULL, IOERR, BUSY or NOMEM, SQLite may roll back automatically, and the scope changed between 3.28 and 3.38, so issue an explicit ROLLBACK. SQLITE_FULL can also come from max_page_count, and retrying unchanged fails. Old WAL builds raised SIGBUS on disk-full during shm mapping; the fix was fallocate so the error surfaces as SQLITE_FULL. _(접근: Snippets only.)_
- **`gofrs-flock`** — A Go flock wrapper with a non-blocking TryLock. The kernel releases the lock when the process exits, so no stale lock is left. The lock is advisory and unreliable on NFS. _(접근: Snippet only.)_
- **`kleppmann-distributed-locking`** — A lease holder that pauses can write after its lease expires. Correctness locks need monotonic fencing tokens checked by the resource. A TTL lease row or lock file alone does not stop a zombie writer. _(접근: The original was not fetched; the URL is cited by antirez and secondary summaries.)_
- **`aws-ebs-pg-whitepaper`** — io2 is designed for 99.999% durability and gp2, gp3 and io1 for 99.8% to 99.9%, with replication inside one Availability Zone. The figures describe volume loss, not fsync semantics. _(접근: Snippet only.)_
- **`gcp-pd-durability`** — Persistent Disk keeps three or more copies per zone, checksums every operation, and regional PD replicates synchronously across two zones. The page has no statement on flush semantics. _(접근: Snippet only.)_
- **`sqlitemigrate-zerokspot`** — sqlitemigrate stores the schema version in PRAGMA user_version, as this repo does. goose embeds SQL migrations with go:embed and tracks versions in a goose_db_version table. Both are weak sources and neither covers drift testing. _(접근: Snippets only.)_
