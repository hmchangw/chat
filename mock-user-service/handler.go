package main

import (
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// --- mock data constants ---

const (
	mockStatusText   = "available"
	mockStatusIsShow = true
	mockDisplayName  = "Mock User"
	mockEmail        = "mock@example.test"
)

var mockJoinedAt = time.Unix(0, 0).UTC()

// --- request / response types ---

type statusGetByNameReq struct {
	Name string `json:"name"`
}

type statusSetReq struct {
	StatusText   string `json:"statusText"`
	StatusIsShow bool   `json:"statusIsShow"`
}

type statusResp struct {
	Name         string `json:"name"`
	StatusText   string `json:"statusText"`
	StatusIsShow bool   `json:"statusIsShow"`
}

type profileGetByNameReq struct {
	Name string `json:"name"`
}

type profileResp struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
}

type getSubsReq struct {
	Favorite       *bool    `json:"favorite,omitempty"`
	MembersContain []string `json:"membersContain,omitempty"`
	AccountNames   []string `json:"accountNames,omitempty"`
}

type getAppSubsReq struct {
	Favorite *bool `json:"favorite,omitempty"`
}

type getDMSubReq struct {
	TargetAccount string `json:"targetAccount"`
}

type appSubscriptionReq struct {
	AppID string `json:"appId"`
}

type subscriptionListResp struct {
	Subscriptions []model.Subscription `json:"subscriptions"`
	Total         int                  `json:"total"`
}

type dmSubscriptionResp struct {
	Subscription model.Subscription `json:"subscription"`
}

type roomSubscriptionResp struct {
	Subscription model.Subscription `json:"subscription"`
}

type appListResp struct {
	Apps  []model.App `json:"apps"`
	Total int         `json:"total"`
}

type okResp struct {
	Success bool `json:"success"`
}

// --- handler ---

type Handler struct {
	siteID string
}

func NewHandler(siteID string) *Handler {
	return &Handler{siteID: siteID}
}

func (h *Handler) checkSite(c *natsrouter.Context) error {
	if c.Param("siteID") != h.siteID {
		return natsrouter.ErrNotFound("unknown site")
	}
	return nil
}

// --- mock data helpers ---

func buildMockSub(account, siteID string) model.Subscription {
	return model.Subscription{
		ID:       "mock-sub-" + account,
		User:     model.SubscriptionUser{ID: "mock-user-" + account, Account: account},
		RoomID:   "mock-room",
		SiteID:   siteID,
		Roles:    []model.Role{model.RoleMember},
		Name:     "Mock Room",
		RoomType: model.RoomTypeChannel,
		JoinedAt: mockJoinedAt,
	}
}

func buildMockApp(id, name string) model.App {
	return model.App{ID: id, Name: name}
}

func (h *Handler) statusGetByName(c *natsrouter.Context, req statusGetByNameReq) (*statusResp, error) {
	if err := h.checkSite(c); err != nil {
		return nil, err
	}
	return &statusResp{
		Name:         req.Name,
		StatusText:   mockStatusText,
		StatusIsShow: mockStatusIsShow,
	}, nil
}

func (h *Handler) statusSet(c *natsrouter.Context, req statusSetReq) (*okResp, error) {
	if err := h.checkSite(c); err != nil {
		return nil, err
	}
	_ = req
	return &okResp{Success: true}, nil
}

func (h *Handler) profileGetByName(c *natsrouter.Context, req profileGetByNameReq) (*profileResp, error) {
	if err := h.checkSite(c); err != nil {
		return nil, err
	}
	return &profileResp{
		Name:        req.Name,
		DisplayName: mockDisplayName,
		Email:       mockEmail,
	}, nil
}
