# payments-events

API id: `payments-events` · Kind: CloudEvents · Version: 1.0.0 · Lifecycle: deprecated

> **Deprecated**, sunset 2027-01-31. Don't build new integrations on it.

Events emitted by the payments service.

Owner: team-payments

## Environments

- prod: broker kafka-prod

## Event types

- [`com.acme.payments.payment.captured.v1`](https://portal.internal/events/com.acme.payments.payment.captured.v1.md) (produces): A payment was captured for an order.
- [`com.acme.payments.refund.issued.v1`](https://portal.internal/events/com.acme.payments.refund.issued.v1.md) (produces): A refund was issued to the customer.

## Consumes

- `com.acme.orders.order.created.v1` from `orders-events`

## `com.acme.payments.payment.captured.v1`

A payment was captured for an order.

API: [payments-events](https://portal.internal/apis/payments-events/versions/1.0.0.md) 1.0.0 · Role: produces · Owner: team-payments

### Envelope (CloudEvents attributes)

- `type`: `com.acme.payments.payment.captured.v1`
- `source`: `/payments-service`
- `subject`: `orders/{orderId}`
- `datacontenttype`: `application/json`

### Bindings

- kafka `payments.events` (key: {"from":"data","pointer":"/orderId"}; mode: binary)

### Payload (data)

- `amount` (integer, required): Minor units.
- `orderId` (string, required)

## `com.acme.payments.refund.issued.v1`

A refund was issued to the customer.

API: [payments-events](https://portal.internal/apis/payments-events/versions/1.0.0.md) 1.0.0 · Role: produces · Owner: team-payments

Emitted once the refund is accepted by the payment provider.

### Envelope (CloudEvents attributes)

- `type`: `com.acme.payments.refund.issued.v1`
- `source`: `/payments-service`
- `subject`: `orders/{orderId}`
- `datacontenttype`: `application/json`

### Bindings

- kafka `payments.events` (key: {"from":"data","pointer":"/orderId"}; mode: binary)

### Payload (data)

- `amount` (integer, required): Minor units refunded.
- `orderId` (string, required)
- `refundId` (string, required)

### Examples

partial:

```json
{
  "amount": 500,
  "orderId": "ord_123",
  "refundId": "rf_1"
}
```
