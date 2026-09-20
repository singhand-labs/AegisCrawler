import * as fs from 'fs';
import * as os from 'os';
import * as path from 'path';
import { afterEach, describe, expect, it } from 'vitest';
import {
  assertWindowsKeyACL,
  loadLocalLLMConfig,
  providerAdapterFor,
} from './local-live-llm-canary';

const roots: string[] = [];

function keyFile(mode = 0o600): string {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-live-llm-config-'));
  roots.push(root);
  const file = path.join(root, 'provider-key');
  fs.writeFileSync(file, 'test-provider-key\n', { mode });
  fs.chmodSync(file, mode);
  return file;
}

function environment(file: string): NodeJS.ProcessEnv {
  return {
    AEGIS_LIVE_ALLOW_PAID_LLM: 'I approve one paid Stage 11B LLM canary',
    AEGIS_LIVE_ALLOW_MUTATIONS: 'I approve temporary Stage 11B records',
    AEGIS_LOCAL_LLM_PROVIDER: 'OpenRouter',
    AEGIS_LOCAL_LLM_BASE_URL: 'https://openrouter.example/api/v1/',
    AEGIS_LOCAL_LLM_MODEL: 'provider/model',
    AEGIS_LOCAL_LLM_API_KEY_FILE: file,
  };
}

afterEach(() => {
  for (const root of roots.splice(0)) fs.rmSync(root, { recursive: true, force: true });
});

describe('local live LLM canary configuration', () => {
  it('maps Aliyun Model Studio endpoints to the OpenAI-compatible adapter', () => {
    expect(providerAdapterFor(
      'aliyun',
      'workspace.cn-beijing.maas.aliyuncs.com',
    )).toBe('openai');
    expect(providerAdapterFor('aliyun', 'dashscope.aliyuncs.com')).toBe('openai');
    expect(providerAdapterFor('aliyun', 'dashscope-intl.aliyuncs.com')).toBe('openai');
  });

  it('rejects an Aliyun label on a non-Model-Studio endpoint', () => {
    expect(() => providerAdapterFor('aliyun', 'api.example.com'))
      .toThrow('requires an Alibaba Model Studio endpoint');
  });

  it('accepts only the current user and Windows administrative identities', () => {
    expect(() => assertWindowsKeyACL({
      currentUserSid: 'S-1-5-21-1000',
      allowedSids: ['S-1-5-21-1000', 'S-1-5-18', 'S-1-5-32-544'],
    })).not.toThrow();
    expect(() => assertWindowsKeyACL({
      currentUserSid: 'S-1-5-21-1000',
      allowedSids: ['S-1-5-21-1000', 'S-1-5-32-545'],
    })).toThrow('ACL grants access outside');
  });

  it('maps OpenRouter to the OpenAI adapter and normalizes the base URL', () => {
    const config = loadLocalLLMConfig(environment(keyFile()));

    expect(config).toMatchObject({
      providerLabel: 'openrouter',
      providerAdapter: 'openai',
      providerBaseURL: 'https://openrouter.example/api/v1',
      providerHostname: 'openrouter.example',
      model: 'provider/model',
      apiKey: 'test-provider-key',
    });
  });

  it('requires both exact approvals', () => {
    const env = environment(keyFile());
    delete env.AEGIS_LIVE_ALLOW_MUTATIONS;

    expect(() => loadLocalLLMConfig(env)).toThrow('temporary-record mutation approval is required');
  });

  it.skipIf(process.platform === 'win32')('rejects POSIX provider key files accessible by group or others', () => {
    expect(() => loadLocalLLMConfig(environment(keyFile(0o644))))
      .toThrow('must not be accessible by group or others');
  });
});
