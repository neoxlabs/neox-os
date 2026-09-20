# Security Policy

## Reporting a vulnerability

Please do not report security vulnerabilities through public GitHub issues.

Email **support@neox-dev.com** with the subject line `Security`, including steps to
reproduce and the affected version. You can also use GitHub's private vulnerability
reporting on this repository.

We aim to acknowledge reports within 3 business days.

## Areas of particular interest

NeoX OS gives agent processes a boundary and then trusts it. The things that matter most:

- **Escaping the confinement** — reaching a path, a network destination or a resource
  outside the capabilities a process was granted, including via a child process,
  `nohup`, a re-exec, or a symlink out of the workspace.
- **Bypassing approval** — getting a boundary-crossing action to proceed without the
  human decision that the OS is supposed to require.
- **Credential exposure** — provider keys are meant to stay with the host and never
  enter a confined process. Any path by which an agent can read them is a vulnerability.
- **The observe endpoint** — the console is protected by a bearer token. Token leakage,
  bypass, or any unauthenticated route that returns ledger contents.

## Known and intentional limits

These are documented, not bugs:

- Kernel-level enforcement holds on **Linux only**. On macOS the enforcement degrades to
  a Seatbelt sandbox that intercepts writes only; the self-check reports which layer is
  actually in effect. Do not treat macOS as a security boundary.
- Binding the console to a non-loopback address (`NEOX_OBSERVE_ADDR`) is opt-in and
  deliberately leaves transport security to you — use a private network, a tunnel or TLS.
- `docs/TRAPS.md` and `docs/STATUS.md` record the current outstanding limits.
