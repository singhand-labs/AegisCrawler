import * as fs from 'node:fs';
import { execFileSync } from 'node:child_process';
import * as os from 'node:os';
import * as path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { ContentRecorder } from '../../extension/src/content';
import { applyPatch, contentOf } from '../../extension/src/recording/snapshot-delta';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import {
  loadReusableBaiduRecording,
  loadReusableBooksRecording,
  loadReusableQuotesRecording,
  reusableBaiduRecordingPaths,
  reusableBooksRecordingPaths,
  reusableQuotesRecordingPaths,
  saveReusableBaiduRecording,
  saveReusableBooksRecording,
  saveReusableQuotesRecording,
} from './reusable-recording';
import { BOOKS_ENTRY_URL, BOOKS_ORIGIN } from './books-category-matrix';

const temporaryDirectories: string[] = [];

function recording(): PageAgentRecording {
  const completeSnapshot = (sequence: number, phase: 'initial' | 'before-action' | 'final', actionIndex?: number) => ({
    timestamp: sequence + 1,
    url: sequence < 3 ? 'https://www.baidu.com/' : 'https://www.baidu.com/s',
    selectorMap: {},
    phase,
    sequence,
    ...(actionIndex === undefined ? {} : { actionIndex }),
    domTree: { type: 'element' as const, tagName: 'html', children: [] },
    capture: {
      status: 'complete' as const,
      nodeCount: 1,
      redactionCount: 0,
      removedNodeCount: 0,
      frames: [],
    },
  });
  return {
    version: '2.0.0',
    meta: {
      serverRecordingId: 'ephemeral-server-id',
      title: 'Baidu',
      domain: 'www.baidu.com',
      startUrl: 'https://www.baidu.com/',
      recordedAt: new Date().toISOString(),
      endedAt: new Date().toISOString(),
      semanticDomVersion: '1',
      sanitizationVersion: 'extension-v2',
    },
    limits: { maxActions: 500, maxDurationMs: 7_200_000, maxBytes: 64 * 1024 * 1024, warningThreshold: 0.8 },
    events: [
      { type: 'inputText', timestamp: 2, index: 1, text: 'example query' },
      { type: 'submitForm', timestamp: 3, index: 1 },
      { type: 'scroll', timestamp: 4, direction: 'down', amount: 1, unit: 'pages' },
    ],
    snapshots: [
      completeSnapshot(0, 'initial'),
      completeSnapshot(1, 'before-action', 0),
      completeSnapshot(2, 'before-action', 1),
      completeSnapshot(3, 'before-action', 2),
      completeSnapshot(4, 'final'),
    ],
    termination: { reason: 'user', message: 'done', complete: true, timestamp: 5 },
  };
}

function quotesRecording(): PageAgentRecording {
  const urls = [
    'https://quotes.toscrape.com/',
    'https://quotes.toscrape.com/',
    'https://quotes.toscrape.com/tag/love/',
    'https://quotes.toscrape.com/tag/love/page/2/',
  ];
  const snapshot = (
    sequence: number,
    phase: 'initial' | 'before-action' | 'final',
    actionIndex?: number,
  ) => ({
    timestamp: sequence + 1,
    url: urls[sequence],
    selectorMap: {},
    phase,
    sequence,
    ...(actionIndex === undefined ? {} : { actionIndex }),
    domTree: { type: 'element' as const, tagName: 'html', children: [] },
    capture: {
      status: 'complete' as const,
      nodeCount: 1,
      redactionCount: 0,
      removedNodeCount: 0,
      frames: [],
    },
  });
  return {
    version: '2.0.0',
    meta: {
      serverRecordingId: 'ephemeral-quotes-id',
      title: 'Quotes',
      domain: 'quotes.toscrape.com',
      startUrl: urls[0],
      recordedAt: new Date().toISOString(),
      endedAt: new Date().toISOString(),
      semanticDomVersion: '1',
      sanitizationVersion: 'extension-v2',
    },
    limits: { maxActions: 500, maxDurationMs: 7_200_000, maxBytes: 64 * 1024 * 1024, warningThreshold: 0.8 },
    events: [
      { type: 'click', timestamp: 2, index: 1 },
      { type: 'click', timestamp: 3, index: 2 },
    ],
    snapshots: [
      snapshot(0, 'initial'),
      snapshot(1, 'before-action', 0),
      snapshot(2, 'before-action', 1),
      snapshot(3, 'final'),
    ],
    termination: { reason: 'user', message: 'done', complete: true, timestamp: 4 },
  };
}

