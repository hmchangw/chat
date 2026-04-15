// Command cli is an interactive request driver for the cross-site room-service demo.
//
// It connects to the NATS cluster of a specified site (f12 or f18) and issues
// NATS request/reply to room-service. Each subcommand prints the outbound
// subject, payload, and the reply from room-service so you can verify what the
// service validated and accepted.
//
// All destructive state changes go through room-service (and then room-worker
// over the ROOMS stream). The only exception is `seed`, which writes users
// directly into MongoDB because there is no user-service in the chat system —
// users come from an external HR source in production.
//
// Usage:
//
//	go run ./demo/cli seed
//	go run ./demo/cli room create --site f12 --name general --owner alice
//	go run ./demo/cli room list   --site f12 --requester alice
//	go run ./demo/cli room get    --site f12 --requester alice --room <id>
//	go run ./demo/cli member add    --site f12 --requester alice --room <id> --users bob,carol
//	go run ./demo/cli member add    --site f12 --requester alice --room <id> --orgs eng
//	go run ./demo/cli member remove --site f12 --requester alice --room <id> --account bob
//	go run ./demo/cli member remove --site f12 --requester alice --room <id> --org eng
//	go run ./demo/cli role          --site f12 --requester alice --room <id> --account bob --to owner
//	go run ./demo/cli snapshot --site f12
//	go run ./demo/cli snapshot --all
//	go run ./demo/cli reset    --all
//
// Global flags:
//
//	--nats-f12  default: nats://localhost:4222
//	--nats-f18  default: nats://localhost:4223
//	--mongo-uri default: mongodb://localhost:27017
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/subject"
)

// ---- Site directory --------------------------------------------------------

type siteDef struct {
	ID       string
	NATSURL  string
	MongoDB  string
	Defaults struct{ NATS string }
}

var sites = map[string]*siteDef{
	"f12": {ID: "f12", MongoDB: "chat_f12"},
	"f18": {ID: "f18", MongoDB: "chat_f18"},
}

type globalFlags struct {
	natsF12  string
	natsF18  string
	mongoURI string
}

func (g *globalFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&g.natsF12, "nats-f12", envOr("NATS_F12", "nats://localhost:4222"), "NATS URL for site f12")
	fs.StringVar(&g.natsF18, "nats-f18", envOr("NATS_F18", "nats://localhost:4223"), "NATS URL for site f18")
	fs.StringVar(&g.mongoURI, "mongo-uri", envOr("MONGO_URI", "mongodb://localhost:27017"), "MongoDB URI (shared across sites)")
}

