import { JSDOM } from 'jsdom';
import { afterEach, describe, expect, it } from 'vitest';
import type { PageAgentRecording } from '../../src/rule-generator/types';
import { DUCKDUCKGO_QUERY, DUCKDUCKGO_REQUIREMENT, assertDuckDuckGoRecording, assertDuckDuckGoRequirement, assertDuckDuckGoRows, assertDuckDuckGoRule, canonicalDuckDuckGoDestination, collectDuckDuckGoClassifierDiagnostics, collectVisibleDuckDuckGoChoices, duckDuckGoEnvironmentBlock, duckDuckGoResultsPageBlock } from './duckduckgo-search-readonly';

const original = { document: globalThis.document, location: globalThis.location, getComputedStyle: globalThis.getComputedStyle };
afterEach(() => { Object.assign(globalThis, original); });

function installDOM(body: string): void {
  const dom = new JSDOM(`<!doctype html><body>${body}</body>`, { url: `https://duckduckgo.com/?q=${encodeURIComponent(DUCKDUCKGO_QUERY)}` });
  Object.assign(globalThis, { document: dom.window.document, location: dom.window.location, getComputedStyle: dom.window.getComputedStyle.bind(dom.window) });
  for (const element of Array.from(dom.window.document.querySelectorAll('*'))) {
    Object.defineProperty(element, 'getBoundingClientRect', { configurable: true, value: () => ({ width: 100, height: 20, x: 0, y: 0, top: 0, left: 0, right: 100, bottom: 20 }) });
  }
}

function recording(events: PageAgentRecording['events']): PageAgentRecording {
  return { version: '2.0.0', meta: { startUrl: 'https://duckduckgo.com/', title: 'DuckDuckGo', recordedAt: '2026-08-07T00:00:00Z', domain: 'duckduckgo.com' }, events,
    snapshots: [{ timestamp: 1, url: 'https://duckduckgo.com/', selectorMap: {}, capture: {
      status: 'complete', frames: [], nodeCount: 1, redactionCount: 0, removedNodeCount: 0,
    } }],
    termination: { complete: true, reason: 'user', message: 'stopped', timestamp: 10 } } as PageAgentRecording;
}

