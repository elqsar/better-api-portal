# `com.acme.payments.refund.issued.v1`

A refund was issued to the customer.

API: [payments-events](https://portal.internal/apis/payments-events/versions/1.0.0.md) 1.0.0 · Role: produces · Owner: team-payments

Emitted once the refund is accepted by the payment provider.

## Envelope (CloudEvents attributes)

- `type`: `com.acme.payments.refund.issued.v1`
- `source`: `/payments-service`
- `subject`: `orders/{orderId}`
- `datacontenttype`: `application/json`

## Bindings

- kafka `payments.events` (key: {"from":"data","pointer":"/orderId"}; mode: binary)

## Payload (data)

- `amount` (integer, required): Minor units refunded.
- `orderId` (string, required)
- `refundId` (string, required)

## Examples

partial:

```json
{
  "amount": 500,
  "orderId": "ord_123",
  "refundId": "rf_1"
}
```

## Consumers

- github.com/acme/ledger (ledger-events), team-finance: everything
