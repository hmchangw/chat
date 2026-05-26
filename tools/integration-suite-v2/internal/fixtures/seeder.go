package fixtures

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/nats-io/nkeys"
)

// CastSpec is the YAML shape (what fixtures/cast.yaml looks like).
type CastSpec struct {
	Users []UserSpec `yaml:"users"`
}

type UserSpec struct {
	ID          string         `yaml:"id"`
	Tags        []string       `yaml:"tags"`
	Credentials map[string]any `yaml:"credentials"`
}

// Seeder materializes the cast: for each user with strategy
// "generate-and-auth", generates an nkey + calls auth-service /auth
// in dev mode to mint a JWT.
type Seeder struct {
	AuthServiceURL string
	HTTPClient     *resty.Client
}

func NewSeeder(authURL string) *Seeder {
	return &Seeder{
		AuthServiceURL: authURL,
		HTTPClient:     resty.New().SetTimeout(5 * time.Second),
	}
}

func (s *Seeder) Seed(ctx context.Context, spec CastSpec) (*Cast, error) {
	cast := &Cast{}
	for _, u := range spec.Users {
		strategy, _ := u.Credentials["strategy"].(string)
		account, _ := u.Credentials["account"].(string)
		switch strategy {
		case "generate-and-auth":
			seed, jwt, err := s.mintCredentials(ctx, account)
			if err != nil {
				return nil, fmt.Errorf("seed user %s: %w", u.ID, err)
			}
			cast.Users = append(cast.Users, CastUser{
				ID:       u.ID,
				Tags:     u.Tags,
				Account:  account,
				JWT:      jwt,
				NkeySeed: seed,
			})
		case "":
			// No credentials needed (e.g., unregistered users)
			cast.Users = append(cast.Users, CastUser{ID: u.ID, Tags: u.Tags, Account: account})
		default:
			return nil, fmt.Errorf("seed user %s: unknown credentials strategy %q", u.ID, strategy)
		}
	}
	return cast, nil
}

func (s *Seeder) mintCredentials(ctx context.Context, account string) (seed, jwt string, err error) {
	kp, err := nkeys.CreateUser()
	if err != nil {
		return "", "", fmt.Errorf("nkey create: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return "", "", fmt.Errorf("nkey pub: %w", err)
	}
	seedBytes, err := kp.Seed()
	if err != nil {
		return "", "", fmt.Errorf("nkey seed: %w", err)
	}

	resp, err := s.HTTPClient.R().SetContext(ctx).
		SetBody(map[string]string{"account": account, "natsPublicKey": pub}).
		SetHeader("Content-Type", "application/json").
		Post(s.AuthServiceURL + "/auth")
	if err != nil {
		return "", "", fmt.Errorf("auth-service POST: %w", err)
	}
	if resp.StatusCode() != 200 {
		return "", "", fmt.Errorf("auth-service /auth: status %d body %s", resp.StatusCode(), string(resp.Body()))
	}
	var body struct {
		NATSJwt string `json:"natsJwt"`
	}
	if err := json.Unmarshal(resp.Body(), &body); err != nil {
		return "", "", fmt.Errorf("auth-service /auth: parse: %w", err)
	}
	if body.NATSJwt == "" {
		return "", "", fmt.Errorf("auth-service /auth: empty natsJwt")
	}
	return string(seedBytes), body.NATSJwt, nil
}
