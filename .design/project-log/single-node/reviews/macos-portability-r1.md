# Task #89 — macOS / BSD-userland portability audit of the single-node tier

Scope: `scripts/single-node/deploy.sh` (973 lines) and `scripts/single-node/teardown.sh`
(105 lines) at `main = 1befe923` (#1335, merged 01:38:29Z). Both are run by the operator on
their own machine, so both are in scope. Audit only — no code changed, no PR, no deploy.

**This is a READING audit** (I have no macOS). Every row is labelled CONFIRMED or CANDIDATE
with the route used. "CONFIRMED" here means confirmed against the macOS-14 man page or a
local proxy measurement (`sed --posix`), **not** run on macOS. The one-paste diagnostic at
the end converts the whole thing to a real measurement from ptone's machine.

## Headline

**The credential-handling and resource-mutating paths are POSIX-clean.** Every `sed`,
`grep`, `awk`, `tr`, `head`, `mktemp` call in the deploy/preflight/gate path uses only the
GNU∩BSD common subset. **There is no silent-misbehaviour finding anywhere** — the thing you
ranked as most expensive does not appear. The macOS exposure is two *loud* breaks:

1. bash 3.2 `${var,,}` — already hit by ptone, **owned by #88** (but there are **two** sites,
   not one — see row 1).
2. the `--help` `sed` one-liner — breaks on macOS `sed`, but only affects `deploy.sh --help`;
   no deploy, credential, or resource path touches it.

**Your top suspect — bare `mktemp` — is falsified for macOS** (row 4). macOS `mktemp`
defaults to `-t tmp` when given no template; it does not error. Confirmed against the macOS-14
man page. Kept in the diagnostic so ptone's box confirms it, but I do not expect it to break.

## Ranked findings (by consequence, not line order)

| # | Line(s) | Construct | GNU | BSD/macOS | Effect | Label | Route |
|---|---|---|---|---|---|---|---|
| 1 | 286, 294 | `${scheme,,}`, `${host,,}` | lowercases | bash 3.2: **parse error** `bad substitution` | **BREAK**, aborts every run before any side effect | CONFIRMED (ptone hit 286) | **#88** — both sites already tabled there (see correction below) |
| 2 | 637 | `sed -n '…{ /^#/s/^# \?//p }'` | works | `}` must be preceded by a newline (macOS man page) **and** `\?` is not a BRE quantifier | **BREAK** of `--help` only (parse error or empty output). No deploy path. | CONFIRMED (macOS man page + `sed --posix` proxy) | fixable in a portability pass; not #88 |
| 3 | 70, 86, 297, 309, 444 | `[[ … =~ … ]]` + `BASH_REMATCH` | works | bash 3.2 `=~` works; **RHS is unquoted everywhere**, so the 3.2 quoting trap does not apply | FINE on 3.2 as written | CONFIRMED-fine by reading (all RHS unquoted) | **#88** owns the regex question (line ~298); listed so it lands on their list |
| 4 | 353, 425, 515, 564, 841 | `mktemp` (no template) | works | **works** — defaults to `-t tmp` | FINE (top suspect falsified) | CONFIRMED via macOS-14 man page | confirm on ptone's box |
| 5 | 393, 395, 542, 576 | `grep '"email"'` / `grep -i '^location:'`; `sed 's/…[[:space:]]…//'` | works | POSIX BRE + `[[:space:]]` class — identical | FINE | CONFIRMED by reading (POSIX subset) | — |
| 6 | 356, 700, 727, 542, 576 | `tr -d '[:space:]'`, `tr -d '\r'` | works | POSIX class + `\r` escape — identical | FINE | CONFIRMED by reading | — |
| 7 | 452, 542, 576, 857 | `head -c 500`, `head -1` | works | BSD `head` supports `-c` and obsolescent `-1` | FINE | CONFIRMED by reading | — |
| 8 | 927–940 | `awk` policy parse (`gsub`, user fn, `END`) | works | one-true-awk supports all; no `gensub`/`length(arr)`/`--version`-only feature; display-only (step 6) | FINE | CONFIRMED by reading | — |
| 9 | 200, 203, 611, 648–653, 783–802, 917 | `&>`, heredoc, arrays, `+=`, `${arr[@]:0:6}`, `<<<`, `set -u`+arrays | works | bash 3.2 supports all; empty array `missing` is only ever **counted** (`${#…}`), never expanded while possibly empty, so the 3.2 `set -u` empty-array trap is avoided | FINE on 3.2 | CONFIRMED by reading | — |
| 10 | teardown.sh (all) | `[[ ]]`, `case`, `echo`, `${2:-}`, `"$2" == -*` | works | no GNU userland at all; no `${,,}`; no bash-4 constructs | FINE | CONFIRMED by reading | — |

