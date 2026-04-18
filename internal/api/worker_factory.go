package api

import (
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

func (s *Server) workerFactory(store beads.Store) (*worker.Factory, error) {
	cfg := s.state.Config()
	var resolveTransport func(template string) string
	if cfg != nil {
		resolveTransport = func(template string) string {
			agentCfg, ok := resolveSessionTemplateAgent(cfg, template)
			if !ok {
				return ""
			}
			return agentCfg.Session
		}
	}
	return worker.NewFactory(worker.FactoryConfig{
		Store:            store,
		Provider:         s.state.SessionProvider(),
		CityPath:         s.state.CityPath(),
		SearchPaths:      s.sessionLogPaths(),
		ResolveTransport: resolveTransport,
		DecorateSessionSpec: func(info session.Info, _ string, spec *worker.SessionSpec) {
			s.decorateWorkerSessionSpec(info, spec)
		},
	})
}

func (s *Server) workerSessionCatalog(store beads.Store) (*worker.SessionCatalog, error) {
	factory, err := s.workerFactory(store)
	if err != nil {
		return nil, err
	}
	return factory.Catalog()
}

func (s *Server) decorateWorkerSessionSpec(info session.Info, spec *worker.SessionSpec) {
	if spec == nil {
		return
	}
	resolved, workDir := s.resolveSessionRuntime(info)
	if resolved == nil {
		return
	}

	spec.Command = firstNonEmptyString(resolved.CommandString(), spec.Command)
	spec.Provider = firstNonEmptyString(resolved.Name, spec.Provider)
	spec.WorkDir = firstNonEmptyString(spec.WorkDir, workDir)
	spec.Hints = sessionResumeHints(resolved, spec.WorkDir)
	spec.Resume = session.ProviderResume{
		ResumeFlag:    resolved.ResumeFlag,
		ResumeStyle:   resolved.ResumeStyle,
		ResumeCommand: resolved.ResumeCommand,
		SessionIDFlag: resolved.SessionIDFlag,
	}
}
