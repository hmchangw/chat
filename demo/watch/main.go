// Command watch subscribes to every interesting NATS subject on both demo sites
// and streams events to stdout as they happen. Run it in its own terminal while
// you drive requests from demo/cli in another.
//
// Subjects watched (per site):
//
//	chat.user.*.event.subscription.update    — per-user subscription events
//	chat.user.*.event.room.update            — per-user room list events
//	chat.room.*.event.member                 — room-scoped member change fan-out
//	chat.msg.canonical.{site}.>              — system messages (members_added, member_removed)
//	chat.room.canonical.{site}.>             — validated requests hitting the ROOMS stream
//	outbox.{site}.>                          — cross-site outbox events
//
// Usage:
//
//	go run ./demo/watch                     # both sites
//	go run ./demo/watch --site f12          # f12 only
//	go run ./demo/watch --nats-f12 ... --nats-f18 ...
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/subject"
)

type siteConn struct {
	id     string
	url    string
	nc     *nats.Conn
	colour string
}

func main() {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	natsF12 := fs.String("nats-f12", envOr("NATS_F12", "nats://localhost:4222"), "NATS URL for site f12")
	natsF18 := fs.String("nats-f18", envOr("NATS_F18", "nats://localhost:4223"), "NATS URL for site f18")
	site := fs.String("site", "", "only watch a single site (f12 or f18)")
	raw := fs.Bool("raw", false, "print raw payloads instead of indented JSON")
	_ = fs.Parse(os.Args[1:])

	var targets []*siteConn
	if *site == "" || *site == "f12" {
		targets = append(targets, &siteConn{id: "f12", url: *natsF12, colour: "\x1b[36m"}) // cyan
	}
	if *site == "" || *site == "f18" {
		targets = append(targets, &siteConn{id: "f18", url: *natsF18, colour: "\x1b[35m"}) // magenta
	}
	if len(targets) == 0 {
		fmt.Fprintf(os.Stderr, "unknown site %q\n", *site)
		os.Exit(2)
	}

	for _, t := range targets {
		nc, err := nats.Connect(t.url, nats.Name("demo-watch-"+t.id), nats.Timeout(5*time.Second))
		if err != nil {
			fmt.Fprintf(os.Stderr, "connect %s (%s): %v\n", t.id, t.url, err)
			os.Exit(1)
		}
		t.nc = nc
	}

	var mu sync.Mutex
	print := func(site *siteConn, m *nats.Msg) {
		mu.Lock()
		defer mu.Unlock()
		tag := fmt.Sprintf("%s[%s]\x1b[0m", site.colour, site.id)
		ts := time.Now().Format("15:04:05.000")
		fmt.Printf("%s %s  %s\n", tag, ts, m.Subject)
		if *raw {
			fmt.Printf("                %s\n", string(m.Data))
		} else {
			fmt.Println(indentJSON(m.Data, "                "))
		}
	}

	for _, t := range targets {
		patterns := []string{
			"chat.user.*.event.subscription.update",
			"chat.user.*.event.room.update",
			"chat.room.*.event.member",
			subject.MsgCanonicalWildcard(t.id),
			subject.RoomCanonicalWildcard(t.id),
			subject.OutboxWildcard(t.id),
		}
		for _, pat := range patterns {
			t := t
			if _, err := t.nc.Subscribe(pat, func(m *nats.Msg) { print(t, m) }); err != nil {
				fmt.Fprintf(os.Stderr, "subscribe %s on %s: %v\n", pat, t.id, err)
				os.Exit(1)
			}
		}
		if err := t.nc.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "flush %s: %v\n", t.id, err)
			os.Exit(1)
		}
		fmt.Printf("%s[%s]\x1b[0m watching %s\n", t.colour, t.id, t.url)
		for _, pat := range patterns {
			fmt.Printf("          %s\n", pat)
		}
	}
	fmt.Println()
	fmt.Println("waiting for events — Ctrl-C to stop")

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM)
	<-sigc

	fmt.Println("\ndraining")
	for _, t := range targets {
		_ = t.nc.Drain()
	}
}

func indentJSON(data []byte, prefix string) string {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return prefix + string(data)
	}
	out, err := json.MarshalIndent(v, prefix, "  ")
	if err != nil {
		return prefix + string(data)
	}
	return prefix + string(out)
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
