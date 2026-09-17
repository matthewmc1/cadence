import '../styles/para.css';
import { createContext, useContext, useEffect, useMemo, useState } from 'react';
import type { Client, ClientKind, Project, Requirement } from '../api/types';
import { useApp, useActions } from '../state/store';
import { selectProjectProgress, type ClientHealth, type TaskRow } from '../state/selectors';
import { KINDS } from '../lib/energy';
import { ymd } from '../lib/time';
import { ContextLine } from '../components/ContextLine';
import { ProjectPicker } from '../components/ProjectPicker';
import { BoardView } from './BoardView';

/**
 * Projects — the PARA page (Tiago Forte): Projects · Areas · Resources ·
 * Archive. Work answers "what am I doing"; this page answers "what have I
 * committed to, and why". A project is an outcome with a date; an area is a
 * standing responsibility held to a standard; resources are the references the
 * work has gathered; the archive is what is finished. Every action listed here
 * carries its why · when · where, inheriting the why from its project.
 */

/**
 * Below the width where a rail and four lanes both fit, the rail becomes a
 * drawer; every pane's header carries the button that opens it.
 */
const RailCtx = createContext<() => void>(() => {});
function RailButton() {
  const open = useContext(RailCtx);
  return (
    <button type="button" className="para__railbtn" onClick={open} aria-label="Show projects and areas" title="Projects and areas">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" aria-hidden>
        <path d="M4 7h16M4 12h16M4 17h10" />
      </svg>
    </button>
  );
}

const DETAILS_KEY = 'cadence.projectDetails';

type Pane = { type: 'project'; id: string } | { type: 'area'; id: string } | { type: 'resources' } | { type: 'archive' } | { type: 'unfiled' };

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
const AREA_KIND_LABEL: Record<ClientKind, string> = { client: 'Client', internal: 'Internal', area: 'Area' };

const fmtDate = (iso: string) => {
  const d = new Date(iso.length === 10 ? iso + 'T00:00:00' : iso);
  return `${d.getDate()} ${MONTHS[d.getMonth()]}${d.getFullYear() !== new Date().getFullYear() ? ' ' + d.getFullYear() : ''}`;
};
const isPast = (day: string) => day.slice(0, 10) < ymd(new Date());

/** What PARA would say is wrong with a project as it stands. */
function projectGaps(p: Project, open: TaskRow[]): string[] {
  const gaps: string[] = [];
  if (!p.outcome.trim()) gaps.push('No outcome — say what finishing looks like, so its actions have a why.');
  if (!p.due) gaps.push('No date — a project without an end is an area.');
  else if (isPast(p.due)) gaps.push(`Past its date (${fmtDate(p.due)}) — finish it, move the date, or archive it.`);
  if (!open.some((r) => r.stage === 'todo' || r.stage === 'doing')) gaps.push('No next action — nothing is moving this forward.');
  return gaps;
}

