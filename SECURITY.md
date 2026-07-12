# Security Policy

## Reporting a vulnerability

Please do NOT open a public issue for security problems. Instead, either
email **hello@clishake.dev** or use GitHub's private vulnerability
reporting on this repository ("Security" tab → "Report a vulnerability").
You'll get an acknowledgement within a few days.

## Scope worth knowing about

CLIshake orchestrates coding-agent CLIs that run **as your own user** on
your own machine. By design it provides no sandbox: agents can read and
modify what you can. The security-relevant guarantees CLIshake does make —
and where reports are very welcome:

- clishake-authored input to agent terminals must never end up in a
  harness's permission/trust dialogs (readiness veto logic).
- The managed tmux server must never touch sessions outside its dedicated
  socket.
- Nothing in `.clishake/` runtime state should ever contain credentials;
  configuration explicitly documents that secrets don't belong in
  `config.toml`.
- `CLISHAKE_AGENT` attribution is documented as advisory, not a security
  boundary — but escalations that let an agent impersonate the *lead* in a
  way the audit log cannot distinguish are bugs.
