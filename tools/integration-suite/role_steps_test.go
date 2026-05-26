package integrationsuite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

func registerRoleSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^"([^"]+)" requests role "([^"]+)" for member "([^"]+)" in room "([^"]+)"$`, requestsRoleForMember)
}

// requestsRoleForMember drives room-service's member.role-update NATS
// request/reply (subject.MemberRoleUpdate). The role string is sent verbatim
// so scenarios can exercise the role-validity rule.
func requestsRoleForMember(ctx context.Context, actor, role, member, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("role: %q not authenticated (missing `Given user %q is authenticated`)", actor, actor)
	}

	site := suiteConfig.PrimarySite
	prefixedRoom := suiteWorld.Prefix().ID(roomName)
	prefixedMember := suiteWorld.Prefix().ID(member)

	body, err := json.Marshal(model.UpdateRoleRequest{
		RoomID:  prefixedRoom,
		Account: prefixedMember,
		NewRole: model.Role(role),
	})
	if err != nil {
		return fmt.Errorf("role: marshal update-role request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.MemberRoleUpdate(creds.Account, prefixedRoom, site), body)
}
