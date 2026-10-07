package ports

import "context"

// ClearDevProjectSource is a read-only Git observation, never a health check or
// permission to execute project code. Empty describes the selected commit tree.
type ClearDevProjectSource struct {
	ResolvedRef   string
	BaseCommitSHA string
	RepositoryURL string
	Empty         bool
}

// ClearDevProjectSourceInspector resolves the registered project's selected
// branch without checking out, fetching, installing or running repository code.
type ClearDevProjectSourceInspector interface {
	InspectProjectSource(context.Context, string, string) (ClearDevProjectSource, error)
}

// ClearDevDeliveredProjectSourceInspector observes an exact backend-verified
// completed commit without moving the registered repository's default branch.
// The service and SQLite must first prove its final delivery provenance.
type ClearDevDeliveredProjectSourceInspector interface {
	InspectDeliveredProjectSource(context.Context, string, string, string) (ClearDevProjectSource, error)
}

// ClearDevSourcePreparation binds preparation to the original observed checkout.
// Current is checked again immediately before publishing files.
type ClearDevSourcePreparation struct {
	RequestID     string
	Workspace     string
	Branch        string
	RepositoryURL string
	ExpectedBase  string
	Current       func(context.Context) error
}

// ClearDevProjectSourcePreparer prepares the exact chosen source without running project code.
type ClearDevProjectSourcePreparer interface {
	PrepareProjectSource(context.Context, ClearDevSourcePreparation) (ClearDevProjectSource, error)
}
