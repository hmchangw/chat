package main

import "context"

//go:generate mockgen -source=store.go -destination=mock_store_test.go -package=main

// employee is one row of the HR-owned hr_employee collection: an account's
// home site and NATS coordinates. The collection is rewritten by a daily HR
// sync cron; the portal reads it and enforces a unique account index at startup.
type employee struct {
	Account    string `json:"account"    bson:"account"`
	EmployeeID string `json:"employeeId" bson:"employeeId"`
	SiteID     string `json:"siteId"     bson:"siteId"`
	NATSURL    string `json:"natsUrl"    bson:"natsUrl"`
}

// DirectoryStore reads the HR employee directory that backs the in-memory cache.
type DirectoryStore interface {
	ListEmployees(ctx context.Context) ([]employee, error)
}
