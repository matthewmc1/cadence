package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/cadence/server/internal/domain"
	"github.com/cadence/server/internal/store"
	"github.com/cadence/server/internal/store/memory"
)

// Handler-level tests for the signal surface: the wiring above the store —
// status codes, the query-string contract, If-Match, the tenant fence and the
// audit side-effects. The parity suite proves the store; none of it proves
// that a handler still calls auditLog or still answers 400 for a bad stage.

// asTenant builds a request already carrying an identity (the way
// resolveIdentity would) plus any {path} values the mux would have set.
func asTenant(tenant, method, target, body string, pathValues ...string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	for i := 0; i+1 < len(pathValues); i += 2 {
		r.SetPathValue(pathValues[i], pathValues[i+1])
	}
	ctx := context.WithValue(r.Context(), ctxTenant, tenant)
	ctx = context.WithValue(ctx, ctxActor, "u"+strings.TrimPrefix(tenant, "t"))
	return r.WithContext(ctx)
}

// call runs one handler and returns the recorder.
func call(h func(http.ResponseWriter, *http.Request), r *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h(rr, r)
	return rr
}

// auditRows returns the tenant's audit rows of one kind, newest first.
func auditRows(t *testing.T, st store.Store, tenantID, kind string) []domain.AuditEntry {
	t.Helper()
	page, err := st.ListAudit(context.Background(), tenantID, store.Page{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.AuditEntry
	for _, e := range page.Items {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func decodeInto(t *testing.T, rr *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", rr.Body.String(), err)
	}
}

// TestSignalRoutesRequireASession walks every route in the signal surface
// through the real mux with no cookie: the fence is the router's, not each
// handler's, so it is worth proving once for all of them.
func TestSignalRoutesRequireASession(t *testing.T) {
	h := New(memory.New(), Options{}).Handler()
	routes := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/signals"},
		{http.MethodGet, "/api/v1/signals/count"},
		{http.MethodPost, "/api/v1/signals"},
		{http.MethodGet, "/api/v1/signals/abc"},
		{http.MethodGet, "/api/v1/signals/abc/body"},
		{http.MethodPatch, "/api/v1/signals/abc"},
		{http.MethodDelete, "/api/v1/signals/abc"},
		{http.MethodPost, "/api/v1/signals/abc/extract"},
		{http.MethodGet, "/api/v1/sources"},
		{http.MethodPost, "/api/v1/sources"},
		{http.MethodPatch, "/api/v1/sources/abc"},
		{http.MethodDelete, "/api/v1/sources/abc"},
		{http.MethodGet, "/api/v1/tasks/abc/origins"},
		{http.MethodPost, "/api/v1/tasks/abc/origins"},
		{http.MethodDelete, "/api/v1/tasks/abc/origins/def"},
		{http.MethodGet, "/api/v1/tasks/abc/outputs"},
		{http.MethodPost, "/api/v1/tasks/abc/outputs"},
		{http.MethodDelete, "/api/v1/tasks/abc/outputs/def"},
		{http.MethodGet, "/api/v1/requirements"},
		{http.MethodPost, "/api/v1/requirements"},
		{http.MethodPatch, "/api/v1/requirements/abc"},
		{http.MethodDelete, "/api/v1/requirements/abc"},
		{http.MethodGet, "/api/v1/audit"},
	}
	for _, rt := range routes {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}")))
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a session: status = %d, want 401", rt.method, rt.path, rr.Code)
		}
	}
}

func TestSignalFilterValidation(t *testing.T) {
	s := New(memory.New(), Options{})
	cases := []struct {
		query, field string
	}{
		{"stage=archived", "stage"},
		{"stage=INBOX", "stage"},
		{"occurredAfter=notatime", "occurredAfter"},
		{"occurredAfter=2026-09-06", "occurredAfter"},
	}
	for _, c := range cases {
		for _, h := range []struct {
			name string
			fn   func(http.ResponseWriter, *http.Request)
		}{{"list", s.handleListSignals}, {"count", s.handleCountSignals}} {
			rr := call(h.fn, asTenant("t1", http.MethodGet, "/api/v1/signals?"+c.query, ""))
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s ?%s: status = %d, want 400", h.name, c.query, rr.Code)
				continue
			}
			var body errBody
			decodeInto(t, rr, &body)
			if body.Error.Field != c.field || body.Error.Code != "invalid" {
				t.Errorf("%s ?%s: error = %+v", h.name, c.query, body.Error)
			}
		}
	}
	// a valid filter is a page, not a 400
	if rr := call(s.handleListSignals, asTenant("t1", http.MethodGet, "/api/v1/signals?stage=inbox&occurredAfter=2026-01-01T00:00:00Z", "")); rr.Code != http.StatusOK {
		t.Errorf("valid filter: status = %d %s", rr.Code, rr.Body.String())
	}
}