function humanQuotesRecording(
  scenario: 'quotes-human-choose-requirement' | 'quotes-human-input-requirement',
): PageAgentRecording {
  const choose = scenario === 'quotes-human-choose-requirement';
  const urls = choose
    ? [
      'https://quotes.toscrape.com/',
      'https://quotes.toscrape.com/',
      'https://quotes.toscrape.com/tag/reading/',
    ]
    : [
      'https://quotes.toscrape.com/tag/humor/',
      'https://quotes.toscrape.com/tag/humor/',
      'https://quotes.toscrape.com/tag/humor/page/2/',
    ];
  const source = quotesRecording();
  source.meta.startUrl = urls[0];
  source.events = [{ type: 'click', timestamp: 2, index: 1 }];
  source.snapshots = urls.map((url, sequence) => ({
    timestamp: sequence + 1,
    url,
    selectorMap: {},
    phase: sequence === 0
      ? 'initial' as const
      : sequence === urls.length - 1 ? 'final' as const : 'before-action' as const,
    sequence,
    ...(sequence === 1 ? { actionIndex: 0 } : {}),
    domTree: { type: 'element' as const, tagName: 'html', children: [] },
    capture: {
      status: 'complete' as const,
      nodeCount: 1,
      redactionCount: 0,
      removedNodeCount: 0,
      frames: [],
    },
  }));
  return source;
}

function booksRecording(): PageAgentRecording {
  const pageOne = `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/index.html`;
  const pageTwo = `${BOOKS_ORIGIN}/catalogue/category/books/mystery_3/page-2.html`;
  const target = (index: number, text: string, selector: string) => ({
    index,
    tagName: 'a',
    selector,
    text,
    boundingRect: { x: 1, y: 1, width: 10, height: 10 },
  });
  const bookDOM = () => ({
    type: 'element' as const,
    tagName: 'html',
    children: [{
      type: 'element' as const,
      tagName: 'article',
      attributes: [{ name: 'class', value: 'product_pod' }],
      children: [
        {
          type: 'element' as const,
          tagName: 'p',
          attributes: [{ name: 'class', value: 'star-rating Three' }],
        },
        {
          type: 'element' as const,
          tagName: 'h3',
          children: [{
            type: 'element' as const,
            tagName: 'a',
            attributes: [
              { name: 'title', value: 'Book' },
              { name: 'href', value: '/catalogue/book_1/index.html' },
            ],
            children: [{ type: 'text' as const, text: 'Book' }],
          }],
        },
        {
          type: 'element' as const,
          tagName: 'p',
          attributes: [{ name: 'class', value: 'price_color' }],
          children: [{ type: 'text' as const, text: '£10.00' }],
        },
        {
          type: 'element' as const,
          tagName: 'p',
          attributes: [{ name: 'class', value: 'availability' }],
          children: [{ type: 'text' as const, text: 'In stock' }],
        },
      ],
    }],
  });
  const snapshot = (
    sequence: number,
    url: string,
    phase: 'initial' | 'before-action' | 'final',
    actionIndex?: number,
    selectorMap: Record<number, ReturnType<typeof target>> = {},
  ) => ({
    timestamp: sequence + 1,
    url,
    selectorMap,
    phase,
    sequence,
    ...(actionIndex === undefined ? {} : { actionIndex }),
    domTree: url === BOOKS_ENTRY_URL
      ? { type: 'element' as const, tagName: 'html', children: [] }
      : bookDOM(),
    capture: {
      status: 'complete' as const,
      nodeCount: 1,
      redactionCount: 0,
      removedNodeCount: 0,
      frames: [{
        frameId: 0,
        parentFrameId: -1,
        url,
        status: 'captured' as const,
      }],
    },
  });
  return {
    version: '2.0.0',
    meta: {
      serverRecordingId: 'ephemeral-books-id',
      title: 'Books to Scrape',
      domain: 'books.toscrape.com',
      startUrl: BOOKS_ENTRY_URL,
      recordedAt: new Date().toISOString(),
      endedAt: new Date().toISOString(),
      semanticDomVersion: '1',
      sanitizationVersion: 'extension-v2',
    },
    limits: { maxActions: 500, maxDurationMs: 7_200_000, maxBytes: 64 * 1024 * 1024, warningThreshold: 0.8 },
    events: [
      { type: 'click', timestamp: 2, index: 1 },
      { type: 'scroll', timestamp: 3, direction: 'down', amount: 1, unit: 'pages' },
      { type: 'click', timestamp: 4, index: 2 },
    ],
    snapshots: [
      snapshot(0, BOOKS_ENTRY_URL, 'initial'),
      snapshot(1, BOOKS_ENTRY_URL, 'before-action', 0, {
        1: target(1, 'Mystery', '.side_categories a'),
      }),
      snapshot(2, pageOne, 'before-action', 1),
      snapshot(3, pageOne, 'before-action', 2, {
        2: target(2, 'next', 'li.next > a'),
      }),
      snapshot(4, pageTwo, 'final'),
    ],
    termination: { reason: 'user', message: 'done', complete: true, timestamp: 5 },
  };
}

