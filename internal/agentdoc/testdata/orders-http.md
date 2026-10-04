# Orders API

API id: `orders-http` · Kind: OpenAPI · Version: 2.3.0 · Lifecycle: production

Create, read and cancel orders.

Owner: team-orders · System: commerce · Repo: github.com/acme/orders-service
Tags: orders

## Environments

- prod: https://orders.prod.internal

## Links

- [#team-orders](https://chat.internal/channels/team-orders)

## Operations

- [`GET /orders/{orderId}`](https://portal.internal/apis/orders-http/versions/2.3.0/operations/getOrder.md): Get an order
- [`POST /orders/{orderId}/cancellation`](https://portal.internal/apis/orders-http/versions/2.3.0/operations/cancelOrder.md): Cancel an order

## Consumers

- github.com/acme/web-shop (web-shop), team-web: everything
