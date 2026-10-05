# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased] (v1.0.4)

### Fixed

- `gentle-mesh rpc` answers an unsupported command with an explicit error frame without terminating
  the session. (#56)
- A local `.engram/` directory is ignored. (#53)

### Planned

- `feat(cli): add TLS client flags` so the CLI clients (`nodes`, `radar`, `run`, `rpc`) can present a
  client certificate and reach a coordinator started with `-require-mtls`.

## [v1.0.3] - Unreleased

### Security

Security release. It aligns the implementation with the guarantees already documented for mTLS and
CORS, which were not enforced by the code. See the security advisory in
[GitHub Security Advisories](https://github.com/Rafaeldelinares/gentle-mesh/security/advisories) for
the full description, the residual-risk guidance and the mitigation steps.

- **mTLS enforcement**: the TLS configuration is now applied to the HTTP server, so `-require-mtls`
  actually requires a verified client certificate and the client CA is built from the mesh CA.
  Startup fails when `-require-mtls` is set without a CA. (#52)
- **CORS allowlist and Host validation**: only the documented origins are allowed, `*` is never
  returned when an `Origin` header is present, and unknown `Host` values are rejected with 421. (#54)
- **Exposed runner startup**: a coordinator bound to a non-loopback address with `-runner pi` and no
  `-token`/`-require-mtls` now refuses to start unless `-insecure-no-auth` is passed. (#54)

### Added

- `SECURITY.md` with the private reporting channel and an honest scope statement. (#55)
- Coordinator flags `-cors-origins`, `-allowed-hosts` and `-insecure-no-auth`. (#54)

### Changed

- `POST /v1/mesh/join` requires the bearer token when `-token` is configured, and under
  `-require-mtls` the client certificate CN must match `node_id`.
- The `mTLS required` log line is emitted only after the TLS configuration is applied.

### Migration

- Default loopback, non-mTLS deployments need no changes.
- If the coordinator is exposed on a network, set `-token` and/or `-require-mtls`, and list custom DNS
  names (including docker service names such as `coordinator`) in `-allowed-hosts`.
- `https://localhost` and `https://127.0.0.1` are not in the default CORS allowlist; add them with
  `-cors-origins`.
- CLI clients (`nodes`, `radar`, `run`, `rpc`) do not present client certificates yet, so a
  coordinator with `-require-mtls` is not reachable from them; the CLI flags are planned for v1.0.4.

## [v1.0.0] - 2024-09-25

### Added

#### Core Features
- **Remote Task Execution**: Execute subagent tasks on remote nodes via REST + SSE
- **TLS/mTLS Security**: HTTPS with optional mutual TLS authentication
- **Certificate Enrollment**: CSR-based automatic certificate enrollment with tokens (zero-knowledge)
- **Territory Scheduling**: Branch-aware scheduling with conflict detection
- **Multi-client Streaming**: Multiple viewers can subscribe to task events simultaneously
- **Checkpoint/Resume**: Save and restore task progress for long-running tasks
- **Rate Limiting**: Protect coordinator from abuse with configurable per-IP limits

#### Infrastructure
- **Coordinator Server**: HTTP server with task management, SSE streaming, and enrollment
- **Worker Node**: Remote execution client with auto-discovery
- **Registry**: Node registry with heartbeat keepalive
- **Federation**: M2M peering for multi-coordinator meshes

#### Operations
- **Webhook Notifications**: Push notifications for task.completed, task.failed, task.timeout events
- **Automatic Retry**: Configurable retry with configurable delay
- **Task Priority**: Priority-based scheduling (-100 to 100)
- **Timeout Protection**: Task timeout and inactivity timeout (5 min)
- **Idempotency Keys**: Prevent duplicate task execution
- **Checkpoint API**: POST/GET /v1/tasks/{id}/checkpoint

#### Developer Experience
- **Single Binary**: Zero-dependency deployment
- **Cross-platform**: Builds for Linux, macOS, Windows, ARM
- **Docker Support**: Docker Compose for local development
- **Interactive Architecture Diagrams**: HTML-based visual documentation

### Security

- mTLS with certificate pinning
- Token-based enrollment (CSR never exposes private keys)
- HMAC-SHA256 webhook signatures
- Bearer token authentication

### Documentation

- Complete API documentation in README
- Architecture diagrams with Archify
- RFC 001 - Remote Agent Transport & M2M Federation
- External evaluation guide for LLMs
