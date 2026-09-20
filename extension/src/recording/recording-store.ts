import type { PageAgentRecording } from '../../../src/rule-generator';

export const DB_NAME = 'opencrawler-recordings';
const DB_VERSION = 1;
const STORE_NAME = 'recordings';
const LAST_RECORDING_KEY = 'lastRecording';

export interface RecordingStore {
  set(recording: PageAgentRecording | null): Promise<void>;
  get(): Promise<PageAgentRecording | null>;
  remove(): Promise<void>;
}

class InMemoryRecordingStore implements RecordingStore {
  private data: PageAgentRecording | null = null;

  async set(recording: PageAgentRecording | null): Promise<void> {
    this.data = recording;
  }

  async get(): Promise<PageAgentRecording | null> {
    return this.data;
  }

  async remove(): Promise<void> {
    this.data = null;
  }
}

class IndexedDbRecordingStore implements RecordingStore {
  private db: IDBDatabase | null = null;
  private readonly initPromise: Promise<void>;

  constructor(private readonly dbName: string = DB_NAME) {
    this.initPromise = this.open();
  }

  private open(): Promise<void> {
    return new Promise((resolve, reject) => {
      if (typeof indexedDB === 'undefined') {
        reject(new Error('indexedDB is not available'));
        return;
      }
      const request = indexedDB.open(this.dbName, DB_VERSION);
      request.onerror = () => reject(new Error(`failed to open IndexedDB: ${request.error?.message ?? 'unknown'}`));
      request.onsuccess = () => {
        this.db = request.result;
        resolve();
      };
      request.onupgradeneeded = (event) => {
        const db = (event.target as IDBOpenDBRequest).result;
        if (!db.objectStoreNames.contains(STORE_NAME)) {
          db.createObjectStore(STORE_NAME);
        }
      };
    });
  }

  private async withStore(mode: IDBTransactionMode): Promise<{ store: IDBObjectStore; transactionComplete: Promise<void> }> {
    await this.initPromise;
    if (!this.db) {
      throw new Error('IndexedDB is not initialized');
    }
    const transaction = this.db.transaction([STORE_NAME], mode);
    const store = transaction.objectStore(STORE_NAME);
    const transactionComplete = new Promise<void>((resolve, reject) => {
      transaction.oncomplete = () => resolve();
      transaction.onerror = () => reject(new Error(`IndexedDB transaction failed: ${transaction.error?.message ?? 'unknown'}`));
      transaction.onabort = () => reject(new Error(`IndexedDB transaction aborted: ${transaction.error?.message ?? 'unknown'}`));
    });
    return { store, transactionComplete };
  }

  async set(recording: PageAgentRecording | null): Promise<void> {
    const { store, transactionComplete } = await this.withStore('readwrite');
    if (recording === null) {
      store.delete(LAST_RECORDING_KEY);
    } else {
      store.put(recording, LAST_RECORDING_KEY);
    }
    await transactionComplete;
  }

  async get(): Promise<PageAgentRecording | null> {
    const { store, transactionComplete } = await this.withStore('readonly');
    const request = store.get(LAST_RECORDING_KEY);
    const value = await new Promise<unknown>((resolve, reject) => {
      request.onsuccess = () => resolve(request.result ?? null);
      request.onerror = () => reject(new Error(`failed to read recording: ${request.error?.message ?? 'unknown'}`));
    });
    await transactionComplete;
    return (value as PageAgentRecording | null) ?? null;
  }

  async remove(): Promise<void> {
    const { store, transactionComplete } = await this.withStore('readwrite');
    store.delete(LAST_RECORDING_KEY);
    await transactionComplete;
  }
}

export function createRecordingStore(dbName?: string): RecordingStore {
  if (typeof indexedDB !== 'undefined') {
    try {
      return new IndexedDbRecordingStore(dbName);
    } catch {
      // Fall back to in-memory store if IndexedDB initialization fails.
    }
  }
  return new InMemoryRecordingStore();
}
