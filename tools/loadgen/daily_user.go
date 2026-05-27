package main

import (
	"math/rand"
	"time"
)

// actionKind enumerates the user-day operations the simulator can perform.
type actionKind int

const (
	actionSend actionKind = iota
	actionReadReceipt
	actionScrollHistory
	actionRefreshRoomList
	actionMemberAdd
	actionRoomCreate
	actionMuteToggle
)

// actionWeights is the per-user-per-day count for each action kind.
// Source of truth: spec section 4 "daily-heavy" budget.
type actionWeights struct {
	Send            float64
	ReadReceipt     float64
	ScrollHistory   float64
	RefreshRoomList float64
	MemberAdd       float64
	RoomCreate      float64
	MuteToggle      float64
}

func defaultActionWeights() actionWeights {
	return actionWeights{
		Send: 60, ReadReceipt: 25, ScrollHistory: 3,
		RefreshRoomList: 5, MemberAdd: 0.5, RoomCreate: 0.2, MuteToggle: 0.2,
	}
}

func (w actionWeights) totalPerDay() float64 {
	return w.Send + w.ReadReceipt + w.ScrollHistory + w.RefreshRoomList +
		w.MemberAdd + w.RoomCreate + w.MuteToggle
}

// actionRatePerSecond converts a per-day count to a Poisson rate
// (actions per second), scaled to the active fraction of a workday.
func actionRatePerSecond(perDay float64, workday time.Duration) float64 {
	return perDay / workday.Seconds()
}

// pickAction returns one actionKind chosen with probability proportional
// to w. r is the source of randomness.
func pickAction(r *rand.Rand, w actionWeights) actionKind {
	total := w.totalPerDay()
	x := r.Float64() * total
	cumulative := []struct {
		k actionKind
		w float64
	}{
		{actionSend, w.Send},
		{actionReadReceipt, w.ReadReceipt},
		{actionScrollHistory, w.ScrollHistory},
		{actionRefreshRoomList, w.RefreshRoomList},
		{actionMemberAdd, w.MemberAdd},
		{actionRoomCreate, w.RoomCreate},
		{actionMuteToggle, w.MuteToggle},
	}
	var acc float64
	for _, c := range cumulative {
		acc += c.w
		if x < acc {
			return c.k
		}
	}
	return actionSend
}

// userState is the per-user runtime state for a daily-IM simulated user.
type userState struct {
	ID         string
	Account    string
	Rooms      []string
	active     bool
	activeProb float64 // P(stay active | active)
	idleProb   float64 // P(stay idle | idle)
}

func newUserState(id, account string, rooms []string, _seed int64) *userState {
	return &userState{
		ID: id, Account: account, Rooms: rooms,
		active: false,
		// Tuned so stationary active fraction ≈ 25%: P(idle->active)=0.05, P(active->idle)=0.15.
		activeProb: 0.85, idleProb: 0.95,
	}
}

// step advances the Markov chain by one tick. Call at the per-user tick
// interval (e.g. every 1s of simulated time).
func (u *userState) step(r *rand.Rand) {
	x := r.Float64()
	if u.active {
		if x > u.activeProb {
			u.active = false
		}
	} else {
		if x > u.idleProb {
			u.active = true
		}
	}
}
