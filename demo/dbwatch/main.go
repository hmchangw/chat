// Command dbwatch polls both demo sites' MongoDB collections every few seconds
// and prints a compact snapshot whenever anything changes. Run it in its own
// terminal while you drive requests from demo/cli.
//
// It never writes — pure observer. The snapshot shows rooms, subscriptions, and
// room_members per site. Hashing the marshalled snapshot is how it decides
// whether state moved between ticks, so it stays quiet when nothing changed.
//
// Usage:
//
//	go run ./demo/dbwatch
//	go run ./demo/dbwatch --interval 1s
//	go run ./demo/dbwatch --site f12
package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
)

type siteDef struct {
	ID      string
	MongoDB string
	Colour  string
}

var allSites = []siteDef{
	{ID: "f12", MongoDB: "chat_f12", Colour: "\x1b[36m"}, // cyan
	{ID: "f18", MongoDB: "chat_f18", Colour: "\x1b[35m"}, // magenta
}

func main() {
	fs := flag.NewFlagSet("dbwatch", flag.ExitOnError)
	mongoURI := fs.String("mongo-uri", envOr("MONGO_URI", "mongodb://localhost:27017"), "MongoDB URI")
	interval := fs.Duration("interval", 2*time.Second, "poll interval")
	site := fs.String("site", "", "only watch a single site (f12 or f18)")
	_ = fs.Parse(os.Args[1:])

	var targets []siteDef
	for _, s := range allSites {
		if *site == "" || *site == s.ID {
			targets = append(targets, s)
		}
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "unknown site %q\n", *site)
		os.Exit(2)
	}

	if err := runLoop(targets, *mongoURI, *interval); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runLoop(targets []siteDef, mongoURI string, interval time.Duration) error {
	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mc, err := mongoutil.Connect(rootCtx, mongoURI)
	if err != nil {
		return fmt.Errorf("mongo connect: %w", err)
	}
	defer mongoutil.Disconnect(rootCtx, mc)

	fmt.Printf("dbwatch polling every %s on %s\n", interval.String(), mongoURI)
	for _, t := range targets {
		fmt.Printf("%s[%s]\x1b[0m %s\n", t.Colour, t.ID, t.MongoDB)
	}
	fmt.Println("Ctrl-C to stop")
	fmt.Println()

	// Initial render.
	hashes := make(map[string]string, len(targets))
	for _, t := range targets {
		snap, h := snapshot(rootCtx, mc, t)
		hashes[t.ID] = h
		printSnap(t, &snap, "initial")
	}

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-sigc:
			return nil
		case <-ticker.C:
			for _, t := range targets {
				snap, h := snapshot(rootCtx, mc, t)
				if h != hashes[t.ID] {
					hashes[t.ID] = h
					printSnap(t, &snap, "changed")
				}
			}
		}
	}
}

type siteSnapshot struct {
	Rooms       []model.Room
	Subs        []model.Subscription
	RoomMembers []model.RoomMember
	Users       int // count only — user seed rarely changes
	TakenAt     time.Time
}

func snapshot(ctx context.Context, mc *mongo.Client, s siteDef) (siteSnapshot, string) {
	db := mc.Database(s.MongoDB)
	var snap siteSnapshot
	snap.TakenAt = time.Now()

	if cur, err := db.Collection("rooms").Find(ctx, bson.M{}); err == nil {
		_ = cur.All(ctx, &snap.Rooms)
	}
	if cur, err := db.Collection("subscriptions").Find(ctx, bson.M{}); err == nil {
		_ = cur.All(ctx, &snap.Subs)
	}
	if cur, err := db.Collection("room_members").Find(ctx, bson.M{}); err == nil {
		_ = cur.All(ctx, &snap.RoomMembers)
	}
	if n, err := db.Collection("users").CountDocuments(ctx, bson.M{}); err == nil {
		snap.Users = int(n)
	}

	// Hash a stable representation to detect change.
	h := sha1.New()
	for i := range snap.Rooms {
		fmt.Fprintf(h, "R|%s|%s|%d\n", snap.Rooms[i].ID, snap.Rooms[i].Name, snap.Rooms[i].UserCount)
	}
	for i := range snap.Subs {
		fmt.Fprintf(h, "S|%s|%s|%v|%s\n", snap.Subs[i].RoomID, snap.Subs[i].User.Account, snap.Subs[i].Roles, snap.Subs[i].SiteID)
	}
	for i := range snap.RoomMembers {
		fmt.Fprintf(h, "M|%s|%s|%s|%s\n", snap.RoomMembers[i].RoomID, snap.RoomMembers[i].Member.Type, snap.RoomMembers[i].Member.ID, snap.RoomMembers[i].Member.Account)
	}
	fmt.Fprintf(h, "U|%d\n", snap.Users)

	return snap, hex.EncodeToString(h.Sum(nil))
}

func printSnap(s siteDef, snap *siteSnapshot, reason string) {
	ts := snap.TakenAt.Format("15:04:05.000")
	banner := fmt.Sprintf("%s[%s]\x1b[0m %s (%s)", s.Colour, s.ID, ts, reason)
	fmt.Println()
	fmt.Println(banner + " " + strings.Repeat("─", max(0, 78-len(banner))))
	fmt.Printf("  users          %d\n", snap.Users)
	fmt.Printf("  rooms          %d\n", len(snap.Rooms))
	for i := range snap.Rooms {
		fmt.Printf("    - %s  name=%q  userCount=%d  siteId=%s\n",
			snap.Rooms[i].ID, snap.Rooms[i].Name, snap.Rooms[i].UserCount, snap.Rooms[i].SiteID)
	}
	fmt.Printf("  subscriptions  %d\n", len(snap.Subs))
	for i := range snap.Subs {
		fmt.Printf("    - room=%s  account=%-10s roles=%v  siteId=%s\n",
			short(snap.Subs[i].RoomID), snap.Subs[i].User.Account, snap.Subs[i].Roles, snap.Subs[i].SiteID)
	}
	fmt.Printf("  room_members   %d\n", len(snap.RoomMembers))
	for i := range snap.RoomMembers {
		fmt.Printf("    - room=%s  type=%-10s id=%s  account=%s\n",
			short(snap.RoomMembers[i].RoomID), snap.RoomMembers[i].Member.Type, snap.RoomMembers[i].Member.ID, snap.RoomMembers[i].Member.Account)
	}
}

func short(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
