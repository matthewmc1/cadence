/**
 * Generation backend with two interchangeable engines (pattern from ~/loam):
 *  - WebLLM  — fully in-browser via WebGPU (Gemma 2 2B / Gemma 3 1B). Primary.
 *    Weights + wasm come from the Cadence server (/models/) when it hosts them,
 *    otherwise from WebLLM's upstream CDN (HuggingFace / GitHub) on first use.
 *  - Ollama  — via the server's AI gateway (/api/v1/ai/*). The browser never
 *    talks to Ollama itself; the server forwards, applies policy, logs egress.
 *
 * Both are loaded lazily, only when the user runs the scheduler.
 */
import type { AppConfig } from '@mlc-ai/web-llm';
import { apiBase, ApiError } from '../api/client';
import type { LlmModelOption } from './config';

export interface ChatMessage {
  role: 'system' | 'user' | 'assistant';
  content: string;
}

type ProgressFn = (text: string, ratio?: number) => void;

/* ------------------------------ AI gateway ------------------------------ */

/** GET /api/v1/ai/status — what this deployment offers. */
export interface AiStatus {
  ollama: { configured: boolean; reachable: boolean; models: string[] };
  webllm: { localModels: boolean; models: string[] };
  policy: 'local' | 'local+cloud';
}

const OFFLINE_STATUS: AiStatus = {
  ollama: { configured: false, reachable: false, models: [] },
  webllm: { localModels: false, models: [] },
  policy: 'local',
};

