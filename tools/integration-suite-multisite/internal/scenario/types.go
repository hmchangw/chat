package scenario

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Scenario is one test unit: one input fire + one set of assertions.
// No cases, no base_input/case.input distinction. Variants are
// separate scenario files.
type Scenario struct {
	Name           string                `yaml:"scenario"`
	Source         string                `yaml:"source"`
	Status         string                `yaml:"status,omitempty"`
	Tag            string                `yaml:"tag"` // "positive" | "negative"
	Sites          map[string]SiteBlock  `yaml:"sites"`
	CassandraData  []SeedCassandraTable  `yaml:"cassandra_data,omitempty"`
	MongoData      []SeedMongoCollection `yaml:"mongo_data,omitempty"`
	PreFireScripts []string              `yaml:"pre_fire_scripts,omitempty"`
	Input          TaskList              `yaml:"input"`
	Expected       []Expected            `yaml:"expected"`
	Flow           *FlowExpression       `yaml:"flow,omitempty"`

	// SourcePath is the absolute path of the YAML file this Scenario
	// was loaded from. Populated by LoadFile; not in the YAML itself.
	// Used by runScenario to resolve PreFireScripts paths relative to
	// the scenario file and to set the script working directory.
	SourcePath string `yaml:"-"`
}

// SiteBlock is the per-site seed data — exactly the single-site
// SeedBlock, but nested under sites.<site>.
type SiteBlock struct {
	Seed SiteSeed `yaml:"seed"`
}

// SiteSeed mirrors single-site's SeedBlock minus cassandra_data
// (which moves to scenario top level since Cassandra is shared).
//
// RemoteUsers declares users whose IDENTITY was minted by another
// site's seed.users block but whose user profile must also exist HERE
// (with siteId = home_site) so production code on this site can
// classify them as remote — required for cross-site federation tests.
// The author is explicit about which aliases to project, on which
// site, with which home_site. The engine writes exactly that and
// nothing more.
type SiteSeed struct {
	Users       map[string]SeedUserFlags    `yaml:"users,omitempty"`
	RemoteUsers map[string]SeedRemoteUser   `yaml:"remote_users,omitempty"`
	Rooms       []SeedRoom                  `yaml:"rooms,omitempty"`
	Memberships map[string][]SeedMembership `yaml:"memberships,omitempty"`
}

// SeedRemoteUser declares a remote-user stub. The alias MUST also
// appear in the home_site's seed.users block — that's where the user
// is minted; this block only projects a user-profile document into
// THIS site's `users` collection with siteId = home_site so the
// production data model on THIS site can see the user as remote.
type SeedRemoteUser struct {
	HomeSite string `yaml:"home_site"`
}

// SeedCassandraTable, SeedCassandraRow, SeedUserFlags, SeedRoom,
// RoomType, SeedMembership, role constants — preserved verbatim from
// single-site so the seed engine is identical.

type SeedCassandraTable struct {
	Table string             `yaml:"table"`
	Rows  []SeedCassandraRow `yaml:"rows"`
}

type SeedCassandraRow map[string]any

// SeedMongoCollection is one entry under top-level `mongo_data:`. Lets
// a scenario pre-populate a Mongo collection beyond the three
// `seed.<site>.seed.{users,rooms,memberships}` shapes — typically
// chat-app collections the engine doesn't expose first-class fields
// for (thread_rooms, thread_subscriptions, …).
//
// Site-scoped because Mongo is per-site (unlike shared Cassandra).
// Docs uses plain []map[string]any (NOT a named type) so YAML-decoded
// nested mappings inherit the unnamed map type — avoiding the §2.7
// Gap B class of bug where named map types fall through gocql's
// type switch. The Mongo BSON encoder doesn't have that quirk
// (reflection-based, not exact-type), so this is belt-and-suspenders
// rather than a hard requirement; the value type costs nothing extra.
//
// See sandbox_mongo_data.go for the substitution + insertion
// pipeline. Collection names are validated against the
// sandbox-owned collection list at Setup time — that list IS the
// closed catalog of collections the harness will drop between
// scenarios, so any collection a scenario writes must be in it.
type SeedMongoCollection struct {
	Site       string           `yaml:"site"`
	Collection string           `yaml:"collection"`
	Docs       []map[string]any `yaml:"docs"`
}

type SeedUserFlags map[string]bool

type SeedRoom struct {
	ID        string   `yaml:"id"`
	Name      string   `yaml:"name,omitempty"`
	Type      RoomType `yaml:"type,omitempty"`
	CreatedAt string   `yaml:"created_at,omitempty"`

	// UserCount overrides the auto-derived count when set. The default
	// behavior (UserCount == nil) writes `userCount = len(memberships)`
	// into Mongo's `rooms` doc, which matches what room-worker would
	// have written for a real create-room flow. Scenarios that need to
	// trip the large-room post restriction
	// (message-gatekeeper/handler.go:232-236 reads rooms.userCount via
	// roommetacache.FetchFromMongo) set this explicitly to a value
	// above the configured threshold — the gate keys on the cached
	// metadata value, not on the actual subscription count, so
	// `userCount: 501` with two seeded members is the canonical
	// scenario shape for exercising the cap.
	UserCount *int `yaml:"user_count,omitempty"`
}

