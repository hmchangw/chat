package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mustWriteFile is a t-helper that writes a YAML fixture into a
// temp dir. Keeps the cross_check tests' boilerplate short.
func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// TestCrossScenarioCheck_CleanSet exercises the "no conflicts"
// happy path. Two scenarios with disjoint cache keys → zero errors.
func TestCrossScenarioCheck_CleanSet(t *testing.T) {
	scs := []*Scenario{
		{
			Name:       "scenario-a",
			SourcePath: "scenario-a.yaml",
			Sites: map[string]SiteBlock{
				"site-a": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"alice": {"verified": true}},
					Memberships: map[string][]SeedMembership{
						"alice": {{Room: "r-a", Roles: []string{"member"}}},
					},
				}},
			},
		},
		{
			Name:       "scenario-b",
			SourcePath: "scenario-b.yaml",
			Sites: map[string]SiteBlock{
				"site-a": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"alice": {"verified": true}},
					Memberships: map[string][]SeedMembership{
						"alice": {{Room: "r-b", Roles: []string{"owner"}}},
					},
				}},
			},
		},
	}
	assert.Empty(t, CrossScenarioCheck(scs))
}

// TestCrossScenarioCheck_AliasRoomRoleConflict — the cycle report's
// canonical F-009 shape: two scenarios both put alice in r-busy but
// with different roles. The first scenario caches the
// gatekeeper's (alice@r-busy=[member]) projection; the second
// reads it instead of its own seeded [owner].
func TestCrossScenarioCheck_AliasRoomRoleConflict(t *testing.T) {
	scs := []*Scenario{
		{
			Name:       "large-room-member-blocked",
			SourcePath: "large-room-member-blocked.yaml",
			Sites: map[string]SiteBlock{
				"site-a": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"alice": {"verified": true}},
					Memberships: map[string][]SeedMembership{
						"alice": {{Room: "r-busy", Roles: []string{"member"}}},
					},
				}},
			},
		},
		{
			Name:       "large-room-owner-bypass",
			SourcePath: "large-room-owner-bypass.yaml",
			Sites: map[string]SiteBlock{
				"site-a": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"alice": {"verified": true}},
					Memberships: map[string][]SeedMembership{
						"alice": {{Room: "r-busy", Roles: []string{"owner"}}},
					},
				}},
			},
		},
	}

	errs := CrossScenarioCheck(scs)
	require.Len(t, errs, 1)
	msg := errs[0].Error()
	assert.Contains(t, msg, `alias="alice"`)
	assert.Contains(t, msg, `room="r-busy"`)
	assert.Contains(t, msg, "roles=member")
	assert.Contains(t, msg, "roles=owner")
	assert.Contains(t, msg, "large-room-member-blocked.yaml")
	assert.Contains(t, msg, "large-room-owner-bypass.yaml")
	assert.Contains(t, msg, "F-009", "error message must cite the finding")
}

// TestCrossScenarioCheck_AliasHomeSiteConflict — same alias declared
// as local on different sites in two scenarios. Trips the worker
// user-cache.
func TestCrossScenarioCheck_AliasHomeSiteConflict(t *testing.T) {
	scs := []*Scenario{
		{
			Name:       "scenario-x",
			SourcePath: "scenario-x.yaml",
			Sites: map[string]SiteBlock{
				"site-a": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"bob": {"verified": true}},
				}},
			},
		},
		{
			Name:       "scenario-y",
			SourcePath: "scenario-y.yaml",
			Sites: map[string]SiteBlock{
				"site-b": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"bob": {"verified": true}},
				}},
			},
		},
	}

	errs := CrossScenarioCheck(scs)
	require.Len(t, errs, 1)
	msg := errs[0].Error()
	assert.Contains(t, msg, `alias "bob"`)
	assert.Contains(t, msg, "home_site=site-a")
	assert.Contains(t, msg, "home_site=site-b")
	assert.Contains(t, msg, "F-009")
}

