package api

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
)

func (s *Server) sessionManager(store beads.Store) *session.Manager {
	cfg := s.state.Config()
	if cfg == nil {
		return session.NewManager(store, s.state.SessionProvider())
	}
	return session.NewManagerWithTransportResolver(store, s.state.SessionProvider(), func(template string) string {
		agentCfg, ok := resolveSessionTemplateAgent(cfg, template)
		if !ok {
			return ""
		}
		return agentCfg.Session
	})
}
