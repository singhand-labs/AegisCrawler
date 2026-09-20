/// <reference types="vitest/globals" />
import * as path from 'node:path';
import { describe, it, expect } from 'vitest';
import { deriveWorkerSlots } from '../slots';

const base = {
  hostname: 'testhost',
  profile: 'production-account',
  profilesDir: '/tmp/profiles',
};

describe('deriveWorkerSlots', () => {
  it('derives a single unsuffixed slot for one worker', () => {
    expect(deriveWorkerSlots({ ...base, workers: 1 })).toEqual([{
      workerId: 'testhost-production-account',
      browserProfileId: 'production-account',
      profileDir: path.join('/tmp/profiles', 'production-account'),
    }]);
  });

  it('derives suffixed dirs and worker IDs but one logical profile for N workers', () => {
    const slots = deriveWorkerSlots({ ...base, workers: 3 });
    expect(slots).toHaveLength(3);
    expect(slots.map((s) => s.workerId)).toEqual([
      'testhost-production-account-1',
      'testhost-production-account-2',
      'testhost-production-account-3',
    ]);
    expect(slots.map((s) => s.profileDir)).toEqual([
      path.join('/tmp/profiles', 'production-account-1'),
      path.join('/tmp/profiles', 'production-account-2'),
      path.join('/tmp/profiles', 'production-account-3'),
    ]);
    // Profile affinity: every slot advertises the same logical named profile.
    expect(slots.every((s) => s.browserProfileId === 'production-account')).toBe(true);
  });

  it('honors a worker ID prefix override', () => {
    const [slot] = deriveWorkerSlots({ ...base, workers: 1, workerIdPrefix: 'fleet-a' });
    expect(slot.workerId).toBe('fleet-a-production-account');
  });

  it('rejects unsafe profile names', () => {
    for (const profile of ['', ' ', '../escape', 'a/b', 'a\\b', 'a b', 'a;b']) {
      expect(() => deriveWorkerSlots({ ...base, profile, workers: 1 })).toThrow(/profile name/i);
    }
  });

  it('rejects invalid worker counts', () => {
    for (const workers of [0, -1, 1.5, 51, Number.NaN]) {
      expect(() => deriveWorkerSlots({ ...base, workers })).toThrow(/workers/);
    }
  });
});
