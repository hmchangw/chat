package integrationsuite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cucumber/godog"
)

// registerAuthSteps wires auth-related step definitions.
func registerAuthSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^user "([^"]+)" is authenticated$`, userIsAuthenticated)
	ctx.Step(`^user "([^"]+)" is authenticated in site "([^"]+)"$`, userIsAuthenticatedInSite)
}

func userIsAuthenticated(ctx context.Context, name string) error {
	return userIsAuthenticatedInSite(ctx, name, suiteConfig.PrimarySite)
}

func userIsAuthenticatedInSite(_ context.Context, name, site string) error {
	prefixedAccount := suiteWorld.Prefix().ID(name)

	pub, seed, err := GenerateUserNkey()
	if err != nil {
		return fmt.Errorf("auth: nkey gen for %q: %w", prefixedAccount, err)
	}

	client := newHTTPClient(suiteWorld)
	resp, err := client.R().
		SetBody(map[string]string{
			"account":       prefixedAccount,
			"natsPublicKey": pub,
		}).
		SetHeader("Content-Type", "application/json").
		Post(suiteConfig.AuthServiceURL(site) + "/auth")
	if err != nil {
		return fmt.Errorf("auth: POST /auth for %q: %w", prefixedAccount, err)
	}
	if resp.StatusCode() != 200 {
		return fmt.Errorf("auth: POST /auth for %q: status %d body %s",
			prefixedAccount, resp.StatusCode(), string(resp.Body()))
	}

	var body struct {
		NATSJwt string `json:"natsJwt"`
	}
	if err := json.Unmarshal(resp.Body(), &body); err != nil {
		return fmt.Errorf("auth: parsing /auth response: %w", err)
	}
	if body.NATSJwt == "" {
		return fmt.Errorf("auth: empty natsJwt in response")
	}

	suiteWorld.SetCredentials(name, &Credentials{
		Account: prefixedAccount,
		JWT:     body.NATSJwt,
		Seed:    seed,
	})
	return nil
}