afterEach(() => {
  for (const directory of temporaryDirectories.splice(0)) fs.rmSync(directory, { recursive: true, force: true });
});

describe('encrypted reusable Baidu recordings', {
  timeout: process.platform === 'win32' ? 60_000 : 5_000,
}, () => {
  it('round-trips without retaining the ephemeral server id or plaintext DOM', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableBaiduRecordingPaths(home);
    const source = recording();
    saveReusableBaiduRecording(paths, source);

    if (process.platform !== 'win32') {
      expect(fs.statSync(paths.directory).mode & 0o777).toBe(0o700);
      expect(fs.statSync(paths.keyFile).mode & 0o777).toBe(0o600);
      expect(fs.statSync(paths.artifact).mode & 0o777).toBe(0o600);
    }
    const encrypted = fs.readFileSync(paths.artifact, 'utf8');
    expect(encrypted).not.toContain('www.baidu.com');
    expect(encrypted).not.toContain('ephemeral-server-id');

    const loaded = loadReusableBaiduRecording(paths);
    expect(loaded.meta.serverRecordingId).toBeUndefined();
    expect(loaded.events).toHaveLength(3);
    expect(loaded.snapshots).toHaveLength(5);
    expect(loaded.events).toEqual(source.events);
    expect(loaded.snapshots).toEqual(source.snapshots);
  });

  it('rejects tampering and non-private key permissions', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableBaiduRecordingPaths(home);
    saveReusableBaiduRecording(paths, recording());
    const envelope = JSON.parse(fs.readFileSync(paths.artifact, 'utf8')) as { ciphertext: string };
    envelope.ciphertext = `${envelope.ciphertext.slice(0, -4)}AAAA`;
    fs.writeFileSync(paths.artifact, JSON.stringify(envelope), { mode: 0o600 });
    expect(() => loadReusableBaiduRecording(paths)).toThrow();

    saveReusableBaiduRecording(paths, recording());
    if (process.platform !== 'win32') {
      fs.chmodSync(paths.keyFile, 0o644);
      expect(() => loadReusableBaiduRecording(paths)).toThrow(/mode 0600/);
    }
  });

  it('rejects incomplete, unsafe, and structurally inconsistent recordings before creating a key', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableBaiduRecordingPaths(home);

    const incomplete = recording();
    incomplete.snapshots[2].capture!.status = 'partial';
    expect(() => saveReusableBaiduRecording(paths, incomplete)).toThrow(/complete semantic DOM/);
    expect(fs.existsSync(paths.keyFile)).toBe(false);

    const legacy = recording();
    legacy.meta.sanitizationVersion = 'extension-v1';
    expect(() => saveReusableBaiduRecording(paths, legacy)).toThrow(/extension-v2 sanitization provenance/);
    expect(fs.existsSync(paths.keyFile)).toBe(false);

    const unsafe = recording();
    unsafe.events[1] = { type: 'executeJavascript', timestamp: 3, script: 'void 0' };
    expect(() => saveReusableBaiduRecording(paths, unsafe)).toThrow(/executeJavascript/);

    const missingPreAction = recording();
    missingPreAction.snapshots.splice(2, 1);
    expect(() => saveReusableBaiduRecording(paths, missingPreAction)).toThrow(/one pre-action per event/);

    const unavailableFrame = recording();
    unavailableFrame.snapshots[1].capture!.frames = [{
      frameId: 1,
      parentFrameId: 0,
      url: 'https://www.baidu.com/frame',
      status: 'unavailable',
    }];
    expect(() => saveReusableBaiduRecording(paths, unavailableFrame)).toThrow(/unavailable frame/);
  });

  it('rejects paths outside the dedicated qualification layout', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableBaiduRecordingPaths(home);
    expect(() => saveReusableBaiduRecording({
      ...paths,
      artifact: path.join(paths.directory, 'other.enc.json'),
    }, recording())).toThrow(/dedicated qualification directory layout/);
  });

  it('rejects a symlinked .aegiscrawler parent before writing the artifact', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-recording-'));
    const target = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-recording-target-'));
    temporaryDirectories.push(home, target);
    fs.symlinkSync(target, path.join(home, '.aegiscrawler'));
    const paths = reusableBaiduRecordingPaths(home);
    expect(() => saveReusableBaiduRecording(paths, recording())).toThrow(/non-symlink .aegiscrawler/);
    expect(fs.existsSync(path.join(target, 'qualification-recordings'))).toBe(false);
  });
});