// TestSignalLifecycleOverHTTP is the whole disposition walk through the
// handlers: capture → get → count → body (audited) → stale If-Match → patch →
// delete (audited), with the tenant fence checked at every read.
func TestSignalLifecycleOverHTTP(t *testing.T) {
	st := memory.New()
	s := New(st, Options{})
	sig := capture(t, s, captureText, nil)

	if rows := auditRows(t, st, "t1", domain.AuditSignalCapture); len(rows) != 1 {
		t.Fatalf("signal.capture rows = %d", len(rows))
	} else if !strings.Contains(string(rows[0].Detail), `"textChars":`) || strings.Contains(string(rows[0].Detail), "Jane") {
		t.Errorf("signal.capture detail = %s", rows[0].Detail)
	}

	// get + count
	rr := call(s.handleGetSignal, asTenant("t1", http.MethodGet, "/api/v1/signals/"+sig.ID, "", "id", sig.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rr.Code, rr.Body.String())
	}
	rr = call(s.handleCountSignals, asTenant("t1", http.MethodGet, "/api/v1/signals/count?stage=inbox", ""))
	var count struct {
		Count int `json:"count"`
	}
	decodeInto(t, rr, &count)
	if count.Count != 1 {
		t.Errorf("count = %d, want 1", count.Count)
	}

	// the body is its own endpoint, and reading it is recorded — with the size
	// of what was read and nothing of what it said
	rr = call(s.handleGetSignalBody, asTenant("t1", http.MethodGet, "/api/v1/signals/"+sig.ID+"/body", "", "id", sig.ID))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Q3 numbers") {
		t.Fatalf("body: %d %s", rr.Code, rr.Body.String())
	}
	reads := auditRows(t, st, "t1", domain.AuditSignalBodyRead)
	if len(reads) != 1 {
		t.Fatalf("signal.body.read rows = %d", len(reads))
	}
	if !strings.Contains(string(reads[0].Detail), `"bodyChars":`+strconv.Itoa(len(captureText))) {
		t.Errorf("body.read detail = %s", reads[0].Detail)
	}
	if strings.Contains(string(reads[0].Detail), "Jane") || strings.Contains(string(reads[0].Detail), "Q3") {
		t.Errorf("body.read detail carries the body: %s", reads[0].Detail)
	}
	if reads[0].EntityID == nil || *reads[0].EntityID != sig.ID {
		t.Errorf("body.read entity = %v", reads[0].EntityID)
	}

	// another tenant sees none of it
	for name, fn := range map[string]func(http.ResponseWriter, *http.Request){
		"get": s.handleGetSignal, "body": s.handleGetSignalBody, "delete": s.handleDeleteSignal,
	} {
		r := asTenant("t2", http.MethodGet, "/api/v1/signals/"+sig.ID, "", "id", sig.ID)
		if rr := call(fn, r); rr.Code != http.StatusNotFound {
			t.Errorf("t2 %s: status = %d, want 404", name, rr.Code)
		}
	}
	if rr := call(s.handleCountSignals, asTenant("t2", http.MethodGet, "/api/v1/signals/count", "")); !strings.Contains(rr.Body.String(), `"count":0`) {
		t.Errorf("t2 count = %s", rr.Body.String())
	}

	// If-Match is the optimistic-concurrency token: a stale one is a 409
	stale := asTenant("t1", http.MethodPatch, "/api/v1/signals/"+sig.ID, `{"stage":"dismissed"}`, "id", sig.ID)
	stale.Header.Set("If-Match", "99")
	if rr := call(s.handleUpdateSignal, stale); rr.Code != http.StatusConflict {
		t.Errorf("stale If-Match: status = %d, want 409", rr.Code)
	}
	fresh := asTenant("t1", http.MethodPatch, "/api/v1/signals/"+sig.ID, `{"stage":"snoozed","snoozedUntil":"2026-09-08T09:00:00Z"}`, "id", sig.ID)
	fresh.Header.Set("If-Match", strconv.Itoa(sig.Version))
	rr = call(s.handleUpdateSignal, fresh)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body.String())
	}
	var snoozed domain.Signal
	decodeInto(t, rr, &snoozed)
	if snoozed.Stage != domain.SignalSnoozed || snoozed.SnoozedUntil == nil {
		t.Errorf("patched signal = %+v", snoozed)
	}
	// an immutable field is a 400 naming it, never a 500 from the trigger
	rr = call(s.handleUpdateSignal, asTenant("t1", http.MethodPatch, "/api/v1/signals/"+sig.ID, `{"title":"rewritten"}`, "id", sig.ID))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("patching title: status = %d, want 400", rr.Code)
	}

	// delete is 204 and leaves its row
	rr = call(s.handleDeleteSignal, asTenant("t1", http.MethodDelete, "/api/v1/signals/"+sig.ID, "", "id", sig.ID))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	dels := auditRows(t, st, "t1", domain.AuditSignalDelete)
	if len(dels) != 1 || dels[0].EntityID == nil || *dels[0].EntityID != sig.ID {
		t.Fatalf("signal.delete rows = %+v", dels)
	}
	if rr := call(s.handleDeleteSignal, asTenant("t1", http.MethodDelete, "/api/v1/signals/"+sig.ID, "", "id", sig.ID)); rr.Code != http.StatusNotFound {
		t.Errorf("second delete: status = %d, want 404", rr.Code)
	}
}

