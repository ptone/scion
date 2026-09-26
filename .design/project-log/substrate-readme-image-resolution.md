# Substrate README: Image Resolution Order and Enforcement Wording

**Date:** 2026-09-26
**Branch:** scion/substrate-readme-image

## Problem

`deploy/substrate/README.md`'s "Pin the agent image by digest (REQUIRED)"
section documented the harness-config/template/`--image` interaction
piecemeal, without stating the resolution order explicitly, and without
stating plainly that the fail-closed digest-pinned check is the only
enforcement point (as opposed to the broker profile pin, which is an
operator default/fallback).

## Change

Added, near the top of the section (right after the fail-closed error
block):

- A "How to pin" line: pin by digest in the template's `scion-agent.yaml`
  `image:` (honoured at runtime) or with `--image`.
- An explicit resolution order: harness-config → broker profile (fallback)
  → template → `--image`; the last one set wins. The broker active-profile
  image applies only when neither the template nor `--image` sets one.
- An explicit enforcement statement: any image not pinned by digest is
  refused by the fail-closed check; any digest-pinned image is accepted
  regardless of source — substrate does not restrict which images users may
  run in Phase 1. The broker profile pin is not an enforcement point; it is
  the operator default/fallback and loses to the template and `--image`.

Also added a note near the "Diagnostic" paragraph: the hub's template
image/config fields are not populated by the template-upload path, though
the file is read at runtime — verify the running image from the resolved
actor image or broker log line, not from the hub template record.

The existing "Recommended / Per-agent alternative / Not recommended /
Diagnostic" subsections and the quoted fail-closed error text are
unchanged.

## Files Changed

| File | Change |
|------|--------|
| `deploy/substrate/README.md` | Added resolution-order/enforcement paragraph and hub column note to the digest-pinning section |
