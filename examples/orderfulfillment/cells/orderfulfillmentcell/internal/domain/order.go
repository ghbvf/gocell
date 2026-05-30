// Package domain contains the core Order entity for the orderfulfillment example.
package domain

import "time"

// Order is the aggregate root for a customer order.
type Order struct {
	ID                string
	Item              string
	AmountCents       int64
	PaymentShouldFail bool // deliberate compensate trigger for the failure journey
	CreatedAt         time.Time
}
