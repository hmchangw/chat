package main

import "context"

//go:generate mockgen -source=store.go -destination=mock_store_test.go -package=main

// directoryRecord is one account→home-site row; employeeId is informational only.
type directoryRecord struct {
	Account    string `json:"account" bson:"_id"`
	EmployeeID string `json:"employeeId" bson:"employeeId"`
	SiteID     string `json:"siteId" bson:"siteId"`
}

// DirectoryStore bulk-loads the directory; read only at startup and on reload,
// never on the request path.
type DirectoryStore interface {
	LoadAll(ctx context.Context) ([]directoryRecord, error)
}