describe('encrypted reusable Quotes recordings', {
  timeout: process.platform === 'win32' ? 60_000 : 5_000,
}, () => {
  it('round-trips only after the complete quotes recording contract passes', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-quotes-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableQuotesRecordingPaths(home);
    const source = quotesRecording();
    saveReusableQuotesRecording(paths, source);

    const encrypted = fs.readFileSync(paths.artifact, 'utf8');
    expect(encrypted).not.toContain('quotes.toscrape.com');
    expect(encrypted).not.toContain('ephemeral-quotes-id');
    const loaded = loadReusableQuotesRecording(paths);
    expect(loaded.meta.serverRecordingId).toBeUndefined();
    expect(loaded.events).toEqual(source.events);
    expect(loaded.snapshots).toEqual(source.snapshots);
  });

  it('rejects an invalid quotes origin and cross-scenario loading', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-quotes-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableQuotesRecordingPaths(home);
    const invalid = quotesRecording();
    invalid.snapshots[3].url = 'https://example.test/tag/love/page/2/';
    expect(() => saveReusableQuotesRecording(paths, invalid)).toThrow();

    saveReusableQuotesRecording(paths, quotesRecording());
    expect(() => loadReusableBaiduRecording(paths)).toThrow(/dedicated qualification directory layout/);
  });

  it.each([
    'quotes-human-choose-requirement',
    'quotes-human-input-requirement',
  ] as const)('keeps %s recordings in a separately authenticated artifact', (scenario) => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-human-quotes-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableQuotesRecordingPaths(home, scenario);
    const source = humanQuotesRecording(scenario);
    saveReusableQuotesRecording(paths, source, scenario);
    expect(path.basename(paths.artifact)).toBe(`${scenario}.recording.enc.json`);
    expect(loadReusableQuotesRecording(paths, scenario).snapshots).toEqual(source.snapshots);
    expect(() => loadReusableQuotesRecording(paths, 'quotes-by-tag')).toThrow();
  });
});