// TestCrossScenarioCheck_LegitimateRemoteUsersPattern verifies the
// false positive that earlier iterations of this check tripped on:
// the same alias declared in BOTH seed.users on the home site AND
// in remote_users on the peer site (with home_site = the home).
// Both projections resolve to the same home site → no conflict.
func TestCrossScenarioCheck_LegitimateRemoteUsersPattern(t *testing.T) {
	scs := []*Scenario{
		{
			Name:       "thread-first-reply-remote-parent",
			SourcePath: "thread-first-reply-remote-parent.yaml",
			Sites: map[string]SiteBlock{
				"site-a": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"alice": {"verified": true}},
					RemoteUsers: map[string]SeedRemoteUser{
						"remotebob": {HomeSite: "site-b"},
					},
				}},
				"site-b": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"remotebob": {"verified": true}},
				}},
			},
		},
	}
	assert.Empty(t, CrossScenarioCheck(scs),
		"remotebob in seed.users (site-b) + remote_users (site-a, home_site=site-b) is the legitimate pattern — both resolve to site-b")
}

// TestCrossScenarioCheck_MultipleConflictsReportedDeterministic — two
// distinct conflicts should both surface in a stable order so the
// operator's error-list diff stays clean across runs.
func TestCrossScenarioCheck_MultipleConflictsReportedDeterministic(t *testing.T) {
	scs := []*Scenario{
		{
			Name:       "scenario-a",
			SourcePath: "scenario-a.yaml",
			Sites: map[string]SiteBlock{
				"site-a": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{
						"alice": {"verified": true},
						"bob":   {"verified": true},
					},
					Memberships: map[string][]SeedMembership{
						"alice": {{Room: "r-shared", Roles: []string{"member"}}},
					},
				}},
			},
		},
		{
			Name:       "scenario-b",
			SourcePath: "scenario-b.yaml",
			Sites: map[string]SiteBlock{
				"site-a": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"alice": {"verified": true}},
					Memberships: map[string][]SeedMembership{
						"alice": {{Room: "r-shared", Roles: []string{"owner"}}},
					},
				}},
				"site-b": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"bob": {"verified": true}},
				}},
			},
		},
	}

	errs := CrossScenarioCheck(scs)
	require.Len(t, errs, 2)
	// Stable lex-sort puts (alias, room) conflicts before alias-only.
	assert.Contains(t, errs[0].Error(), `alias="alice"`)
	assert.Contains(t, errs[0].Error(), "r-shared")
	assert.Contains(t, errs[1].Error(), `alias "bob"`)
}

// TestCrossScenarioCheck_BareStringMembershipDefault — a membership
// declared as a bare room-id string defaults to [member] at runtime.
// The check must canonicalize so a bare-string membership in one
// scenario matches a `roles: [member]` in another (no conflict).
func TestCrossScenarioCheck_BareStringMembershipDefault(t *testing.T) {
	scs := []*Scenario{
		{
			Name:       "scenario-bare",
			SourcePath: "scenario-bare.yaml",
			Sites: map[string]SiteBlock{
				"site-a": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"alice": {"verified": true}},
					Memberships: map[string][]SeedMembership{
						"alice": {{Room: "r-x"}}, // bare; Roles nil → defaults to [member]
					},
				}},
			},
		},
		{
			Name:       "scenario-explicit",
			SourcePath: "scenario-explicit.yaml",
			Sites: map[string]SiteBlock{
				"site-a": {Seed: SiteSeed{
					Users: map[string]SeedUserFlags{"alice": {"verified": true}},
					Memberships: map[string][]SeedMembership{
						"alice": {{Room: "r-x", Roles: []string{"member"}}},
					},
				}},
			},
		},
	}
	assert.Empty(t, CrossScenarioCheck(scs),
		"bare-string membership must canonicalize to [member] for conflict-detection equality")
}

// TestCanonicalRolesFingerprint covers the helper's edge cases —
// nil slice (bare-string membership default), single role, multi-role
// stability across permutation.
func TestCanonicalRolesFingerprint(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"nil-defaults-to-member", nil, "member"},
		{"empty-defaults-to-member", []string{}, "member"},
		{"single", []string{"owner"}, "owner"},
		{"sorted", []string{"member", "owner"}, "member,owner"},
		{"permuted-canonicalizes", []string{"owner", "member"}, "member,owner"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, canonicalRolesFingerprint(c.in))
		})
	}
}

