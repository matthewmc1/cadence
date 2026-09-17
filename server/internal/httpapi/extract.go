package httpapi

// Fact extraction (R4.2) over HTTP.
//
//   - POST /api/v1/signals/{id}/extract — run the extractor on the signal's
//     body now and return the Facts; the result is also stored on the row.
//   - every POST /signals capture queues the same work in the background,
//     so the paste box answers at once and `extracted` fills in via the
//     signal.updated event.
//
// Which extractor runs follows CADENCE_AI_POLICY: under either policy the
// only provider today is the deployment's own Ollama, so it is "the LLM
// when OLLAMA_URL is set, else the heuristic" — and the LLM extractor itself
// falls back to the heuristic on any error. Facts are stored through the
// disposition path (`extracted` is a permitted mutable column) and never
// include the body: only what was pulled out of it.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/extract"
)

const (
	// extractWorkers bounds the background extractors: a paste storm queues
	// rather than fanning out one goroutine per capture.
	extractWorkers = 2
	// extractQueueCap is how many captures may wait; beyond it a capture's
	// background extraction is skipped (logged), and the row still gets its
	// facts on the next explicit POST …/extract.
	extractQueueCap = 64
	// extractJobTimeout bounds one background extraction end to end — the
	// model's own 60 s (extract.DefaultLLMTimeout) plus the store round trips.
	extractJobTimeout = 90 * time.Second
)

// extractJob is one queued capture-time extraction. It carries only ids: the
// worker re-reads the signal under the tenant fence, so a job can never act
// on another tenant's row.
type extractJob struct {
	tenantID, actorID, signalID string
}

// extractor picks the extractor for this deployment (see the file header).
func (s *Server) extractor() extract.Extractor {
	if s.ollamaURL == "" {
		return extract.Heuristic{}
	}
	return &extract.LLM{
		Client: extract.Ollama{BaseURL: s.ollamaURL, HTTP: s.aiClient},
		Model:  s.extractModel,
		Log:    s.log,
	}
}

// POST /signals/{id}/extract — extract (or re-extract) now. Answers with the
// stored row and the Facts: {"signal": Signal, "extracted": Facts}.
func (s *Server) handleExtractSignal(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sig, facts, err := s.extractSignal(ctx, r, TenantID(ctx), ActorID(ctx), r.PathValue("id"))
	if err != nil {
		writeError(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"signal": sig, "extracted": facts})
}

// extractSignal loads the signal and its body under the tenant fence, runs
// the extractor, stores the Facts over the row's `extracted` (keeping any
// other keys a client seeded there, the manual capture's seeded links and
// every fact a human corrected — see keepFromExisting) and appends the
// ai.extract audit row. r may be nil (background).
func (s *Server) extractSignal(ctx context.Context, r *http.Request, tenantID, actorID, signalID string) (*domain.Signal, extract.Facts, error) {
	sig, err := s.store.GetSignal(ctx, tenantID, signalID)
	if err != nil {
		return nil, extract.Facts{}, err
	}
	body, err := s.store.GetSignalBody(ctx, tenantID, signalID)
	if err != nil {
		return nil, extract.Facts{}, err
	}
	text := body.Body
	if text == "" {
		text = sig.Excerpt
	}

	facts, err := s.extractor().Extract(ctx, extract.Input{Title: sig.Title, Text: text, Now: time.Now()})
	if err != nil {
		return nil, extract.Facts{}, err
	}

	// Overlay on whatever `extracted` already holds: a manual capture seeds
	// {"links": […]} and a client may have parked its own keys there.
	existing := map[string]any{}
	_ = json.Unmarshal(sig.Extracted, &existing)
	facts = extract.Merge(keepFromExisting(sig.Extracted), facts)
	facts.Trim(domain.MaxSignalExtractedLen - overheadBytes(existing))

	var factsMap map[string]any
	if b, err := json.Marshal(facts); err != nil {
		return nil, extract.Facts{}, err
	} else if err := json.Unmarshal(b, &factsMap); err != nil {
		return nil, extract.Facts{}, err
	}
	for k, v := range factsMap {
		existing[k] = v
	}
	updated, err := s.store.UpdateSignalDisposition(ctx, tenantID, actorID, signalID, map[string]any{"extracted": existing}, nil)
	if err != nil {
		return nil, extract.Facts{}, err
	}

	// One audit row per extraction: what ran, on which signal — never what
	// it read or found.
	e := domain.AuditEntry{
		Kind: domain.AuditAIExtract, EntityType: ptr(domain.EntitySignal), EntityID: &signalID,
		Detail: auditDetail(map[string]any{"model": facts.Model, "promptVersion": facts.PromptVersion, "signalId": signalID}),
	}
	if actorID != "" {
		e.ActorID = ptr(actorID)
	}
	if r != nil {
		s.auditLog(r, tenantID, e)
	} else {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), auditWriteTimeout)
		defer cancel()
		if _, err := s.store.AppendAudit(actx, tenantID, e); err != nil {
			s.log.Warn("audit append failed", "kind", e.Kind, "err", err)
		}
	}
	return updated, facts, nil
}

