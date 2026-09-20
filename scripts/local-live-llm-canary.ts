import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { ChildProcess, execFileSync } from 'child_process';
import {
  ADMIN_API_KEY,
  buildServer,
  getFreePort,
  killServer,
  removeIfExists,
  startServer,
  waitForHealth,
} from './e2e-deployment-test';

const {
  MUTATION_APPROVAL,
  PAID_LLM_APPROVAL,
  redact,
  runLLMCanary,
} = require('./external-production-qualification') as {
  MUTATION_APPROVAL: string;
  PAID_LLM_APPROVAL: string;
  redact: (value: unknown, secrets: string[]) => unknown;
  runLLMCanary: (config: Record<string, unknown>) => Promise<Record<string, unknown>>;
};

export interface LocalLLMConfig {
  providerLabel: string;
  providerAdapter: 'openai' | 'anthropic';
  providerBaseURL: string;
  providerHostname: string;
  model: string;
  apiKey: string;
}

export function providerAdapterFor(
  providerLabel: string,
  providerHostname: string,
): 'openai' | 'anthropic' {
  if (providerLabel === 'aliyun') {
    const modelStudioHostname = providerHostname === 'dashscope.aliyuncs.com'
      || providerHostname === 'dashscope-intl.aliyuncs.com'
      || providerHostname.endsWith('.maas.aliyuncs.com');
    assert(modelStudioHostname,
      'AEGIS_LOCAL_LLM_PROVIDER=aliyun requires an Alibaba Model Studio endpoint');
    return 'openai';
  }
  if (providerLabel === 'openrouter') return 'openai';
  assert(providerLabel === 'openai' || providerLabel === 'anthropic',
    'AEGIS_LOCAL_LLM_PROVIDER must be aliyun, openrouter, openai, or anthropic');
  return providerLabel;
}

interface WindowsKeyACL {
  currentUserSid: string;
  allowedSids: string[];
}

export function assertWindowsKeyACL(acl: WindowsKeyACL): void {
  const trusted = new Set([
    acl.currentUserSid,
    'S-1-5-18', // LocalSystem
    'S-1-5-32-544', // Builtin Administrators
  ]);
  const untrusted = acl.allowedSids.filter((sid) => !trusted.has(sid));
  assert(untrusted.length === 0,
    'AEGIS_LOCAL_LLM_API_KEY_FILE ACL grants access outside the current user and system administrators');
}

function readWindowsKeyACL(keyFile: string): WindowsKeyACL {
  const script = [
    '& { param($p)',
    '$acl = Get-Acl -LiteralPath $p;',
    '$allowed = @($acl.Access | Where-Object { $_.AccessControlType -eq "Allow" } |',
    'ForEach-Object { $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value } |',
    'Sort-Object -Unique);',
    '[PSCustomObject]@{',
    'currentUserSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value;',
    'allowedSids = $allowed',
    '} | ConvertTo-Json -Compress',
    '}',
  ].join(' ');
  let output: string;
  try {
    output = execFileSync('powershell.exe', [
      '-NoLogo', '-NoProfile', '-NonInteractive', '-Command', script, keyFile,
    ], { encoding: 'utf8', windowsHide: true }).trim();
  } catch {
    throw new Error('AEGIS_LOCAL_LLM_API_KEY_FILE ACL could not be verified');
  }
  let parsed: { currentUserSid?: unknown; allowedSids?: unknown };
  try {
    parsed = JSON.parse(output) as { currentUserSid?: unknown; allowedSids?: unknown };
  } catch {
    throw new Error('AEGIS_LOCAL_LLM_API_KEY_FILE ACL returned invalid metadata');
  }
  const allowed = Array.isArray(parsed.allowedSids)
    ? parsed.allowedSids
    : typeof parsed.allowedSids === 'string' ? [parsed.allowedSids] : [];
  assert(typeof parsed.currentUserSid === 'string' && parsed.currentUserSid.length > 0,
    'AEGIS_LOCAL_LLM_API_KEY_FILE ACL omitted the current user');
  assert(allowed.every((sid): sid is string => typeof sid === 'string'),
    'AEGIS_LOCAL_LLM_API_KEY_FILE ACL returned invalid identities');
  return { currentUserSid: parsed.currentUserSid, allowedSids: allowed };
}

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