// Same conventions as src/api/client.ts: cookie identity, JSON error envelope.
async function gateway<T>(method: 'GET' | 'POST', path: string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${apiBase}${path}`, {
      method,
      headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'include',
    });
  } catch {
    throw new ApiError('Could not reach the Cadence server', 0);
  }
  const text = await res.text();
  const data = text ? JSON.parse(text) : undefined;
  if (!res.ok) {
    const err = data?.error;
    throw new ApiError(err?.message ?? `Request failed (${res.status})`, res.status, err?.field);
  }
  return data as T;
}

/** Providers available right now; an unreachable server reads as "nothing configured". */
export async function aiStatus(): Promise<AiStatus> {
  try {
    return await gateway<AiStatus>('GET', '/api/v1/ai/status');
  } catch {
    return OFFLINE_STATUS;
  }
}

/* ------------------------------- Ollama --------------------------------- */

export async function ollamaAvailable(): Promise<boolean> {
  const st = await aiStatus();
  return st.ollama.configured && st.ollama.reachable;
}

/** Names of models installed on the server's Ollama (empty if unreachable). */
export async function ollamaList(): Promise<string[]> {
  return (await aiStatus()).ollama.models;
}

export function ollamaHasModel(names: string[], model: string): boolean {
  const base = model.split(':')[0];
  return names.some((n) => n === model || n.startsWith(base));
}

async function ollamaChat(model: string, messages: ChatMessage[], json: boolean): Promise<string> {
  // The server adds stream:false + temperature and forwards to Ollama.
  const data = await gateway<{ message?: { content?: string } }>('POST', '/api/v1/ai/chat', {
    model,
    messages,
    format: json ? 'json' : undefined,
  });
  return data.message?.content ?? '';
}

/* ------------------------------- WebLLM --------------------------------- */

// kept loosely-typed so @mlc-ai/web-llm stays a lazy import (out of the main bundle)
let webllmEngine: {
  chat: { completions: { create: (req: unknown) => Promise<unknown> } };
} | null = null;
let webllmModel = '';
let webllmSource: 'server' | 'upstream' = 'upstream';

/** Where the last-loaded WebLLM artifacts came from. */
export function webllmArtifactSource(): 'server' | 'upstream' {
  return webllmSource;
}

/**
 * Server-hosted artifacts are useful only if WebLLM's own (cookie-less) fetch
 * can read them: that holds when the app and API share an origin, not for the
 * split-origin dev setup. Probe with the same kind of request WebLLM makes.
 */
async function serverHostsModel(model: string): Promise<boolean> {
  try {
    const r = await fetch(`${apiBase}/models/${encodeURIComponent(model)}/mlc-chat-config.json`, { method: 'HEAD' });
    return r.ok;
  } catch {
    return false;
  }
}

/**
 * Mirror prebuiltAppConfig for one model, pointing `model` (weights) and
 * `model_lib` (wasm) at the Cadence server. Layout: /models/<id>/… and
 * /models/lib/<wasm>. WebLLM appends `resolve/main/` to the weights URL, which
 * the server strips.
 *
 * The URLs must be absolute: web-llm resolves each with `new URL(url)` and no
 * base, which throws for a relative path — and in the embedded build apiBase
 * is "" (same origin), exactly the case where the server hosts the weights.
 */
function localAppConfig(prebuilt: AppConfig, model: string): AppConfig | undefined {
  const rec = prebuilt.model_list.find((m) => m.model_id === model);
  if (!rec) return undefined;
  const wasm = rec.model_lib.slice(rec.model_lib.lastIndexOf('/') + 1);
  const abs = (path: string) => new URL(`${apiBase}${path}`, window.location.href).href;
  return {
    ...prebuilt,
    model_list: [{ ...rec, model: abs(`/models/${model}/`), model_lib: abs(`/models/lib/${wasm}`) }],
  };
}

async function loadWebllm(model: string, hosted: boolean, onProgress?: ProgressFn): Promise<void> {
  if (webllmEngine && webllmModel === model) return;
  const webllm = await import('@mlc-ai/web-llm');
  let appConfig: AppConfig | undefined;
  if (hosted && (await serverHostsModel(model))) appConfig = localAppConfig(webllm.prebuiltAppConfig, model);
  webllmSource = appConfig ? 'server' : 'upstream';
  onProgress?.(appConfig ? 'Loading model from your Cadence server…' : 'Loading model (weights from HuggingFace on first use)…');
  const worker = new Worker(new URL('./llm.worker.ts', import.meta.url), { type: 'module' });
  webllmEngine = (await webllm.CreateWebWorkerMLCEngine(worker, model, {
    appConfig,
    initProgressCallback: (p: { text: string; progress: number }) => onProgress?.(p.text, p.progress),
  })) as typeof webllmEngine;
  webllmModel = model;
}

async function webllmChat(messages: ChatMessage[], json: boolean): Promise<string> {
  if (!webllmEngine) throw new Error('WebLLM engine not loaded');
  // WebLLM's grammar-based JSON mode throws for Gemma builds, so we nudge with
  // the prompt and parse defensively via parseJSON instead.
  const msgs =
    json && messages.length
      ? messages.map((m, i) =>
          i === messages.length - 1 ? { ...m, content: m.content + '\n\nRespond with ONLY a single valid JSON object.' } : m,
        )
      : messages;
  const reply = (await webllmEngine.chat.completions.create({
    messages: msgs,
    temperature: 0.2,
    max_tokens: 1200,
  })) as { choices: { message: { content: string } }[] };
  return reply.choices[0]?.message?.content ?? '';
}

/* ------------------------------ unified --------------------------------- */

let active: LlmModelOption | null = null;

export function activeModel(): LlmModelOption | null {
  return active;
}

export function webgpuAvailable(): boolean {
  return typeof navigator !== 'undefined' && 'gpu' in navigator;
}

/** Load (or warm up) the chosen generation backend. */
export async function loadLlm(opt: LlmModelOption, onProgress?: ProgressFn): Promise<void> {
  onProgress?.(opt.backend === 'ollama' ? 'Checking the server…' : 'Checking WebGPU…');
  const st = await aiStatus();
  if (opt.backend === 'ollama') {
    if (!st.ollama.configured) throw new Error('Ollama isn’t configured on the server (set OLLAMA_URL) — switch to an in-browser model.');
    if (!st.ollama.reachable) throw new Error('The server can’t reach Ollama — is it running?');
    if (!ollamaHasModel(st.ollama.models, opt.model)) throw new Error(`Model not found on the server — run: ollama pull ${opt.model}`);
  } else {
    if (!webgpuAvailable()) throw new Error('WebGPU is not available in this browser — switch to an Ollama model.');
    await loadWebllm(opt.model, st.webllm.localModels && st.webllm.models.includes(opt.model), onProgress);
  }
  active = opt;
}

export async function chat(messages: ChatMessage[], json = false): Promise<string> {
  if (!active) throw new Error('No model loaded');
  return active.backend === 'ollama' ? ollamaChat(active.model, messages, json) : webllmChat(messages, json);
}

/** Ask the model for JSON and parse it defensively. */
export async function chatJSON<T>(system: string, user: string, fallback: T): Promise<T> {
  const raw = await chat(
    [
      { role: 'system', content: system },
      { role: 'user', content: user },
    ],
    true,
  );
  return parseJSON(raw, fallback);
}

export function parseJSON<T>(raw: string, fallback: T): T {
  try {
    return JSON.parse(raw) as T;
  } catch {
    const start = raw.indexOf('{');
    const end = raw.lastIndexOf('}');
    if (start >= 0 && end > start) {
      try {
        return JSON.parse(raw.slice(start, end + 1)) as T;
      } catch {
        /* fall through */
      }
    }
    return fallback;
  }
}
