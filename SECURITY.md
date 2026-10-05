# Security Policy

> ⚠️ **Status: Experimental — NOT Audited**
>
> This project is a proof-of-concept / RFC proposal actively under development and has
> **not** undergone an independent security audit. The implementation must not be described
> as "secure" or "production-ready" in public documentation, READMEs, or releases until a
> formal audit is completed.

## Reporting a vulnerability

Please do **not** open a public GitHub issue for a security report.

Report privately through GitHub Security Advisories:

→ https://github.com/Rafaeldelinares/gentle-mesh/security/advisories/new

Include as much detail as you can: a description, the affected version(s), the potential
impact, and any suggested fix. Anonymized reproduction notes are fine.

## Supported versions

Only the latest `1.0.x` release receives security fixes. Older releases are unsupported.

## Scope and expectations

This project is a community proof-of-concept / RFC proposal, not an audited product.

- No independent security audit has been performed.
- Do not rely on it for production workloads or to protect sensitive data.
- Security fixes are applied to the latest release at the maintainer's discretion.
- Reports are handled on a best-effort basis; there is no guaranteed response timeline.

## Known limitations

- **No node certificate revocation**: node certificates are valid for one year and there is no
  revocation mechanism; a compromised or leaked node certificate stays valid until it expires
  (#25).
- **No authentication or loopback binding by default**: the coordinator listens on every interface
  without a token by default; v1.0.3 only logs a warning for that case (tracked for v1.1.0, #57).
- **CLI clients without a client certificate**: `nodes`, `radar`, `run` and `rpc` cannot reach a
  coordinator started with `-require-mtls`; TLS client flags are planned for v1.0.4.

## Acknowledgements

Security researchers who report issues responsibly will be acknowledged here (with
permission) once a fix is publicly released.
