package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/msgraph"
)

// callActivities are the Teams activities that map to our in-call status
// (call/meeting activities only, per design).
var callActivities = map[string]struct{}{
	"InACall":           {},
	"InAConferenceCall": {},
	"Presenting":        {},
}

// isInCall reports whether a Teams presence reflects an active call.
func isInCall(p msgraph.Presence) bool {
	_, ok := callActivities[p.Activity]
	return ok
}

// accountFromEmail returns the local part of an email when its domain matches
// (case-insensitive on domain); ok=false otherwise.
func accountFromEmail(email, domain string) (string, bool) {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return "", false
	}
	if !strings.EqualFold(email[at+1:], domain) {
		return "", false
	}
	return email[:at], true
}

type reconcileConfig struct {
	SiteID          string
	EmailDomain     string
	ExternalTTL     time.Duration
	IDMapRefreshTTL time.Duration
}

type reconciler struct {
	accts accountLister
	users userLister
	pres  presenceReader
	app   externalApplier
	idx   inCallIndex
	idm   idMapStore
	pub   statePublisher
	cfg   reconcileConfig
}

func newReconciler(accts accountLister, users userLister, pres presenceReader, app externalApplier, idx inCallIndex, idm idMapStore, pub statePublisher, cfg reconcileConfig) *reconciler {
	return &reconciler{accts: accts, users: users, pres: pres, app: app, idx: idx, idm: idm, pub: pub, cfg: cfg}
}

// run performs one full reconciliation: resolve our accounts to Azure IDs (via
// the TTL-gated id-map cache), read Teams presence, then set in-call accounts
// and clear those no longer in a call.
func (r *reconciler) run(ctx context.Context) error {
	accounts, err := r.accts.ListSiteAccounts(ctx, r.cfg.SiteID)
	if err != nil {
		return fmt.Errorf("list site accounts: %w", err)
	}

	if err := r.refreshIfStale(ctx, accounts); err != nil {
		return err
	}

	idByAccount, err := r.idm.Resolve(ctx, accounts)
	if err != nil {
		return fmt.Errorf("resolve id map: %w", err)
	}
	ids := make([]string, 0, len(idByAccount))
	accountByID := make(map[string]string, len(idByAccount))
	for account, id := range idByAccount {
		ids = append(ids, id)
		accountByID[id] = account
	}

	presences, err := r.pres.GetPresencesByUserId(ctx, ids)
	if err != nil {
		return fmt.Errorf("get presences: %w", err)
	}
	current := make(map[string]struct{}, len(presences))
	for _, p := range presences {
		if !isInCall(p) {
			continue
		}
		if account, ok := accountByID[p.ID]; ok {
			current[account] = struct{}{}
		}
	}

	prev, err := r.idx.Members(ctx)
	if err != nil {
		return fmt.Errorf("read in-call index: %w", err)
	}

	for account := range current {
		if err := r.apply(ctx, account, model.StatusInCall); err != nil {
			return err
		}
	}
	for _, account := range prev {
		if _, still := current[account]; still {
			continue
		}
		if err := r.apply(ctx, account, model.StatusNone); err != nil {
			return err
		}
	}

	slog.Info("teams presence reconcile complete",
		"site", r.cfg.SiteID, "accounts", len(accounts), "inCall", len(current))
	return nil
}

// refreshIfStale rebuilds the id map from a full ListUsers when the freshness
// marker has expired; otherwise it does nothing.
func (r *reconciler) refreshIfStale(ctx context.Context, accounts []string) error {
	fresh, err := r.idm.Fresh(ctx)
	if err != nil {
		return fmt.Errorf("check id map freshness: %w", err)
	}
	if fresh {
		return nil
	}
	ours := make(map[string]struct{}, len(accounts))
	for _, a := range accounts {
		ours[a] = struct{}{}
	}
	tenant, err := r.users.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("list tenant users: %w", err)
	}
	mapping := make(map[string]string, len(ours))
	for _, u := range tenant {
		if u.ID == "" {
			continue
		}
		email := u.Mail
		if email == "" {
			email = u.UserPrincipalName
		}
		account, ok := accountFromEmail(email, r.cfg.EmailDomain)
		if !ok {
			continue
		}
		if _, mine := ours[account]; !mine {
			continue
		}
		mapping[account] = u.ID
	}
	if err := r.idm.Refresh(ctx, mapping, r.cfg.IDMapRefreshTTL); err != nil {
		return fmt.Errorf("refresh id map: %w", err)
	}
	return nil
}

// apply sets/clears the external status, updates the in-call index, and
// publishes a state change only when the effective status changed.
func (r *reconciler) apply(ctx context.Context, account string, status model.PresenceStatus) error {
	changed, eff, err := r.app.SetExternal(ctx, account, status, r.cfg.ExternalTTL)
	if err != nil {
		return fmt.Errorf("set external %q: %w", account, err)
	}
	if status == model.StatusNone {
		if err := r.idx.Remove(ctx, account); err != nil {
			return fmt.Errorf("index remove %q: %w", account, err)
		}
	} else {
		if err := r.idx.Add(ctx, account); err != nil {
			return fmt.Errorf("index add %q: %w", account, err)
		}
	}
	if changed {
		r.pub.Publish(ctx, account, eff)
	}
	return nil
}
