package integrationsuite

import (
	"context"
	"fmt"

	"github.com/cucumber/godog"
)

func registerErrorSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^the response is a (\w+) error$`, responseIsErrorClass)
	ctx.Step(`^the response is successful$`, responseIsSuccessful)
}

func responseIsErrorClass(_ context.Context, want string) error {
	last := suiteWorld.LastResponse()
	if last == nil {
		return fmt.Errorf("error-class assertion: no response captured (did a When step run?)")
	}
	got := last.Class()
	if string(got) != want {
		return fmt.Errorf("error-class: want %s, got %s (transport=%s status=%d errText=%q trace=%s)",
			want, got, last.Transport, last.StatusCode, last.ErrorText, last.TraceID)
	}
	return nil
}

func responseIsSuccessful(_ context.Context) error {
	last := suiteWorld.LastResponse()
	if last == nil {
		return fmt.Errorf("successful assertion: no response captured")
	}
	got := last.Class()
	if got != ClassNone {
		return fmt.Errorf("successful: response is not a success (class=%s transport=%s status=%d errText=%q err=%v trace=%s)",
			got, last.Transport, last.StatusCode, last.ErrorText, last.Err, last.TraceID)
	}
	return nil
}
