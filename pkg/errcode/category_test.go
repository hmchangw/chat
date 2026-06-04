package errcode

import "testing"

func TestCode_HTTPStatus(t *testing.T) {
	cases := map[Code]int{
		CodeBadRequest:      400,
		CodeUnauthenticated: 401,
		CodeForbidden:       403,
		CodeNotFound:        404,
		CodeConflict:        409,
		CodeTooManyRequests: 429,
		CodeUnavailable:     503,
		CodeInternal:        500,
		Code("weird"):       500,
	}
	for c, want := range cases {
		if got := c.HTTPStatus(); got != want {
			t.Errorf("%s.HTTPStatus() = %d, want %d", c, got, want)
		}
	}
}
