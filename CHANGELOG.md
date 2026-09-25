# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **Gateway transport and parse failures are now structured `Internal` errors.**
  Mellat and Parsian returned `internal/soap` errors with the subject `soap`
  and their detail (HTTP status and body, SOAP `faultcode`/`faultstring`) only
  in `Params`. Microjet's HTTP error middleware never logs `Params`, and since
  v0.41.0 it drops them from production `Internal` responses, so that detail was
  lost everywhere. Zarinpal, IDPay, Saman and Pasargad returned raw `net/http`
  and JSON-decode errors, so `errorx.IsInternalError` was false for them and
  they had no gateway subject. Every gateway now wraps these failures with
  `payjet.Fault(gateway, op, …, cause)`. The subject is the gateway name,
  `Params` carries `op`, and the cause is the `Inner`, which the middleware logs.
  `errors.Is` still matches the cause, for example `context.DeadlineExceeded`.
- SOAP error bodies kept on the error are capped at 1 KiB, now that they reach
  the logs.
- Corrected the README's Errors section and the `gatewayError` doc comment on
  what microjet's middleware puts in responses and logs.

### Changed

- Saman sends its requests through a `postJSON` helper, like the other JSON
  gateways.
- The webshop example's `config.toml` documents `[http] trustedProxies`.
- The README no longer repeats the pinned microjet version; `go.mod` is the
  source.

## [0.8.0] - 2026-09-25

### Changed

- **Updated to microjet v0.41.0** (from v0.30.0). No payjet code changes were
  needed. Two microjet behavior changes are worth knowing when serving payjet
  errors over HTTP:
  - Since v0.41.0, the HTTP error middleware renders `Internal` errors (payjet's
    transport and parse faults) as a generic 500 outside debug mode, without the
    subject, message, code, or params. `Business` and `BadRequest` responses
    still carry subject, message, and params; the inner cause is dropped for
    every category. Since v0.33.0 the middleware also logs every attached
    error's subject, message, code, and inner cause (not its params). See 0.8.1
    for the follow-up fix this required.
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
