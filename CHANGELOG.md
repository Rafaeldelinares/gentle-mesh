# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [v1.0.0] - 2024-09-25

### Added

#### Core Features
- **Remote Task Execution**: Execute subagent tasks on remote nodes via REST + SSE
- **TLS/mTLS Security**: HTTPS with optional mutual TLS authentication
- **Certificate Enrollment**: CSR-based automatic certificate enrollment with tokens (zero-knowledge)
- **Territory Scheduling**: Branch-aware scheduling with conflict detection
- **Multi-client Streaming**: Multiple viewers can subscribe to task events simultaneously

#### Infrastructure
- **Coordinator Server**: HTTP server with task management, SSE streaming, and enrollment
- **Worker Node**: Remote execution client with auto-discovery
- **Registry**: Node registry with heartbeat keepalive
- **Federation**: M2M peering for multi-coordinator meshes

#### Operations
- **Webhook Notifications**: Push notifications for task.completed, task.failed, task.timeout events
- **Automatic Retry**: Configurable retry with exponential backoff
- **Task Priority**: Priority-based scheduling (-100 to 100)
- **Timeout Protection**: Task timeout and inactivity timeout (5 min)
- **Idempotency Keys**: Prevent duplicate task execution

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