// keepFromExisting is the part of a signal's current `extracted` that a fresh
// extraction must not undo: the links a manual capture seeded, and every fact
// a human has corrected. The client's chip editor writes a correction back
// through PATCH /signals/{id} as `{…, "corrected": true}` on that one entry
// (FactChips.tsx); extract.Merge then keeps it whatever the model now reads.
// Everything else the extractor is free to restate, so a re-run still picks up
// a better answer for facts nobody has touched.
//
// A malformed list (a client parked something else under "dates") decodes to
// nothing rather than failing the extraction: encoding/json fills what it can
// and reports the type error, which is exactly the outcome we want here.
func keepFromExisting(extracted json.RawMessage) extract.Facts {
	var prev extract.Facts
	_ = json.Unmarshal(extracted, &prev)
	keep := extract.Facts{Links: prev.Links}
	for _, d := range prev.Dates {
		if d.Corrected {
			keep.Dates = append(keep.Dates, d)
		}
	}
	for _, d := range prev.Deadlines {
		if d.Corrected {
			keep.Deadlines = append(keep.Deadlines, d)
		}
	}
	for _, p := range prev.People {
		if p.Corrected {
			keep.People = append(keep.People, p)
		}
	}
	return keep
}

// overheadBytes is how much of the `extracted` budget the keys we are NOT
// replacing already use, so Trim leaves room for them.
func overheadBytes(existing map[string]any) int {
	rest := map[string]any{}
	for k, v := range existing {
		switch k {
		case "dates", "people", "links", "deadlines", "suggestedTitle", "model", "promptVersion", "extractedAt":
		default:
			rest[k] = v
		}
	}
	b, err := json.Marshal(rest)
	if err != nil {
		return 0
	}
	return len(b)
}

// enqueueExtract hands a capture to the background workers without ever
// blocking the request: a full queue drops the job with a log line rather
// than stalling the paste box. Workers start on first use.
func (s *Server) enqueueExtract(job extractJob) {
	s.extractOnce.Do(func() {
		for i := 0; i < extractWorkers; i++ {
			go s.extractWorker()
		}
	})
	select {
	case s.extractQueue <- job:
	default:
		s.log.Warn("extract queue full; skipping background extraction", "signalId", job.signalID)
	}
}

// extractWorker drains the queue for the life of the process. Each job runs
// under its own deadline and a context detached from any request.
func (s *Server) extractWorker() {
	for job := range s.extractQueue {
		ctx, cancel := context.WithTimeout(context.Background(), extractJobTimeout)
		if _, _, err := s.extractSignal(ctx, nil, job.tenantID, job.actorID, job.signalID); err != nil {
			// a signal dismissed-and-deleted before its turn is not an error worth a WARN
			if err != domain.ErrNotFound {
				s.log.Warn("background extraction failed", "signalId", job.signalID, "err", err)
			}
		}
		cancel()
	}
}
