/**
 * Generation backend with two interchangeable engines (pattern from ~/loam):
 *  - WebLLM  — fully in-browser via WebGPU (Gemma 2 2B / Gemma 3 1B). Primary.
 *  - Ollama  — local server (Gemma 3 4B, Qwen 2.5 7B, …). Fallback.
 *
 * Both are loaded lazily, only when the user runs the scheduler.
 */
import { OLLAMA_URL, type LlmModelOption } from './config';

export interface ChatMessage {
  role: 'system' | 'user' | 'assistant';
  content: string;
}

type ProgressFn = (text: string, ratio?: number) => void;

/* ------------------------------- Ollama --------------------------------- */

export async function ollamaAvailable(): Promise<boolean> {
  try {
    const r = await fetch(`${OLLAMA_URL}/api/tags`, { method: 'GET' });
    return r.ok;
  } catch {
    return false;
  }
}

/** Names of models installed in the local Ollama server (empty if unreachable). */
export async function ollamaList(): Promise<string[]> {
  try {
    const r = await fetch(`${OLLAMA_URL}/api/tags`);
    if (!r.ok) return [];
    const data = (await r.json()) as { models?: { name: string }[] };
    return (data.models ?? []).map((m) => m.name);
  } catch {
    return [];
  }
}

export async function ollamaHasModel(model: string): Promise<boolean> {
  const names = await ollamaList();
  const base = model.split(':')[0];
  return names.some((n) => n === model || n.startsWith(base));
}

async function ollamaChat(model: string, messages: ChatMessage[], json: boolean): Promise<string> {
  const r = await fetch(`${OLLAMA_URL}/api/chat`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      model,
      messages,
      stream: false,
      format: json ? 'json' : undefined,
      options: { temperature: 0.2 },
    }),
  });
  if (!r.ok) throw new Error(`Ollama ${r.status}: ${await r.text()}`);
  const data = (await r.json()) as { message?: { content?: string } };
  return data.message?.content ?? '';
}

/* ------------------------------- WebLLM --------------------------------- */

// kept loosely-typed so @mlc-ai/web-llm stays a lazy import (out of the main bundle)
let webllmEngine: {
  chat: { completions: { create: (req: unknown) => Promise<unknown> } };
} | null = null;
let webllmModel = '';

async function loadWebllm(model: string, onProgress?: ProgressFn): Promise<void> {
  if (webllmEngine && webllmModel === model) return;
  const webllm = await import('@mlc-ai/web-llm');
  const worker = new Worker(new URL('./llm.worker.ts', import.meta.url), { type: 'module' });
  webllmEngine = (await webllm.CreateWebWorkerMLCEngine(worker, model, {
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
  if (opt.backend === 'ollama') {
    onProgress?.('Connecting to Ollama…');
    if (!(await ollamaAvailable())) throw new Error('Ollama is not running on localhost:11434');
    if (!(await ollamaHasModel(opt.model))) throw new Error(`Model not found — run: ollama pull ${opt.model}`);
  } else {
    if (!webgpuAvailable()) throw new Error('WebGPU is not available in this browser — switch to an Ollama model.');
    await loadWebllm(opt.model, onProgress);
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
