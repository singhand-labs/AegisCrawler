import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import 'fake-indexeddb/auto';
import type { PageAgentRecording } from '../../../src/rule-generator';

const sampleRecording: PageAgentRecording = {
  version: '1.0.0',
  meta: {
    startUrl: 'https://example.com/',
    title: 'Example',
    recordedAt: '2026-07-06T00:00:00Z',
    domain: 'example.com',
  },
  events: [],
  snapshots: [],
};

let testDbIndex = 0;

function nextDbName(): string {
  return `opencrawler-recordings-test-${testDbIndex++}`;
}

describe('recording-store', () => {
  beforeEach(() => {
    vi.resetModules();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('stores and retrieves a recording using IndexedDB', async () => {
    const { createRecordingStore } = await import('./recording-store');
    const store = createRecordingStore(nextDbName());
    await store.set(sampleRecording);
    const retrieved = await store.get();
    expect(retrieved).toEqual(sampleRecording);
  });

  it('returns null when no recording has been stored', async () => {
    const { createRecordingStore } = await import('./recording-store');
    const store = createRecordingStore(nextDbName());
    const retrieved = await store.get();
    expect(retrieved).toBeNull();
  });

  it('removes a stored recording', async () => {
    const { createRecordingStore } = await import('./recording-store');
    const store = createRecordingStore(nextDbName());
    await store.set(sampleRecording);
    await store.remove();
    const retrieved = await store.get();
    expect(retrieved).toBeNull();
  });

  it('overwrites a previously stored recording', async () => {
    const { createRecordingStore } = await import('./recording-store');
    const store = createRecordingStore(nextDbName());
    await store.set(sampleRecording);
    const updated = { ...sampleRecording, version: '2.0.0' } as unknown as PageAgentRecording;
    await store.set(updated);
    const retrieved = await store.get();
    expect(retrieved?.version).toBe('2.0.0');
  });

  it('falls back to an in-memory store when indexedDB is unavailable', async () => {
    const originalIndexedDB = globalThis.indexedDB;
    // @ts-expect-error intentionally remove indexedDB to test fallback
    globalThis.indexedDB = undefined;

    try {
      const { createRecordingStore } = await import('./recording-store');
      const store = createRecordingStore();
      await store.set(sampleRecording);
      const retrieved = await store.get();
      expect(retrieved).toEqual(sampleRecording);
      await store.remove();
      expect(await store.get()).toBeNull();
    } finally {
      globalThis.indexedDB = originalIndexedDB;
    }
  });

  it('reports when indexedDB disappears during store initialization', async () => {
    const originalDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'indexedDB');
    let reads = 0;
    Object.defineProperty(globalThis, 'indexedDB', {
      configurable: true,
      get: () => {
        reads += 1;
        return reads === 1 ? originalDescriptor?.value : undefined;
      },
    });

    try {
      const { createRecordingStore } = await import('./recording-store');
      const store = createRecordingStore(nextDbName());
      await expect(store.get()).rejects.toThrow('indexedDB is not available');
    } finally {
      if (originalDescriptor) {
        Object.defineProperty(globalThis, 'indexedDB', originalDescriptor);
      }
    }
  });

  it('deletes the previous recording when set to null', async () => {
    const { createRecordingStore } = await import('./recording-store');
    const store = createRecordingStore(nextDbName());
    await store.set(sampleRecording);
    await store.set(null);
    const retrieved = await store.get();
    expect(retrieved).toBeNull();
  });

  it('reports IndexedDB open failures with the underlying error', async () => {
    const openSpy = vi.spyOn(globalThis.indexedDB, 'open').mockImplementation(() => {
      const request: Record<string, any> = { error: new Error('open failure') };
      queueMicrotask(() => request.onerror?.());
      return request as IDBOpenDBRequest;
    });
    const { createRecordingStore } = await import('./recording-store');
    const store = createRecordingStore(nextDbName());

    await expect(store.get()).rejects.toThrow('failed to open IndexedDB: open failure');
    openSpy.mockRestore();
  });

  it('uses a safe fallback message for IndexedDB open failures', async () => {
    const openSpy = vi.spyOn(globalThis.indexedDB, 'open').mockImplementation(() => {
      const request: Record<string, any> = { error: null };
      queueMicrotask(() => request.onerror?.());
      return request as IDBOpenDBRequest;
    });
    const { createRecordingStore } = await import('./recording-store');
    const store = createRecordingStore(nextDbName());

    await expect(store.get()).rejects.toThrow('failed to open IndexedDB: unknown');
    openSpy.mockRestore();
  });
});