export function ProjectsView() {
  const { para, allTasks, requirements, clientHealth, selectedProjectId } = useApp();
  const { selectProject } = useActions();
  const [pane, setPaneRaw] = useState<Pane | null>(null);
  const [newProject, setNewProject] = useState(false);
  const [newArea, setNewArea] = useState(false);
  const [railOpen, setRailOpen] = useState(false);

  const projects = useMemo(() => para.projects.filter((p) => !p.archivedAt), [para.projects]);
  const areas = useMemo(() => para.areas.filter((a) => !a.archivedAt), [para.areas]);
  const archivedProjects = useMemo(() => para.projects.filter((p) => p.archivedAt), [para.projects]);
  const archivedAreas = useMemo(() => para.areas.filter((a) => a.archivedAt), [para.areas]);
  const areaById = useMemo(() => new Map(para.areas.map((a) => [a.id, a])), [para.areas]);

  const openByProject = useMemo(() => {
    const m = new Map<string, TaskRow[]>();
    for (const r of allTasks) {
      if (!r.projectId || r.stage === 'done') continue;
      const list = m.get(r.projectId);
      if (list) list.push(r);
      else m.set(r.projectId, [r]);
    }
    return m;
  }, [allTasks]);
  const unfiled = useMemo(() => allTasks.filter((r) => !r.projectId && r.stage !== 'done'), [allTasks]);
  const resourceCount = useMemo(() => allTasks.reduce((n, r) => n + r.links.length, 0), [allTasks]);

  // Land on the store's selected project (so "project created" opens it), else the first one.
  const current: Pane | null = useMemo(() => {
    if (pane) {
      if (pane.type === 'project' && !para.projects.some((p) => p.id === pane.id)) return null;
      if (pane.type === 'area' && !para.areas.some((a) => a.id === pane.id)) return null;
      return pane;
    }
    const p = projects.find((x) => x.id === selectedProjectId) ?? projects[0];
    return p ? { type: 'project', id: p.id } : null;
  }, [pane, para.projects, para.areas, projects, selectedProjectId]);

  const setPane = (p: Pane) => {
    setPaneRaw(p);
    setRailOpen(false);
  };
  const openProject = (id: string) => {
    selectProject(id);
    setPane({ type: 'project', id });
    setNewProject(false);
  };

  const isOn = (p: Pane) => current != null && current.type === p.type && ('id' in p ? 'id' in current && current.id === p.id : true);

  return (
    <RailCtx.Provider value={() => setRailOpen(true)}>
    <div className={'viewbody para' + (railOpen ? ' is-rail-open' : '')} onKeyDown={(e) => e.key === 'Escape' && setRailOpen(false)}>
      {railOpen && <div className="para__scrim" onClick={() => setRailOpen(false)} aria-hidden />}
      {/* no page heading: the rail names the page, and the height goes to the board */}
      <h1 className="para__sr">Projects</h1>
      <div className="para__layout">
        <nav className="para__rail" aria-label="Projects, areas, resources and archive">
          <RailHead letter="P" name="Projects" hint="an outcome, with a date" n={projects.length} onAdd={() => setNewProject((v) => !v)} addLabel="New project" />
          {newProject && <NewProject areas={areas} onDone={() => setNewProject(false)} />}
          <ul className="para__list">
            {projects.map((p) => {
              const open = openByProject.get(p.id) ?? [];
              const prog = selectProjectProgress(p.id, allTasks, requirements);
              const gaps = projectGaps(p, open);
              const on = isOn({ type: 'project', id: p.id });
              return (
                <li key={p.id}>
                  <button className={'para__item' + (on ? ' is-on' : '')} aria-current={on ? 'true' : undefined} onClick={() => openProject(p.id)}>
                    <span className="para__item-row">
                      <span className="dot" style={{ width: 7, height: 7, background: p.color }} />
                      <span className="para__item-name">{p.name}</span>
                      {gaps.length > 0 && (
                        <span className="para__flag" title={gaps.join('\n')} aria-label={`${gaps.length} thing${gaps.length > 1 ? 's' : ''} to clarify`}>
                          {gaps.length}
                        </span>
                      )}
                    </span>
                    <span className="para__item-row para__item-sub">
                      <span className="para__track" aria-hidden>
                        <span className="para__fill" style={{ width: `${prog.pct}%` }} />
                      </span>
                      <span className={'para__item-due tnum' + (p.due && isPast(p.due) ? ' is-late' : '')}>{p.due ? fmtDate(p.due) : 'no date'}</span>
                    </span>
                  </button>
                </li>
              );
            })}
            {projects.length === 0 && !newProject && <li className="para__empty">No projects yet.</li>}
          </ul>

          <RailHead letter="A" name="Areas" hint="a standard, kept up" n={areas.length} onAdd={() => setNewArea((v) => !v)} addLabel="New area" />
          {newArea && (
            <NewArea
              onDone={(a) => {
                setNewArea(false);
                if (a) setPane({ type: 'area', id: a.id });
              }}
            />
          )}
          <ul className="para__list">
            {areas.map((a) => {
              const h = clientHealth.find((c) => c.id === a.id);
              const on = isOn({ type: 'area', id: a.id });
              return (
                <li key={a.id}>
                  <button className={'para__item' + (on ? ' is-on' : '')} aria-current={on ? 'true' : undefined} onClick={() => setPane({ type: 'area', id: a.id })}>
                    <span className="para__item-row">
                      <span className="dot" style={{ width: 7, height: 7, background: a.color }} />
                      <span className="para__item-name">{a.name}</span>
                      {h?.underserved && (
                        <span className="para__flag" title={`Untouched for ${h.daysSince} days`}>
                          !
                        </span>
                      )}
                    </span>
                    <span className="para__item-row para__item-sub">
                      <span className="para__item-kind">{AREA_KIND_LABEL[a.kind]}</span>
                      <span className="para__item-due tnum">
                        {h?.projectCount ?? 0} project{(h?.projectCount ?? 0) === 1 ? '' : 's'}
                      </span>
                    </span>
                  </button>
                </li>
              );
            })}
            {areas.length === 0 && !newArea && <li className="para__empty">No areas yet.</li>}
          </ul>

          <ul className="para__list para__list--links">
            <li>
              <button className={'para__link' + (isOn({ type: 'resources' }) ? ' is-on' : '')} onClick={() => setPane({ type: 'resources' })}>
                <span className="para__letter">R</span>Resources<span className="para__n tnum">{resourceCount}</span>
              </button>
            </li>
            <li>
              <button className={'para__link' + (isOn({ type: 'archive' }) ? ' is-on' : '')} onClick={() => setPane({ type: 'archive' })}>
                <span className="para__letter">A</span>Archive<span className="para__n tnum">{archivedProjects.length + archivedAreas.length}</span>
              </button>
            </li>
            <li>
              <button className={'para__link' + (isOn({ type: 'unfiled' }) ? ' is-on' : '') + (unfiled.length ? ' has-some' : '')} onClick={() => setPane({ type: 'unfiled' })}>
                <span className="para__letter">·</span>Unfiled actions<span className="para__n tnum">{unfiled.length}</span>
              </button>
            </li>
          </ul>
        </nav>

        <section className="para__main">
          {current == null && (
            <div className="para__blank">
              <p className="para__blank-lead serif">Start with one project.</p>
              <p>A project is an outcome you can finish, with a date. Give it both and every action under it knows why it exists.</p>
              <button
                className="btn-primary"
                onClick={() => {
                  setNewProject(true);
                  setRailOpen(true);
                }}
              >
                New project
              </button>
            </div>
          )}
          {current?.type === 'project' && (
            <ProjectPane
              key={current.id}
              project={para.projects.find((p) => p.id === current.id)!}
              areas={areas}
              area={areaById.get(para.projects.find((p) => p.id === current.id)?.clientId ?? '') ?? null}
              rows={allTasks.filter((r) => r.projectId === current.id)}
              requirements={requirements.filter((r) => r.projectId === current.id && !r.archivedAt)}
              onOpenArea={(id) => setPane({ type: 'area', id })}
            />
          )}
          {current?.type === 'area' && (
            <AreaPane
              key={current.id}
              area={areaById.get(current.id)!}
              health={clientHealth.find((c) => c.id === current.id) ?? null}
              projects={para.projects.filter((p) => p.clientId === current.id)}
              allTasks={allTasks}
              requirements={requirements}
              onOpenProject={openProject}
            />
          )}
          {current?.type === 'resources' && <ResourcesPane allTasks={allTasks} projects={para.projects} />}
          {current?.type === 'archive' && <ArchivePane projects={archivedProjects} areas={archivedAreas} allTasks={allTasks} />}
          {current?.type === 'unfiled' && <UnfiledPane rows={unfiled} />}
        </section>
      </div>
    </div>
    </RailCtx.Provider>
  );
}

