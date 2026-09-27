# Project Log: Root-Context Resolver Was Executing the Wrong Path for Multi-Call Binaries

**Date:** 2026-09-27
**Component:** `pkg/sciontool/rootexec`, `pkg/sciontool/dirfd`, `cmd/sciontool/commands/substrate_rootfs.go`, `cmd/sciontool/commands/init.go`, `cmd/sciontool/commands/doctor.go`, `pkg/sciontool/metadata`, `pkg/sciontool/substrate`

## The regression

The root-context command resolver introduced to stop root from searching
its own inherited PATH (see the prior entry in this log) fully resolved a
candidate's symlink chain with `filepath.EvalSymlinks` and handed back the
destination, not the candidate itself. On Debian, `iptables` is a symlink
chain through `/etc/alternatives` to `xtables-nft-multi`, a single binary
that implements several tools and decides which one to behave as by
inspecting `basename(argv[0])`. Executing the destination directly means
argv[0] is the destination's own name, not `iptables`, so the binary prints
"No valid subcommand given" and exits instead of doing anything — breaking
both the metadata server's traffic redirect and its defense-in-depth
firewall rule on every runtime, silently re-exposing the real cloud
metadata endpoint the whole mechanism exists to protect. The same
resolve-then-exec-the-destination shape was one env-lookup change away from
mattering for any other multi-call binary reached through a symlink (some
`whoami` implementations included), even though the specific tools this
codebase resolves today happen not to hit it elsewhere.

The resolver's fixed search list was also narrower than the actual root
filesystem layout of a real agent image: it excluded `/usr/local/sbin` and
`/usr/local/bin`, but that image installs `git` (among other tools) only
under `/usr/local/bin`, silently breaking every shared-workspace git
credential setup on that image.

## The fix

The resolver now verifies every hop of a candidate's own symlink chain
itself — each hop's directory chain and the final destination, all owned
by root (or, on a runtime with no separate root/workload identity, by the
calling process) and free of group/other write — but returns the verified
CANDIDATE path, never a resolved destination. Every caller already passed
that value as the command path or embedded it directly in generated script
text without a separate override, so this one change fixed every affected
site uniformly; each site was checked individually to confirm none of them
separately re-derived a bare name after resolution.

The fixed search list now matches the standard root PATH order
(`/usr/local/sbin`, `/usr/local/bin`, `/usr/sbin`, `/usr/bin`, `/sbin`,
`/bin`) instead of a hand-picked subset. Widening it is safe because the
verification is unconditional and independent of directory name — a
workload-owned or group-writable copy of any of these directories is still
refused, not trusted because of where it sits. The rootfs fixup's sudo
lookup and the bootstrap precondition that also enumerates candidate sudo
locations now derive their directory list from the same source as the
resolver, rather than maintaining a second copy that could drift.

A related gap in the rootfs fixup: stripping the setuid bit from a `sudo`
binary opened only the exact path with `O_NOFOLLOW`, so a symlinked sudo
implementation (as `update-alternatives` produces) was left with its
setuid bit intact — and the bootstrap precondition that checks for exactly
this deliberately follows symlinks, so the combination would make every
future bootstrap on such an image refuse to start. The fixup now verifies
the whole chain the same way the resolver does, then strips the special
bits from the real destination through a file descriptor opened
`O_NOFOLLOW` on that destination.

A parity test meant to keep this sudo-hardening machinery reachable only
from its one legitimate call chain allowlisted an entire source file,
because that file also happens to define one of the legitimate call sites
— which meant a future call added anywhere else in that same file,
including the general-purpose entry point every other runtime goes
through, would have passed the check silently. It now walks the syntax
tree and checks each reference against the specific function it appears
in, not the file it lives in.

## Verification

A hermetic test builds a two-hop symlink chain mirroring the real
Debian layout and confirms the resolved path's own basename — not its
destination's — is what a script sees when executed. A companion test
runs the same claim against the real system `iptables` binary directly
wherever one is resolvable, so a regression in real-world dispatch, not
just the synthetic shape, is caught. The default search-list value itself
is now pinned by a dedicated test, independent of the tests that build
their own synthetic override. The sudo symlink fix has both a positive
path (a fully-trusted symlink chain is neutralized, and the bootstrap
precondition passes immediately afterward) and a negative one (a symlink
reached through a workload-writable directory is still refused). The
parity guard rewrite was confirmed by deliberately introducing a call from
the general entry point and from an unrelated function, both caught, then
reverted.

Every existing regression test that could only demonstrate a resolution
failure as a negative ("the planted binary never ran") now has a positive
counterpart proving the real, resolved binary still works correctly, since
a negative-only assertion is exactly the shape that let this regression
through undetected in the first place.

Build, vet, and the full test suite for every touched package are green
under a scrubbed environment. Each new regression test was confirmed to
fail when the fix it covers is reverted, and a full rerun of the existing
suite confirmed no unrelated regression.
