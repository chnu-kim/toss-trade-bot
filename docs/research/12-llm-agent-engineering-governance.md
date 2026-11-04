# Governance of LLM-agent software engineering: verification limits, N-of-2 review, LLM-as-judge, prompt injection in CI, self-protection, ADR/TDD evidence

> **왜 중요한가** — ADR-0007/0008/0009/0010/0011/0015/0016/0017 define the loop-engineering gate (mechanical verdicts, codex cross-engine, N-of-2 for risk:critical, sacred invariants via CODEOWNERS/branch protection, pure-transformer protection flip). Retro 2026-07-19 found self-catch ≈ 0 and twin-artifact drift; open issues #83-#87 complete Phase B activation. The literature should validate or challenge: judge reliability, injection hardening (ADR-0011 4(e)), same-model independence assumption (ADR-0008 point 2), and whether TDD/ADR practices measurably help agents.

수집 방식: 학술·공식/표준·업계/포스트모템·한국어 4경로 병렬 검색 → 종합·공백 판정(최대 2라운드 재수집). 출처 0건. 상태 집계: ❌ 미답 13

## 핵심 결론

- (완전 답변된 질문 없음 — 아래 부분 답변 참조)

## 질문별 답변

### T11.Q1 · What does the empirical literature say about LLM self-verification and self-correction limits (e.g. 'Large Language Models Cannot Self-Correct Reasoning Yet', self-consistency, verifier-generator gap) — does it support ADR-0008's self-cert ban and cross-engine default?

❌ 미답 · 우선순위 must · 확신 low

No verified source. All 8 input pools were empty, so there was nothing to curate. The user asked not to overuse the model, and earlier rounds showed the search budget was used up and arxiv.org was outside the allowed network, so this step made no new web calls. The only evidence is internal: ADR-0008 Context cites PR#22, where a same-family agent falsely self-reported done/CONFIRMED. That is a repo anecdote, not literature. The self-cert ban and cross-engine default are neither validated nor challenged externally.

### T11.Q2 · LLM-as-a-judge reliability for code review: position/verbosity/self-preference bias (arXiv 2410.21819), 'Reliability without Validity' (2606.19544), agreement with humans on defect detection, and evidence on whether two different model families give independent errors (error correlation) — core to the N-of-2 design.

❌ 미답 · 우선순위 must · 확신 low

No verified source. arXiv 2410.21819, 2606.19544, 2306.05685 and work on cross-family error correlation were not retrievable in any pool. The N-of-2 independence premise (ADR-0008 points 2-3) has no external grounding in this corpus.

### T11.Q3 · Agentic coding evaluations (SWE-bench Verified/Pro, SWE-bench Live, Terminal-Bench, ARC of long-horizon tasks) and failure taxonomies: how often agents report success falsely (retro: workers' self-reports needed ground-truthing), test-gaming, and 'reward hacking' in coding agents.

❌ 미답 · 우선순위 must · 확신 low

No verified source on SWE-bench variants, Terminal-Bench, false success reports or reward hacking. Repo-internal only: retro 2026-07-19 and ADR-0008 Context record that worker self-reports needed ground-truthing.

### T11.Q4 · Prompt injection in CI/PR review contexts: attacks via PR titles/bodies/diffs/comments against LLM reviewers, GitHub Copilot/Claude Code Action advisories, OWASP LLM Top 10 (LLM01), instruction/data separation and structured-output constraints — validating ADR-0011 4(e) hardening and precondition ⑤.

❌ 미답 · 우선순위 must · 확신 low

No verified source. OWASP LLM01 and the GitHub/Claude Code Action advisories were unreachable in every pool. ADR-0011 4(e) and precondition ⑤ have no external grounding in this corpus.

### T11.Q5 · GitHub platform self-protection mechanics: CODEOWNERS last-match-wins, branch protection vs rulesets (bypass actors, enforce_admins), required status checks name-matching, GitHub App vs PAT authorship rules, self-approval blocking, and known bypass vectors — grounding ADR-0009/0011/0016/0017 and #84/#85.