func (g *globalFlags) siteNATS(site string) (string, error) {
	switch site {
	case "f12":
		return g.natsF12, nil
	case "f18":
		return g.natsF18, nil
	default:
		return "", fmt.Errorf("unknown site %q (want f12 or f18)", site)
	}
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// ---- Dispatch --------------------------------------------------------------

func main() {
	if len(os.Args) < 2 {
		usageAndExit()
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "seed":
		err = runSeed(args)
	case "seed-room":
		err = runSeedRoom(args)
	case "room":
		err = runRoom(args)
	case "member":
		err = runMember(args)
	case "role":
		err = runRole(args)
	case "snapshot":
		err = runSnapshot(args)
	case "reset":
		err = runReset(args)
	case "-h", "--help", "help":
		usageAndExit()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		usageAndExit()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usageAndExit() {
	fmt.Fprint(os.Stderr, `demo cli — cross-site room-service request driver

Commands:
  seed                              Seed users AND a fixture room into each site (writes Mongo directly)
  seed-room                         Insert an additional dummy room + owner subscription into Mongo
  room create                       Create a room via room-service (subject: chat.user.{owner}.request.rooms.create)
  room list                         List rooms (subject: chat.user.{requester}.request.rooms.list)
  room get                          Get a room (subject: chat.user.{requester}.request.rooms.get.{id})
  member add                        Add members / orgs / channels to a room
  member remove                     Remove a member or an org from a room
  role                              Promote or demote a member (to owner | member)
  snapshot [--site f12|f18|--all]   Dump current rooms / subscriptions / room_members
  reset    [--site f12|f18|--all]   Drop demo collections (users, rooms, subscriptions, room_members)

Tip: after 'seed' you can immediately drive member.add / remove / role on the
fixture rooms 'room-f12-general' (owner alice) and 'room-f18-general' (owner bob)
without needing 'room create' to succeed.

Run any command with -h for subcommand flags.
`)
	os.Exit(2)
}

// ---- seed ------------------------------------------------------------------

// Seed fixture — small enough to follow, large enough to exercise every path.
type userSeed struct {
	Account string
	Site    string
	Org     string
	Eng     string
}

var seedFixture = []userSeed{
	// Site f12 ---------------------------------------------------------------
	{Account: "alice", Site: "f12", Org: "eng", Eng: "Alice"},
	{Account: "carol", Site: "f12", Org: "eng", Eng: "Carol"},
	{Account: "dave", Site: "f12", Org: "eng", Eng: "Dave"},
	{Account: "mallory", Site: "f12", Org: "sales", Eng: "Mallory"},
	{Account: "eve.bot", Site: "f12", Org: "eng", Eng: "Eve Bot"},
	{Account: "p_system", Site: "f12", Org: "eng", Eng: "System Bot"},
	// Site f18 (the "cross-site" users) --------------------------------------
	{Account: "bob", Site: "f18", Org: "eng", Eng: "Bob"},
	{Account: "frank", Site: "f18", Org: "sales", Eng: "Frank"},
}

// fixtureRoom defines a room that `seed` drops directly into a site's Mongo so
// that member.add / remove / role can be exercised immediately without
// depending on `room create` going through room-service.
type fixtureRoom struct {
	ID    string
	Name  string
	Site  string
	Owner string // account
}

var seedFixtureRooms = []fixtureRoom{
	{ID: "room-f12-general", Name: "general", Site: "f12", Owner: "alice"},
	{ID: "room-f18-general", Name: "general", Site: "f18", Owner: "bob"},
}

func runSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	var g globalFlags
	g.bind(fs)
	_ = fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mc, err := mongoutil.Connect(ctx, g.mongoURI)
	if err != nil {
		return err
	}
	defer mongoutil.Disconnect(ctx, mc)

	// Bucket users by home site.
	bySite := map[string][]interface{}{}
	for _, u := range seedFixture {
		user := model.User{
			ID:      uuid.NewSHA1(uuid.NameSpaceOID, []byte(u.Account)).String(),
			Account: u.Account,
			SiteID:  u.Site,
			SectID:  u.Org,
			EngName: u.Eng,
		}
		bySite[u.Site] = append(bySite[u.Site], user)
	}

	// IMPORTANT: users are replicated into EVERY site's database so that
	// room-worker and room-service on any site can look up any account. This
	// models the HR feed that pushes the full user directory into every site.
	for _, site := range sites {
		db := mc.Database(site.MongoDB)
		if _, err := db.Collection("users").DeleteMany(ctx, bson.M{}); err != nil {
			return fmt.Errorf("clear users in %s: %w", site.MongoDB, err)
		}
		var all []interface{}
		for _, docs := range bySite {
			all = append(all, docs...)
		}
		if _, err := db.Collection("users").InsertMany(ctx, all); err != nil {
			return fmt.Errorf("seed users in %s: %w", site.MongoDB, err)
		}
		fmt.Printf("seeded %d users into %s\n", len(all), site.MongoDB)
	}

	fmt.Println()
	fmt.Println("Users seeded:")
	for _, u := range seedFixture {
		tag := u.Site
		if isBot(u.Account) {
			tag += " [BOT]"
		}
		fmt.Printf("  %-10s site=%-4s org=%-5s  %s\n", u.Account, tag, u.Org, u.Eng)
	}

	// Drop a fixture room + owner subscription into each site so the user can
	// run member.add / remove / role immediately. We wipe `rooms`,
	// `subscriptions`, `room_members` first so the seed is idempotent.
	for _, site := range sites {
		db := mc.Database(site.MongoDB)
		for _, coll := range []string{"rooms", "subscriptions", "room_members"} {
			if _, err := db.Collection(coll).DeleteMany(ctx, bson.M{}); err != nil {
				return fmt.Errorf("clear %s/%s: %w", site.MongoDB, coll, err)
			}
		}
	}
	fmt.Println()
	fmt.Println("Fixture rooms seeded:")
	for i := range seedFixtureRooms {
		fr := seedFixtureRooms[i]
		if err := insertFixtureRoom(ctx, mc, fr); err != nil {
			return fmt.Errorf("insert fixture room %s: %w", fr.ID, err)
		}
		fmt.Printf("  %-20s site=%-4s owner=%s\n", fr.ID, fr.Site, fr.Owner)
	}
	fmt.Println()
	fmt.Println("Try it:")
	fmt.Println("  go run ./demo/cli member add    --site f12 --requester alice --room room-f12-general --users carol,dave")
	fmt.Println("  go run ./demo/cli member add    --site f12 --requester alice --room room-f12-general --users bob")
	fmt.Println("  go run ./demo/cli role          --site f12 --requester alice --room room-f12-general --account carol --to owner")
	fmt.Println("  go run ./demo/cli member remove --site f12 --requester alice --room room-f12-general --account dave")
	return nil
}

// insertFixtureRoom writes a Room and its owner Subscription directly into the
// site's MongoDB. It bypasses room-service entirely, so it works even if the
// NATS request/reply path is misbehaving.
func insertFixtureRoom(ctx context.Context, mc *mongo.Client, fr fixtureRoom) error {
	site, ok := sites[fr.Site]
	if !ok {
		return fmt.Errorf("unknown site %q", fr.Site)
	}
	db := mc.Database(site.MongoDB)
	now := time.Now().UTC()

	ownerID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fr.Owner)).String()

	room := model.Room{
		ID: fr.ID, Name: fr.Name, Type: model.RoomTypeGroup,
		CreatedBy: ownerID, SiteID: fr.Site, UserCount: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := db.Collection("rooms").InsertOne(ctx, room); err != nil {
		return fmt.Errorf("insert room: %w", err)
	}

	sub := model.Subscription{
		ID:                 uuid.New().String(),
		User:               model.SubscriptionUser{ID: ownerID, Account: fr.Owner},
		RoomID:             fr.ID,
		SiteID:             fr.Site,
		Roles:              []model.Role{model.RoleOwner},
		HistorySharedSince: &now,
		JoinedAt:           now,
	}
	if _, err := db.Collection("subscriptions").InsertOne(ctx, sub); err != nil {
		return fmt.Errorf("insert owner subscription: %w", err)
	}
	return nil
}

// runSeedRoom inserts an additional ad-hoc room directly into Mongo. Useful
// when you want a clean room for a specific scenario without re-running the
// full seed.
func runSeedRoom(args []string) error {
	fs := flag.NewFlagSet("seed-room", flag.ExitOnError)
	var g globalFlags
	g.bind(fs)
	site := fs.String("site", "", "site id (f12 or f18)")
	id := fs.String("id", "", "room id (default: room-<site>-<name>)")
	name := fs.String("name", "", "room name (required)")
	owner := fs.String("owner", "", "owner account (required, must already exist in users)")
	members := fs.String("members", "", "comma-separated additional member accounts")
	_ = fs.Parse(args)

	if *site == "" || *name == "" || *owner == "" {
		return errors.New("seed-room: --site, --name, --owner are required")
	}
	if _, ok := sites[*site]; !ok {
		return fmt.Errorf("unknown site %q", *site)
	}
	if *id == "" {
		*id = fmt.Sprintf("room-%s-%s", *site, *name)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mc, err := mongoutil.Connect(ctx, g.mongoURI)
	if err != nil {
		return err
	}
	defer mongoutil.Disconnect(ctx, mc)

	if err := insertFixtureRoom(ctx, mc, fixtureRoom{
		ID: *id, Name: *name, Site: *site, Owner: *owner,
	}); err != nil {
		return err
	}
	fmt.Printf("inserted room %s (site=%s, owner=%s)\n", *id, *site, *owner)

	// Optional extra members go straight in as `member` subscriptions.
	for _, account := range splitCSV(*members) {
		now := time.Now().UTC()
		userID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(account)).String()
		sub := model.Subscription{
			ID:       uuid.New().String(),
			User:     model.SubscriptionUser{ID: userID, Account: account},
			RoomID:   *id,
			SiteID:   *site,
			Roles:    []model.Role{model.RoleMember},
			JoinedAt: now,
		}
		db := mc.Database(sites[*site].MongoDB)
		if _, err := db.Collection("subscriptions").InsertOne(ctx, sub); err != nil {
			return fmt.Errorf("insert member %s: %w", account, err)
		}
		if _, err := db.Collection("rooms").UpdateByID(ctx, *id, bson.M{"$inc": bson.M{"userCount": 1}}); err != nil {
			return fmt.Errorf("increment userCount: %w", err)
		}
		fmt.Printf("  added member %s\n", account)
	}
	return nil
}

func isBot(account string) bool {
	return strings.HasSuffix(account, ".bot") || strings.HasPrefix(account, "p_")
}

// ---- room ------------------------------------------------------------------

func runRoom(args []string) error {
	if len(args) < 1 {
		return errors.New("room: expected subcommand create|list|get")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return runRoomCreate(rest)
	case "list":
		return runRoomList(rest)
	case "get":
		return runRoomGet(rest)
	default:
		return fmt.Errorf("room: unknown subcommand %q", sub)
	}
}

func runRoomCreate(args []string) error {
	fs := flag.NewFlagSet("room create", flag.ExitOnError)
	var g globalFlags
	g.bind(fs)
	site := fs.String("site", "", "site id (f12 or f18)")
	name := fs.String("name", "", "room name")
	owner := fs.String("owner", "", "owner account (must exist in users collection)")
	roomType := fs.String("type", "group", "room type: group or dm")
	_ = fs.Parse(args)

	if *site == "" || *name == "" || *owner == "" {
		return errors.New("room create: --site, --name, --owner are required")
	}
	natsURL, err := g.siteNATS(*site)
	if err != nil {
		return err
	}

	// room-service looks up the user by ID and account, so the caller must
	// match the seeded fixture.
	ownerID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(*owner)).String()

	req := model.CreateRoomRequest{
		Name:             *name,
		Type:             model.RoomType(*roomType),
		CreatedBy:        ownerID,
		CreatedByAccount: *owner,
		SiteID:           *site,
	}

	subj := subject.RoomsCreate(*owner)
	reply, err := doRequest(natsURL, subj, req)
	if err != nil {
		return err
	}

	// Reply is either a Room or an ErrorResponse. doRequest already turned
	// error replies into an error; we only need to print the created room for
	// the happy path.
	var room model.Room
	if err := json.Unmarshal(reply, &room); err != nil {
		return fmt.Errorf("decode room reply: %w", err)
	}
	fmt.Println()
	fmt.Printf("→ room created on %s: id=%s name=%q\n", *site, room.ID, room.Name)
	return nil
}