describe('DuckDuckGo read-only search contract', () => {
  it('locks the reviewed query and structured contract', () => {
    expect(DUCKDUCKGO_QUERY).toBe('how do ocean tides work');
    expect(() => assertDuckDuckGoRequirement(DUCKDUCKGO_REQUIREMENT)).not.toThrow();
  });
  it('accepts exact HTTPS result URLs and rejects boundary drift', () => {
    expect(duckDuckGoResultsPageBlock(`https://duckduckgo.com/?q=${encodeURIComponent(DUCKDUCKGO_QUERY)}`)).toBe('');
    expect(duckDuckGoResultsPageBlock('https://example.org/?q=how+do+ocean+tides+work')).toMatch(/unexpected host/);
    expect(duckDuckGoResultsPageBlock('http://duckduckgo.com/?q=how+do+ocean+tides+work')).toBe('non-HTTPS page');
    expect(duckDuckGoResultsPageBlock('https://duckduckgo.com/?q=different')).toMatch(/unexpected/);
  });
  it('unwraps reviewed redirects and rejects unsafe destinations', () => {
    const redirect = `https://duckduckgo.com/l/?uddg=${encodeURIComponent('https://ocean.example/guide#section')}`;
    expect(canonicalDuckDuckGoDestination(redirect)?.href).toBe('https://ocean.example/guide');
    expect(canonicalDuckDuckGoDestination('http://ocean.example/guide')).toBeUndefined();
    expect(canonicalDuckDuckGoDestination('javascript:alert(1)')).toBeUndefined();
    expect(canonicalDuckDuckGoDestination('https://user:secret@ocean.example/')).toBeUndefined();
    expect(canonicalDuckDuckGoDestination('https://duckduckgo.com/settings')).toBeUndefined();
  });
  it('detects challenge, traffic, and consent boundaries', () => {
    expect(duckDuckGoEnvironmentBlock('Please verify you are human')).toBe('verify you are human');
    expect(duckDuckGoEnvironmentBlock('Unusual traffic was detected')).toBe('unusual traffic');
    expect(duckDuckGoEnvironmentBlock('Consent required')).toBe('consent required');
    expect(duckDuckGoEnvironmentBlock('很遗憾，机器人也使用 DuckDuckGo。请完成以下挑战，以确认这项搜索由真人进行。'))
      .toBe('请完成以下挑战');
    expect(duckDuckGoEnvironmentBlock('选择所有包含鸭子的正方形：')).toBe('选择所有包含鸭子的正方形');
    expect(duckDuckGoEnvironmentBlock('普通中文搜索结果：海洋潮汐如何形成')).toBeUndefined();
    expect(duckDuckGoEnvironmentBlock('Ordinary search results')).toBeUndefined();
  });
  it('collects organic results and excludes ads, answers, hidden and unsafe rows', () => {
    installDOM(`<main><a href="https://decoy.example/">Unrelated auxiliary main</a></main>
    <div data-testid="mainline">
      <article data-testid="result"><h2><a data-testid="result-title-a" href="https://ocean.example/tides">Ocean tides explained</a></h2></article>
      <article data-testid="result"><h2><a href="https://duckduckgo.com/l/?uddg=https%3A%2F%2Fmoon.example%2Ftides">Moon and tides</a></h2></article>
      <article data-testid="ad-result"><h2><a href="https://ads.example/">Sponsored</a></h2></article>
      <article data-testid="result"><span aria-label="Sponsored"></span><h2><a href="https://sponsor.example/">Sponsor</a></h2></article>
      <article data-testid="answer"><h2><a href="https://answer.example/">Answer</a></h2></article>
      <article data-testid="result" hidden><h2><a href="https://hidden.example/">Hidden</a></h2></article>
      <article data-testid="result"><h2><a href="http://unsafe.example/">Unsafe</a></h2></article>
    </div>`);
    Object.defineProperty(document.querySelector('[data-testid="mainline"]'), 'getBoundingClientRect', {
      value: () => ({ width: 0, height: 0, x: 0, y: 0, top: 0, left: 0, right: 0, bottom: 0 }),
    });
    expect(collectDuckDuckGoClassifierDiagnostics()).toEqual({
      explicitRoots: 1, fallbackRoots: 1, selectedRoots: 1, titleLinks: 7, allLinks: 7,
    });
    expect(collectVisibleDuckDuckGoChoices()).toEqual([
      { title: 'Ocean tides explained', href: 'https://ocean.example/tides', host: 'ocean.example' },
      { title: 'Moon and tides', href: 'https://moon.example/tides', host: 'moon.example' },
    ]);
  });
  it('requires complete input/single-submit/results/scroll recording order', () => {
    const valid = recording([
      { type: 'click', timestamp: 1, index: 1 },
      { type: 'inputText', timestamp: 2, index: 1, text: DUCKDUCKGO_QUERY },
      { type: 'submitForm', timestamp: 3, index: 1 },
      { type: 'navigate', timestamp: 4, url: `https://duckduckgo.com/?q=${encodeURIComponent(DUCKDUCKGO_QUERY)}` },
      { type: 'scroll', timestamp: 5, x: 0, y: 180 },
    ] as PageAgentRecording['events']);
    expect(() => assertDuckDuckGoRecording(valid)).not.toThrow();
    const navigationProvenance = structuredClone(valid);
    navigationProvenance.events = navigationProvenance.events.filter((event) => event.type !== 'submitForm');
    expect(() => assertDuckDuckGoRecording(navigationProvenance)).not.toThrow();
    const twice = structuredClone(valid); twice.events.splice(3, 0, { type: 'submitForm', timestamp: 3.5, index: 1 } as never);
    expect(() => assertDuckDuckGoRecording(twice)).toThrow(/duplicate direct submissions/);
    const interrupted = structuredClone(navigationProvenance);
    interrupted.events.splice(2, 0, { type: 'click', timestamp: 3, index: 9 } as never);
    expect(() => assertDuckDuckGoRecording(interrupted)).toThrow(/uninterrupted exact-input/);
    const left = structuredClone(valid); left.events.push({ type: 'navigate', timestamp: 6, url: 'https://example.org/' } as never);
    expect(() => assertDuckDuckGoRecording(left)).toThrow(/left its results/);
    const clicked = structuredClone(valid); clicked.events.push({ type: 'click', timestamp: 6, index: 9 } as never);
    expect(() => assertDuckDuckGoRecording(clicked)).toThrow(/clicked a result-page target/);
    const noClick = structuredClone(valid); noClick.events.shift();
    expect(() => assertDuckDuckGoRecording(noClick)).toThrow(/search-box click/);
  });
  it('requires semantic bindings and forbids positional or unsafe rules', () => {
    const valid = { domain: 'duckduckgo.com', actions: [
      { action: 'navigate', url: 'https://duckduckgo.com/?q={{keyword}}' },
      { action: 'waitForElementVisible', selector: 'main' },
      { action: 'extract', where: { title: '{{target_title}}', host: '{{target_host}}' } },
    ] };
    expect(() => assertDuckDuckGoRule(valid)).not.toThrow();
    expect(() => assertDuckDuckGoRule({ ...valid, actions: [...valid.actions, { action: 'extract', index: 0 }] })).toThrow(/rank or index/);
    expect(() => assertDuckDuckGoRule({ ...valid, actions: [...valid.actions, { action: 'executeJavascript' }] })).toThrow(/unsafe/);
  });
  it('compares exactly one selected identity and rejects drift', () => {
    const schema = { type: 'object' }; const expected = { title: 'Ocean tides explained', host: 'ocean.example' };
    expect(() => assertDuckDuckGoRows([{ title: expected.title, website: expected.host }], schema, expected, 'replay')).not.toThrow();
    expect(() => assertDuckDuckGoRows([{ title: 'Different', website: expected.host }], schema, expected, 'replay')).toThrow(/recorded result identity/);
    expect(() => assertDuckDuckGoRows([{ title: expected.title, website: 'other.example' }], schema, expected, 'task')).toThrow(/recorded result identity/);
  });
});
