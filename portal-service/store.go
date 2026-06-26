package main

import "context"

//go:generate mockgen -source=store.go -destination=mock_store_test.go -package=main

// employee is one row of the load-time intersection of the HR-owned hr_employee
// collection and the users collection: an account's home site and canonical userId.
type employee struct {
	Account    string `json:"account"    bson:"account"`
	EmployeeID string `json:"employeeId" bson:"employeeId"`
	SiteID     string `json:"siteId"     bson:"siteId"`
	// UserID is users._id, projected by the directory $lookup. Held in memory so
	// the portal needs no per-request users query; not returned to the client.
	UserID string `json:"userId" bson:"userId"`
}

// DirectoryStore reads the HR employee directory (joined with users) that backs
// the in-memory cache.
type DirectoryStore interface {
	ListEmployees(ctx context.Context) ([]employee, error)
}
