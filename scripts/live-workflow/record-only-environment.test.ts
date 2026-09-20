// @vitest-environment node
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { EventEmitter } from 'node:events';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  assertScenarioProvisionalRule,
  bootLiveEnvironment,
  bootLiveServer,
  liveServerEnvironment,
  shutdownLiveEnvironment,
  shutdownLiveServer,
} from './support';

afterEach(() => vi.unstubAllEnvs());

describe('record-only live environment', () => {
  it('runs a scenario canary only against an awaiting-replay provisional rule', () => {
    const provisionalRule = { id: 'provisional' };
    const seen: unknown[] = [];
    expect(() => assertScenarioProvisionalRule({
      assertProvisionalRule: (rule) => seen.push(rule),
    }, {
      status: 'awaiting_replay',
      provisionalRule,
    })).not.toThrow();
    expect(seen).toEqual([provisionalRule]);
    expect(() => assertScenarioProvisionalRule({
      assertProvisionalRule: () => undefined,
    }, {
      status: 'replaying',
      provisionalRule,
    })).toThrow(/awaiting_replay/);
  });

  it('enables recording while omitting every provider credential setting', () => {
    const environment = liveServerEnvironment();
    expect(environment.FEATURE_RECORDING_V2).toBe('true');
    expect(environment.LLM_ENABLED).toBe('false');
    expect(environment.LLM_PROVIDER).toBe('');
    expect(environment.LLM_API_KEY).toBe('');
    expect(environment.LLM_API_KEY_FILE).toBe('');
    expect(environment.LLM_BASE_URL).toBe('');
    expect(environment.LLM_MODEL).toBe('');
    expect(environment.LLM_PROVIDER_CONFIGS).toBe('');
    expect(environment.LLM_OPENAI_ENABLE_THINKING).toBe('');
    expect(environment.LLM_OPENAI_STRICT_TOOL_OUTPUT).toBe('');
    expect(environment.LLM_REQUEST_TIMEOUT).toBe('120s');
  });

  it('allows a bounded provider timeout below the progress stall window', () => {
    vi.stubEnv('AEGIS_LIVE_LLM_REQUEST_TIMEOUT', '480s');
    expect(liveServerEnvironment().LLM_REQUEST_TIMEOUT).toBe('480s');
    vi.stubEnv('AEGIS_LIVE_LLM_REQUEST_TIMEOUT', '10m');
    expect(() => liveServerEnvironment()).toThrow(/below the 10-minute/);
    vi.stubEnv('AEGIS_LIVE_LLM_REQUEST_TIMEOUT', 'eight minutes');
    expect(() => liveServerEnvironment()).toThrow(/positive integer duration/);
  });

  it('rejects Qwen Beijing request caps above the provider maximum', () => {
    const qwen = {
      providerLabel: 'openai',
      providerAdapter: 'openai' as const,
      providerBaseURL: 'https://example.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
      providerHostname: 'example.cn-beijing.maas.aliyuncs.com',
      model: 'qwen3.6-flash',
      apiKey: 'not-a-real-key',
    };
    vi.stubEnv('AEGIS_LIVE_LLM_MAX_INPUT_TOKENS', '1000000');
    expect(() => liveServerEnvironment(qwen)).toThrow(/983616/);
    vi.stubEnv('AEGIS_LIVE_LLM_MAX_INPUT_TOKENS', '983616');
    const environment = liveServerEnvironment(qwen);
    expect(environment.LLM_MAX_INPUT_TOKENS).toBe('983616');
    expect(environment.LLM_OPENAI_ENABLE_THINKING).toBe('false');
    expect(environment.LLM_OPENAI_STRICT_TOOL_OUTPUT).toBe('true');
  });

  it('does not send the non-standard thinking field to ordinary OpenAI endpoints', () => {
    const openAI = {
      providerLabel: 'openai',
      providerAdapter: 'openai' as const,
      providerBaseURL: 'https://api.openai.com/v1',
      providerHostname: 'api.openai.com',
      model: 'gpt-4o',
      apiKey: 'not-a-real-key',
    };
    expect(liveServerEnvironment(openAI).LLM_OPENAI_ENABLE_THINKING).toBe('');
    // Strict tool output applies to every OpenAI-compatible provider/model.
    expect(liveServerEnvironment(openAI).LLM_OPENAI_STRICT_TOOL_OUTPUT).toBe('true');
  });

  it('requires DeepSeek beta and enables strict tool output for V4 workflow generation', () => {
    const deepSeek = {
      providerLabel: 'openai',
      providerAdapter: 'openai' as const,
      providerBaseURL: 'https://api.deepseek.com/beta',
      providerHostname: 'api.deepseek.com',
      model: 'deepseek-v4-flash',
      apiKey: 'not-a-real-key',
    };
    const environment = liveServerEnvironment(deepSeek);
    expect(environment.LLM_OPENAI_ENABLE_THINKING).toBe('false');
    expect(environment.LLM_OPENAI_STRICT_TOOL_OUTPUT).toBe('true');
    expect(() => liveServerEnvironment({
      ...deepSeek,
      providerBaseURL: 'https://api.deepseek.com/v1',
    })).toThrow(/https:\/\/api\.deepseek\.com\/beta/);
  });

  it('enables standard-dialect strict tools for Alibaba Beijing DeepSeek V4', () => {
    const deepSeek = {
      providerLabel: 'openai',
      providerAdapter: 'openai' as const,
      providerBaseURL: 'https://example.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
      providerHostname: 'example.cn-beijing.maas.aliyuncs.com',
      model: 'deepseek-v4-flash',
      apiKey: 'not-a-real-key',
    };
    const environment = liveServerEnvironment(deepSeek);
    expect(environment.LLM_OPENAI_ENABLE_THINKING).toBe('false');
    expect(environment.LLM_OPENAI_STRICT_TOOL_OUTPUT).toBe('true');
    vi.stubEnv('AEGIS_LIVE_LLM_MAX_INPUT_TOKENS', '393217');
    expect(() => liveServerEnvironment(deepSeek)).toThrow(/393216/);
    vi.stubEnv('AEGIS_LIVE_LLM_MAX_INPUT_TOKENS', '393216');
    expect(liveServerEnvironment(deepSeek).LLM_MAX_INPUT_TOKENS).toBe('393216');
  });

  it('clears ambient provider configs and alternate key files in paid mode', () => {
    const deepSeek = {
      providerLabel: 'openai',
      providerAdapter: 'openai' as const,
      providerBaseURL: 'https://reviewed.cn-beijing.maas.aliyuncs.com/compatible-mode/v1',
      providerHostname: 'reviewed.cn-beijing.maas.aliyuncs.com',
      model: 'deepseek-v4-flash',
      apiKey: 'reviewed-key',
    };
    vi.stubEnv('LLM_PROVIDER', 'poisoned');
    vi.stubEnv('LLM_API_KEY_FILE', '/tmp/poisoned-key');
    vi.stubEnv('LLM_PROVIDER_CONFIGS', JSON.stringify({
      openai: {
        provider: 'openai',
        apiKey: 'poisoned-key',
        baseURL: 'https://poisoned.invalid/v1',
        model: 'poisoned-model',
        openAiStrictToolOutput: false,
      },
    }));

    const environment = liveServerEnvironment(deepSeek);
    expect(environment.LLM_PROVIDER_CONFIGS).toBe('');
    expect(environment.LLM_API_KEY_FILE).toBe('');
    expect(environment.LLM_PROVIDER).toBe('openai');
    expect(environment.LLM_API_KEY).toBe('reviewed-key');
    expect(environment.LLM_BASE_URL).toBe(deepSeek.providerBaseURL);
    expect(environment.LLM_MODEL).toBe(deepSeek.model);
    expect(environment.LLM_OPENAI_STRICT_TOOL_OUTPUT).toBe('true');
  });

  it('removes a temporary Chromium profile when server boot fails', async () => {
    const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-live-boot-failure-'));
    await expect(bootLiveEnvironment(
      { headed: false },
      {
        makeTemporaryProfile: () => profile,
        bootServer: async () => {
          throw new Error('simulated server boot failure');
        },
      },
    )).rejects.toThrow(/simulated server boot failure/);
    expect(fs.existsSync(profile)).toBe(false);
  });

  it('removes a generated binary when server build fails before returning a handle', async () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-live-server-boot-failure-'));
    const serverDir = path.join(root, 'server');
    fs.mkdirSync(serverDir);
    const generatedBinary = path.join(serverDir, 'e2e-server.exe');

    await expect(bootLiveServer(
      {},
      {
        serverDir,
        getFreePort: async () => 1,
        buildServer: async () => {
          fs.writeFileSync(generatedBinary, 'partial build');
          throw new Error('simulated server build failure');
        },
      },
    )).rejects.toThrow(/simulated server build failure/);

    expect(fs.existsSync(generatedBinary)).toBe(false);
    fs.rmSync(root, { recursive: true, force: true });
  });

  it('kills the server and removes generated artifacts when health checking fails', async () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-live-server-health-failure-'));
    const serverDir = path.join(root, 'server');
    fs.mkdirSync(serverDir);
    const generatedBinary = path.join(serverDir, 'e2e-server.exe');
    const process = new EventEmitter() as any;
    process.killed = false;
    process.stdout = undefined;
    process.stderr = undefined;
    process.kill = () => {
      process.killed = true;
      queueMicrotask(() => process.emit('exit'));
      return true;
    };

    await expect(bootLiveServer(
      {},
      {
        serverDir,
        getFreePort: async () => 1,
        buildServer: async () => {
          fs.writeFileSync(generatedBinary, 'built server');
        },
        startServer: () => process,
        waitForHealth: async () => {
          throw new Error('simulated health failure');
        },
      },
    )).rejects.toThrow(/simulated health failure/);

    expect(process.killed).toBe(true);
    expect(fs.existsSync(generatedBinary)).toBe(false);
    fs.rmSync(root, { recursive: true, force: true });
  });

  it('removes server databases and the generated binary for a canary shutdown', async () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-live-server-cleanup-'));
    const serverDir = path.join(root, 'server');
    const dbPath = path.join(root, 'canary.db');
    fs.mkdirSync(serverDir);
    for (const file of [
      dbPath,
      `${dbPath}-wal`,
      `${dbPath}-shm`,
      path.join(serverDir, 'e2e-server.exe'),
    ]) {
      fs.writeFileSync(file, 'generated');
    }

    await shutdownLiveServer({
      baseUrl: 'http://127.0.0.1:1',
      api: {} as any,
      serverDir,
      dbPath,
      serverProc: { killed: true } as any,
      diagnostics: { value: '' },
    }, true);

    expect(fs.existsSync(dbPath)).toBe(false);
    expect(fs.existsSync(`${dbPath}-wal`)).toBe(false);
    expect(fs.existsSync(`${dbPath}-shm`)).toBe(false);
    expect(fs.existsSync(path.join(serverDir, 'e2e-server.exe'))).toBe(false);
    fs.rmSync(root, { recursive: true, force: true });
  });

  it('removes the generated binary on provider-free browser shutdown', async () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-live-provider-free-cleanup-'));
    const serverDir = path.join(root, 'server');
    const userDataDir = path.join(root, 'profile');
    const dbPath = path.join(root, 'record-only.db');
    fs.mkdirSync(serverDir);
    fs.mkdirSync(userDataDir);
    fs.writeFileSync(path.join(serverDir, 'e2e-server.exe'), 'generated');
    fs.writeFileSync(dbPath, 'database');
    const context = { close: vi.fn(async () => {}) };
    const environment = {
      baseUrl: 'http://127.0.0.1:1',
      api: {} as any,
      serverDir,
      dbPath,
      serverProc: { killed: true } as any,
      diagnostics: { value: '' },
      userDataDir,
      context,
      extensionId: 'extension',
      popup: {} as any,
      contextClosed: false,
    };

    await shutdownLiveEnvironment(environment as any, true);

    expect(context.close).toHaveBeenCalledOnce();
    expect(environment.contextClosed).toBe(true);
    expect(fs.existsSync(dbPath)).toBe(false);
    expect(fs.existsSync(path.join(serverDir, 'e2e-server.exe'))).toBe(false);
    expect(fs.existsSync(userDataDir)).toBe(false);
    fs.rmSync(root, { recursive: true, force: true });
  });
});