describe('encrypted reusable Books recordings', {
  timeout: process.platform === 'win32' ? 60_000 : 5_000,
}, () => {
  it('round-trips only the exact scripted root-to-Mystery-page-2 recording', async () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-books-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableBooksRecordingPaths(home);
    const source = booksRecording();
    // The scripted demo activates both exact anchors without scrolling so
    // navigation clicks cannot overtake an asynchronous scroll snapshot.
    source.events = [source.events[0], source.events[2]];
    source.snapshots = [
      source.snapshots[0],
      source.snapshots[1],
      source.snapshots[3],
      source.snapshots[4],
    ];
    source.snapshots.forEach((snapshot, sequence) => {
      snapshot.sequence = sequence;
    });
    source.snapshots[2].actionIndex = 1;
    // selectorMap labels are descriptive recording hints, not the
    // authoritative click lineage. Exact authenticated page transitions prove
    // root -> Mystery page 1 -> page 2 even when those hints are absent.
    delete source.snapshots[1].selectorMap[1].text;
    delete source.snapshots[2].selectorMap[2].text;
    document.head.innerHTML = '<title>Books</title>';
    document.body.innerHTML = `
      <article class="product_pod">
        <p class="star-rating Three"></p>
        <h3><a title="Book" href="/catalogue/book_1/index.html">Book</a></h3>
        <p class="price_color">£10.00</p>
        <p class="availability">In stock</p>
      </article>
      <script>window.nonSemantic = true;</script>
      <!-- non-semantic comment -->
      <a id="next" href="/next">Next</a>
    `;
    document.getElementById('next')!.addEventListener('click', (event) => event.preventDefault());
    const recorder = new ContentRecorder({ protocolVersion: '2.0.0', maxEvents: 10 });
    recorder.start();
    await new Promise((resolve) => setTimeout(resolve, 0));
    document.getElementById('next')!.dispatchEvent(new MouseEvent('click', {
      bubbles: true,
      cancelable: true,
    }));
    const recording = await recorder.stopAsync();
    const produced = recording.snapshots.find((snapshot) => snapshot.phase === 'before-action');
    // The before-action capture may be stored as a reference or a delta;
    // resolve it against the initial full snapshot either way.
    let domTree = produced?.domTree;
    let capture = produced?.capture;
    if (!domTree || !capture) {
      const initial = recording.snapshots.find((snapshot) => snapshot.phase === 'initial');
      if (produced?.ref !== undefined && initial) {
        domTree = initial.domTree;
        capture = initial.capture;
      } else if (produced?.patch !== undefined && initial) {
        const resolved = contentOf(initial);
        applyPatch(resolved, produced.patch);
        domTree = resolved.domTree as typeof domTree;
        capture = resolved.capture as typeof capture;
      }
    }
    if (!domTree || !capture) {
      throw new Error('ContentRecorder returned no navigation snapshot');
    }
    const body = domTree.children?.find((node) => node.tagName === 'body');
    expect(capture).toMatchObject({ redactionCount: 0, removedNodeCount: 2 });
    expect(body?.sanitization).toMatchObject({ contentOmitted: true });
    for (const snapshot of source.snapshots.slice(2)) {
      snapshot.domTree = structuredClone(domTree);
      snapshot.capture = structuredClone(capture);
      snapshot.capture.frames[0].url = snapshot.url;
    }
    saveReusableBooksRecording(paths, source);

    expect(fs.readFileSync(paths.artifact, 'utf8')).not.toContain('books.toscrape.com');
    const expected = structuredClone(source);
    delete expected.meta.serverRecordingId;
    expect(loadReusableBooksRecording(paths)).toEqual(expected);
  });

  it('rejects changed click lineage, extra action families, and sanitization', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-books-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableBooksRecordingPaths(home);

    const wrongCategory = booksRecording();
    wrongCategory.snapshots[2].url =
      `${BOOKS_ORIGIN}/catalogue/category/books/travel_2/index.html`;
    expect(() => saveReusableBooksRecording(paths, wrongCategory))
      .toThrow(/selected category route|Mystery page/);

    const wrongNextPage = booksRecording();
    wrongNextPage.snapshots[3].url =
      `${BOOKS_ORIGIN}/catalogue/category/books/travel_2/index.html`;
    expect(() => saveReusableBooksRecording(paths, wrongNextPage)).toThrow(/selected category route|Next/);

    const extraInput = booksRecording();
    extraInput.events[1] = { type: 'inputText', timestamp: 3, index: 3, text: 'unsafe' };
    expect(() => saveReusableBooksRecording(paths, extraInput)).toThrow(/optional scrolls only/);

    const redacted = booksRecording();
    redacted.snapshots[2].capture!.redactionCount = 1;
    expect(() => saveReusableBooksRecording(paths, redacted)).toThrow(/zero redacted/);

    const invalidRemovalAudit = booksRecording();
    invalidRemovalAudit.snapshots[2].capture!.removedNodeCount = -1;
    expect(() => saveReusableBooksRecording(paths, invalidRemovalAudit))
      .toThrow(/invalid removed-node audit/);
  });

  it('rejects hidden, omitted, or altered Books field evidence', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-books-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableBooksRecordingPaths(home);
    const rows = (source: PageAgentRecording) =>
      [2, 3, 4].map((index) => source.snapshots[index].domTree!.children![0]);

    const hiddenRow = booksRecording();
    rows(hiddenRow).forEach((row) => {
      row.rendered = false;
    });
    expect(() => saveReusableBooksRecording(paths, hiddenRow)).toThrow(/complete product evidence/);

    const hiddenPrice = booksRecording();
    rows(hiddenPrice).forEach((row) => {
      row.children![2].rendered = false;
    });
    expect(() => saveReusableBooksRecording(paths, hiddenPrice)).toThrow(/complete product evidence/);

    const omittedPrice = booksRecording();
    rows(omittedPrice).forEach((row) => {
      row.children![2].sanitization = { contentOmitted: true };
    });
    expect(() => saveReusableBooksRecording(paths, omittedPrice)).toThrow(/complete product evidence/);

    for (const attribute of ['title', 'href']) {
      const alteredLink = booksRecording();
      rows(alteredLink).forEach((row) => {
        row.children![1].children![0].sanitization = {
          alteredAttributes: [attribute],
        };
      });
      expect(() => saveReusableBooksRecording(paths, alteredLink))
        .toThrow(/complete product evidence/);
    }

    const alteredRating = booksRecording();
    rows(alteredRating).forEach((row) => {
      row.children![0].sanitization = { alteredAttributes: ['class'] };
    });
    expect(() => saveReusableBooksRecording(paths, alteredRating))
      .toThrow(/complete product evidence/);
  });

  it('requires exact private modes and real scenario-bound AES authentication', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-books-recording-'));
    temporaryDirectories.push(home);
    const booksPaths = reusableBooksRecordingPaths(home);
    saveReusableBooksRecording(booksPaths, booksRecording());

    if (process.platform !== 'win32') {
      fs.chmodSync(booksPaths.artifact, 0o400);
      expect(() => loadReusableBooksRecording(booksPaths)).toThrow(/mode 0600/);
      fs.chmodSync(booksPaths.artifact, 0o600);
      fs.chmodSync(booksPaths.keyFile, 0o700);
      expect(() => loadReusableBooksRecording(booksPaths)).toThrow(/mode 0600/);
      fs.chmodSync(booksPaths.keyFile, 0o600);
      fs.chmodSync(booksPaths.directory, 0o755);
      expect(() => loadReusableBooksRecording(booksPaths)).toThrow(/mode 0700/);
      fs.chmodSync(booksPaths.directory, 0o700);
    } else {
      execFileSync('icacls.exe', [booksPaths.artifact, '/inheritance:e', '/Q']);
      expect(() => loadReusableBooksRecording(booksPaths)).toThrow(/private Windows DACL/);
    }

    const baiduPaths = reusableBaiduRecordingPaths(home);
    fs.copyFileSync(booksPaths.artifact, baiduPaths.artifact);
    fs.copyFileSync(booksPaths.keyFile, baiduPaths.keyFile);
    fs.chmodSync(baiduPaths.artifact, 0o600);
    fs.chmodSync(baiduPaths.keyFile, 0o600);
    expect(() => loadReusableBaiduRecording(baiduPaths)).toThrow();
  });

  it('does not overwrite the authenticated artifact when new Books validation fails', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-books-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableBooksRecordingPaths(home);
    saveReusableBooksRecording(paths, booksRecording());
    const before = fs.readFileSync(paths.artifact);

    const invalid = booksRecording();
    invalid.snapshots[2].capture!.redactionCount = 1;
    expect(() => saveReusableBooksRecording(paths, invalid)).toThrow(/zero redacted/);
    expect(fs.readFileSync(paths.artifact)).toEqual(before);
  });

  it('requires captured and semantic Books evidence to be frame-free', () => {
    const home = fs.mkdtempSync(path.join(os.tmpdir(), 'aegis-reusable-books-recording-'));
    temporaryDirectories.push(home);
    const paths = reusableBooksRecordingPaths(home);
    for (const url of [
      'https://books.toscrape.com/',
      'https://user:password@books.toscrape.com/',
      'https://books.toscrape.com/?token=value',
      'https://books.toscrape.com/arbitrary-frame',
    ]) {
      const invalid = booksRecording();
      invalid.snapshots[2].capture!.frames.push({
        frameId: 1,
        parentFrameId: 0,
        url,
        status: 'captured',
      });
      expect(() => saveReusableBooksRecording(paths, invalid)).toThrow(/frame-free/);
    }

    const mismatchedRoot = booksRecording();
    mismatchedRoot.snapshots[2].capture!.frames[0].url =
      'https://books.toscrape.com/catalogue/category/books/travel_2/index.html';
    expect(() => saveReusableBooksRecording(paths, mismatchedRoot)).toThrow(/frame-free/);

    const semanticFrame = booksRecording();
    semanticFrame.snapshots[2].domTree!.children!.push({
      type: 'element',
      tagName: 'iframe',
      frameOrigin: 'cross-origin',
      frameSrc: 'https://user:password@outside.invalid/frame?token=value',
      frameStatus: 'unavailable',
    });
    expect(() => saveReusableBooksRecording(paths, semanticFrame)).toThrow(/frame-free/);
  });
});
