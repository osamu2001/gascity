package main

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

func newSessionManager(store beads.Store, sp runtime.Provider) *session.Manager {
	cityPath, err := resolveCity()
	if err != nil {
		return session.NewManager(store, sp)
	}
	cfg, err := loadCityConfig(cityPath)
	if err != nil {
		return session.NewManager(store, sp)
	}
	return newSessionManagerWithConfig(store, sp, cfg)
}

func newSessionManagerWithConfig(store beads.Store, sp runtime.Provider, cfg *config.City) *session.Manager {
	if cfg == nil {
		return session.NewManager(store, sp)
	}
	rigContext := currentRigContext(cfg)
	return session.NewManagerWithTransportResolver(store, sp, func(template string) string {
		agentCfg, ok := resolveAgentIdentity(cfg, template, rigContext)
		if !ok {
			return ""
		}
		return agentCfg.Session
	})
}
