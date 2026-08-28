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
