#!/usr/bin/env python3
"""Build and VALIDATE a landing link against the compare-URL protocol.

Protocol confirmed by coordinator 2026-08-30. This protocol has been mis-followed and
corrected repeatedly by multiple competent agents -- bare URLs, missing quick_pull args,
wrong thread, prose alongside the link -- including by the coordinator itself. That makes
it a format problem, not a memory problem. Do not hand-assemble the message. Run this and
send exactly what it prints, nothing more.

THE PROTOCOL
  1. Markdown link, never a bare URL:  [<PR title>](<compare url>)
  2. URL must carry GitHub's pre-fill args:
       https://github.com/<upstream>/<repo>/compare/main...<fork>:<repo>:<branch>
         ?quick_pull=1&title=<urlencoded>&body=<urlencoded>
     quick_pull=1 -- NOT expand=1.
  3. The link and NOTHING else goes to the dedicated thread. No prose, no trailer,
     no label, no acknowledgment. Every time.
  4. Status / escalation / discussion stays on your own project thread.
     Only the landing link moves.
  5. You open nothing. The human clicks through under their own GitHub session.

USAGE
  compare-link.py <branch> <title> <body-file> [--fork X] [--upstream Y] [--repo Z]
  compare-link.py --self-test        # verify this checker still catches violations

  Then, to send (note: link alone, dedicated thread):
    scion message user:<human> "$(cat out.txt)" --channel discord --thread-id <DEDICATED>
"""
import sys, urllib.parse

DEDICATED_THREAD = "1532864101909528737"   # landing links ONLY. Not your project thread.
DEF_UPSTREAM, DEF_FORK, DEF_REPO = "GoogleCloudPlatform", "ptone", "scion"
CAP = 2000   # Discord user-message cap, SERVER-ENFORCED. Over-cap sends fail outright.


def build(branch, title, body, upstream=DEF_UPSTREAM, fork=DEF_FORK, repo=DEF_REPO):
    """Return (message, url). Encoding is done here so callers cannot skip it."""
    url = (f"https://github.com/{upstream}/{repo}/compare/"
           f"main...{fork}:{repo}:{branch}?quick_pull=1"
           f"&title={urllib.parse.quote(title)}"
           f"&body={urllib.parse.quote(body)}")
    return f"[{title}]({url})", url


def validate(msg, url, title):
    """Return a list of protocol violations. Empty list means conforming."""
    errs = []
    if "quick_pull=1" not in url:
        errs.append("URL missing quick_pull=1")
    if "expand=1" in url:
        errs.append("URL uses expand=1; spec requires quick_pull=1")
    if "&title=" not in url or "&body=" not in url:
        errs.append("URL missing pre-fill title= and/or body= args")
    if any(c in url for c in (" ", "\n", "\t")):
        errs.append("URL contains raw whitespace; args are not URL-encoded")
    if not (msg.startswith("[") and msg.endswith(")")):
        errs.append("message is not a markdown link -- spec forbids a bare URL")
    if msg != f"[{title}]({url})":
        errs.append("message contains content besides the markdown link")
    if "\n" in msg:
        errs.append("message is multi-line; spec allows the link alone")
    if len(msg) >= CAP:
        errs.append(f"message is {len(msg)} runes, at/over the {CAP} cap "
                    f"(URL-encoding roughly doubles the raw body -- shorten the body, "
                    f"budget ~700 raw chars)")
    return errs


def self_test():
    """Prove the checker still rejects each violation it claims to catch.

    Exists because this tool is shared and nobody reviews it before relying on it.
    A checker that has never rejected anything is not known to check. Run it before
    trusting this file, especially after anyone edits it.
    """
    good_msg, good_url = build("scion/example", "fix: a title", "A body.")
    cases = [
        ("conforming message", good_msg, good_url, "fix: a title", None),
        ("bare URL", good_url, good_url, "fix: a title", "not a markdown link"),
        ("expand=1", *(lambda u: (f"[t]({u})", u))(good_url.replace("quick_pull=1", "expand=1")),
         "t", "expand=1"),
        ("prose trailer", good_msg + "\n\nready for review", good_url, "fix: a title",
         "besides the markdown link"),
        ("missing prefill", "[t](https://github.com/o/r/compare/main...f:r:b?quick_pull=1)",
         "https://github.com/o/r/compare/main...f:r:b?quick_pull=1", "t", "pre-fill"),
        ("over cap", *(lambda m, u: (m, u))(*build("b", "t", "x" * 3000)), "t", "cap"),
    ]
    failures = 0
    for name, msg, url, title, expect in cases:
        errs = validate(msg, url, title)
        if expect is None:
            ok = not errs
        else:
            ok = any(expect in e for e in errs)
        print(f"  [{'PASS' if ok else 'FAIL'}] {name}"
              + ("" if ok else f"  -- expected {expect!r}, got {errs}"))
        failures += 0 if ok else 1
    print(f"\n{'SELF-TEST OK' if not failures else f'SELF-TEST FAILED ({failures})'}")
    return 1 if failures else 0


def main():
    a = sys.argv[1:]
    if "--self-test" in a:
        sys.exit(self_test())
    opts = {"upstream": DEF_UPSTREAM, "fork": DEF_FORK, "repo": DEF_REPO}
    pos = []
    i = 0
    while i < len(a):
        if a[i].startswith("--") and a[i][2:] in opts and i + 1 < len(a):
            opts[a[i][2:]] = a[i + 1]; i += 2
        else:
            pos.append(a[i]); i += 1
    if len(pos) != 3:
        sys.exit(__doc__)
    branch, title = pos[0], pos[1]
    body = open(pos[2]).read().strip()
    msg, url = build(branch, title, body, opts["upstream"], opts["fork"], opts["repo"])
    errs = validate(msg, url, title)
    if errs:
        print("PROTOCOL VIOLATION -- do not send:", file=sys.stderr)
        for e in errs:
            print("  - " + e, file=sys.stderr)
        sys.exit(1)
    print(f"# {len(msg)} runes, conforming. Send ONLY the line below, and send it to "
          f"thread {DEDICATED_THREAD}:\n", file=sys.stderr)
    print(msg)


if __name__ == "__main__":
    main()
