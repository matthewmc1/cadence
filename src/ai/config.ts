/**
 * Local-AI configuration for Cadence's scheduling assistant. Everything here
 * runs on-device — either fully in-browser on the GPU (WebGPU / WebLLM) or
 * against a local Ollama server. No task data ever leaves the machine.
 *
 * Pattern mirrors ~/loam: WebGPU is the zero-install primary; Ollama is the
 * local fallback for bigger / newer models.
 */
export interface LlmModelOption {
  backend: 'webllm' | 'ollama';
  /** model id passed to the backend */
  model: string;
  label: string;
  note: string;
  /** approx download size, for the UI */
  size: string;
}

export const LLM_MODELS: LlmModelOption[] = [
  // In-browser (WebGPU) — primary, no install, fully private
  { backend: 'webllm', model: 'gemma-2-2b-it-q4f16_1-MLC-1k', label: 'Gemma 2 · 2B', note: 'balanced · in-browser', size: '~1.4 GB' },
  { backend: 'webllm', model: 'gemma3-1b-it-q4f16_1-MLC', label: 'Gemma 3 · 1B', note: 'fastest · in-browser', size: '~0.9 GB' },
  // Ollama — local server, larger / newer models
  { backend: 'ollama', model: 'gemma3:4b', label: 'Gemma 3 · 4B', note: 'most capable · Ollama', size: '3.3 GB' },
  { backend: 'ollama', model: 'qwen2.5:7b', label: 'Qwen 2.5 · 7B', note: 'strong reasoning · Ollama', size: '4.7 GB' },
  { backend: 'ollama', model: 'gemma3:1b', label: 'Gemma 3 · 1B', note: 'fastest · Ollama', size: '0.8 GB' },
];

export const WEBLLM_MODELS = LLM_MODELS.filter((m) => m.backend === 'webllm');
export const OLLAMA_MODELS = LLM_MODELS.filter((m) => m.backend === 'ollama');

/** Primary path = in-browser WebGPU; fallback = Ollama for bigger models. */
export const WEBLLM_DEFAULT = WEBLLM_MODELS[0];
export const OLLAMA_DEFAULT = OLLAMA_MODELS[0];

export const OLLAMA_URL = 'http://localhost:11434';

const STORE_KEY = 'cadence-ai-model';

export function modelKey(opt: LlmModelOption): string {
  return opt.backend + ':' + opt.model;
}

export function loadSavedModel(webgpu: boolean): LlmModelOption {
  try {
    const raw = localStorage.getItem(STORE_KEY);
    if (raw) {
      const found = LLM_MODELS.find((m) => modelKey(m) === raw);
      if (found) return found;
    }
  } catch {
    /* ignore */
  }
  return webgpu ? WEBLLM_DEFAULT : OLLAMA_DEFAULT;
}

export function saveModel(opt: LlmModelOption): void {
  try {
    localStorage.setItem(STORE_KEY, modelKey(opt));
  } catch {
    /* ignore */
  }
}
