package agentv3

import (
	"context"
	"sync"
)

type agentV3SkillState struct {
	mu     sync.Mutex
	names  map[string]struct{}
	layers []runtimeSkillEnvLayer
}

func (s *agentV3SkillState) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names = nil
	s.layers = nil
}

func (s *agentV3SkillState) activate(skill agentV3SkillDescriptor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.names[skill.Name]; ok {
		return
	}
	if s.names == nil {
		s.names = make(map[string]struct{})
	}
	s.names[skill.Name] = struct{}{}
	switch skill.Source {
	case agentV3SkillSourceBuiltin:
	case agentV3SkillSourceBotLocal:
		if len(skill.environment) > 0 {
			s.layers = append(s.layers, runtimeSkillEnvLayer{Source: skill.Source, Name: skill.Name, Env: cloneAgentV3SkillEnvironment(skill.environment)})
		}
	case agentV3SkillSourceRuntimeGlobal:
		s.layers = append(s.layers, runtimeSkillEnvLayer{Source: skill.Source, Name: skill.Name, SkillSHA256: skill.SHA256})
	}
}

func (s *agentV3SkillState) loaded(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.names[name]
	return ok
}

func (s *agentV3SkillState) environment() []runtimeSkillEnvLayer {
	s.mu.Lock()
	defer s.mu.Unlock()
	layers := make([]runtimeSkillEnvLayer, len(s.layers))
	for i, layer := range s.layers {
		layers[i] = layer
		layers[i].Env = cloneAgentV3SkillEnvironment(layer.Env)
	}
	return layers
}

// A nil admission has no loader; a nil allowed set admits the entire current catalog.
type agentV3SkillAdmission struct {
	allowed map[string]struct{}
}

func (a *agentV3SkillAdmission) skill(tc *TurnContext, name string) (agentV3SkillDescriptor, bool) {
	if a == nil || tc == nil || tc.V3 == nil {
		return agentV3SkillDescriptor{}, false
	}
	if a.allowed != nil {
		if _, ok := a.allowed[name]; !ok {
			return agentV3SkillDescriptor{}, false
		}
	}
	skill, ok := tc.V3.SkillCatalog.ByName[name]
	return skill, ok
}

type agentV3SkillScopeKey struct{}

type agentV3SkillScope struct {
	state *agentV3SkillState
}

func beginAgentV3SkillScope(ctx context.Context, nested bool) context.Context {
	tc := GetTurnContext(ctx)
	if tc == nil || tc.V3 == nil {
		return ctx
	}
	state := &tc.skills
	if nested || ctx.Value(agentV3SkillScopeKey{}) != nil {
		state = &agentV3SkillState{}
	} else {
		state.reset()
	}
	ctx = context.WithValue(ctx, agentV3SkillScopeKey{}, &agentV3SkillScope{state: state})
	return context.WithValue(ctx, agentV3VisibleSkillsKey{}, agentV3VisibleSkills{})
}

func agentV3SkillsForContext(ctx context.Context) *agentV3SkillState {
	if scope, _ := ctx.Value(agentV3SkillScopeKey{}).(*agentV3SkillScope); scope != nil {
		return scope.state
	}
	if tc := GetTurnContext(ctx); tc != nil && tc.V3 != nil {
		return &tc.skills
	}
	return nil
}

func agentV3SkillLoaded(ctx context.Context, name string) bool {
	state := agentV3SkillsForContext(ctx)
	return state != nil && state.loaded(name)
}

func agentV3RuntimeEnvironment(ctx context.Context) (map[string]string, []runtimeSkillEnvLayer) {
	tc := GetTurnContext(ctx)
	state := agentV3SkillsForContext(ctx)
	if tc == nil || tc.V3 == nil || state == nil {
		return nil, nil
	}
	return cloneAgentV3SkillEnvironment(tc.V3.runtimeEnv), state.environment()
}
