# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project aims to
follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.9.0] - 2026-09-25

Reviewed every gateway against [Parbad](https://github.com/Sina-Soltani/Parbad).
Several verified callbacks that did not belong to the payment, and some could
not verify a real payment at all. Upgrading needs one change: set
`Payment.Token` when calling `Verify` (see **Added**); Parsian and Pasargad
now require it.

### Security

- **Parsian: a paid token could confirm a different order.** Verify confirmed
  whatever token the callback carried and only checked `OrderId`, and
  `ConfirmPayment` reports no amount. A customer could pay a cheap order,
  withhold its callback, and send one for an expensive order carrying the cheap
  order's token; the expensive order verified. Verify now requires
  `Payment.Token` and rejects a different callback token with
  `ErrTokenMismatch`. It also checks the callback amount when present.
- **IDPay: verify trusted the callback's `order_id`.** It sent the callback's
  `order_id` to the verify API instead of the payment's, never compared the two,
  and ignored the verified amount. It now rejects a mismatched `order_id`
  (`ErrOrderMismatch`), callback amount or verified amount (`ErrAmountMismatch`),
  or `id` (`ErrTokenMismatch`, when `Payment.Token` is set), and verifies with
  the payment's own order ID.
- **Mellat: the callback's `SaleOrderId` was sent back to the bank unchecked.** It
  must now equal the payment's order ID, and the payment's ID is what is sent.
  The callback `RefId` is checked against `Payment.Token` when set.
- **Zarinpal: the callback `Authority` is checked against `Payment.Token`** when
  set.
- **Virtual gateway: any `result=true` callback verified.** Verify now accepts
  only transaction codes the gateway issued from its page or `SimulatePayment`,
  for the matching order and token.
- **Webshop example:** the result page interpolated `ref` and `msg` from the URL
  into HTML unescaped (reflected XSS); both are escaped now. The callback
  handler verified orders in any state, so a replayed callback re-fulfilled a
  paid order and a forged decline flipped it to failed; only pending orders are
  verified now.

### Fixed

- **Pasargad verify sent an empty `UrlId`.** It read `urlId` from the callback,
  which carries only `invoiceId`, `status`, `referenceNumber` and `trackId`.
  Verify now sends `Payment.Token` (the purchase `UrlId`) and requires it.
- **Mellat SOAP fields were namespace-qualified.** `<int:terminalId>` and the
  other fields are now unqualified (`<terminalId>`), as Mellat's JAX-WS schema
  and Parbad expect; only the operation element keeps its namespace.
- **Callback fields are matched case-insensitively** in every gateway, through
  the new `payjet.Param`. Parsian posts `Token`, `OrderId` and `Amount`, which
  the exact `token`/`orderId` lookups missed, so every Parsian verify failed and
  its callback order lookup returned nothing.
- **Zarinpal error responses could not be decoded.** Failures arrive as
  `{"data": [], "errors": {...}}` (or an `errors` array), which did not fit the
  response struct, so every rejection became a generic decode fault and the
  bank's code was lost. They are now `Rejected` errors carrying the code in
  `gatewayCode`, and -51 (payment failed) is `Declined` (`ErrCancelled`). The
  callback `Status` is matched case-insensitively, as in Parbad.
- **IDPay responses with string-typed numbers failed to decode.** IDPay documents
  `status`, `track_id` and `amount` as strings; the verify response now accepts
  strings or numbers. A failed verify reports IDPay's `error_code` instead of
  `0`.
- **Saman verify checks the verified transaction's terminal and `RefNum`,** as
  Parbad does, and reports `OrderID` from the payment rather than the callback.
- **`GetPaymentByToken("")` matched any payment saved without a token** in the
  default store, such as one whose Request failed. It now returns nil.
- **Webshop example:** a network fault during verify marked the order failed
  although the bank may have taken the money; the order now stays pending. Order
  IDs are numeric, since Mellat and Parsian reject `order-<ms>`. A failed
  Request marks the order failed, and a failure saving the token is reported
  instead of ignored.

### Added

- `Payment.Token` — the `RequestResult.Token` issued for the payment. Set it
  when calling `Verify`; Parsian and Pasargad require it and the other gateways
  check the callback against it when set.
- `VerifyResult.AlreadyVerified` — set for a repeat verification (Zarinpal 101,
  IDPay 101, Mellat 43, a virtual code verified before), so a replayed callback
  can be told apart from a first one.
- `ErrTokenMismatch` — the callback's token is not the payment's.
- `payjet.Param(params, name)` — reads a callback field, matching the name
  case-insensitively when there is no exact match.
- `StoredPayment.Payment()` — rebuilds the `Payment`, token included, to pass to
  `Verify`.

### Changed

- The virtual gateway's callback carries `token`, and `SimulatePayment` includes
  the token of the payment it completes.

## [0.8.1] - 2026-09-25

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