func runRoomList(args []string) error {
	fs := flag.NewFlagSet("room list", flag.ExitOnError)
	var g globalFlags
	g.bind(fs)
	site := fs.String("site", "", "site id (f12 or f18)")
	requester := fs.String("requester", "", "requester account")
	_ = fs.Parse(args)

	if *site == "" || *requester == "" {
		return errors.New("room list: --site and --requester are required")
	}
	natsURL, err := g.siteNATS(*site)
	if err != nil {
		return err
	}
	subj := subject.RoomsList(*requester)
	_, err = doRequest(natsURL, subj, map[string]string{})
	return err
}

func runRoomGet(args []string) error {
	fs := flag.NewFlagSet("room get", flag.ExitOnError)
	var g globalFlags
	g.bind(fs)
	site := fs.String("site", "", "site id (f12 or f18)")
	requester := fs.String("requester", "", "requester account")
	roomID := fs.String("room", "", "room id")
	_ = fs.Parse(args)

	if *site == "" || *requester == "" || *roomID == "" {
		return errors.New("room get: --site, --requester, --room are required")
	}
	natsURL, err := g.siteNATS(*site)
	if err != nil {
		return err
	}
	subj := subject.RoomsGet(*requester, *roomID)
	_, err = doRequest(natsURL, subj, map[string]string{})
	return err
}

