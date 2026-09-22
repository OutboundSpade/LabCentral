package controlplane

import (
	"context"
	"encoding/json"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func (s *Service) consoleControlStore(ctx context.Context, actor domain.UserID, id domain.AccessSessionID, mutate bool) (app.ConsoleControlStore, error) {
	r, err := s.GetAccessSession(ctx, actor, id)
	if err != nil {
		return nil, err
	}
	if r.SubjectID != actor || !r.SharedConsole {
		return nil, ErrNotFound
	}
	if mutate {
		if _, err = s.authorizer.AuthorizeInstance(ctx, actor, app.ActionAccessSessionCreate, r.InstanceID); err != nil {
			return nil, err
		}
	}
	store, ok := s.store.(app.ConsoleControlStore)
	if !ok {
		return nil, ErrAccessUnavailable
	}
	return store, nil
}
func (s *Service) ConsolePresence(ctx context.Context, actor domain.UserID, id domain.AccessSessionID) (app.ConsolePresence, error) {
	store, err := s.consoleControlStore(ctx, actor, id, false)
	if err != nil {
		return app.ConsolePresence{}, err
	}
	return store.ConsolePresence(ctx, actor, id)
}
func (s *Service) ChangeConsoleControl(ctx context.Context, actor domain.UserID, id domain.AccessSessionID, take bool, key string) (app.ConsolePresence, error) {
	return s.ChangeConsoleControlConditional(ctx, actor, id, take, false, key)
}
func (s *Service) ChangeConsoleControlConditional(ctx context.Context, actor domain.UserID, id domain.AccessSessionID, take, onlyIfAlone bool, key string) (app.ConsolePresence, error) {
	store, err := s.consoleControlStore(ctx, actor, id, true)
	if err != nil {
		return app.ConsolePresence{}, err
	}
	raw, _ := json.Marshal(struct {
		ID          domain.AccessSessionID
		Take        bool
		OnlyIfAlone bool `json:",omitempty"`
	}{id, take, onlyIfAlone})
	return store.ChangeConsoleControl(ctx, app.ChangeConsoleControl{ActorID: actor, SessionID: id, Take: take, OnlyIfAlone: onlyIfAlone, IdempotencyKey: key, RequestDigest: digestBytes("console.control/v1", raw)})
}
