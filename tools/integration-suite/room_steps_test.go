package integrationsuite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

func registerRoomSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^"([^"]+)" creates channel room "([^"]+)"$`, createsChannelRoom)
}

func createsChannelRoom(ctx context.Context, actor, roomName string) error {
	creds := suiteWorld.Credentials(actor)
	if creds == nil {
		return fmt.Errorf("room: %q not authenticated (missing `Given user %q is authenticated`)", actor, actor)
	}

	site := suiteConfig.PrimarySite
	prefixedName := suiteWorld.Prefix().ID(roomName)

	body, err := json.Marshal(model.CreateRoomRequest{
		Name:             prefixedName,
		Type:             model.RoomTypeChannel,
		CreatedBy:        creds.Account,        // use account as user-ID for test purposes
		CreatedByAccount: creds.Account,
		SiteID:           site,
	})
	if err != nil {
		return fmt.Errorf("room: marshal create request: %w", err)
	}

	return natsRequest(ctx, suiteWorld, suiteConfig, actor, site,
		subject.RoomsCreate(creds.Account), body)
}
