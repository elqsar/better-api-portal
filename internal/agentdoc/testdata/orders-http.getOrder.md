# `GET /orders/{orderId}`: Get an order

operationId: `getOrder` · API: [Orders API](https://portal.internal/apis/orders-http/versions/2.3.0.md) 2.3.0 · Auth: bearerAuth (http bearer JWT)

Returns a single order by id.

## Parameters

- `orderId` (path, required, string): Order identifier. pattern: ^ord_[a-zA-Z0-9]+$; max length 64.

## Responses

### 200: The order.

`application/json`:

- `customerId` (string, required): max length 64.
- `orderId` (string, required): max length 64.
- `status` (string, required): One of "placed", "paid", "shipped", "cancelled".
- `total` (object, required, Money)
  - `amount` (integer, required): Minor units. format: int64.
  - `currency` (string, required): pattern: ^[A-Z]{3}$; max length 3.

### 401, 404: Error (RFC 9457 problem details).

`application/problem+json`:

- `detail` (string): max length 2048.
- `status` (integer): format: int32.
- `title` (string): max length 256.
- `type` (string): format: uri.
