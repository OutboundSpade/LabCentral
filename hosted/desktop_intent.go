package hosted

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type desktopOperation struct{ Generation, Status string }

var errDesktopBusy = errors.New("desktop operation pending")

func (s *Service) recordAuthorized(ctx context.Context, a accessRecord) bool {
	dctx, stop := context.WithCancel(ctx)
	defer stop()
	return s.desktopAuthorized(ctx, &desktopSession{record: a, ctx: dctx})
}
func (s *Service) reserveDesktop(ctx context.Context, a accessRecord) (string, error) {
	// Identity refresh can contact OIDC; it happens before the intent lock.
	if !s.recordAuthorized(ctx, a) {
		return "", fmt.Errorf("desktop authorization ended")
	}
	unlock, err := s.lockUser(ctx, a.UserID)
	if err != nil {
		return "", err
	}
	defer unlock()
	if !s.recordCurrent(ctx, a) {
		return "", fmt.Errorf("desktop authorization ended")
	}
	var operation desktopOperation
	err = s.Store.Get(ctx, "desktop-operation", a.ID, &operation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil && operation.Status != "ready" {
		return "", errDesktopBusy
	}
	s.desktopMu.Lock()
	busy := s.accessFlights[a.ID] != ""
	s.desktopMu.Unlock()
	if busy {
		return "", errDesktopBusy
	}
	// A different login must close the old session before reusing its identity.
	var previous accessRecord
	if err = s.Store.Get(ctx, "desktop", a.ID, &previous); err == nil && previous.SessionID != a.SessionID {
		operation = desktopOperation{randomID(12), "closing"}
		err = s.Store.Put(ctx, "desktop-operation", a.ID, operation)
		if err != nil {
			return "", err
		}
		return "", errDesktopBusy
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	operation = desktopOperation{randomID(12), "opening"}
	err = s.Store.atomic(ctx, func(ctx context.Context) error {
		if e := s.Store.Put(ctx, "desktop", a.ID, a); e != nil {
			return e
		}
		return s.Store.Put(ctx, "desktop-operation", a.ID, operation)
	})
	if err != nil {
		return "", err
	}
	s.desktopMu.Lock()
	if s.accessFlights == nil {
		s.accessFlights = map[string]string{}
	}
	s.accessFlights[a.ID] = operation.Generation
	s.desktopMu.Unlock()
	return operation.Generation, nil
}
func (s *Service) finishDesktop(ctx context.Context, a accessRecord, generation string, next *desktopSession, openErr error) (string, error) {
	authorized := openErr == nil && s.recordAuthorized(ctx, a)
	unlock, err := s.lockUser(ctx, a.UserID)
	if err != nil {
		s.desktopMu.Lock()
		delete(s.accessFlights, a.ID)
		s.desktopMu.Unlock()
		return "", err
	}
	defer unlock()
	defer func() { s.desktopMu.Lock(); delete(s.accessFlights, a.ID); s.desktopMu.Unlock() }()
	var operation desktopOperation
	if err = s.Store.Get(ctx, "desktop-operation", a.ID, &operation); err != nil {
		return "", err
	}
	if operation.Generation != generation {
		return "", errDesktopBusy
	}
	if openErr != nil || operation.Status != "opening" || !authorized || !s.recordCurrent(ctx, a) {
		operation.Status = "closing"
		return "", errors.Join(errDesktopBusy, s.Store.Put(ctx, "desktop-operation", a.ID, operation))
	}
	operation.Status = "ready"
	if err = s.Store.Put(ctx, "desktop-operation", a.ID, operation); err != nil {
		return "", err
	}
	ticket := randomID(32)
	s.desktopMu.Lock()
	if old := s.desktops[a.ID]; old != nil {
		old.cancel()
	}
	s.desktops[a.ID] = next
	s.tickets[digest(ticket)] = launchTicket{a.ID, a.SessionID, time.Now().Add(time.Minute)}
	s.desktopMu.Unlock()
	return ticket, nil
}

// inactiveOnly rechecks the current relay and record, so an old expiry snapshot
// cannot close a freshly opened desktop with the same deterministic identity.
func (s *Service) cleanupDesktop(ctx context.Context, id string, inactiveOnly bool) error {
	var a accessRecord
	if err := s.Store.Get(ctx, "desktop", id, &a); errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	unlock, err := s.lockUser(ctx, a.UserID)
	if err != nil {
		return err
	}
	if err = s.Store.Get(ctx, "desktop", id, &a); err != nil {
		unlock()
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var operation desktopOperation
	err = s.Store.Get(ctx, "desktop-operation", id, &operation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		unlock()
		return err
	}
	s.desktopMu.Lock()
	d := s.desktops[id]
	active := d != nil && desktopAvailable(d, time.Now())
	busy := s.accessFlights[id] != ""
	s.desktopMu.Unlock()
	if inactiveOnly && busy && operation.Status == "opening" {
		unlock()
		return nil
	}
	if inactiveOnly && active && (operation.Status == "ready" || operation.Status == "") {
		unlock()
		return nil
	}
	if operation.Generation == "" {
		operation.Generation = randomID(12)
	}
	operation.Status = "closing"
	if err = s.Store.Put(ctx, "desktop-operation", id, operation); err != nil {
		unlock()
		return err
	}
	s.desktopMu.Lock()
	if d != nil {
		d.cancel()
		delete(s.desktops, id)
	}
	if !busy {
		if s.accessFlights == nil {
			s.accessFlights = map[string]string{}
		}
		s.accessFlights[id] = operation.Generation
	}
	s.desktopMu.Unlock()
	unlock()
	if busy {
		return errDesktopBusy
	}
	// No user or relay mutex is held during native cleanup.
	budgetCtx, release, admit := s.sshContext(ctx)
	if admit != nil {
		err = admit
	} else {
		err = s.Engine.CloseAccess(budgetCtx, id)
		release()
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	unlock, wait := s.lockUser(saveCtx, a.UserID)
	if wait != nil {
		s.desktopMu.Lock()
		delete(s.accessFlights, id)
		s.desktopMu.Unlock()
		return errors.Join(err, wait)
	}
	defer unlock()
	defer func() { s.desktopMu.Lock(); delete(s.accessFlights, id); s.desktopMu.Unlock() }()
	if err != nil {
		return err
	}
	var current desktopOperation
	if err = s.Store.Get(saveCtx, "desktop-operation", id, &current); err != nil {
		return err
	}
	if current != operation {
		return errDesktopBusy
	}
	return s.Store.atomic(saveCtx, func(ctx context.Context) error {
		if err := s.Store.Delete(ctx, "desktop", id); err != nil {
			return err
		}
		return s.Store.Delete(ctx, "desktop-operation", id)
	})
}

// Under the user intent lock use only durable local authorization. The caller
// already refreshed identity outside it; logout/switch/expiry can still win.
func (s *Service) recordCurrent(ctx context.Context, a accessRecord) bool {
	var v Session
	if s.Store.Get(ctx, "session", a.SessionID, &v) != nil || v.User.ID != a.UserID || !v.Expires.After(time.Now()) || !a.Expires.After(time.Now()) {
		return false
	}
	if strings.HasPrefix(a.TeamID, "practice:") {
		run, err := s.Store.practice(ctx, strings.TrimPrefix(a.TeamID, "practice:"))
		return err == nil && run.UserID == a.UserID && run.InstanceID == a.InstanceID && run.Status == "ready" && run.Expires.After(time.Now()) && s.practiceDisclosure(ctx, run.LabID) == nil
	}
	team, _, err := s.readyTeam(ctx, a.UserID)
	return err == nil && team.ID == a.TeamID && team.InstanceID == a.InstanceID
}
