package controlplane

import (
	"context"
	app "github.com/OutboundSpade/LabCentral/internal/application"
	"github.com/OutboundSpade/LabCentral/internal/domain"
)

func (s *Service) preparationWorkActor(ctx context.Context, id domain.OperationID) (domain.UserID, error) {
	store, ok := s.store.(app.PreparationAuthorityStore)
	if !ok {
		return "", ErrAccessUnavailable
	}
	return store.AuthorizePreparationWork(ctx, id)
}
func (s *Service) authorizePreparation(ctx context.Context, actor domain.UserID, project domain.ProjectID, id domain.OperationID) error {
	w, err := s.store.(app.BuildStore).GetExtensionWork(ctx, id)
	if err != nil {
		return err
	}
	if w.ProjectID != project || w.ActorID != actor || w.Request.Preparation == nil || w.Request.Preparation.Version != 2 {
		return app.ErrAuthorityChanged
	}
	if err = s.authorizeExtension(ctx, actor, project, app.ActionTemplateRead); err != nil {
		return err
	}
	_, err = s.preparationWorkActor(ctx, id)
	return err
}