// ---- member ----------------------------------------------------------------

func runMember(args []string) error {
	if len(args) < 1 {
		return errors.New("member: expected subcommand add|remove")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "add":
		return runMemberAdd(rest)
	case "remove":
		return runMemberRemove(rest)
	default:
		return fmt.Errorf("member: unknown subcommand %q", sub)
	}
}

func runMemberAdd(args []string) error {
	fs := flag.NewFlagSet("member add", flag.ExitOnError)
	var g globalFlags
	g.bind(fs)
	site := fs.String("site", "", "site id of the ROOM (f12 or f18)")
	requester := fs.String("requester", "", "requester account (must already be a member)")
	roomID := fs.String("room", "", "room id")
	users := fs.String("users", "", "comma-separated user accounts")
	orgs := fs.String("orgs", "", "comma-separated org ids (resolved via users.sectId)")
	channels := fs.String("channels", "", "comma-separated channel room ids to copy members from")
	history := fs.String("history", "none", "history mode: none | all")
	_ = fs.Parse(args)

	if *site == "" || *requester == "" || *roomID == "" {
		return errors.New("member add: --site, --requester, --room are required")
	}
	if *users == "" && *orgs == "" && *channels == "" {
		return errors.New("member add: at least one of --users, --orgs, --channels must be set")
	}
	natsURL, err := g.siteNATS(*site)
	if err != nil {
		return err
	}

	req := model.AddMembersRequest{
		RoomID:   *roomID,
		Users:    splitCSV(*users),
		Orgs:     splitCSV(*orgs),
		Channels: splitCSV(*channels),
		History:  model.HistoryConfig{Mode: model.HistoryMode(*history)},
	}

	subj := subject.MemberAdd(*requester, *roomID, *site)
	_, err = doRequest(natsURL, subj, req)
	return err
}

