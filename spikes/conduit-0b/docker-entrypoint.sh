#!/bin/bash
# Per-container ("per agent incarnation") host key, generated at start.
set -e
mkdir -p "$HOME/.conduit"
/usr/local/bin/conduit-sshd -init-hostkey -hostkey "$HOME/.conduit/hostkey" -hostkey-pub-out "$HOME/.conduit/hostkey.pub" >/dev/null
mkdir -p /workspace/demo
[ -f /workspace/demo/hello.txt ] || printf 'Hello from the conduit-0b spike container.\nEdit me in VS Code and save.\n' > /workspace/demo/hello.txt
echo "conduit-0b ready; host key: $(cat "$HOME/.conduit/hostkey.pub")"
exec sleep infinity
