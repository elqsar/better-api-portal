# `POST /orders/{orderId}/cancellation`: Cancel an order

operationId: `cancelOrder` · API: [Orders API](https://portal.internal/apis/orders-http/versions/2.3.0.md) 2.3.0 · Auth: bearerAuth (http bearer JWT)

Requests cancellation. Emits com.acme.orders.order.cancelled.v1 on success.

## Parameters

- `orderId` (path, required, string): Order identifier. pattern: ^ord_[a-zA-Z0-9]+$; max length 64.

## Request body

Required.

`application/json`:

- `reason` (string, required): One of "customer_request", "payment_failed", "out_of_stock".

## Responses

### 202: Cancellation accepted.

No body.

### 401, 404, 409: Error (RFC 9457 problem details).

`application/problem+json`:

- `detail` (string): max length 2048.
- `status` (integer): format: int32.
- `title` (string): max length 256.
- `type` (string): format: uri.
