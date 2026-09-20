# AGENTS.md

Stable repository instructions for human and coding-agent contributors.

## Source of truth

- GitHub Issues and Projects are the live assignment registry.
- Pull requests expose code in progress and review state.
- This tracked file contains stable repository rules only.
- Machine-specific paths, temporary artifacts, local qualification reports,
  browser profiles, and session checkpoints belong in the ignored
  `AGENTS.local.md`. Local handover text never grants ownership or authority.

## Work-item and ownership contract

- Every implementation starts from a GitHub Issue, or an explicitly equivalent
  centrally managed work item.
- The work item names exactly one owner, the intended base commit, planned
  file/path scope, affected public contracts, dependencies, required tests,
  and whether live, paid, credentialed, public-site, or human-operated testing
  is expected.
- At session start, verify the operator identity and work-item assignment
  centrally. Stop if the item is inaccessible, stale, unassigned, or assigned
  to someone else.
- Before editing, fetch the current remote state and inspect open/draft PRs and
  active work items. Read full diffs only where paths, modules, public
  contracts, generated artifacts, migrations, or test infrastructure overlap.
- Claim the work item atomically with its unique assignee and active state
  before implementation. The absence of an open PR is not an ownership claim.

## Branch and PR contract

- Use one independent branch per work item, branched from current
  `origin/main`. Record the exact base SHA in the work item.
- Never share a writable feature branch across agents or devices, and never
  append commits to another owner's branch without explicit central
  reassignment.
- After the first meaningful commit, push the branch and open a Draft PR linked
  to the work item. State planned paths, contract changes, verification,
  live/paid effects, dependencies, and known overlap.
- If active work overlaps a planned file or public contract, split at a
  reviewed interface, serialize behind the existing work, or obtain explicit
  reassignment. Security, provider, DSL, replay, storage, and other high-churn
  trust boundaries have one active writer at a time.
- Keep PRs small and single-purpose. Do not combine unrelated refactors,
  formatting, generated churn, test-harness changes, and product behavior.
- Immediately before review or merge, update against current `origin/main` and
  rerun the relevant verification. Use the merge queue when available.
- When an old base was squash-merged, transplant only commits made after that
  base; do not replay content already present in `main`.
- Convert a Draft PR to ready only after implementation and required
  verification are complete.

## Contract and verification rules

- A clean textual merge is not proof of semantic compatibility.
- Contract owners review cross-module behavior. Add contract/regression tests
  for changes affecting providers, strict schemas, selector authorization,
  replay, navigation, storage, task results, MCP/Admin agreement, migrations,
  or live-test infrastructure.
- Use the test inventory in [`docs/testing.md`](docs/testing.md) to select the
  smallest sufficient checks, then broaden verification in proportion to risk.
- Every test-and-retry loop must optimize for fast iteration: first use the
  smallest deterministic, repeatable reproduction; reuse authenticated inputs
  and artifacts instead of repeating setup or external stages; and run only
  the focused checks needed for the current hypothesis. Run broad suites and
  live, paid, credentialed, public-site, browser/server, or human-operated
  qualification only after that inner loop is stable. This sequencing never
  permits weakening fail-closed contracts or omitting required regression and
  final broad verification.
- Live, paid, credentialed, public-site, persistent-profile, or human-operated
  tests require fresh explicit authorization recorded in the work item. An
  authorization is consumed by the run it covers and never implies a retry.
- Never bypass CAPTCHA, consent, authentication, domain, cost, schema,
  selector, replay, or evidence boundaries to force a passing test.
- Never commit credentials, browser profiles, encrypted-recording keys,
  plaintext provider artifacts, or machine-specific qualification reports.

## Local files and handoff

- Keep `AGENTS.local.md` and `.tools/` local, ignored, and untracked. Never
  force-add them.
- Do not stage broadly with `git add .` or `git add -A`; name intended files.
- After a merge, publish the PR number, new `main` SHA, absorbed branch/commit
  range, verification actually run, and rebase guidance for active work.
- Update `AGENTS.local.md` after material local qualification or handoff.
- Add durable, sanitized qualification results to
  [`docs/qualification-history.md`](docs/qualification-history.md). Link the
  central Issue/PR and record hashes, not credentials or local secret paths.
