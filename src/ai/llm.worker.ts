/// <reference lib="webworker" />
/** WebLLM worker host — runs the model in-browser via WebGPU. */
import { WebWorkerMLCEngineHandler } from '@mlc-ai/web-llm';

const handler = new WebWorkerMLCEngineHandler();
self.onmessage = (msg: MessageEvent) => handler.onmessage(msg);