Absent by grep, so not a concern: `date`, `readlink`, `realpath`, `base64`, `xargs`, `stat`,
`sort`, `timeout`, `mapfile`/`readarray`, `declare -A`, `${var^^}`, `coproc`, `echo -e/-n`.
`echo` is the bash builtin (not `/bin/echo`), consistent across platforms; no value printed
starts with `-e`/`-n` or contains backslashes.

## Route notes (how each was checked)

- Rows 1, 3, 9: read against known bash 3.2 semantics; row 1 also confirmed live by ptone.
- Row 2: `\?` confirmed locally with `sed --posix` (empty output — a faithful proxy for BSD
  BRE where `\?` is literal); `}`-needs-newline confirmed against the **macOS-14 sed man
  page** ("The terminating '}' must be preceded by a newline").
- Row 4: confirmed against the **macOS-14 mktemp man page** ("If no arguments are passed …
  behaves as if `-t tmp` was supplied"; SYNOPSIS allows the no-template form).
- Rows 5–8, 10: read against the POSIX-common subset; none use a GNU-only flag or extension.

## Fix-candidate risk — "a portability fix is a semantics change until proven"

Per the #89 addition: for every row that would need a change, the third dimension is not "what
does BSD do" but **"what is the obvious fix, and could it change behaviour on an input the 38-row
table does not cover."** That is where the risk lives. All CANDIDATE — none proposed as ready.

- **Rows 1/294 (`${host,,}`) — DO NOT take the obvious fix; dev2 already measured it as a
  security regression.** Replacing `${host,,}` with a command substitution
  (`$(printf %s "$host" | tr 'A-Z' 'a-z')` or similar) **strips trailing newlines**, so three
  values flip REJECT→ALLOW and round-4's class reopens. The 38-row table goes **fully green**
  because no row carries trailing whitespace — the table cannot see this regression. This is the
  exemplar for the whole rule. The correct 3.2 fix must preserve exact bytes (lowercase without
  a `$()` round-trip, or reject trailing whitespace explicitly first) and is **#88's call**, not
  mine. I flag it only so no row of my audit gets "fixed" the same way.
- **Row 1/286 (`${scheme,,}`)** — same class, same trailing-newline trap for the obvious
  command-substitution fix. #88.
- **Row 2/637 (`--help` sed)** — obvious fix is a POSIX rewrite: `\{0,1\}` for `\?`, and put the
  `}` on its own line (or split into `-e` clauses / drop the group). Semantics risk: the rewrite
  changes *which lines* the range prints; a wrong rewrite could silently truncate or duplicate
  help lines. Low stakes (help text) but still must be judged by output diff, not by inspection.
- **Rows 4–10** — FINE as written; no fix, so no fix-risk.

**Instrument for judging any fix candidate:** dev2's differential harness — real function vs
candidate, over all 38 table values **plus adversarial rows (trailing `\n`, `\r`, CR, embedded
whitespace, mixed case)**, comparing **exit code AND exact stderr bytes via `od -c`**. A
candidate is not portable-equivalent until it is byte-identical there. A green 38-row table is
explicitly *not* sufficient — the line-294 regression proves the table is blind to the exact
input class these fixes perturb.

## How much of this script has ever run on anything but Linux + GNU?

**Essentially none until ptone's run ~10 minutes ago — and that run aborted at line 286**
(`${scheme,,}`) inside `di_validate_override_url`, which `di_main` calls before any external
tool. So **beyond the bash-3.2 parse point, zero of the GNU-userland calls (`mktemp`, `sed`,
`grep`, `awk`, `tr`, `head`) have ever executed on macOS.** The reading audit above is
therefore the only coverage those lines have on BSD userland until the diagnostic runs. That
is exactly why the diagnostic exists and why the labels matter.

## One-paste diagnostic for ptone (nothing sensitive printed)

Paste this whole block into a terminal on the Mac. It prints tool identities and answers
every load-bearing question above. No tokens, no project IDs, no network calls.

```sh
{
  echo "=== deploy.sh macOS portability probe ==="
  echo "uname     : $(uname -srm)"
  echo "PATH bash : $(command -v bash) :: $(bash --version 2>/dev/null | head -1)"
  echo "/bin/bash : $(/bin/bash --version 2>/dev/null | head -1)"
  printf 'env-bash ${x,,}: '; bash -c 'x=ABC; printf "%s" "${x,,}"' 2>/dev/null && echo " -> supported (bash>=4)" || echo "UNSUPPORTED -> bash 3.2 (#88)"
  printf 'sed  : '; sed --version 2>/dev/null | head -1 || echo "BSD/macOS sed (no --version)"
  printf 'grep : '; grep --version 2>/dev/null | head -1 || echo "BSD/macOS grep (no --version)"
  printf 'awk  : '; awk --version 2>/dev/null | head -1 || echo "BSD/macOS awk (no --version)"
  printf 'bare mktemp: '; T=$(mktemp 2>/tmp/di_mkerr) && { echo "OK -> $T"; rm -f "$T"; } || echo "FAIL: $(cat /tmp/di_mkerr)"; rm -f /tmp/di_mkerr
  printf 'help-sed   : '; printf '# deploy.sh h\n# body\nend\n' | sed -n '/^# deploy\.sh/,/^[^#]/{ /^#/s/^# \?//p }' >/tmp/di_hp 2>/tmp/di_he && { if [ -s /tmp/di_hp ]; then echo "parsed; output=[$(tr "\n" "|" </tmp/di_hp)]"; else echo "parsed but EMPTY (\\? / } issue)"; fi; } || echo "PARSE ERROR: $(head -1 /tmp/di_he)"; rm -f /tmp/di_hp /tmp/di_he
  echo "-- line ~297 =~ + BASH_REMATCH, run on real values under env-selected bash --"
  bash -c '
    for h in "example.com:443" "example.com" "[::1]" "[::1]:8080"; do
      if [[ "$h" =~ ^(.*):[0-9]+$ ]]; then printf "  unquoted: %-16s -> [%s]\n" "$h" "${BASH_REMATCH[1]}"; else printf "  unquoted: %-16s -> (no port, host kept)\n" "$h"; fi
    done
    h="example.com:443"; rx="^(.*):[0-9]+$"
    if [[ "$h" =~ "$rx" ]]; then echo "  quoted-RHS: MATCHED (regex) -> bash treats quoted RHS as regex"; else echo "  quoted-RHS: NO match (literal) -> bash 3.2 quoted-RHS trap present; current code correctly uses UNQUOTED"; fi
  ' 2>/tmp/di_re || echo "  =~ block ERRORED: $(head -1 /tmp/di_re)"; rm -f /tmp/di_re
  echo "=== end probe ==="
}
```

**Expected on stock macOS (my predictions — the point is to check them):**
- `env-bash ${x,,}` → **UNSUPPORTED (bash 3.2)** — the #88 break.
- `sed`/`grep`/`awk` → **BSD/macOS (no --version)**.
- `bare mktemp` → **OK** (falsifies the top suspect).
- `help-sed` → **PARSE ERROR** (or EMPTY) — row 2.
- `=~` unquoted → **`example.com:443`→`[example.com]`, `example.com`→(no port), `[::1]`→(no
  port, kept), `[::1]:8080`→`[[::1]]`** — i.e. identical to bash 5, and the bare-IPv6 literal
  is preserved (the invariant the line 295-296 comment claims). If bash 3.2 diverges here, the
  `:port` strip is a live portability bug and a new finding.
- `=~` quoted-RHS → **NO match (literal)** on bash 3.2 — confirming the trap exists and that
  the current code sidesteps it by *not* quoting. This answers dev2's open question directly.

A row that comes back against prediction is a new finding; a row that matches converts a
CANDIDATE/CONFIRMED-by-manpage into a measurement.

## What in the brief is wrong

1. **Top suspect is wrong for macOS.** Bare `mktemp` does not require a template on macOS; it
   defaults to `-t tmp`. It would break on some older/other BSDs, but ptone is on macOS. This
   is the "measure-do-not-read" rule earning its keep in reverse — the recalled BSD behavior
   is not macOS's.
2. **"Seven of ten tools differ in ways that bite" overstates the bite.** They differ, yes,
   but `deploy.sh` uses only their common POSIX subset except in the `--help` path. The
   *difference* surface is wide; the *bite* surface is two loud breaks, one of them cosmetic.
   I'd rather say that plainly than let the 7/10 framing imply seven live hazards.
3. ~~**The bash-3.2 `${,,}` break has two sites, not one** (286 and 294). dev2 was pointed at
   286; a fix or a version-gate must cover both.~~ **RETRACTED (architect correction, 01:55):**
   the #88 brief already tables **both** 286 and 294 with separate commits, and the architect's
   correction message named both — there is no gap to close. My inference of a single site came
   from ptone's error text, which names only 286. Row 1's requirement (a fix must cover both)
   stands; the claim that #88 had missed one does not.

## Recommendation: send the diagnostic now; the audit still earned its cost

You offered "diagnostic first and alone" if the reading audit was not worth it. It was worth
it: it bounded the blast radius (two loud breaks, zero silent), falsified the top suspect, and
found the second `${,,}` site — none of which the diagnostic alone tells you. So send the
one-paste probe to ptone now (he is awake, and his machine beats my inference), **and** carry
this audit. If any probe row defies its prediction, I re-open that row.
