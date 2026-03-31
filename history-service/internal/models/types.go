package models

import "github.com/gocql/gocql"

// Participant maps to the Cassandra "Participant" UDT.
type Participant struct {
	ID          string `json:"id"`
	UserName    string `json:"userName"`
	EngName     string `json:"engName,omitempty"`
	CompanyName string `json:"companyName,omitempty"`
	AppID       string `json:"appId,omitempty"`
	AppName     string `json:"appName,omitempty"`
	IsBot       bool   `json:"isBot,omitempty"`
}

func (p *Participant) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	switch name {
	case "id":
		return gocql.Unmarshal(info, data, &p.ID)
	case "user_name":
		return gocql.Unmarshal(info, data, &p.UserName)
	case "eng_name":
		return gocql.Unmarshal(info, data, &p.EngName)
	case "company_name":
		return gocql.Unmarshal(info, data, &p.CompanyName)
	case "app_id":
		return gocql.Unmarshal(info, data, &p.AppID)
	case "app_name":
		return gocql.Unmarshal(info, data, &p.AppName)
	case "is_bot":
		return gocql.Unmarshal(info, data, &p.IsBot)
	}
	return nil
}

func (p *Participant) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "id":
		return gocql.Marshal(info, p.ID)
	case "user_name":
		return gocql.Marshal(info, p.UserName)
	case "eng_name":
		return gocql.Marshal(info, p.EngName)
	case "company_name":
		return gocql.Marshal(info, p.CompanyName)
	case "app_id":
		return gocql.Marshal(info, p.AppID)
	case "app_name":
		return gocql.Marshal(info, p.AppName)
	case "is_bot":
		return gocql.Marshal(info, p.IsBot)
	}
	return nil, nil
}

// File maps to the Cassandra "File" UDT.
type File struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (f *File) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	switch name {
	case "id":
		return gocql.Unmarshal(info, data, &f.ID)
	case "name":
		return gocql.Unmarshal(info, data, &f.Name)
	case "type":
		return gocql.Unmarshal(info, data, &f.Type)
	}
	return nil
}

func (f File) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "id":
		return gocql.Marshal(info, f.ID)
	case "name":
		return gocql.Marshal(info, f.Name)
	case "type":
		return gocql.Marshal(info, f.Type)
	}
	return nil, nil
}

// Card maps to the Cassandra "Card" UDT.
type Card struct {
	Template string `json:"template"`
	Data     []byte `json:"data,omitempty"`
}

func (c *Card) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	switch name {
	case "template":
		return gocql.Unmarshal(info, data, &c.Template)
	case "data":
		return gocql.Unmarshal(info, data, &c.Data)
	}
	return nil
}

func (c Card) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "template":
		return gocql.Marshal(info, c.Template)
	case "data":
		return gocql.Marshal(info, c.Data)
	}
	return nil, nil
}

// CardAction maps to the Cassandra "CardAction" UDT.
type CardAction struct {
	Verb        string `json:"verb"`
	Text        string `json:"text,omitempty"`
	CardID      string `json:"cardId,omitempty"`
	DisplayText string `json:"displayText,omitempty"`
	HideExecLog bool   `json:"hideExecLog,omitempty"`
	CardTmID    string `json:"cardTmId,omitempty"`
	Data        []byte `json:"data,omitempty"`
}

func (ca *CardAction) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	switch name {
	case "verb":
		return gocql.Unmarshal(info, data, &ca.Verb)
	case "text":
		return gocql.Unmarshal(info, data, &ca.Text)
	case "card_id":
		return gocql.Unmarshal(info, data, &ca.CardID)
	case "display_text":
		return gocql.Unmarshal(info, data, &ca.DisplayText)
	case "hide_exec_log":
		return gocql.Unmarshal(info, data, &ca.HideExecLog)
	case "card_tmid":
		return gocql.Unmarshal(info, data, &ca.CardTmID)
	case "data":
		return gocql.Unmarshal(info, data, &ca.Data)
	}
	return nil
}

func (ca *CardAction) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "verb":
		return gocql.Marshal(info, ca.Verb)
	case "text":
		return gocql.Marshal(info, ca.Text)
	case "card_id":
		return gocql.Marshal(info, ca.CardID)
	case "display_text":
		return gocql.Marshal(info, ca.DisplayText)
	case "hide_exec_log":
		return gocql.Marshal(info, ca.HideExecLog)
	case "card_tmid":
		return gocql.Marshal(info, ca.CardTmID)
	case "data":
		return gocql.Marshal(info, ca.Data)
	}
	return nil, nil
}
