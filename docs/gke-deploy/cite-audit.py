#!/usr/bin/env python3
"""gd-doc citation audit. ADMISSION RULE STATED BEFORE THE COUNT EXISTS (rule 100).

IN   : a token <name>.<ext>:<digits>[-<digits>]  -- a path:line citation.
OUT  : anything inside a fenced ``` block (command transcripts, not citations).
EDGE : bare section numbers (17.1), rule numbers, times -- NOT citations, excluded.
PASS : a citation is COMPLIANT iff a >=7-hex-digit token occurs on the SAME line.
EXIT : 0 clean / 1 findings / >=2 no reading taken (rule: exit-code contract v3).
"""
import re, sys

CITE = re.compile(r'\b[\w./-]+\.[A-Za-z][\w]{0,6}:\d+(?:-\d+)?\b')
SHA  = re.compile(r'\b[0-9a-f]{7,40}\b')

def audit(text):
    total=comp=0; bad=[]; fenced=False
    for n,line in enumerate(text.split('\n'),1):
        if line.lstrip().startswith('```'):
            fenced = not fenced; continue
        if fenced: continue
        hits = CITE.findall(line)
        if not hits: continue
        total += len(hits)
        if SHA.search(line): comp += len(hits)
        else: bad.append((n,hits))
    return total, comp, bad

def main():
    path = sys.argv[1] if len(sys.argv)>1 else None
    if not path: print('ERR: no argv path (rule: a hardcoded DOC is not an argument)'); return 2
    try: text = open(path, encoding='utf-8').read()
    except Exception as e: print('ERR: unreadable: %s' % e); return 2
    if not text.strip(): print('ERR: empty corpus, zero cases evaluated'); return 2

    total, comp, bad = audit(text)
    if total == 0: print('ERR: zero citations parsed -- extractor is dead, not the file clean'); return 2

    # MUTATION CONTROLS (rule 132: not evidence until something independent goes red).
    t1,_,b1 = audit(text + '\nplanted control: fake-file.yaml:999 with no sha here\n')
    t2,c2,_ = audit(text + '\nplanted control: fake-file.yaml:999 @ deadbeef1234567\n')
    if len(b1) != len(bad)+1: print('CONTROL A FAILED: negative plant did not raise the finding count'); return 2
    if t2 != total+1 or c2 != comp+1: print('CONTROL B FAILED: positive plant not scored compliant'); return 2
    print('controls: A negative-plant raised findings %d -> %d  |  B positive-plant scored compliant. BOTH FIRED.'
          % (len(bad), len(b1)))

    print('corpus       : %s' % path)
    print('citations IN : %d' % total)
    print('with a SHA   : %d' % comp)
    print('VIOLATIONS   : %d citations on %d lines' % (total-comp, len(bad)))
    for n,h in bad[:8]: print('   design.md:%d  %s' % (n, ' '.join(h[:4])))
    if len(bad)>8: print('   ... and %d more lines' % (len(bad)-8))
    return 1 if bad else 0

sys.exit(main())