// TestCrossScenarioCheck_LoaderIntegrationViaLoadAllParsedInDir
// verifies the loader-side glue: LoadAllParsedInDir returns parsed
// scenarios that CrossScenarioCheck can consume.
func TestCrossScenarioCheck_LoaderIntegrationViaLoadAllParsedInDir(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir+"/a.yaml", strings.TrimSpace(`
scenario: a
source: file:line
tag: positive
sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
      memberships:
        alice:
          - room: r-shared
            roles: [member]
input:
  site: site-a
  verb: nats_request
  subject: dummy
  payload: {x: 1}
  credential: ${alice.credential}
expected:
  - location: reply
    match:
      body_json: {status: ok}
`))
	mustWriteFile(t, dir+"/b.yaml", strings.TrimSpace(`
scenario: b
source: file:line
tag: positive
sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
      memberships:
        alice:
          - room: r-shared
            roles: [owner]
input:
  site: site-a
  verb: nats_request
  subject: dummy
  payload: {x: 1}
  credential: ${alice.credential}
expected:
  - location: reply
    match:
      body_json: {status: ok}
`))

	scs, parseErrs := LoadAllParsedInDir(dir)
	require.Empty(t, parseErrs)
	require.Len(t, scs, 2)

	crossErrs := CrossScenarioCheck(scs)
	require.Len(t, crossErrs, 1)
	assert.Contains(t, crossErrs[0].Error(), `alias="alice"`)
	assert.Contains(t, crossErrs[0].Error(), "r-shared")
}

// TestCommonDirPrefix covers the longest-common-directory computation
// the warning labels rely on.
func TestCommonDirPrefix(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, ""},
		{"single-file-no-trim", []string{"/d/drafts/a.yaml"}, ""},
		{"same-dir", []string{"/d/drafts/a.yaml", "/d/drafts/b.yaml"}, "/d/drafts"},
		{"nested-subdirs", []string{"/d/drafts/msg/a.yaml", "/d/drafts/rooms/b.yaml"}, "/d/drafts"},
		{"mixed-depth", []string{"/d/drafts/a.yaml", "/d/drafts/msg/b.yaml"}, "/d/drafts"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, commonDirPrefix(c.in))
		})
	}
}

// TestScenarioLabels_DisambiguatesSameBasenameInSubdirs is the §2.8
// regression guard: two scenarios with the same filename in different
// subdirectories must render distinctly in F-009 warnings (a plain
// basename would collide).
func TestScenarioLabels_DisambiguatesSameBasenameInSubdirs(t *testing.T) {
	scs := []*Scenario{
		{Name: "x", SourcePath: "/d/drafts/messages/room-creates.yaml"},
		{Name: "y", SourcePath: "/d/drafts/rooms/room-creates.yaml"},
	}
	labels := scenarioLabels(scs)
	assert.Equal(t, "messages/room-creates.yaml", labels["/d/drafts/messages/room-creates.yaml"])
	assert.Equal(t, "rooms/room-creates.yaml", labels["/d/drafts/rooms/room-creates.yaml"])
}

// TestScenarioLabels_SameDirCollapsesToBasename keeps the flat-layout
// output compact: when every scenario shares one directory, labels are
// bare basenames (backward-compatible with the pre-§2.8 reports).
func TestScenarioLabels_SameDirCollapsesToBasename(t *testing.T) {
	scs := []*Scenario{
		{Name: "a", SourcePath: "/d/drafts/a.yaml"},
		{Name: "b", SourcePath: "/d/drafts/b.yaml"},
	}
	labels := scenarioLabels(scs)
	assert.Equal(t, "a.yaml", labels["/d/drafts/a.yaml"])
	assert.Equal(t, "b.yaml", labels["/d/drafts/b.yaml"])
}