// TestRetentionPatchIsAudited: arming the retention sweep on a signal is a
// scheduled deletion, and leaves the same kind of trace one does.
func TestRetentionPatchIsAudited(t *testing.T) {
	st := memory.New()
	s := New(st, Options{})
	sig := capture(t, s, "a note worth keeping for a while", nil)

	rr := call(s.handleUpdateSignal, asTenant("t1", http.MethodPatch, "/api/v1/signals/"+sig.ID,
		`{"retentionUntil":"1970-01-01T00:00:00Z"}`, "id", sig.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body.String())
	}
	rows := auditRows(t, st, "t1", domain.AuditSignalRetention)
	if len(rows) != 1 {
		t.Fatalf("signal.retention rows = %d", len(rows))
	}
	if !strings.Contains(string(rows[0].Detail), "1970-01-01T00:00:00Z") || rows[0].EntityID == nil || *rows[0].EntityID != sig.ID {
		t.Errorf("signal.retention row = %s (%v)", rows[0].Detail, rows[0].EntityID)
	}
	// a disposition patch that does not touch retention writes no such row
	call(s.handleUpdateSignal, asTenant("t1", http.MethodPatch, "/api/v1/signals/"+sig.ID, `{"stage":"dismissed"}`, "id", sig.ID))
	if rows := auditRows(t, st, "t1", domain.AuditSignalRetention); len(rows) != 1 {
		t.Errorf("signal.retention rows after an unrelated patch = %d", len(rows))
	}
}

