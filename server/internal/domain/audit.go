package domain

import (
	"encoding/json"
	"time"
)

// Audit kinds. The set is open — connector.<name> rows are added by whichever
// connector produced them, and the column is deliberately not CHECKed — but
// every in-tree producer uses one of these so a reader can filter reliably.
const (
	AuditLoginIssued   = "auth.login.issued"   // a magic link was sent to an existing account
	AuditLoginVerified = "auth.login.verified" // a magic link was consumed → session started
	AuditTokenCreate   = "auth.token.create"   // a personal access token was minted
	AuditTokenRevoke   = "auth.token.revoke"   // …or revoked
	AuditAIChat        = "ai.chat"             // one provider call through the AI gateway
	AuditAIExtract     = "ai.extract"          // facts were extracted from a signal (model + prompt version, never the text)
	AuditMCPRead       = "mcp.read"            // an MCP client read tenant data (reserved; not wired yet)
	AuditExport        = "export"              // a data export was produced (reserved)
	AuditPurge         = "purge"               // the tenant was purged (reserved; the row goes with it)
)

// Entity type label for rows about a personal access token.
const EntityAPIToken = "api_token"

// MaxAuditDetailBytes bounds AuditEntry.Detail. The log is append-only and
// tenant-visible, so a detail blob is a small structured note, not a dump.
const MaxAuditDetailBytes = 4096

// AuditEntry is one immutable row in a tenant's audit log: who (ActorID) did
// what (Kind) to which thing (EntityType/EntityID), when (At), with a bounded
// structured Detail.
//
// RULE — no PII in an audit row. Detail carries counts, a model name, a
// token's display name, an entity id; never an email, an address, a prompt,
// a signal body or a credential. The only trace of the network peer is
// IPHash, a salted digest computed by the HTTP layer, so "same source"
// correlation works without the address ever being stored.
type AuditEntry struct {
	ID         string          `json:"id"`
	TenantID   string          `json:"tenantId"`
	At         time.Time       `json:"at"`
	ActorID    *string         `json:"actorId"` // nil for system-initiated rows
	Kind       string          `json:"kind"`
	EntityType *string         `json:"entityType"`
	EntityID   *string         `json:"entityId"`
	Detail     json.RawMessage `json:"detail"` // always a JSON value; {} when nothing to say
	IPHash     *string         `json:"ipHash"`
}