❌ 미답 · 우선순위 must · 확신 low

No verified source, because docs.github.com was blocked. These platform claims remain unverified against primary docs: CODEOWNERS last-match-wins (CLAUDE.md, codeowners.go); enforce_admins=true does not stop an admin credential from editing the protection itself (ADR-0011 Context); merge attribution under Contents:write; and the untested combination required_approving_review_count=0 + require_code_owner_reviews=true (ADR-0011 precondition 4).

### T11.Q6 · ADR literature and practice: Nygard's original ADR post, MADR template, ADR tooling/linting, 'architecture decision' research (Zimmermann, Kruchten), and evidence on ADRs improving onboarding/agent context (ADR-0010 frontmatter SSOT, docs/adr/why-adr.md).

❌ 미답 · 우선순위 should · 확신 low

No verified source. Nygard 2011, MADR and the Zimmermann/Kruchten work were unreachable.

### T11.Q7 · TDD effectiveness evidence (Nagappan et al. 2008 Microsoft/IBM, Fucci et al. replications, Karac & Turhan meta-analyses) and recent work on test-first prompting for LLM code generation — does TDD measurably reduce defects for agents?

❌ 미답 · 우선순위 should · 확신 low

No verified source. Nagappan 2008, Fucci et al., Karac & Turhan and work on test-first prompting for LLMs were unreachable.

### T11.Q8 · Human-in-the-loop autonomy boundaries: frameworks for deciding which actions require human authorization (irreversibility, blast radius), Anthropic/OpenAI/Google agent safety guidance, 'autonomy levels' taxonomies — relate to ADR-0007's 'live-execution authorize' boundary.

❌ 미답 · 우선순위 should · 확신 low

No verified source on autonomy taxonomies or vendor guidance, so the ADR-0007 live-execution authorize boundary is not compared against any external framework.

### T11.Q9 · Multi-agent review and adversarial debate: evidence that adversarial/critic agents find more defects than single review, diminishing returns across rounds (retro: 9-round convergence), and stopping criteria for review loops.

❌ 미답 · 우선순위 should · 확신 low

No verified source. The only data point is internal: ADR-0011 frontmatter records 9 review rounds and 28 blocking defects, with no core-decision defects from round 5 on. That fits diminishing returns, but it is a single case.

### T11.Q10 · Mutation testing and 'non-vacuous test' practices (retro #14 M4 SURVIVED): Go mutation tools (gremlins, go-mutesting), mutation score as a gate, and literature on mutation testing effectiveness vs coverage.

❌ 미답 · 우선순위 should · 확신 low

No verified source on mutation-testing effectiveness or on the Go tools gremlins and go-mutesting.

### T11.Q11 · Documentation-code consistency ('twin artifact' drift): tooling and research on detecting stale docs/runbooks vs code, enumerable completeness checks (the retro's 'stop enumerating, use completeness' lesson), and executable documentation.

❌ 미답 · 우선순위 should · 확신 low

No verified source. Repo practice only: instructionsurface_test checks the whole .claude/ and docs/runbooks/ trees, and adrprotects_test checks ADR protects: against registration (enumerable completeness checks).

### T11.Q12 · Cost/latency and availability engineering for LLM gates: handling codex/Claude outages (ADR-0008 point 4 fail-closed queueing), rate limits, model deprecation, prompt/version pinning for reproducible verdicts, and verdict schema evolution.

❌ 미답 · 우선순위 nice · 확신 low

No verified source. Repo design only: under ADR-0008 point 4, risk:critical waits fail-closed (queued) when codex is unavailable, and non-critical work falls back to same-model advisory review.

### T11.Q13 · Provenance and attribution for agent commits/PRs (Co-Authored-By trailers, bot identities, signed commits with SSH/Sigstore gitsign) and auditability of who/what authored each change (ADR-0011 identity split).

