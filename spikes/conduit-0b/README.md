# conduit-0b spike: embedded SSH server vs VS Code Remote-SSH (THROWAWAY)

Spike for Conduit design Q5 / Phase 0b (ptone/scion#2776). This code is **not**
meant for main. It is a standalone Go module, so the root module's `./...`
ignores it.

- `main.go` is `conduit-sshd`, a gliderlabs/ssh server with the design §3.8 (A3) forwarding
  profile. It serves over `-stdio` (ProxyCommand) or `-listen`.
- `scripts/remote_ssh_sim.py` is a scripted approximation of the Remote-SSH client. It uses
  the real VS Code Server, and the real PersistentProtocol, remoteFilesystem and
  remoteterminal RPCs.
- `scripts/negative.sh` holds the forwarding-profile checks (non-loopback, deny-list,
  streamlocal, -R, -A, X11, 32-channel cap).
- `scripts/run-all.sh` builds the server and runs everything.
- `Dockerfile` and `docker-entrypoint.sh` provide an agent-like container for the real-client
  runbook (`docker exec -i <ctr> conduit-sshd -stdio`).

The report and runbook are at `gs://scion-xproject-exchange/conduit/notes/0b-report.md` and
`0b-ptone-runbook.md`.
