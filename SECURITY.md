# Security Policy

> ⚠️ **Status: Experimental — NOT Audited**
>
> This project is actively under development and has **not** undergone an independent
> security audit. The implementation must not be described as "secure" or "production-ready"
> in public documentation, READMEs, or releases until a formal audit is completed.

## Reporting Vulnerabilities

Found a security issue? We take all reports seriously.

**Please do NOT open a public GitHub issue for security vulnerabilities.**

Report privately via one of:

1. **GitHub Security Advisories** (preferred)
   → https://github.com/Rafaeldelinares/gentle-mesh/security/advisories/new

2. **Email** (if GitHub is unavailable)
   → Contact via GitHub profile

### What to include

Please provide as much detail as possible:

- Description of the vulnerability
- Steps to reproduce (anonymized if needed)
- Affected version(s)
- Potential impact
- Any suggested fixes (optional)

### Response timeline

| Severity | First response | Target resolution |
|----------|----------------|-------------------|
| Critical (RCE, key exfiltration) | 48 hours | 7 days |
| High (protocol bypass, integrity) | 1 week | 30 days |
| Medium (DoS, info disclosure) | 2 weeks | 60 days |
| Low / Informational | 4 weeks | 90 days |

These are **targets**, not guarantees. Patches may be released faster for critical issues.

## Scope

### In scope (RFC-002 Settlement Protocol)

- Ed25519 signature verification and determinism (R7)
- SHA-256 chain integrity (S7)
- JCS canonical serialization (S6)
- mTLS identity (S1)
- Protocol version enforcement (S9)
- Settlement assertions and receipt chaining
- Lease lifecycle and idempotency (R1, R2)

### Out of scope (per RFC-002 non-objectives)

- **OS/kernel isolation escapes** — use infrastructure (gVisor, nsjail, microVMs)
- **Side channels outside the mesh** — DNS, shared storage, public services
- **Compromised executor host** — keys in HSM/KMS, external chain anchoring
- **Agent quality/intent** — protocol verifies assertions, not goodness
- **General-purpose transport or agent discovery** — use A2A, MCP
- **Custom cryptography or sandboxing** — standard libraries and audited tools only

See `docs/rfcs/002-goals-and-non-goals.md` for the complete objective and non-objective list.

## Security model summary

```
┌─────────────────────────────────────────────────────────────┐
│  RFC-002 Threat Model: docs/architecture/THREAT-MODEL.md    │
├─────────────────────────────────────────────────────────────┤
│  Adversaries (any may be malicious or compromised):        │
│  • Emitter: tries to expand permissions via delegation      │
│  • Executor: pursues goal by unauthorized means             │
│  • Network attacker: intercepts, replays, alters messages   │
│  • Storage attacker: modifies receipts or state at rest     │
├─────────────────────────────────────────────────────────────┤
│  Protections:                                               │
│  • S1: mTLS + CN/SAN = agent_id                            │
│  • S2: verify Ed25519 before executing or persisting        │
│  • S3: deny-by-default (minimal profile if no capabilities) │
│  • S6: JSON canonical only, no arbitrary code execution     │
│  • S7: receipts signed + SHA-256 chained (see #43)          │
│  • S9: protocol_version enforced, no downgrade              │
├─────────────────────────────────────────────────────────────┤
│  NOT protected by this protocol:                           │
│  • N1: authorized agent doing harm within its perimeter     │
│  • N2: container/kernel escapes                            │
│  • N3: side channels outside the mesh                      │
│  • N4: compromised executor host                            │
└─────────────────────────────────────────────────────────────┘
```

## Known limitations

- Development certificates are self-signed and for local/Tailscale use only
- No TOTP or QR-based key enrollment in MVP
- Out-of-band key fingerprint verification deferred (Signal/email)
- No revocation propagation in MVP (S8 — deferred to Phase 3)
- No hardware key storage (HSM/KMS) in MVP (N4 — deferred)
- No external chain anchoring in MVP
- The executor signature does NOT cover `previous_receipt_hash` (nor `sequence_number`): an actor with database write access can delete a middle receipt or reorder receipts and recompute prev/seq without invalidating any signature (issue #43). `VerifyChainIntegrity` validates internal coherence, not structural authenticity.
- The emitter signature does NOT cover the acceptance/dispute decision, its reason or its timestamp: `ComputeReceiptHash` clears `emitter_acceptance`, `emitter_acceptance_at` and `dispute_reason`. The counter-signature sent by the emitter is stored without being verified at all, and the decision is set by the server (issue #48). Flipping ACCEPTED↔DISPUTED and rewriting the reason passes both `VerifyEmitterSignature` and `VerifyChainIntegrity`.

## Version policy

- **Development**: master branch, may contain known issues
- **Releases**: semantic versioning; security fixes backported at maintainer's discretion
- **Unsupported versions**: only the latest release receives security patches

## Acknowledgements

Security researchers who report issues responsibly will be acknowledged here
(with permission) once the fix is publicly released.
