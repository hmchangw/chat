// Package errhttp adapts errcode.Error to Gin HTTP responses.
package errhttp

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/hmchangw/chat/pkg/errcode"
)

// Write classifies err (logging once) and writes the envelope with its HTTP status.
func Write(ctx context.Context, c *gin.Context, err error) {
	e := errcode.Classify(ctx, err)
	c.JSON(e.HTTPStatus(), e)
}
