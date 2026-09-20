/**
 * WorkerHost — the supported real-browser worker runtime.
 *
 * Owns the worker credential (WORKER_API_KEY), one or more worker slots, and
 * each slot's named persistent Chromium profile. Every slot runs the
 * production Worker claim loop (src/worker/Worker.ts); claimed tasks execute
 * inside real Chromium pages through the page driver (page-driver.ts) and the
 * in-page runtime bundle (src/worker-page/entry.ts).
 *
 * The credential never leaves this process: pages interact with the server
 * exclusively through the host-mediated __ocWorkerBridge channel.
 */

import * as os from 'os';
import { chromium, type BrowserContext, type Page } from 'playwright';
import { Worker, type RuleExecutorResult } from '../worker/Worker';
import { buildWorkerPageBundle } from './bundle';
import { createPageRuleExecutor } from './page-driver';
import { deriveWorkerSlots, type WorkerSlot } from './slots';

export interface WorkerHostConfig {
  /** AegisCrawler server base URL, e.g. http://127.0.0.1:8080 */
  serverUrl: string;
  /** WORKER_API_KEY value (read from a 0600 key file by the CLI). */
  apiKey: string;
  /** Logical named profile to serve (server-side profile affinity). */
  profile: string;
  /** Parent directory for the per-slot persistent Chromium profiles. */
  profilesDir: string;
  /** Number of concurrent worker slots (each executes one task at a time). */
  workers: number;
  headless: boolean;
  workerIdPrefix?: string;
  pollIntervalMs?: number;
  heartbeatIntervalMs?: number;
  verbose?: boolean;
  /** Optional host-only observer used by qualification harnesses. */
  onPageComplete?: (page: Page, result: RuleExecutorResult) => Promise<void> | void;
}

interface RunningSlot {
  slot: WorkerSlot;
  context: BrowserContext;
  worker: Worker;
  run: Promise<void>;
}

export class WorkerHost {
  private config: WorkerHostConfig;
  private slots: RunningSlot[] = [];
  private started = false;

  constructor(config: WorkerHostConfig) {
    this.config = config;
  }

  /** Fails fast when the server does not advertise worker protocol v2. */
  private async checkCapabilities(): Promise<void> {
    const url = `${this.config.serverUrl.replace(/\/$/, '')}/api/v1/capabilities`;
    let body: { workerProtocolVersions?: unknown };
    try {
      const res = await fetch(url);
      if (!res.ok) {
        throw new Error(`HTTP ${res.status}`);
      }
      body = (await res.json()) as { workerProtocolVersions?: unknown };
    } catch (err) {
      throw new Error(
        `cannot reach ${url}: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
    const versions = Array.isArray(body.workerProtocolVersions) ? body.workerProtocolVersions : [];
    if (!versions.includes('v2')) {
      throw new Error(
        `server does not advertise worker protocol v2 (got ${JSON.stringify(versions)}); `
        + 'start the server with FEATURE_WORKER_PROTOCOL_V2=true',
      );
    }
  }

  async start(): Promise<void> {
    if (this.started) {
      throw new Error('WorkerHost already started');
    }
    this.started = true;
    await this.checkCapabilities();
    const bundle = await buildWorkerPageBundle();
    const slots = deriveWorkerSlots({
      hostname: os.hostname(),
      profile: this.config.profile,
      workers: this.config.workers,
      profilesDir: this.config.profilesDir,
      workerIdPrefix: this.config.workerIdPrefix,
    });
    for (const slot of slots) {
      const context = await chromium.launchPersistentContext(slot.profileDir, {
        headless: this.config.headless,
      });
      const worker = new Worker({
        baseUrl: this.config.serverUrl,
        workerId: slot.workerId,
        browserProfileId: slot.browserProfileId,
        apiKey: this.config.apiKey,
        pollIntervalMs: this.config.pollIntervalMs,
        heartbeatIntervalMs: this.config.heartbeatIntervalMs,
        ruleExecutor: createPageRuleExecutor({
          context,
          bundle,
          verbose: this.config.verbose,
          onPageComplete: this.config.onPageComplete,
        }),
      });
      const run = worker.start();
      this.slots.push({ slot, context, worker, run });
      if (this.config.verbose) {
        // eslint-disable-next-line no-console
        console.log(
          `[worker-host] slot ${slot.workerId} running (profile=${slot.browserProfileId}, dir=${slot.profileDir})`,
        );
      }
    }
    await Promise.all(this.slots.map((s) => s.run));
  }

  async stop(): Promise<void> {
    for (const { worker } of this.slots) {
      worker.stop();
    }
    await Promise.all(this.slots.map(({ run }) => run.catch(() => undefined)));
    for (const { context } of this.slots) {
      await context.close().catch(() => undefined);
    }
    this.slots = [];
  }
}