func runMemberRemove(args []string) error {
	fs := flag.NewFlagSet("member remove", flag.ExitOnError)
	var g globalFlags
	g.bind(fs)
	site := fs.String("site", "", "site id of the ROOM (f12 or f18)")
	requester := fs.String("requester", "", "requester account")
	roomID := fs.String("room", "", "room id")
	account := fs.String("account", "", "account to remove (mutually exclusive with --org)")
	org := fs.String("org", "", "org id to remove (mutually exclusive with --account)")
	_ = fs.Parse(args)

	if *site == "" || *requester == "" || *roomID == "" {
		return errors.New("member remove: --site, --requester, --room are required")
	}
	if (*account == "" && *org == "") || (*account != "" && *org != "") {
		return errors.New("member remove: exactly one of --account or --org is required")
	}
	natsURL, err := g.siteNATS(*site)
	if err != nil {
		return err
	}

	req := model.RemoveMemberRequest{
		RoomID:  *roomID,
		Account: *account,
		OrgID:   *org,
	}
	subj := subject.MemberRemove(*requester, *roomID, *site)
	_, err = doRequest(natsURL, subj, req)
	return err
}

// ---- role ------------------------------------------------------------------

func runRole(args []string) error {
	fs := flag.NewFlagSet("role", flag.ExitOnError)
	var g globalFlags
	g.bind(fs)
	site := fs.String("site", "", "site id of the ROOM (f12 or f18)")
	requester := fs.String("requester", "", "requester account (must be owner)")
	roomID := fs.String("room", "", "room id")
	account := fs.String("account", "", "target member account")
	to := fs.String("to", "", "new role: owner or member")
	_ = fs.Parse(args)

	if *site == "" || *requester == "" || *roomID == "" || *account == "" || *to == "" {
		return errors.New("role: --site, --requester, --room, --account, --to are required")
	}
	natsURL, err := g.siteNATS(*site)
	if err != nil {
		return err
	}

	req := model.UpdateRoleRequest{
		RoomID:  *roomID,
		Account: *account,
		NewRole: model.Role(*to),
	}
	subj := subject.MemberRoleUpdate(*requester, *roomID, *site)
	_, err = doRequest(natsURL, subj, req)
	return err
}

// ---- snapshot / reset ------------------------------------------------------

