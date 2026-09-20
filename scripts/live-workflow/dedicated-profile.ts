import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import type { LiveWorkflowScenarioName } from './human-demonstration';

export const BAIDU_QUALIFICATION_PROFILE = 'baidu-qualification';
export const PERSISTENT_PROFILE_APPROVAL = 'I approve persistent Aegis browser profile use';

export interface DedicatedProfileConfig {
  name: typeof BAIDU_QUALIFICATION_PROFILE;
  profilesDir: string;
  warmup: boolean;
}

interface DedicatedProfileOptions {
  env: NodeJS.ProcessEnv;
  headed: boolean;
  humanDemoEnabled: boolean;
  selection: readonly LiveWorkflowScenarioName[];
  homeDir?: string;
}

/**
 * Resolve the one supported persistent qualification profile. The caller may
 * opt in only by name; arbitrary profile paths (especially a normal Chrome
 * profile) are deliberately outside this harness's authority.
 */
export function resolveDedicatedProfile(options: DedicatedProfileOptions): DedicatedProfileConfig | undefined {
  const requested = options.env.AEGIS_LIVE_BROWSER_PROFILE?.trim();
  const warmup = options.env.AEGIS_LIVE_PROFILE_WARMUP === '1';
  const reusableBaiduRecording = options.selection.length === 1
    && options.selection[0] === 'baidu-search'
    && (options.env.AEGIS_LIVE_SAVE_RECORDING === '1'
      || options.env.AEGIS_LIVE_REUSE_RECORDING === '1');
  if (!requested) {
    if (warmup) throw new Error('AEGIS_LIVE_PROFILE_WARMUP=1 requires AEGIS_LIVE_BROWSER_PROFILE');
    if (reusableBaiduRecording) {
      throw new Error(`reusable recording mode requires the ${BAIDU_QUALIFICATION_PROFILE} dedicated profile`);
    }
    return undefined;
  }
  if (requested !== BAIDU_QUALIFICATION_PROFILE) {
    throw new Error(`unsupported dedicated profile "${requested}"; use ${BAIDU_QUALIFICATION_PROFILE}`);
  }
  if (options.env.AEGIS_LIVE_ALLOW_PERSISTENT_PROFILE !== PERSISTENT_PROFILE_APPROVAL) {
    throw new Error('persistent Aegis browser profile approval is required');
  }
  const reuseWithoutRecording = options.env.AEGIS_LIVE_REUSE_RECORDING === '1';
  if (!reuseWithoutRecording && (!options.headed || !options.humanDemoEnabled)) {
    throw new Error('the dedicated Baidu profile requires headed real-human demonstration mode');
  }
  if (options.selection.length !== 1 || options.selection[0] !== 'baidu-search') {
    throw new Error('the dedicated profile is restricted to the baidu-search qualification');
  }

  const homeDir = path.resolve(options.homeDir ?? os.homedir());
  const profilesDir = path.join(homeDir, '.aegiscrawler', 'worker-profiles');
  return { name: BAIDU_QUALIFICATION_PROFILE, profilesDir, warmup };
}

/** Create the dedicated directory without following a profile-level symlink. */
export function prepareDedicatedProfile(config: DedicatedProfileConfig): string {
  assertSafeProfilesRoot(config.profilesDir);
  fs.mkdirSync(config.profilesDir, { recursive: true, mode: 0o700 });
  const profileDir = path.join(config.profilesDir, config.name);
  if (fs.existsSync(profileDir)) {
    const stat = fs.lstatSync(profileDir);
    if (stat.isSymbolicLink()) throw new Error(`dedicated browser profile must not be a symlink: ${profileDir}`);
    if (!stat.isDirectory()) throw new Error(`dedicated browser profile is not a directory: ${profileDir}`);
  } else {
    fs.mkdirSync(profileDir, { mode: 0o700 });
  }
  return profileDir;
}

function assertSafeProfilesRoot(profilesDir: string): void {
  const resolved = path.resolve(profilesDir);
  const parsed = path.parse(resolved);
  if (resolved === parsed.root || resolved === path.resolve(os.homedir())) {
    throw new Error(`refusing unsafe dedicated browser profiles root: ${resolved}`);
  }
  if (path.basename(resolved) !== 'worker-profiles' || path.basename(path.dirname(resolved)) !== '.aegiscrawler') {
    throw new Error(`dedicated browser profiles must live under .aegiscrawler/worker-profiles: ${resolved}`);
  }
}
