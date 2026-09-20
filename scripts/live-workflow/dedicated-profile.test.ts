import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import {
  BAIDU_QUALIFICATION_PROFILE,
  PERSISTENT_PROFILE_APPROVAL,
  prepareDedicatedProfile,
  resolveDedicatedProfile,
} from './dedicated-profile';

const temporaryRoots: string[] = [];

afterEach(() => {
  for (const root of temporaryRoots.splice(0)) fs.rmSync(root, { recursive: true, force: true });
});

function approvedEnv(extra: NodeJS.ProcessEnv = {}): NodeJS.ProcessEnv {
  return {
    AEGIS_LIVE_BROWSER_PROFILE: BAIDU_QUALIFICATION_PROFILE,
    AEGIS_LIVE_ALLOW_PERSISTENT_PROFILE: PERSISTENT_PROFILE_APPROVAL,
    ...extra,
  };
}

describe('dedicated live-workflow browser profile', () => {
  it('is an explicit headed, human, Baidu-only opt-in', () => {
    const base = { env: approvedEnv(), headed: true, humanDemoEnabled: true };
    expect(resolveDedicatedProfile({ ...base, selection: ['baidu-search'], homeDir: '/safe/home' }))
      .toEqual({
        name: BAIDU_QUALIFICATION_PROFILE,
        profilesDir: path.join(path.resolve('/safe/home'), '.aegiscrawler', 'worker-profiles'),
        warmup: false,
      });
    expect(() => resolveDedicatedProfile({ ...base, env: approvedEnv({
      AEGIS_LIVE_ALLOW_PERSISTENT_PROFILE: undefined,
    }), selection: ['baidu-search'] })).toThrow(/approval/);
    expect(() => resolveDedicatedProfile({ ...base, headed: false, selection: ['baidu-search'] }))
      .toThrow(/headed/);
    expect(() => resolveDedicatedProfile({ ...base, selection: ['apple'] }))
      .toThrow(/baidu-search/);
  });

  it('does not allow arbitrary or personal browser profile paths', () => {
    expect(() => resolveDedicatedProfile({
      env: approvedEnv({ AEGIS_LIVE_BROWSER_PROFILE: '/home/user/.config/google-chrome/Default' }),
      headed: true,
      humanDemoEnabled: true,
      selection: ['baidu-search'],
    })).toThrow(/unsupported dedicated profile/);
  });

  it('allows headless profile reuse with an encrypted reusable recording', () => {
    expect(resolveDedicatedProfile({
      env: approvedEnv({ AEGIS_LIVE_REUSE_RECORDING: '1' }),
      headed: false,
      humanDemoEnabled: false,
      selection: ['baidu-search'],
      homeDir: '/safe/home',
    })).toEqual({
      name: BAIDU_QUALIFICATION_PROFILE,
      profilesDir: path.join(path.resolve('/safe/home'), '.aegiscrawler', 'worker-profiles'),
      warmup: false,
    });
    expect(() => resolveDedicatedProfile({
      env: { AEGIS_LIVE_REUSE_RECORDING: '1' },
      headed: false,
      humanDemoEnabled: false,
      selection: ['baidu-search'],
    })).toThrow(/requires the baidu-qualification dedicated profile/);
    expect(resolveDedicatedProfile({
      env: { AEGIS_LIVE_REUSE_RECORDING: '1' },
      headed: false,
      humanDemoEnabled: false,
      selection: ['quotes-by-tag'],
    })).toBeUndefined();
  });

  it('keeps headed Baidu reuse warm-up on the dedicated profile', () => {
    expect(resolveDedicatedProfile({
      env: approvedEnv({
        AEGIS_LIVE_REUSE_RECORDING: '1',
        AEGIS_LIVE_PROFILE_WARMUP: '1',
      }),
      headed: true,
      humanDemoEnabled: false,
      selection: ['baidu-search'],
      homeDir: '/safe/home',
    })).toEqual({
      name: BAIDU_QUALIFICATION_PROFILE,
      profilesDir: path.join(path.resolve('/safe/home'), '.aegiscrawler', 'worker-profiles'),
      warmup: true,
    });
  });

  it('creates and reuses a real directory but rejects a profile symlink', () => {
    const homeDir = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-dedicated-profile-test-'));
    temporaryRoots.push(homeDir);
    const config = resolveDedicatedProfile({
      env: approvedEnv({ AEGIS_LIVE_PROFILE_WARMUP: '1' }),
      headed: true,
      humanDemoEnabled: true,
      selection: ['baidu-search'],
      homeDir,
    });
    expect(config?.warmup).toBe(true);
    const profileDir = prepareDedicatedProfile(config!);
    expect(fs.statSync(profileDir).isDirectory()).toBe(true);
    expect(prepareDedicatedProfile(config!)).toBe(profileDir);

    fs.rmSync(profileDir, { recursive: true });
    fs.symlinkSync(homeDir, profileDir);
    expect(() => prepareDedicatedProfile(config!)).toThrow(/symlink/);
  });

  it('rejects warm-up without a selected persistent profile', () => {
    expect(() => resolveDedicatedProfile({
      env: { AEGIS_LIVE_PROFILE_WARMUP: '1' },
      headed: true,
      humanDemoEnabled: true,
      selection: ['baidu-search'],
    })).toThrow(/requires AEGIS_LIVE_BROWSER_PROFILE/);
  });
});
