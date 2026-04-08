package model

// Employee holds employee data looked up from the employee MongoDB collection.
type Employee struct {
	AccountName string `bson:"accountName"`
	Name        string `bson:"name"`
	EngName     string `bson:"engName"`
}
