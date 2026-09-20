/**
 * Derives the per-slot identities of a worker host: worker IDs, advertised
 * browser-profile binding, and on-disk persistent profile directories.
 *
 * A single logical named profile (the --profile value) is what the server
 * sees for profile affinity. With N > 1 workers, each slot gets its own
 * on-disk copy <profile>-1..N because one Chromium profile directory cannot
 * be shared between concurrent browser processes; all N slots still advertise
 * the same logical profile name and can therefore claim profile-bound tasks.
 */

import * as path from 'path';

export interface WorkerSlot {
  /** Unique worker identity, e.g. "myhost-production-account-2". */
  workerId: string;
  /** Logical named profile advertised to the server (profile affinity). */
  browserProfileId: string;
  /** On-disk persistent Chromium profile directory for this slot. */
  profileDir: string;
}

export interface WorkerSlotOptions {
  hostname: string;
  profile: string;
  workers: number;
  profilesDir: string;
  workerIdPrefix?: string;
}

const PROFILE_NAME_PATTERN = /^[A-Za-z0-9._-]+$/;

export function deriveWorkerSlots(options: WorkerSlotOptions): WorkerSlot[] {
  const profile = options.profile.trim();
  if (!PROFILE_NAME_PATTERN.test(profile)) {
    throw new Error(
      `Invalid profile name "${options.profile}": use only letters, digits, dot, underscore, and dash`,
    );
  }
  const workers = options.workers;
  if (!Number.isInteger(workers) || workers < 1 || workers > 50) {
    throw new Error(`workers must be an integer between 1 and 50 (got ${options.workers})`);
  }
  const prefix = (options.workerIdPrefix ?? options.hostname).trim() || 'worker';
  const slots: WorkerSlot[] = [];
  for (let i = 0; i < workers; i++) {
    const dirName = workers === 1 ? profile : `${profile}-${i + 1}`;
    slots.push({
      workerId: `${prefix}-${dirName}`,
      browserProfileId: profile,
      profileDir: path.join(options.profilesDir, dirName),
    });
  }
  return slots;
}