// TestSourceDeleteIsAuditedWithItsCascade: DELETE /sources/{id} is the largest
// deletion the API offers — it must say how much it took, and it must refuse
// the one source the user never made.
func TestSourceDeleteIsAuditedWithItsCascade(t *testing.T) {
	st := memory.New()
	s := New(st, Options{})

	rr := call(s.handleCreateSource, asTenant("t1", http.MethodPost, "/api/v1/sources", `{"kind":"email","name":"Work mail"}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create source: %d %s", rr.Code, rr.Body.String())
	}
	var src domain.Source
	decodeInto(t, rr, &src)

	for i := 0; i < 3; i++ {
		body, _ := json.Marshal(map[string]any{"sourceId": src.ID, "kind": "email", "text": "thread " + strconv.Itoa(i)})
		if rr := call(s.handleCreateSignal, asTenant("t1", http.MethodPost, "/api/v1/signals", string(body))); rr.Code != http.StatusCreated {
			t.Fatalf("capture %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}

	// the manual source is refused, with a field-named 400 rather than a
	// silent cascade over every pasted capture
	rr = call(s.handleListSources, asTenant("t1", http.MethodGet, "/api/v1/sources", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("list sources: %d %s", rr.Code, rr.Body.String())
	}
	var sources struct {
		Sources []domain.Source `json:"sources"`
	}
	decodeInto(t, rr, &sources)
	manualID := ""
	for _, got := range sources.Sources {
		if got.Kind == domain.SourceManual {
			manualID = got.ID
		}
	}
	if manualID == "" {
		t.Fatalf("no manual source in %+v", sources.Sources)
	}
	rr = call(s.handleDeleteSource, asTenant("t1", http.MethodDelete, "/api/v1/sources/"+manualID, "", "id", manualID))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("delete manual source: status = %d, want 400", rr.Code)
	}
	if len(auditRows(t, st, "t1", domain.AuditSourceDelete)) != 0 {
		t.Errorf("a refused delete was audited")
	}

	// another tenant cannot delete it either
	if rr := call(s.handleDeleteSource, asTenant("t2", http.MethodDelete, "/api/v1/sources/"+src.ID, "", "id", src.ID)); rr.Code != http.StatusNotFound {
		t.Errorf("t2 delete: status = %d, want 404", rr.Code)
	}

	rr = call(s.handleDeleteSource, asTenant("t1", http.MethodDelete, "/api/v1/sources/"+src.ID, "", "id", src.ID))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete source: %d %s", rr.Code, rr.Body.String())
	}
	rows := auditRows(t, st, "t1", domain.AuditSourceDelete)
	if len(rows) != 1 {
		t.Fatalf("source.delete rows = %d", len(rows))
	}
	if !strings.Contains(string(rows[0].Detail), `"signals":3`) {
		t.Errorf("source.delete detail = %s — the cascade's size is the point of the row", rows[0].Detail)
	}
	if rr := call(s.handleCountSignals, asTenant("t1", http.MethodGet, "/api/v1/signals/count", "")); !strings.Contains(rr.Body.String(), `"count":0`) {
		t.Errorf("signals survived their source: %s", rr.Body.String())
	}
}

// TestOriginsAndOutputsOverHTTP walks a work item's provenance in both
// directions and checks the derived counts the list rides on.
func TestOriginsAndOutputsOverHTTP(t *testing.T) {
	st := memory.New()
	s := New(st, Options{})
	task, err := st.CreateTask(context.Background(), "t1", "u1", domain.CreateTaskInput{Title: "sign the SOW"})
	if err != nil {
		t.Fatal(err)
	}
	sig := capture(t, s, "please sign the SOW by Friday", nil)

	// attach: 201, the signal is disposed 'attached', and it is idempotent
	attach := func(tenant, signalID string) *httptest.ResponseRecorder {
		return call(s.handleAttachOrigin, asTenant(tenant, http.MethodPost, "/api/v1/tasks/"+task.ID+"/origins",
			`{"signalId":"`+signalID+`"}`, "id", task.ID))
	}
	if rr := attach("t1", sig.ID); rr.Code != http.StatusCreated {
		t.Fatalf("attach: %d %s", rr.Code, rr.Body.String())
	}
	if rr := attach("t1", sig.ID); rr.Code != http.StatusCreated {
		t.Errorf("re-attach: status = %d, want the same 201 (idempotent)", rr.Code)
	}
	if rr := attach("t1", ""); rr.Code != http.StatusBadRequest {
		t.Errorf("attach without a signalId: status = %d, want 400", rr.Code)
	}
	if rr := attach("t2", sig.ID); rr.Code != http.StatusNotFound {
		t.Errorf("t2 attach to t1's task: status = %d, want 404", rr.Code)
	}

	rr := call(s.handleListOrigins, asTenant("t1", http.MethodGet, "/api/v1/tasks/"+task.ID+"/origins", "", "id", task.ID))
	var origins struct {
		Origins []domain.Origin `json:"origins"`
	}
	decodeInto(t, rr, &origins)
	if len(origins.Origins) != 1 || origins.Origins[0].Snapshot.Title == "" {
		t.Fatalf("origins = %+v", origins.Origins)
	}

	// outputs: create, list, delete
	rr = call(s.handleCreateOutput, asTenant("t1", http.MethodPost, "/api/v1/tasks/"+task.ID+"/outputs",
		`{"kind":"doc","title":"Signed SOW","url":"https://docs.example.com/sow"}`, "id", task.ID))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create output: %d %s", rr.Code, rr.Body.String())
	}
	var out domain.Output
	decodeInto(t, rr, &out)
	if rr := call(s.handleCreateOutput, asTenant("t1", http.MethodPost, "/api/v1/tasks/"+task.ID+"/outputs",
		`{"kind":"telegram","title":"nope"}`, "id", task.ID)); rr.Code != http.StatusBadRequest {
		t.Errorf("unknown output kind: status = %d, want 400", rr.Code)
	}

	// the counts a work-item list rides on, without a request per row
	rr = call(s.handleGetTask, asTenant("t1", http.MethodGet, "/api/v1/tasks/"+task.ID, "", "id", task.ID))
	var counted domain.Task
	decodeInto(t, rr, &counted)
	if counted.OriginCount != 1 || counted.OutputCount != 1 {
		t.Errorf("task counts = %d origins, %d outputs; want 1 and 1", counted.OriginCount, counted.OutputCount)
	}

	// detach + delete output, and the counts follow
	if rr := call(s.handleDetachOrigin, asTenant("t1", http.MethodDelete, "/api/v1/tasks/"+task.ID+"/origins/"+sig.ID, "",
		"id", task.ID, "signalId", sig.ID)); rr.Code != http.StatusNoContent {
		t.Errorf("detach: status = %d", rr.Code)
	}
	if rr := call(s.handleDetachOrigin, asTenant("t1", http.MethodDelete, "/api/v1/tasks/"+task.ID+"/origins/"+sig.ID, "",
		"id", task.ID, "signalId", sig.ID)); rr.Code != http.StatusNotFound {
		t.Errorf("second detach: status = %d, want 404", rr.Code)
	}
	if rr := call(s.handleDeleteOutput, asTenant("t1", http.MethodDelete, "/api/v1/tasks/"+task.ID+"/outputs/"+out.ID, "",
		"id", task.ID, "outputId", out.ID)); rr.Code != http.StatusNoContent {
		t.Errorf("delete output: status = %d", rr.Code)
	}
	rr = call(s.handleGetTask, asTenant("t1", http.MethodGet, "/api/v1/tasks/"+task.ID, "", "id", task.ID))
	decodeInto(t, rr, &counted)
	if counted.OriginCount != 0 || counted.OutputCount != 0 {
		t.Errorf("counts after detach/delete = %d/%d", counted.OriginCount, counted.OutputCount)
	}
}

func TestRequirementRoutes(t *testing.T) {
	st := memory.New()
	s := New(st, Options{})
	proj, err := st.CreateProject(context.Background(), "t1", "u1", domain.CreateProjectInput{Name: "Acme rollout"})
	if err != nil {
		t.Fatal(err)
	}

	rr := call(s.handleCreateRequirement, asTenant("t1", http.MethodPost, "/api/v1/requirements",
		`{"projectId":"`+proj.ID+`","title":"Signed SOW","weight":3}`))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var req domain.Requirement
	decodeInto(t, rr, &req)

	for _, bad := range []string{
		`{"projectId":"` + proj.ID + `","title":""}`,
		`{"projectId":"` + proj.ID + `","title":"x","weight":9}`,
		`{"projectId":"nope","title":"x"}`,
	} {
		if rr := call(s.handleCreateRequirement, asTenant("t1", http.MethodPost, "/api/v1/requirements", bad)); rr.Code != http.StatusBadRequest {
			t.Errorf("create %s: status = %d, want 400", bad, rr.Code)
		}
	}

	rr = call(s.handleListRequirements, asTenant("t1", http.MethodGet, "/api/v1/requirements?projectId="+proj.ID, ""))
	var list struct {
		Requirements []domain.Requirement `json:"requirements"`
	}
	decodeInto(t, rr, &list)
	if len(list.Requirements) != 1 {
		t.Fatalf("requirements = %+v", list.Requirements)
	}
	if rr := call(s.handleListRequirements, asTenant("t2", http.MethodGet, "/api/v1/requirements", "")); !strings.Contains(rr.Body.String(), `"requirements":[]`) {
		t.Errorf("t2 requirements = %s", rr.Body.String())
	}

	stale := asTenant("t1", http.MethodPatch, "/api/v1/requirements/"+req.ID, `{"status":"met"}`, "id", req.ID)
	stale.Header.Set("If-Match", "99")
	if rr := call(s.handleUpdateRequirement, stale); rr.Code != http.StatusConflict {
		t.Errorf("stale If-Match: status = %d, want 409", rr.Code)
	}
	if rr := call(s.handleUpdateRequirement, asTenant("t1", http.MethodPatch, "/api/v1/requirements/"+req.ID, `{"status":"maybe"}`, "id", req.ID)); rr.Code != http.StatusBadRequest {
		t.Errorf("bad status: %d", rr.Code)
	}
	rr = call(s.handleUpdateRequirement, asTenant("t1", http.MethodPatch, "/api/v1/requirements/"+req.ID, `{"status":"met"}`, "id", req.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body.String())
	}
	if rr := call(s.handleDeleteRequirement, asTenant("t2", http.MethodDelete, "/api/v1/requirements/"+req.ID, "", "id", req.ID)); rr.Code != http.StatusNotFound {
		t.Errorf("t2 delete: status = %d, want 404", rr.Code)
	}
	if rr := call(s.handleDeleteRequirement, asTenant("t1", http.MethodDelete, "/api/v1/requirements/"+req.ID, "", "id", req.ID)); rr.Code != http.StatusNoContent {
		t.Errorf("delete: status = %d", rr.Code)
	}
}

// TestAuditReadIsPagedAndTenantScoped: the log is read-only, keyset-paged and
// never shows another tenant's history.
func TestAuditReadIsPagedAndTenantScoped(t *testing.T) {
	st := memory.New()
	s := New(st, Options{})
	for i := 0; i < 3; i++ {
		capture(t, s, "capture "+strconv.Itoa(i), nil)
	}

	seen := map[string]bool{}
	captures := 0
	cursor, pages := "", 0
	for {
		target := "/api/v1/audit?limit=1"
		if cursor != "" {
			target += "&cursor=" + cursor
		}
		rr := call(s.handleListAudit, asTenant("t1", http.MethodGet, target, ""))
		if rr.Code != http.StatusOK {
			t.Fatalf("audit page: %d %s", rr.Code, rr.Body.String())
		}
		var page store.PageResult[domain.AuditEntry]
		decodeInto(t, rr, &page)
		if len(page.Items) != 1 {
			t.Fatalf("page items = %d, want the requested 1", len(page.Items))
		}
		if seen[page.Items[0].ID] {
			t.Fatalf("paging repeated row %s", page.Items[0].ID)
		}
		seen[page.Items[0].ID] = true
		if page.Items[0].Kind == domain.AuditSignalCapture {
			captures++
		}
		pages++
		if page.NextCursor == "" || pages > 20 {
			break
		}
		cursor = page.NextCursor
	}
	// one signal.capture per capture, each seen exactly once. The background
	// extractor may have landed an ai.extract row alongside them, so the total
	// is a floor, not an equality.
	if captures != 3 || len(seen) < 3 {
		t.Errorf("paged %d row(s), %d of them captures; want the 3 captures", len(seen), captures)
	}

	rr := call(s.handleListAudit, asTenant("t2", http.MethodGet, "/api/v1/audit", ""))
	var other store.PageResult[domain.AuditEntry]
	decodeInto(t, rr, &other)
	if len(other.Items) != 0 {
		t.Errorf("t2 sees %d of t1's audit rows", len(other.Items))
	}
	if rr := call(s.handleListAudit, asTenant("t1", http.MethodGet, "/api/v1/audit?cursor=not-a-cursor", "")); rr.Code != http.StatusBadRequest {
		t.Errorf("bad cursor: status = %d, want 400", rr.Code)
	}
}
