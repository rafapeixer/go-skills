// MISTAKE: one package holds every feature's types, so every package imports
// it and a field added for billing touches, rebuilds and retests orders too.
package models

type Order struct {
	ID, CustomerID, ChargeID string
	Cents                    int64
}

type Charge struct {
	ID, CustomerID string
	Cents          int64
}
