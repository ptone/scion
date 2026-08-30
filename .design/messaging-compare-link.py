#!/usr/bin/env python3
"""Build and VALIDATE a landing link against the compare-URL protocol.

Protocol confirmed by coordinator 2026-08-30T18:33Z. This exact protocol has been
mis-followed and corrected repeatedly, by multiple agents including the coordinator.
Do not hand-assemble the message. Run this, and send only what it prints.

Usage:  python3 compare-link.py <branch> <title> <body-file>
Emits the exact message body, or exits non-zero explaining the violation.
"""
import sys, urllib.parse

THREAD = "1532864101909528737"   # dedicated compare-link thread. NOT 1541161053118005308.
UPSTREAM_OWNER, FORK_OWNER, REPO = "GoogleCloudPlatform", "ptone", "scion"
CAP = 2000

def build(branch, title, body):
    url = (f"https://github.com/{UPSTREAM_OWNER}/{REPO}/compare/"
           f"main...{FORK_OWNER}:{REPO}:{branch}?quick_pull=1"
           f"&title={urllib.parse.quote(title)}"
           f"&body={urllib.parse.quote(body)}")
    return f"[{title}]({url})", url

def validate(msg, url, title):
    errs = []
    # Rule 2: quick_pull=1, not expand=1. expand=1 was the mistake made on Phase 4.
    if "quick_pull=1" not in url:
        errs.append("URL missing quick_pull=1")
    if "expand=1" in url:
        errs.append("URL uses expand=1; spec requires quick_pull=1")
    if "&title=" not in url or "&body=" not in url:
        errs.append("URL missing pre-fill title= and/or body= args")
    # Rule 2: both args must actually be encoded.
    for frag in (" ", "\n"):
        if frag in url:
            errs.append("URL contains a raw space or newline; args are not URL-encoded")
            break
    # Rule 1: markdown link, not a bare URL.
    if not (msg.startswith("[") and msg.endswith(")")):
        errs.append("message is not a markdown link -- spec forbids a bare URL")
    # Rule 3: the link and NOTHING else. No prose, no trailer, no leading label.
    if msg != f"[{title}]({url})":
        errs.append("message contains content besides the markdown link")
    if "\n" in msg:
        errs.append("message is multi-line; spec allows the link alone")
    # Server-enforced Discord cap. An over-cap send FAILS and prints CLI help.
    if len(msg) >= CAP:
        errs.append(f"message is {len(msg)} runes, at/over the {CAP} cap "
                    f"(URL-encoding roughly doubles the raw body; shorten the body)")
    return errs

def main():
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    branch, title, body = sys.argv[1], sys.argv[2], open(sys.argv[3]).read().strip()
    msg, url = build(branch, title, body)
    errs = validate(msg, url, title)
    if errs:
        print("PROTOCOL VIOLATION -- do not send:", file=sys.stderr)
        for e in errs:
            print("  - " + e, file=sys.stderr)
        sys.exit(1)
    print(f"# {len(msg)} runes, OK. Send ONLY the line below, to thread {THREAD}:\n")
    print(msg)
    print(f"\n# scion message user:ptone@google.com \"$(cat FILE)\" "
          f"--channel discord --thread-id {THREAD}", file=sys.stderr)

if __name__ == "__main__":
    main()