type RoomType string

const (
	RoomTypeChannel    RoomType = "channel"
	RoomTypeDM         RoomType = "dm"
	RoomTypeBotDM      RoomType = "botDM"
	RoomTypeDiscussion RoomType = "discussion"
)

type SeedMembership struct {
	Room  string   `yaml:"room"`
	Roles []string `yaml:"roles,omitempty"`
}

func (m *SeedMembership) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value == "" {
			return fmt.Errorf("scenario: empty membership scalar")
		}
		m.Room = node.Value
		m.Roles = nil
		return nil
	case yaml.MappingNode:
		var raw struct {
			Room  string   `yaml:"room"`
			Roles []string `yaml:"roles,omitempty"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("scenario: parse membership object: %w", err)
		}
		m.Room = raw.Room
		m.Roles = raw.Roles
		return nil
	default:
		return fmt.Errorf("scenario: membership must be string or object, got kind=%d", node.Kind)
	}
}

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Task is one verb fire within a scenario. A scenario's `input:` is a
// list of tasks fired in declaration order. `id` is required in the
// list shape (it names the task for ${id.reply.*} substitution and
// `match.task: id` assertion scoping); the legacy single-fire map
// shape decodes to a one-task list with an empty id.
type Task struct {
	ID         string         `yaml:"id"`
	Site       string         `yaml:"site"`
	Verb       string         `yaml:"verb"`
	Subject    string         `yaml:"subject"`
	Payload    map[string]any `yaml:"payload"`
	Credential string         `yaml:"credential,omitempty"`
}

// TaskList is the scenario's `input:`. It accepts EITHER a single
// mapping (legacy single-fire shape) OR a sequence of tasks. The
// dual-shape decode is a direct analog of SeedMembership.UnmarshalYAML
// above — existing single-fire scenarios decode unchanged.
type TaskList []Task

func (tl *TaskList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.MappingNode:
		var t Task
		if err := node.Decode(&t); err != nil {
			return fmt.Errorf("scenario: parse single-fire input: %w", err)
		}
		*tl = TaskList{t}
		return nil
	case yaml.SequenceNode:
		if len(node.Content) == 0 {
			return fmt.Errorf("scenario: input list must have at least one task")
		}
		var tasks []Task
		if err := node.Decode(&tasks); err != nil {
			return fmt.Errorf("scenario: parse input task list: %w", err)
		}
		*tl = TaskList(tasks)
		return nil
	default:
		return fmt.Errorf("scenario: input must be a single fire (map) or a list of tasks, got yaml kind=%d", node.Kind)
	}
}

// Expected is one assertion. ID and Of are present only in flow
// scenarios; legacy scenarios leave both empty.
type Expected struct {
	ID       string         `yaml:"id,omitempty"` // flow shape: addressable id; required when Scenario.Flow is set
	Location string         `yaml:"location"`
	Site     string         `yaml:"site,omitempty"` // required for site-scoped pollers, forbidden for reply/cassandra_select
	Of       string         `yaml:"of,omitempty"`   // flow shape: explicit reply scope; valid only on location: reply
	Args     map[string]any `yaml:"args,omitempty"`
	Match    map[string]any `yaml:"match"`
	Timeout  Duration       `yaml:"timeout,omitempty"`
	Polling  Duration       `yaml:"polling,omitempty"`
	Not      bool           `yaml:"not,omitempty"`
}

// FlowExpression carries the raw ordering string from a scenario's
// `flow:` field. Both the compact form (`a >> b >> [c, d]`) and the
// YAML list form decode into the same normalized compact string in
// Raw; the parser (flow_parse.go) tokenizes Raw.
type FlowExpression struct {
	Raw string
}

func (f *FlowExpression) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		f.Raw = strings.TrimSpace(node.Value)
		if f.Raw == "" {
			return fmt.Errorf("scenario: flow: must not be empty")
		}
		return nil
	case yaml.SequenceNode:
		parts := make([]string, 0, len(node.Content))
		for _, child := range node.Content {
			switch child.Kind {
			case yaml.ScalarNode:
				parts = append(parts, child.Value)
			case yaml.SequenceNode:
				// Parallel group encoded as a nested list.
				inner := make([]string, 0, len(child.Content))
				for _, gc := range child.Content {
					if gc.Kind != yaml.ScalarNode {
						return fmt.Errorf("scenario: flow: parallel group must contain scalar ids, got kind=%d", gc.Kind)
					}
					inner = append(inner, gc.Value)
				}
				parts = append(parts, "["+strings.Join(inner, ", ")+"]")
			default:
				return fmt.Errorf("scenario: flow: list element must be a scalar id or a parallel group, got kind=%d", child.Kind)
			}
		}
		if len(parts) == 0 {
			return fmt.Errorf("scenario: flow: must not be empty")
		}
		f.Raw = strings.Join(parts, " >> ")
		return nil
	default:
		return fmt.Errorf("scenario: flow: must be a string or a list, got yaml kind=%d", node.Kind)
	}
}

// Duration unchanged from single-site.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Value == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("scenario: parse duration %q: %w", value.Value, err)
	}
	*d = Duration(parsed)
	return nil
}