export function loadLocalLLMConfig(env: NodeJS.ProcessEnv = process.env): LocalLLMConfig {
  assert(env.AEGIS_LIVE_ALLOW_PAID_LLM === PAID_LLM_APPROVAL, 'paid LLM approval is required');
  assert(env.AEGIS_LIVE_ALLOW_MUTATIONS === MUTATION_APPROVAL, 'temporary-record mutation approval is required');

  const providerLabel = (env.AEGIS_LOCAL_LLM_PROVIDER || '').trim().toLowerCase();

  let providerURL: URL;
  try {
    providerURL = new URL((env.AEGIS_LOCAL_LLM_BASE_URL || '').trim());
  } catch {
    throw new Error('AEGIS_LOCAL_LLM_BASE_URL must be an absolute HTTPS URL');
  }
  assert(providerURL.protocol === 'https:', 'AEGIS_LOCAL_LLM_BASE_URL must use HTTPS');
  assert(!providerURL.username && !providerURL.password && !providerURL.search && !providerURL.hash,
    'AEGIS_LOCAL_LLM_BASE_URL must not contain credentials, a query, or a fragment');
  providerURL.pathname = providerURL.pathname.replace(/\/+$/, '');
  const providerAdapter = providerAdapterFor(providerLabel, providerURL.hostname);

  const model = (env.AEGIS_LOCAL_LLM_MODEL || '').trim();
  assert(model, 'AEGIS_LOCAL_LLM_MODEL is required');
  const keyFileValue = (env.AEGIS_LOCAL_LLM_API_KEY_FILE || '').trim();
  assert(keyFileValue, 'AEGIS_LOCAL_LLM_API_KEY_FILE is required');
  const keyFile = path.resolve(keyFileValue);

  let stat: fs.Stats;
  try {
    stat = fs.statSync(keyFile);
  } catch {
    throw new Error('AEGIS_LOCAL_LLM_API_KEY_FILE could not be opened');
  }
  assert(stat.isFile(), 'AEGIS_LOCAL_LLM_API_KEY_FILE must be a regular file');
  if (process.platform === 'win32') {
    assertWindowsKeyACL(readWindowsKeyACL(keyFile));
  } else {
    assert((stat.mode & 0o077) === 0,
      'AEGIS_LOCAL_LLM_API_KEY_FILE must not be accessible by group or others');
  }
  const apiKey = fs.readFileSync(keyFile, 'utf8').trim();
  assert(apiKey, 'AEGIS_LOCAL_LLM_API_KEY_FILE is empty');

  return {
    providerLabel,
    providerAdapter,
    providerBaseURL: providerURL.toString(),
    providerHostname: providerURL.hostname,
    model,
    apiKey,
  };
}

async function main(): Promise<void> {
  const config = loadLocalLLMConfig();
  const serverDir = path.resolve(__dirname, '..', 'server');
  const binaryPath = path.join(serverDir, 'e2e-server.exe');
  const stamp = `${Date.now()}-${process.pid}`;
  const dbPath = path.join(os.tmpdir(), `aegis-stage11b-live-llm-${stamp}.db`);
  const port = await getFreePort();
  const baseUrl = `http://127.0.0.1:${port}`;
  let serverProc: ChildProcess | undefined;

  try {
    await buildServer(serverDir);
    serverProc = startServer(serverDir, dbPath, port, {
      FEATURE_RECORDING_V2: 'true',
      FEATURE_WORKFLOW_V2: 'true',
      LLM_ENABLED: 'true',
      LLM_PROVIDER: config.providerAdapter,
      LLM_API_KEY: config.apiKey,
      LLM_BASE_URL: config.providerBaseURL,
      LLM_MODEL: config.model,
      LLM_MAX_RETRIES: '0',
      LLM_ENABLE_REFLECTION: 'false',
      LLM_CACHE_TTL: '0s',
      LLM_MAX_INPUT_TOKENS: '1000000',
      LLM_JOB_WORKER_INTERVAL: '50ms',
      LLM_REQUEST_TIMEOUT: '120s',
    });
    await waitForHealth(baseUrl);
    const startedAt = new Date().toISOString();
    const details = await runLLMCanary({
      baseURL: new URL(`${baseUrl}/`),
      adminAPIKey: ADMIN_API_KEY,
      requestTimeoutMs: 15_000,
      jobTimeoutMs: 180_000,
    });
    process.stdout.write(`${JSON.stringify({
      schema: 'aegiscrawler.stage11b.local-live-llm.v1',
      status: 'passed',
      promotionEligible: false,
      startedAt,
      finishedAt: new Date().toISOString(),
      providerService: config.providerLabel,
      providerAdapter: config.providerAdapter,
      providerHostname: config.providerHostname,
      model: config.model,
      details,
    }, null, 2)}\n`);
  } catch (error) {
    const safe = redact({
      schema: 'aegiscrawler.stage11b.local-live-llm.v1',
      status: 'failed',
      promotionEligible: false,
      error: error instanceof Error ? error.message : String(error),
    }, [config.apiKey]);
    process.stderr.write(`${JSON.stringify(safe, null, 2)}\n`);
    process.exitCode = 1;
  } finally {
    if (serverProc) await killServer(serverProc);
    for (const file of [dbPath, `${dbPath}-wal`, `${dbPath}-shm`, binaryPath]) removeIfExists(file);
  }
}

if (require.main === module) {
  main().catch((error) => {
    process.stderr.write(`${JSON.stringify({ status: 'failed', promotionEligible: false, error: String(error) })}\n`);
    process.exit(1);
  });
}