func runSnapshot(args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	var g globalFlags
	g.bind(fs)
	site := fs.String("site", "", "site id (f12 or f18); omit with --all for both")
	all := fs.Bool("all", false, "snapshot both sites")
	_ = fs.Parse(args)

	if !*all && *site == "" {
		return errors.New("snapshot: --site or --all is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mc, err := mongoutil.Connect(ctx, g.mongoURI)
	if err != nil {
		return err
	}
	defer mongoutil.Disconnect(ctx, mc)

	target := []string{*site}
	if *all {
		target = []string{"f12", "f18"}
	}
	for _, s := range target {
		snapshotSite(ctx, mc, sites[s])
	}
	return nil
}

func snapshotSite(ctx context.Context, mc *mongo.Client, s *siteDef) {
	fmt.Println()
	fmt.Printf("══ %s (%s) %s\n", s.ID, s.MongoDB, strings.Repeat("═", max(0, 70-len(s.ID)-len(s.MongoDB))))
	db := mc.Database(s.MongoDB)

	// rooms
	rCur, err := db.Collection("rooms").Find(ctx, bson.M{})
	if err != nil {
		fmt.Printf("list rooms: %v\n", err)
		return
	}
	var rooms []model.Room
	_ = rCur.All(ctx, &rooms)
	fmt.Printf("rooms (%d):\n", len(rooms))
	for i := range rooms {
		fmt.Printf("  - %s  name=%q  type=%s  userCount=%d  siteId=%s\n",
			rooms[i].ID, rooms[i].Name, rooms[i].Type, rooms[i].UserCount, rooms[i].SiteID)
	}

	// subscriptions
	sCur, err := db.Collection("subscriptions").Find(ctx, bson.M{})
	if err != nil {
		fmt.Printf("list subscriptions: %v\n", err)
		return
	}
	var subs []model.Subscription
	_ = sCur.All(ctx, &subs)
	fmt.Printf("subscriptions (%d):\n", len(subs))
	for i := range subs {
		fmt.Printf("  - roomId=%s  account=%-10s roles=%v  siteId=%s\n",
			subs[i].RoomID, subs[i].User.Account, subs[i].Roles, subs[i].SiteID)
	}

	// room_members
	mCur, err := db.Collection("room_members").Find(ctx, bson.M{})
	if err != nil {
		fmt.Printf("list room_members: %v\n", err)
		return
	}
	var members []model.RoomMember
	_ = mCur.All(ctx, &members)
	fmt.Printf("room_members (%d):\n", len(members))
	for i := range members {
		fmt.Printf("  - roomId=%s  type=%-10s id=%s  account=%s\n",
			members[i].RoomID, members[i].Member.Type, members[i].Member.ID, members[i].Member.Account)
	}
}

func runReset(args []string) error {
	fs := flag.NewFlagSet("reset", flag.ExitOnError)
	var g globalFlags
	g.bind(fs)
	site := fs.String("site", "", "site id (f12 or f18); omit with --all for both")
	all := fs.Bool("all", false, "reset both sites")
	_ = fs.Parse(args)

	if !*all && *site == "" {
		return errors.New("reset: --site or --all is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	mc, err := mongoutil.Connect(ctx, g.mongoURI)
	if err != nil {
		return err
	}
	defer mongoutil.Disconnect(ctx, mc)

	target := []string{*site}
	if *all {
		target = []string{"f12", "f18"}
	}
	for _, s := range target {
		db := mc.Database(sites[s].MongoDB)
		for _, c := range []string{"users", "rooms", "subscriptions", "room_members"} {
			res, err := db.Collection(c).DeleteMany(ctx, bson.M{})
			if err != nil {
				return fmt.Errorf("clear %s/%s: %w", sites[s].MongoDB, c, err)
			}
			fmt.Printf("cleared %s.%s  (%d docs)\n", sites[s].MongoDB, c, res.DeletedCount)
		}
	}
	return nil
}

// ---- NATS request helper ---------------------------------------------------

func doRequest(natsURL, subj string, payload interface{}) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	fmt.Printf("→ NATS %s\n", natsURL)
	fmt.Printf("  subject  %s\n", subj)
	fmt.Printf("  payload  %s\n", string(data))

	nc, err := nats.Connect(natsURL, nats.Name("demo-cli"), nats.Timeout(5*time.Second))
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	defer func() { _ = nc.Drain() }()

	msg, err := nc.Request(subj, data, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}

	// Try to detect an error response.
	var errResp model.ErrorResponse
	if json.Unmarshal(msg.Data, &errResp) == nil && errResp.Error != "" {
		fmt.Printf("← reply    %s\n", string(msg.Data))
		return nil, fmt.Errorf("room-service rejected: %s", errResp.Error)
	}

	fmt.Printf("← reply    %s\n", prettyJSON(msg.Data))
	return msg.Data, nil
}

func prettyJSON(data []byte) string {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	out, err := json.MarshalIndent(v, "           ", "  ")
	if err != nil {
		return string(data)
	}
	return string(out)
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
