import '../styles/ai.css';
import { useEffect, useState } from 'react';
import { useApp, useActions } from '../state/store';
import { LLM_MODELS, WEBLLM_MODELS, OLLAMA_MODELS, loadSavedModel, saveModel, modelKey, type LlmModelOption } from '../ai/config';
import { loadLlm, webgpuAvailable, ollamaAvailable, ollamaList } from '../ai/llm';
import { scheduleBacklog } from '../ai/scheduler';

type Phase = 'idle' | 'loading' | 'thinking' | 'error';

function ModelRow({ m, on, disabled, note, onPick }: { m: LlmModelOption; on: boolean; disabled: boolean; note?: string; onPick: () => void }) {
  return (
    <button className={'ai__opt' + (on ? ' is-on' : '')} disabled={disabled} onClick={onPick}>
      <span className="ai__opt-main">
        <span className="ai__opt-label">{m.label}</span>
        <span className="ai__opt-note">{m.note} · {m.size}</span>
      </span>
      {on && <span className="ai__opt-check">✓</span>}
      {!on && note && <span className="ai__opt-flag">{note}</span>}
    </button>
  );
}

export function AiSchedule() {
  const { backlogTotal } = useApp();
  const { schedContext, applySchedule, autoArrange } = useActions();

  const [webgpu] = useState(() => webgpuAvailable());
  const [model, setModel] = useState<LlmModelOption>(() => loadSavedModel(webgpuAvailable()));
  const [phase, setPhase] = useState<Phase>('idle');
  const [progress, setProgress] = useState(0);
  const [msg, setMsg] = useState('');
  const [err, setErr] = useState<string | null>(null);
  const [open, setOpen] = useState(false);
  const [ollamaUp, setOllamaUp] = useState<boolean | null>(null);
  const [ollamaModels, setOllamaModels] = useState<string[]>([]);

  useEffect(() => {
    let alive = true;
    void ollamaAvailable().then(async (up) => {
      if (!alive) return;
      setOllamaUp(up);
      if (up) setOllamaModels(await ollamaList());
    });
    return () => {
      alive = false;
    };
  }, []);

  const busy = phase === 'loading' || phase === 'thinking';

  const pick = (opt: LlmModelOption) => {
    setModel(opt);
    saveModel(opt);
    setOpen(false);
    setErr(null);
  };

  const run = async () => {
    if (busy) return;
    setErr(null);
    try {
      setPhase('loading');
      setProgress(0);
      setMsg(model.backend === 'webllm' ? 'Loading model…' : 'Connecting to Ollama…');
      await loadLlm(model, (text, ratio) => {
        setMsg(text);
        if (typeof ratio === 'number') setProgress(ratio);
      });
      const { tasks, ctx } = schedContext();
      if (!tasks.length) {
        setPhase('idle');
        return;
      }
      setPhase('thinking');
      setMsg(`Planning ${tasks.length} task${tasks.length === 1 ? '' : 's'} around your energy…`);
      const assignments = await scheduleBacklog(tasks, ctx);
      applySchedule(assignments);
      setPhase('idle');
      setProgress(0);
      setMsg('');
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'Scheduling failed');
      setPhase('error');
    }
  };

  const installed = (m: LlmModelOption) => ollamaModels.some((n) => n === m.model || n.startsWith(m.model.split(':')[0]));

  return (
    <div className="ai">
      <button className="ai__run pill-btn" onClick={run} disabled={!backlogTotal || busy}>
        <span className="spark">✦</span>
        {phase === 'loading' ? 'Loading model…' : phase === 'thinking' ? 'Planning…' : 'Plan my week with AI'}
      </button>

      {busy && model.backend === 'webllm' && (
        <div className="ai__progress" aria-hidden>
          <div className="ai__bar" style={{ width: `${Math.max(4, Math.round(progress * 100))}%` }} />
        </div>
      )}
      {busy && msg && <div className="ai__msg">{msg}</div>}
      {err && <div className="ai__err">{err}</div>}

      <div className="ai__meta">
        <button className="ai__model" onClick={() => setOpen((o) => !o)} aria-haspopup="menu" aria-expanded={open}>
          {model.backend === 'webllm' ? '◴' : '⬡'} {model.label}
          <span className="ai__chev" aria-hidden>⌄</span>
        </button>
        <button className="ai__quick" onClick={autoArrange} disabled={!backlogTotal} title="Instant heuristic placement (no model)">
          quick arrange
        </button>
      </div>

      {open && (
        <div className="ai__pop" role="menu">
          <div className="ai__pop-head">Scheduling model — runs on your device</div>
          <div className="ai__group">
            In-browser · WebGPU{!webgpu && <span className="ai__group-warn"> · unavailable here</span>}
          </div>
          {WEBLLM_MODELS.map((m) => (
            <ModelRow key={modelKey(m)} m={m} on={modelKey(m) === modelKey(model)} disabled={!webgpu} onPick={() => pick(m)} />
          ))}
          <div className="ai__group">
            Local server · Ollama
            {ollamaUp === false && <span className="ai__group-warn"> · not running</span>}
            {ollamaUp === null && <span className="ai__group-warn"> · checking…</span>}
          </div>
          {OLLAMA_MODELS.map((m) => (
            <ModelRow
              key={modelKey(m)}
              m={m}
              on={modelKey(m) === modelKey(model)}
              disabled={ollamaUp === false}
              note={ollamaUp && !installed(m) ? 'pull needed' : undefined}
              onPick={() => pick(m)}
            />
          ))}
          <div className="ai__pop-foot">{LLM_MODELS.length} models · nothing leaves your machine</div>
        </div>
      )}
    </div>
  );
}