/* ------------------------------------------------------------------ rail */

function RailHead({ letter, name, hint, n, onAdd, addLabel }: { letter: string; name: string; hint: string; n: number; onAdd: () => void; addLabel: string }) {
  return (
    <div className="para__railhead">
      <span className="para__letter">{letter}</span>
      <span className="para__railname">
        {name} <span className="para__n tnum">{n}</span>
        <span className="para__railhint">{hint}</span>
      </span>
      <button className="para__add" onClick={onAdd} aria-label={addLabel} title={addLabel}>
        +
      </button>
    </div>
  );
}

/** A project is asked for whole: name, the outcome, the date, the area. Only the name is required. */
function NewProject({ areas, onDone }: { areas: Client[]; onDone: () => void }) {
  const { addProject } = useActions();
  const [name, setName] = useState('');
  const [outcome, setOutcome] = useState('');
  const [due, setDue] = useState('');
  const [areaId, setAreaId] = useState('');
  const submit = () => {
    if (!name.trim()) return;
    addProject({ name: name.trim(), outcome: outcome.trim(), due: due || null, clientId: areaId || null });
    onDone();
  };
  return (
    <form
      className="para__new"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
      onKeyDown={(e) => e.key === 'Escape' && onDone()}
    >
      <input autoFocus className="para__input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Project name" aria-label="Project name" />
      <textarea className="para__input" rows={2} value={outcome} onChange={(e) => setOutcome(e.target.value)} placeholder="Outcome — what is true when this is finished?" aria-label="Outcome" />
      <div className="para__new-row">
        <input className="para__input" type="date" value={due} onChange={(e) => setDue(e.target.value)} aria-label="Due date" />
        <select className="para__input" value={areaId} onChange={(e) => setAreaId(e.target.value)} aria-label="Area">
          <option value="">No area</option>
          {areas.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
      </div>
      <div className="para__new-row para__new-row--end">
        <button type="button" className="para__ghost" onClick={onDone}>
          Cancel
        </button>
        <button type="submit" className="para__go" disabled={!name.trim()}>
          Create
        </button>
      </div>
    </form>
  );
}

function NewArea({ onDone }: { onDone: (a: Client | null) => void }) {
  const { addArea } = useActions();
  const [name, setName] = useState('');
  const [kind, setKind] = useState<ClientKind>('area');
  const [standard, setStandard] = useState('');
  const submit = async () => {
    if (!name.trim()) return;
    onDone(await addArea({ name: name.trim(), kind, standard: standard.trim() }));
  };
  return (
    <form
      className="para__new"
      onSubmit={(e) => {
        e.preventDefault();
        void submit();
      }}
      onKeyDown={(e) => e.key === 'Escape' && onDone(null)}
    >
      <input autoFocus className="para__input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Area name — Finance, Hiring, a client…" aria-label="Area name" />
      <textarea className="para__input" rows={2} value={standard} onChange={(e) => setStandard(e.target.value)} placeholder="Standard — what does ‘kept up’ look like?" aria-label="Standard" />
      <div className="para__new-row">
        <select className="para__input" value={kind} onChange={(e) => setKind(e.target.value as ClientKind)} aria-label="Kind">
          <option value="area">Area of responsibility</option>
          <option value="client">Client</option>
          <option value="internal">Internal initiative</option>
        </select>
      </div>
      <div className="para__new-row para__new-row--end">
        <button type="button" className="para__ghost" onClick={() => onDone(null)}>
          Cancel
        </button>
        <button type="submit" className="para__go" disabled={!name.trim()}>
          Create
        </button>
      </div>
    </form>
  );
}

/* --------------------------------------------------------------- project */

/** Text that saves on blur (or Enter, for a single line) and follows the server's value when it isn't being edited. */
function useDraft(value: string, save: (v: string) => void) {
  const [draft, setDraft] = useState(value);
  const [editing, setEditing] = useState(false);
  useEffect(() => {
    if (!editing) setDraft(value);
  }, [value, editing]);
  return {
    value: draft,
    onChange: (e: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => setDraft(e.target.value),
    onFocus: () => setEditing(true),
    onBlur: () => {
      setEditing(false);
      if (draft.trim() !== value.trim()) save(draft.trim());
    },
  };
}

function ProjectPane({
  project: p,
  areas,
  area,
  rows,
  requirements,
  onOpenArea,
}: {
  project: Project;
  areas: Client[];
  area: Client | null;
  rows: TaskRow[];
  requirements: Requirement[];
  onOpenArea: (id: string) => void;
}) {
  const { patchProject, addProjectAction, openEditor, completeTask, startTask, selectProject, setWorkMode, autoScheduleRemaining } = useActions();
  const { allTasks, requirements: allReqs, workMode, selectedProjectId } = useApp();
  // the board draws the store's selected project: keep that this one, however the pane was reached
  useEffect(() => {
    if (selectedProjectId !== p.id) selectProject(p.id);
  }, [p.id, selectedProjectId, selectProject]);
  const board = workMode === 'board' && p.archivedAt == null;
  const [title, setTitle] = useState('');
  const [showDone, setShowDone] = useState(false);
  // Details: closed by default so the board has the screen; the choice is remembered
  const [details, setDetails] = useState(() => {
    try {
      return localStorage.getItem(DETAILS_KEY) === 'open';
    } catch {
      return false;
    }
  });
  const setDetailsOpen = (open: boolean) => {
    setDetails(open);
    try {
      localStorage.setItem(DETAILS_KEY, open ? 'open' : 'closed');
    } catch {
      /* storage blocked: lasts the session */
    }
  };
  const openDetails = () => setDetailsOpen(true);

  const name = useDraft(p.name, (v) => v && patchProject(p.id, { name: v }));
  const outcome = useDraft(p.outcome, (v) => patchProject(p.id, { outcome: v }));

  const open = rows.filter((r) => r.stage !== 'done');
  const done = rows.filter((r) => r.stage === 'done');
  const groups: { key: string; label: string; note: string; rows: TaskRow[] }[] = [
    { key: 'doing', label: 'Doing', note: 'in flight', rows: open.filter((r) => r.stage === 'doing') },
    { key: 'todo', label: 'Next actions', note: 'what moves it forward', rows: open.filter((r) => r.stage === 'todo').sort((a, b) => b.priority - a.priority) },
    { key: 'waiting', label: 'Waiting', note: 'on someone else', rows: open.filter((r) => r.stage === 'waiting') },
  ];
  const gaps = projectGaps(p, open);
  const prog = selectProjectProgress(p.id, allTasks, allReqs);
  const unclear = open.filter((r) => r.context.missing.length > 0).length;
  const archived = p.archivedAt != null;

  const attention = gaps.length + (unclear > 0 ? 1 : 0);
  const counts = groups.map((g) => `${g.rows.length} ${g.key === 'todo' ? 'next' : g.key}`).join(', ');

  return (
    <article className={'para__project' + (board ? ' is-board' : '')}>
      {/* The summary: one compact header — what it is, why, by when, how far — so
          the board gets the screen. Editing the facts is one click away in Details. */}
      <section className={'para__summary' + (details ? ' is-open' : '')} aria-label="Project summary">
        <div className="para__sumrow">
          <RailButton />
          <span className="dot" style={{ width: 10, height: 10, background: p.color }} />
          <input className="para__name serif" aria-label="Project name" {...name} onKeyDown={(e) => e.key === 'Enter' && e.currentTarget.blur()} />
          {archived && <span className="para__tag">Archived {fmtDate(p.archivedAt!)}</span>}

          <div className="para__glance">
            <button type="button" className={'para__pill' + (p.due && isPast(p.due) && !archived ? ' is-late' : '') + (!p.due ? ' is-missing' : '')} onClick={openDetails} title="Finish by">
              {p.due ? fmtDate(p.due) : 'No date'}
            </button>
            {area && (
              <button type="button" className="para__pill" onClick={() => onOpenArea(area.id)} title="Open this area">
                <span className="dot" style={{ width: 6, height: 6, background: area.color }} />
                {area.name}
              </button>
            )}
            <span className="para__pill para__pill--plain" title={prog.unit === 'requirements' ? 'Requirements met' : 'Actions done'}>
              <span className="tnum">
                {prog.done}/{prog.total}
              </span>
              <span className="para__track para__track--lg" aria-hidden>
                <span className="para__fill" style={{ width: `${prog.pct}%` }} />
              </span>
            </span>
            {!archived && attention > 0 && (
              <button type="button" className="para__pill is-late" onClick={openDetails}>
                {attention} to clarify
              </button>
            )}
          </div>

          <div className="para__summary-tools">
            {!archived && (
              <div className="para__modes" role="group" aria-label="Project view">
                <button type="button" className={'para__mode' + (board ? ' is-on' : '')} aria-pressed={board} onClick={() => setWorkMode('board')}>
                  Board
                </button>
                <button type="button" className={'para__mode' + (!board ? ' is-on' : '')} aria-pressed={!board} onClick={() => setWorkMode('list')}>
                  List
                </button>
              </div>
            )}
            <button type="button" className="para__ghost" aria-expanded={details} onClick={() => setDetailsOpen(!details)}>
              Details {details ? '▴' : '▾'}
            </button>
          </div>
        </div>

        <textarea
          className="para__outcome serif"
          rows={1}
          aria-label="Outcome — why this project exists"
          placeholder="What is true when this is finished? Every action here inherits it as its why."
          {...outcome}
        />

        {details && (
          <div className="para__details">
            <div className="para__stats">
              <label className="para__stat">
                <span className="para__statkey">Finish by</span>
                <input className={'para__field' + (p.due && isPast(p.due) && !archived ? ' is-late' : '')} type="date" value={p.due ?? ''} onChange={(e) => patchProject(p.id, { due: e.target.value || null })} />
              </label>
              <label className="para__stat">
                <span className="para__statkey">Area</span>
                <select className="para__field" value={p.clientId ?? ''} onChange={(e) => patchProject(p.id, { clientId: e.target.value || null })}>
                  <option value="">No area</option>
                  {areas.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name}
                    </option>
                  ))}
                  {area?.archivedAt && <option value={area.id}>{area.name} (archived)</option>}
                </select>
              </label>
              <div className="para__stat">
                <span className="para__statkey">Open</span>
                <span className="para__statval para__statval--text">{open.length === 0 ? 'Nothing open' : counts}</span>
              </div>
              <div className="para__stat para__stat--tools">
                {!archived && open.some((r) => !r.scheduledAt) && (
                  <button type="button" className="para__ghost" onClick={autoScheduleRemaining} title="Place every unscheduled action into your week">
                    Schedule the rest
                  </button>
                )}
                <button type="button" className="para__ghost" onClick={() => patchProject(p.id, { archived: !archived })}>
                  {archived ? 'Restore project' : open.length === 0 && rows.length > 0 ? 'Finished: archive it' : 'Archive'}
                </button>
              </div>
            </div>
            {!archived && attention > 0 && (
              <ul className="para__gaps" aria-label="To clarify">
                {gaps.map((g) => (
                  <li key={g}>{g}</li>
                ))}
                {unclear > 0 && (
                  <li>
                    {unclear} action{unclear === 1 ? ' is' : 's are'} missing a why, when or where. Open one to fill it in.
                  </li>
                )}
              </ul>
            )}
          </div>
        )}
      </section>

      {board && selectedProjectId === p.id && (
        <div className="para__board">
          <BoardView embedded />
        </div>
      )}

      {!board && (
        <div className="para__listcard">
      {requirements.length > 0 && (
        <section className="para__sec">
          <h2 className="para__sechead">
            Must deliver <span className="para__n tnum">{requirements.length}</span>
          </h2>
          <ul className="para__reqs">
            {requirements.map((r) => (
              <li key={r.id} className={'para__req para__req--' + r.status}>
                <span className="para__req-mark" aria-hidden>
                  {r.status === 'met' ? '✓' : r.status === 'dropped' ? '–' : '○'}
                </span>
                <span className="para__req-title">{r.title}</span>
                <span className="para__req-n tnum">{rows.filter((t) => t.requirementId === r.id && t.stage !== 'done').length} open</span>
              </li>
            ))}
          </ul>
        </section>
      )}

      {groups.map(
        (g) =>
          (g.rows.length > 0 || g.key === 'todo') && (
            <section className="para__sec" key={g.key}>
              <h2 className="para__sechead">
                {g.label} <span className="para__n tnum">{g.rows.length}</span>
                <span className="para__secnote">{g.note}</span>
              </h2>
              <ul className="para__actions">
                {g.rows.map((r) => (
                  <ActionRow key={r.id} r={r} onOpen={() => openEditor(r.id)} onDone={() => void completeTask(r.id)} onStart={g.key === 'todo' ? () => void startTask(r.id) : undefined} />
                ))}
              </ul>
              {g.key === 'todo' && !archived && (
                <form
                  className="para__addaction"
                  onSubmit={(e) => {
                    e.preventDefault();
                    addProjectAction(p.id, title);
                    setTitle('');
                  }}
                >
                  <input className="para__input" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Add the next action: a verb, something you could start today" aria-label="New action" />
                  <button type="submit" className="para__go" disabled={!title.trim()}>
                    Add action
                  </button>
                </form>
              )}
            </section>
          ),
      )}

      {done.length > 0 && (
        <section className="para__sec">
          <button className="para__disclose" aria-expanded={showDone} onClick={() => setShowDone((v) => !v)}>
            {showDone ? '▾' : '▸'} Done <span className="para__n tnum">{done.length}</span>
          </button>
          {showDone && (
            <ul className="para__actions para__actions--done">
              {done.map((r) => (
                <li key={r.id} className="para__action">
                  <button type="button" className="para__action-title is-done" onClick={() => openEditor(r.id)}>
                    {r.title}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </section>
      )}

        </div>
      )}
    </article>
  );
}

/** One action: tick, title, the one move it invites, and under it why · when · where. */
function ActionRow({ r, onOpen, onDone, onStart }: { r: TaskRow; onOpen: () => void; onDone: () => void; onStart?: () => void }) {
  return (
    <li className="para__action">
      <div className="para__action-top">
        <button type="button" className="work__tick" onClick={onDone} aria-label={`Mark ${r.title} done`} title="Mark done" />
        <span className="dot" style={{ width: 7, height: 7, background: KINDS[r.kind].dot }} />
        <button type="button" className="para__action-title" onClick={onOpen}>
          {r.title}
        </button>
        {r.urgent && <span className="para__chip para__chip--urgent">Urgent</span>}
        {r.important && <span className="para__chip">Important</span>}
        {onStart && (
          <button type="button" className="work__start" onClick={onStart}>
            Start
          </button>
        )}
      </div>
      <ContextLine ctx={r.context} onClarify={onOpen} />
    </li>
  );
}

/* ------------------------------------------------------------------ area */

function AreaPane({
  area: a,
  health,
  projects,
  allTasks,
  requirements,
  onOpenProject,
}: {
  area: Client;
  health: ClientHealth | null;
  projects: Project[];
  allTasks: TaskRow[];
  requirements: Requirement[];
  onOpenProject: (id: string) => void;
}) {
  const { patchArea, addProject } = useActions();
  const [newName, setNewName] = useState('');
  const name = useDraft(a.name, (v) => v && patchArea(a.id, { name: v }));
  const standard = useDraft(a.standard, (v) => patchArea(a.id, { standard: v }));
  const live = projects.filter((p) => !p.archivedAt);
  const finished = projects.length - live.length;
  const archived = a.archivedAt != null;

  return (
    <article className="para__pane">
      <header className="para__panehead">
        <RailButton />
        <span className="dot" style={{ width: 10, height: 10, background: a.color }} />
        <input className="para__name serif" aria-label="Area name" {...name} onKeyDown={(e) => e.key === 'Enter' && e.currentTarget.blur()} />
        <span className="para__tag">{archived ? 'Archived' : AREA_KIND_LABEL[a.kind]}</span>
      </header>

      <div className="para__facts">
        <label className="para__fact para__fact--wide">
          <span className="para__factkey">Why · the standard to keep</span>
          <textarea className="para__textarea" rows={2} placeholder="An area never finishes — what does ‘kept up’ look like? Projects here inherit it as their why." {...standard} />
        </label>
        <label className="para__fact">
          <span className="para__factkey">When · touch at least every</span>
          <span className="para__fact-inline">
            <input
              className="para__field para__field--num tnum"
              type="number"
              min={1}
              value={a.expectedTouchDays ?? ''}
              placeholder="—"
              onChange={(e) => {
                const n = parseInt(e.target.value, 10);
                patchArea(a.id, { expectedTouchDays: Number.isFinite(n) && n > 0 ? n : null });
              }}
            />
            days
          </span>
        </label>
        <label className="para__fact">
          <span className="para__factkey">Kind</span>
          <select className="para__field" value={a.kind} onChange={(e) => patchArea(a.id, { kind: e.target.value as ClientKind })}>
            <option value="area">Area of responsibility</option>
            <option value="client">Client</option>
            <option value="internal">Internal initiative</option>
          </select>
        </label>
      </div>

      {health && !archived && (
        <p className={'para__health' + (health.underserved ? ' is-late' : '')}>
          {health.daysSince == null ? 'Nothing completed here yet.' : `Last touched ${health.daysSince === 0 ? 'today' : `${health.daysSince} day${health.daysSince === 1 ? '' : 's'} ago`}.`}
          {health.underserved && ' That is past its standard — it needs a next action.'} {health.openCount} open · {health.doneCount} done.
        </p>
      )}

      <section className="para__sec">
        <h2 className="para__sechead">
          Projects <span className="para__n tnum">{live.length}</span>
          {finished > 0 && <span className="para__secnote">{finished} in the archive</span>}
        </h2>
        <ul className="para__cards">
          {live.map((p) => {
            const prog = selectProjectProgress(p.id, allTasks, requirements);
            return (
              <li key={p.id}>
                <button className="para__card" onClick={() => onOpenProject(p.id)}>
                  <span className="para__item-row">
                    <span className="dot" style={{ width: 7, height: 7, background: p.color }} />
                    <span className="para__item-name">{p.name}</span>
                    <span className={'para__item-due tnum' + (p.due && isPast(p.due) ? ' is-late' : '')}>{p.due ? fmtDate(p.due) : 'no date'}</span>
                  </span>
                  <span className="para__card-outcome">{p.outcome || 'No outcome yet'}</span>
                  <span className="para__track" aria-hidden>
                    <span className="para__fill" style={{ width: `${prog.pct}%` }} />
                  </span>
                </button>
              </li>
            );
          })}
        </ul>
        {!archived && (
          <form
            className="para__addaction"
            onSubmit={(e) => {
              e.preventDefault();
              if (!newName.trim()) return;
              addProject({ name: newName.trim(), clientId: a.id });
              setNewName('');
            }}
          >
            <input className="para__input" value={newName} onChange={(e) => setNewName(e.target.value)} placeholder="Start a project in this area" aria-label="New project name" />
            <button type="submit" className="para__go" disabled={!newName.trim()}>
              Create
            </button>
          </form>
        )}
      </section>

      <footer className="para__panefoot">
        <button className="para__ghost" onClick={() => patchArea(a.id, { archived: !archived })}>
          {archived ? 'Restore area' : 'Archive area'}
        </button>
      </footer>
    </article>
  );
}

/* ------------------------------------------------- resources / archive */

function ResourcesPane({ allTasks, projects }: { allTasks: TaskRow[]; projects: Project[] }) {
  const { openEditor } = useActions();
  const groups = useMemo(() => {
    const m = new Map<string, { label: string; color: string; items: { url: string; label: string; taskId: string; taskTitle: string }[] }>();
    const seen = new Set<string>();
    for (const r of allTasks) {
      for (const l of r.links) {
        const key = (r.projectId ?? 'none') + '|' + l.url;
        if (seen.has(key)) continue;
        seen.add(key);
        const gk = r.projectId ?? 'none';
        const proj = projects.find((p) => p.id === r.projectId);
        if (!m.has(gk)) m.set(gk, { label: proj?.name ?? 'No project', color: proj?.color ?? 'var(--tan)', items: [] });
        m.get(gk)!.items.push({ url: l.url, label: l.label || l.url, taskId: r.id, taskTitle: r.title });
      }
    }
    return [...m.values()];
  }, [allTasks, projects]);

  return (
    <article className="para__pane">
      <header className="para__panehead">
        <RailButton />
        <h2 className="para__name serif">Resources</h2>
      </header>
      <p className="para__lede">Every reference your work has gathered — the docs, threads and pages attached to actions — grouped by the project it serves. Attach a link to an action and it shows up here.</p>
      {groups.length === 0 && <p className="para__empty">No links attached to any action yet.</p>}
      {groups.map((g) => (
        <section className="para__sec" key={g.label}>
          <h2 className="para__sechead">
            <span className="dot" style={{ width: 7, height: 7, background: g.color }} /> {g.label} <span className="para__n tnum">{g.items.length}</span>
          </h2>
          <ul className="para__resources">
            {g.items.map((it) => (
              <li key={it.taskId + it.url} className="para__resource">
                <a href={it.url} target="_blank" rel="noreferrer noopener" className="para__resource-link">
                  {it.label} ↗
                </a>
                <button type="button" className="para__resource-from" onClick={() => openEditor(it.taskId)}>
                  from “{it.taskTitle}”
                </button>
              </li>
            ))}
          </ul>
        </section>
      ))}
    </article>
  );
}

function ArchivePane({ projects, areas, allTasks }: { projects: Project[]; areas: Client[]; allTasks: TaskRow[] }) {
  const { patchProject, patchArea } = useActions();
  return (
    <article className="para__pane">
      <header className="para__panehead">
        <RailButton />
        <h2 className="para__name serif">Archive</h2>
      </header>
      <p className="para__lede">Finished or set aside. Nothing here shows up in Work, pickers or filters — and nothing is lost: restore brings it straight back.</p>
      {projects.length + areas.length === 0 && <p className="para__empty">Nothing archived yet.</p>}
      <ul className="para__resources">
        {projects.map((p) => {
          const rows = allTasks.filter((r) => r.projectId === p.id);
          return (
            <li key={p.id} className="para__resource">
              <span className="para__resource-link">
                <span className="dot" style={{ width: 7, height: 7, background: p.color }} /> {p.name}
              </span>
              <span className="para__resource-from">
                project · {rows.filter((r) => r.stage === 'done').length}/{rows.length} done · archived {fmtDate(p.archivedAt!)}
              </span>
              <button className="para__ghost" onClick={() => patchProject(p.id, { archived: false })}>
                Restore
              </button>
            </li>
          );
        })}
        {areas.map((a) => (
          <li key={a.id} className="para__resource">
            <span className="para__resource-link">
              <span className="dot" style={{ width: 7, height: 7, background: a.color }} /> {a.name}
            </span>
            <span className="para__resource-from">
              {AREA_KIND_LABEL[a.kind].toLowerCase()} · archived {fmtDate(a.archivedAt!)}
            </span>
            <button className="para__ghost" onClick={() => patchArea(a.id, { archived: false })}>
              Restore
            </button>
          </li>
        ))}
      </ul>
    </article>
  );
}

function UnfiledPane({ rows }: { rows: TaskRow[] }) {
  const { openEditor } = useActions();
  return (
    <article className="para__pane">
      <header className="para__panehead">
        <RailButton />
        <h2 className="para__name serif">Unfiled actions</h2>
      </header>
      <p className="para__lede">Open actions that belong to no project, so they have no inherited why. File each one — or leave it, if it really is a one-off.</p>
      {rows.length === 0 && <p className="para__empty">Everything open is filed under a project.</p>}
      <ul className="para__actions">
        {rows.map((r) => (
          <li key={r.id} className="para__action">
            <div className="para__action-top">
              <span className="dot" style={{ width: 7, height: 7, background: KINDS[r.kind].dot }} />
              <button type="button" className="para__action-title" onClick={() => openEditor(r.id)}>
                {r.title}
              </button>
              <ProjectPicker taskId={r.id} projectId={null} />
            </div>
            <ContextLine ctx={r.context} onClarify={() => openEditor(r.id)} />
          </li>
        ))}
      </ul>
    </article>
  );
}
