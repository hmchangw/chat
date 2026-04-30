package model

// App is the read-only view of a row in the apps collection.
// Provisioning is upstream; chat services only read.
type App struct {
	ID          string        `json:"id"                    bson:"_id"`
	Name        string        `json:"name"                  bson:"name"`
	Description string        `json:"description,omitempty" bson:"description,omitempty"`
	Assistant   *AppAssistant `json:"assistant,omitempty"   bson:"assistant,omitempty"`
	Sponsors    []AppSponsor  `json:"sponsors,omitempty"    bson:"sponsors,omitempty"`
}

// AppAssistant declares the bot account and whether the assistant is
// active. Assistant.Name is the bot's user account (always ends in
// ".bot"). botDM creation requires Enabled == true.
type AppAssistant struct {
	Enabled     bool   `json:"enabled"               bson:"enabled"`
	Name        string `json:"name"                  bson:"name"`
	SettingsURL string `json:"settingsUrl,omitempty" bson:"settingsUrl,omitempty"`
}

type AppSponsor struct {
	Name  string `json:"name"  bson:"name"`
	Phone string `json:"phone" bson:"phone"`
}
