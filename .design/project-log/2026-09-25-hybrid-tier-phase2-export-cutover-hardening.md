# Hybrid Deployment Tier — Phase 2: export-layout cutover and fstab hardening

Branch `scion/hybrid-tier-p2`.

## Overview

A further pass on the export-layout guidance's cutover procedure and fstab entry.

## Cutover procedure: never delete from a path a mount can shadow

The previous cutover procedure mounted the new filesystem directly over
`/srv/scion-shared` and, as its last step, said to "remove the old data from
`/`" with no further detail. Once that mount is in place, the old tree is
hidden underneath it, so the obvious command for that step (`rm -rf
/srv/scion-shared/*`) deletes the *new*, freshly copied export instead of the
old one. The rewritten procedure never mounts over the export path until the
old tree has already been moved out from under it by name: it copies into a
temporary mountpoint first (`rsync -aHAX`, preserving hard links and ACLs),
stops the hub, agents, and `nfs-server`, takes a final quick sync to catch
anything written since the bulk copy, unmounts the temporary mountpoint, and
only then moves the now-unmounted old tree aside to `/srv/scion-shared.old` --
a plain directory nothing is ever mounted over. The final cleanup step names
that path explicitly, so it can never remove anything other than the retired
copy.

## fstab entry: `nofail`, and a verified nofail/required-by interaction

Without `nofail`, a failed loop mount at boot is *required* by
`local-fs.target`; per systemd.mount(5), that failure cascades to the whole
target and its `OnFailure=emergency.target`, taking the entire VM down (hub,
dockerd, sshd) over a scratchpad mount failure alone, not just the intended
"nfs-server won't start" fail-closed behavior. Checked directly against
systemd.mount(5): `nofail`'s documented effect is scoped only to this mount's
relationship with `local-fs.target`/`remote-fs.target` -- it downgrades that
one edge from required to wanted and drops the boot ordering. It has no
documented effect on `x-systemd.required-by=nfs-server.service`, which
configures a separate, unit-specific dependency on `nfs-server.service`
alone. No conflict between the two is documented anywhere in
systemd.mount(5) or systemd-fstab-generator(8), so the fstab line now carries
both: `nofail` protects the boot from a scratchpad-mount failure, and
`x-systemd.required-by=nfs-server.service` (paired with the exports `mp`
option) still independently keeps NFS itself fail-closed.

## Other fixes to the same recipe

- The fsck pass changes from `2` to `0`: systemd-fstab-generator only ever
  schedules fsck for device paths, so a nonzero pass on a loop-mounted
  regular file is a no-op that just logs a boot-time warning.
- Adds one line of sizing guidance: size the image for the scratchpad's
  expected footprint, leaving headroom on `/`, and remember a cutover also
  needs free space for the image on top of the tree it's copying.
- The separate-disk variant of the recipe is corrected: a device path makes
  the image-creation guard (`test ! -e <path>`) always false, so it never
  actually runs, and `fallocate` doesn't apply to a device at all. The
  variant now says to skip `fallocate`, guard the format with `blkid -p
  <dev>` (format only if it reports no filesystem), and use `UUID=<uuid>` or
  `/dev/disk/by-id/...` in fstab rather than a raw device name that can
  change across reboots.
- The exports(5) `mp` option's enforcement is nfs-utils userspace
  (`exportfs`/`rpc.mountd`), not the kernel; the doc's wording is corrected
  from "makes knfsd itself refuse" to "makes the NFS server refuse".
- The cutover's rsync commands use `-aHAX` (preserving hard links) rather
  than `-aAX`.

## History

Three earlier p2 commit messages are reworded: one named the local (non-NFS)
backend, and two described their own edits ("fixes a caveat that inverted
its own meaning", "the recipe was missing", "so 'by design' no longer stands
uncontradicted") rather than stating what the doc and log now say. Two of
the three are message-only rewords, tree identity verified against the
originals. The third also carries the two project-log wording fixes to the
previous entry's "Export-layout guidance" section (its heading and two
bullets) -- described here rather than repeated, since that content lives in
the commit that already owns it.

## Verification

- `docs/deploy/hybrid-tier.md`'s recipe and cutover blocks are prose/command
  blocks, not executable from this repo; checked by eye against
  systemd.mount(5), systemd-fstab-generator(8), and exports(5) semantics,
  the same way the prior entry's recipe was checked.
- Confidentiality grep across the full history rewrite and this entry:
  clean.
