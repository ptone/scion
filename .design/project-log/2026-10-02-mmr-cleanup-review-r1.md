# Review r1: members-multirole hub cleanup (ptone/scion#2600, ptone/scion#2646)

- Reviewer: mmr-cleanup-rev-1. Reviewed `scion/mmr-hub-cleanup` at
  ce93cc2e1dfa9c250e8e3a1723ac3d9f9a72d5ed (fork staging PR ptone/scion#2688),
  4 commits on GoogleCloudPlatform/scion main e4eb5c0.
- Verdict: REQUEST CHANGES. 0 Critical/High/Medium, 2 Low, 1 Nit, 3 FYI.
  No behaviour change found.
- Evidence: a differential probe (3906 PUT/DELETE/assignable-roles cases
  over 6 actors, plus all mutation-audit rows) gave identical output on
  upstream main and the branch. 12 mutations showed every guard pinned
  except the legacy AddMember in-transaction refusal (Low C1-1: add a pin).
- C1-2 (Low): the `memberRoleDecision` header claims PUT/assignable-roles
  "cannot drift" in order. The PUT's order lives in its own loop sequence
  and is still only pinned by TestAssignableRoles_ConsistentWithPut, so the
  wording needs correcting.
- C1-3 (Nit): unreachable fallback after `customRoleAuthorityError` unwrap.
- Learning: for "no behaviour change" refactors of decision code, a
  differential probe (same test file run on base and head, outputs
  normalised and diffed) gives much stronger evidence than reading the
  code, and costs little (about 13 s per tree here).