❌ 미답 · 우선순위 nice · 확신 low

No verified source. Repo design only: ADR-0011 point 2 splits identity (commit/push = chnu-kim, PR author = mechanu[bot]).

## 프로젝트 함의

- No external source has been verified in any round. The user asked not to overuse the model, so this synthesis made no further retry attempts. Unblocking needs network settings first (allow arxiv.org, docs.github.com and genai.owasp.org, and raise the search budget). Re-running the workflow as-is will not help.
- ADR-0008 internal tension, carried over: point 2 rejects same-model sessions as independent verifiers, but point 3 fills the second N-of-2 vote for risk:critical with a Claude session. The independence of N-of-2 therefore rests on codex alone.
- The self-cert ban rests on a single incident (PR#22). ADR-0011 4(e) and precondition ⑤, which block Phase B entry (#83-#87), have no external grounding. Verify them before declaring ⑤ met.
- The GitHub platform facts in ADR-0011, ADR-0016 and ADR-0017 have not been re-verified against primary docs.

## 출처 간 상충

- No sources, so no contradictions between sources. Intra-repo tension: ADR-0008 point 2 vs point 3 (same-family second approver). Resolution: label that vote as redundancy, matching the 'advisory (same-model)' label, or require a second distinct engine.

## 남은 공백

- **T11.Q1** — All of the literature (search budget used up; arxiv.org blocked) (추가 검색: arXiv 2310.01798 Large Language Models Cannot Self-Correct Reasoning Yet; When can LLMs actually correct their own mistakes TACL 2024; arXiv 2203.11171 self-consistency)
- **T11.Q2** — Evidence on judge bias and on error correlation across model families (추가 검색: arXiv 2410.21819 self-preference bias; arXiv 2606.19544 Reliability without Validity; correlated errors across language models)
- **T11.Q3** — Benchmark papers and reward-hacking data (추가 검색: SWE-bench Verified; SWE-bench Pro arXiv; Terminal-Bench paper)
- **T11.Q4** — OWASP LLM01 and the CI injection advisories (추가 검색: OWASP LLM01:2025; arXiv 2302.12173; claude-code-action GHSA prompt injection)
- **T11.Q5** — Primary GitHub docs (docs.github.com must be allowed) (추가 검색: docs.github.com about code owners; rulesets bypass list; REST branch protection enforce_admins)
- **T11.Q6** — Nygard's post, MADR and ADR research (추가 검색: Nygard Documenting Architecture Decisions 2011; MADR adr.github.io)
- **T11.Q7** — TDD meta-analyses and TDD with LLMs (추가 검색: Nagappan 2008 TDD industrial; Karac Turhan TDD; arXiv 2402.13521 test-driven LLM)
- **T11.Q8** — Autonomy taxonomies and vendor guidance (추가 검색: levels of autonomy AI agents arXiv 2506.12469; OpenAI practices for governing agentic AI systems)
- **T11.Q9** — Evidence on debate and critic effectiveness (추가 검색: arXiv 2305.14325 multi-agent debate; arXiv 2407.00215 LLM critics catch bugs)
- **T11.Q10** — Mutation-testing literature and Go tools (추가 검색: Just 2014 mutants valid substitute real faults; gremlins Go mutation testing)
- **T11.Q11** — Research on doc-code drift (추가 검색: outdated documentation detection code references)
- **T11.Q12** — Guidance on LLM gate availability (추가 검색: LLM nondeterminism temperature 0 reproducibility; Anthropic model deprecations)
- **T11.Q13** — Provenance tooling docs (추가 검색: Sigstore gitsign; SLSA source track)

## 출처

| id | 제목 | 저자/기관 | 연도 | 유형 | 링크 | 관련 | 신뢰 | 핵심 주장 |
|---|---|---|---|---|---|---|---|---|

### 출처 요약

