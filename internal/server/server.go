package server

import (
	"github.com/ruvicode/gateway/internal/config"
	"github.com/ruvicode/gateway/internal/store"
)

// Server bundles the gateway's dependencies.
type Server struct {
	cfg *config.Config
	pg  *store.PostgresStore
	rdb *store.RedisStore
}

// New creates a Server with the given configuration and stores.
func New(cfg *config.Config, pg *store.PostgresStore, rdb *store.RedisStore) *Server {
	return &Server{cfg: cfg, pg: pg, rdb: rdb}
}
