package cassrepo

import (
	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/atrest"
)

type Repository struct {
	session *gocql.Session
	cipher  atrest.Cipher // nil when ATREST_ENABLED=false
}

func NewRepository(session *gocql.Session, cipher atrest.Cipher) *Repository {
	return &Repository{session: session, cipher: cipher}
}
