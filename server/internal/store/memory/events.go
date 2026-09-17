package memory

import "github.com/cadence/server/internal/domain"

// emitEntity publishes a generic-envelope event. It is the one-liner later
// rounds use for new entity types (mirrors postgres.emitEntity):
//
//	s.emitEntity(tenantID, actorID, domain.EventSignalCreated, "signal", sig.ID, sig.Meta())
//
// RULE — payload is METADATA ONLY (id, version, kind, occurredAt, title…).
// Never pass a signal body or participant PII: the event is fanned out to
// every subscriber in the tenant. Pass nil for deletes.
func (s *Store) emitEntity(tenantID, actorID string, typ domain.EventType, entityType, entityID string, payload any) error {
	ev, err := domain.NewEntityEvent(typ, tenantID, actorID, entityType, entityID, payload)
	if err != nil {
		return err
	}
	s.broker.publish(ev)
	return nil
}
