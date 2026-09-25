# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.8.0] - 2026-09-25

### Changed

- **Updated to microjet v0.41.0** (from v0.30.0). No payjet code changes were
  needed. Two microjet behavior changes are worth knowing when serving payjet
  errors over HTTP:
  - Since v0.41.0, the HTTP error middleware renders `Internal` errors (payjet's
    transport and parse faults) as a generic 500 outside debug mode, without the
    subject, message, code, or params. `Business` and `BadRequest` responses
    still carry the full payload. Since v0.33.0 the middleware also logs every
    attached error server-side.
  - Since v0.41.0, `c.ClientIP()` ignores `X-Forwarded-For`/`X-Real-IP` unless
    the peer is listed in `[http] trustedProxies`. Set it when running the
    webshop example (or your app) behind a proxy.

## [0.7.0] - 2026-07-16

### Changed

- **Updated to microjet v0.30.0** (from v0.24.0). Adapted to the breaking
  `gormx.Table` change in v0.30.0: `Delete`, `Update`, `Save`, `Upsert`,
  `CreateMany` and `FindInBatches` now return `(int64, error)` instead of a bare
  `error`. The affected-row count is discarded at `dbPaymentStore.SavePayment`'s
  `Upsert` call, the only affected call site. The bump also pulls in the
  `quic-go` v0.59.1 security fix ([GO-2026-5676](https://pkg.go.dev/vuln/GO-2026-5676))
  released in microjet v0.29.1.

## [0.6.0] - 2026-07-02

### Changed

- Updated to microjet v0.24.0.

## [0.5.0] - 2026-06-24

### Changed

- Updated to microjet v0.21.0.

## [0.4.0] - 2026-06-24

### Changed

- Updated to microjet v0.20.0.

## [0.3.0] - 2026-06-23

### Changed

- Updated to microjet v0.19.0.

## [0.2.0] - 2026-06-19

### Added

- Payment/transaction storage and per-feature examples.

## [0.1.0] - 2026-06-19

### Added

- Initial release built on microjet v0.18.0.
