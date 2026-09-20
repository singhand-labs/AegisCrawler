/**
 * Browser worker CLI — see docs/browser-worker.md.
 *
 * Usage:
 *   npm run worker -- --server <url> --api-key-file <path> --profile <name> \
 *     [--workers N] [--headless] [--profiles-dir <dir>] [--worker-id-prefix <p>] \
 *     [--poll-interval-ms <ms>] [--heartbeat-interval-ms <ms>] [--verbose]
 *
 * The worker API key is read only from a permission-restricted file (mode
 * 0600, i.e. not accessible by group or others), trimmed of surrounding
 * whitespace. It is never accepted on the command line, never logged, and
 * never leaves this process.
 */

import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { WorkerHost } from './host';

interface CliOptions {
  serverUrl: string;
  apiKeyFile: string;
  profile: string;
  workers: number;
  headless: boolean;
  profilesDir: string;
  workerIdPrefix?: string;
  pollIntervalMs?: number;
  heartbeatIntervalMs?: number;
  verbose: boolean;
}

const USAGE = `Usage: npm run worker -- --server <url> --api-key-file <path> --profile <name>
  [--workers N] [--headless] [--profiles-dir <dir>] [--worker-id-prefix <prefix>]
  [--poll-interval-ms <ms>] [--heartbeat-interval-ms <ms>] [--verbose]

Required:
  --server <url>           AegisCrawler server base URL (http://host:port)
  --api-key-file <path>    File containing WORKER_API_KEY (must be mode 0600)
  --profile <name>         Named browser profile to serve (letters, digits, . _ -)

Options:
  --workers <n>            Concurrent worker slots, 1-50 (default 1)
  --headless               Run Chromium headless (default: headed)
  --profiles-dir <dir>     Parent dir for persistent profiles (default: ~/.aegiscrawler/worker-profiles)
  --worker-id-prefix <p>   Worker ID prefix (default: hostname)
  --poll-interval-ms <ms>  Idle claim poll interval (default: 5000)
  --heartbeat-interval-ms  Heartbeat interval (default: 30000)
  --verbose                Verbose slot/driver logging
  --help                   Show this help
`;

function parseArgs(argv: string[]): CliOptions {
  const opts: Record<string, string | boolean> = {};
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (!arg.startsWith('--')) {
      throw new Error(`unexpected argument: ${arg}`);
    }
    const key = arg.slice(2);
    if (key === 'help') {
      process.stdout.write(USAGE);
      process.exit(0);
    }
    if (key === 'headless' || key === 'verbose') {
      opts[key] = true;
      continue;
    }
    const value = argv[++i];
    if (value === undefined || value.startsWith('--')) {
      throw new Error(`missing value for --${key}`);
    }
    opts[key] = value;
  }

  const serverUrl = String(opts['server'] ?? '').trim();
  if (!serverUrl) throw new Error('--server is required');
  let parsed: URL;
  try {
    parsed = new URL(serverUrl);
  } catch {
    throw new Error(`--server is not a valid URL: ${serverUrl}`);
  }
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    throw new Error('--server must be an HTTP(S) URL');
  }

  const apiKeyFile = String(opts['api-key-file'] ?? '').trim();
  if (!apiKeyFile) throw new Error('--api-key-file is required');

  const profile = String(opts['profile'] ?? '').trim();
  if (!profile) throw new Error('--profile is required');

  const parsePositiveInt = (name: string, raw: string | boolean | undefined): number | undefined => {
    if (raw === undefined) return undefined;
    const value = Number(raw);
    if (!Number.isInteger(value) || value <= 0) {
      throw new Error(`--${name} must be a positive integer (got ${String(raw)})`);
    }
    return value;
  };

  return {
    serverUrl: serverUrl.replace(/\/$/, ''),
    apiKeyFile,
    profile,
    workers: parsePositiveInt('workers', opts['workers']) ?? 1,
    headless: opts['headless'] === true,
    profilesDir: String(opts['profiles-dir'] ?? path.join(os.homedir(), '.aegiscrawler', 'worker-profiles')),
    workerIdPrefix: typeof opts['worker-id-prefix'] === 'string' ? opts['worker-id-prefix'] : undefined,
    pollIntervalMs: parsePositiveInt('poll-interval-ms', opts['poll-interval-ms']),
    heartbeatIntervalMs: parsePositiveInt('heartbeat-interval-ms', opts['heartbeat-interval-ms']),
    verbose: opts['verbose'] === true,
  };
}

/** Reads a 0600 permission-restricted key file (never prints the key). */
export function readApiKeyFile(keyFileValue: string): string {
  const keyFile = path.resolve(keyFileValue);
  let stat: fs.Stats;
  try {
    stat = fs.statSync(keyFile);
  } catch {
    throw new Error(`--api-key-file could not be opened: ${keyFileValue}`);
  }
  if (!stat.isFile()) {
    throw new Error('--api-key-file must be a regular file');
  }
  if ((stat.mode & 0o077) !== 0) {
    throw new Error('--api-key-file must not be accessible by group or others (chmod 0600)');
  }
  const apiKey = fs.readFileSync(keyFile, 'utf8').trim();
  if (!apiKey) {
    throw new Error('--api-key-file is empty');
  }
  return apiKey;
}

async function main(): Promise<void> {
  const opts = parseArgs(process.argv.slice(2));
  const apiKey = readApiKeyFile(opts.apiKeyFile);
  const host = new WorkerHost({
    serverUrl: opts.serverUrl,
    apiKey,
    profile: opts.profile,
    profilesDir: opts.profilesDir,
    workers: opts.workers,
    headless: opts.headless,
    workerIdPrefix: opts.workerIdPrefix,
    pollIntervalMs: opts.pollIntervalMs,
    heartbeatIntervalMs: opts.heartbeatIntervalMs,
    verbose: opts.verbose,
  });

  let stopping = false;
  const shutdown = (signalName: string): void => {
    if (stopping) return;
    stopping = true;
    // eslint-disable-next-line no-console
    console.log(`[worker] received ${signalName}, shutting down...`);
    // H-1: force-exit after a grace period. Without this, a hung host.stop()
    // (e.g., Worker run promises blocked on network calls to an unreachable
    // server) keeps the process alive indefinitely, requiring SIGKILL.
    const forceExitTimer = setTimeout(() => {
      // eslint-disable-next-line no-console
      console.error('[worker] graceful shutdown timed out; forcing exit');
      process.exit(1);
    }, 30_000);
    forceExitTimer.unref();
    void host.stop().then(() => {
      clearTimeout(forceExitTimer);
      process.exit(0);
    });
  };
  process.on('SIGINT', () => shutdown('SIGINT'));
  process.on('SIGTERM', () => shutdown('SIGTERM'));

  // eslint-disable-next-line no-console
  console.log(
    `[worker] starting ${opts.workers} slot(s) for profile "${opts.profile}" on ${opts.serverUrl} `
    + `(host ${os.hostname()}, profiles dir ${opts.profilesDir})`,
  );
  await host.start();
}

if (require.main === module) {
  main().catch((err) => {
    // eslint-disable-next-line no-console
    console.error(`[worker] fatal: ${err instanceof Error ? err.message : String(err)}`);
    process.exit(1);
  });
}
