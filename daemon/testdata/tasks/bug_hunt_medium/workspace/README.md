# shop

A small order-pricing library: a catalog, carts, coupons, regional tax, and invoices.

    go run ./cmd/shop -region EU -coupon SAVE10 NB-01:3

Packages: money (cents arithmetic), catalog, cart, coupon, tax, pricing (subtotal, discount,
tax, total), invoice (builds and renders invoices), report (daily summaries), internal/audit.
