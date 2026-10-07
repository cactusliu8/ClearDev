package cleardev

import "time"

// ProductSourcePreparation retains the user's exact request before any source
// download. It is preparation evidence, not a development approval.
type ProductSourcePreparation struct {
	ID         string
	ProductID  string
	PreviousID string
	InputJSON  string
	Selection  ProductSelection
	Branch     string
	Status     string
	Failure    string
	Prepared   *ProductSelection
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
