/// <reference types="vitest/globals" />
import { describe, it, expect } from 'vitest';
import { SelectorAliasRegistry, resolveTarget, findSnapshot, resolveElementInfo } from './selector-resolver';
import type { PageAgentRecording } from '../types';

function makeRecording(): PageAgentRecording {
  return {
    version: '1.0.0',
    meta: { startUrl: 'https://example.com/', title: 'T', recordedAt: '2026-07-04T10:00:00Z', domain: 'example.com' },
    events: [],
    snapshots: [
      {
        timestamp: 1,
        url: 'https://example.com/',
        selectorMap: {
          5: { index: 5, tagName: 'button', selector: 'button#submit.btn', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
        },
      },
    ],
  };
}

function makeRichRecording(): PageAgentRecording {
  return {
    version: '1.0.0',
    meta: { startUrl: 'https://example.com/', title: 'T', recordedAt: '2026-07-04T10:00:00Z', domain: 'example.com' },
    events: [],
    snapshots: [
      {
        timestamp: 1,
        url: 'https://example.com/',
        selectorMap: {
          7: {
            index: 7,
            tagName: 'button',
            selector: 'button#save',
            ariaLabel: 'Save',
            role: 'button',
            boundingRect: { x: 0, y: 0, width: 10, height: 10 },
          },
          8: {
            index: 8,
            tagName: 'input',
            selector: 'input#pw',
            ariaLabel: '[REDACTED]',
            boundingRect: { x: 0, y: 0, width: 10, height: 10 },
          },
        },
      },
    ],
  };
}

describe('selector-resolver', () => {
  it('resolves index to $ref target', () => {
    const recording = makeRecording();
    const registry = new SelectorAliasRegistry();
    const target = resolveTarget(recording, 5, 2, registry, true);
    expect(target).toEqual({ $ref: 'el1' });
  });

  it('throws when index not found', () => {
    const recording = makeRecording();
    const registry = new SelectorAliasRegistry();
    expect(() => resolveTarget(recording, 99, 2, registry, true)).toThrow('Element not found');
  });

  it('throws when no snapshot exists for timestamp', () => {
    const recording = makeRecording();
    recording.snapshots = [];
    const registry = new SelectorAliasRegistry();
    expect(() => resolveTarget(recording, 5, 2, registry, true)).toThrow('No snapshot found');
  });

  it('finds the latest snapshot before timestamp', () => {
    const recording = makeRecording();
    const snapshot = findSnapshot(recording, 2);
    expect(snapshot?.timestamp).toBe(1);
  });

  it('resolves the latest snapshot before the timestamp across multiple snapshots', () => {
    const recording = makeRecording();
    recording.snapshots.push({
      timestamp: 5,
      url: 'https://example.com/',
      selectorMap: {
        5: { index: 5, tagName: 'a', selector: 'a#later', boundingRect: { x: 0, y: 0, width: 1, height: 1 } },
      },
    });
    const registry = new SelectorAliasRegistry();
    const target = resolveTarget(recording, 5, 4, registry, true);
    expect(target).toEqual({ $ref: 'el1' });
    expect(registry.getAliases()).toEqual({ el1: { selector: '#submit', position: { x: 5, y: 5 } } });
  });

  it('prefers exact v2 before-action evidence over a same-timestamp reused index', () => {
    const recording = makeRecording();
    recording.version = '2.0.0';
    recording.snapshots = [
      {
        timestamp: 10,
        url: 'https://example.com/?q=New+York',
        phase: 'before-action',
        sequence: 3,
        actionIndex: 2,
        selectorMap: {
          5: { index: 5, tagName: 'a', selector: '.pagination a.page-one', text: '1', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
        },
      },
      {
        timestamp: 10,
        url: 'https://example.com/?page_num=1&q=New+York',
        phase: 'final',
        sequence: 4,
        selectorMap: {
          5: { index: 5, tagName: 'a', selector: '[aria-label="Next"]', ariaLabel: 'Next', boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
        },
      },
    ];
    const registry = new SelectorAliasRegistry();
    expect(resolveTarget(recording, 5, 10, registry, false)).toEqual({ $ref: 'el1' });
    expect(registry.getAliases()).toEqual({
      el1: { selector: '.pagination a.page-one', text: '1' },
    });
  });

  it('fails closed when exact v2 before-action evidence is ambiguous', () => {
    const recording = makeRecording();
    recording.version = '2.0.0';
    recording.snapshots = [0, 1].map((sequence) => ({
      timestamp: 10,
      url: 'https://example.com/',
      phase: 'before-action' as const,
      sequence,
      actionIndex: sequence,
      selectorMap: {
        5: { index: 5, tagName: 'a', selector: `a:nth-child(${sequence + 1})`, boundingRect: { x: 0, y: 0, width: 10, height: 10 } },
      },
    }));
    expect(() => resolveTarget(recording, 5, 10, new SelectorAliasRegistry(), false))
      .toThrow(/Ambiguous before-action snapshots/);
  });

  it('returns raw selector when optimize is false', () => {
    const recording = makeRecording();
    const registry = new SelectorAliasRegistry();
    const target = resolveTarget(recording, 5, 2, registry, false);
    expect(target).toEqual({ $ref: 'el1' });
    expect(registry.getAliases()).toEqual({ el1: { selector: 'button#submit.btn', position: { x: 5, y: 5 } } });
  });

  it('returns alias map from getAliases', () => {
    const registry = new SelectorAliasRegistry();
    registry.getAlias('#foo');
    registry.getAlias('.bar');
    expect(registry.getAliases()).toEqual({
      el1: { selector: '#foo' },
      el2: { selector: '.bar' },
    });
  });

  it('dedupes by composite key: same selector + same extras → one alias', () => {
    const registry = new SelectorAliasRegistry();
    registry.getAlias('#x', { ariaLabel: 'Save', role: 'button' });
    registry.getAlias('#x', { ariaLabel: 'Save', role: 'button' });
    expect(registry.getAliases()).toEqual({
      el1: { selector: '#x', ariaLabel: 'Save', role: 'button' },
    });
  });

  it('splits aliases when selector matches but disambiguators differ (composite key)', () => {
    const registry = new SelectorAliasRegistry();
    registry.getAlias('#x', { ariaLabel: 'Save', role: 'button' });
    registry.getAlias('#x', { ariaLabel: 'Delete', role: 'button' });
    expect(registry.getAliases()).toEqual({
      el1: { selector: '#x', ariaLabel: 'Save', role: 'button' },
      el2: { selector: '#x', ariaLabel: 'Delete', role: 'button' },
    });
  });

  it('splits aliases on role-only difference', () => {
    const registry = new SelectorAliasRegistry();
    registry.getAlias('#x', { role: 'button' });
    registry.getAlias('#x', { role: 'link' });
    expect(Object.keys(registry.getAliases())).toEqual(['el1', 'el2']);
  });

  it('drops ariaLabel="[REDACTED]" from both target and dedup key', () => {
    const registry = new SelectorAliasRegistry();
    // Two calls — one redacted, one with no ariaLabel at all — must collapse
    // because the redacted sentinel carries no usable semantic identity.
    registry.getAlias('#pw', { ariaLabel: '[REDACTED]' });
    registry.getAlias('#pw');
    expect(registry.getAliases()).toEqual({ el1: { selector: '#pw' } });
  });

  it('drops role="[REDACTED]" from target and dedup key', () => {
    const registry = new SelectorAliasRegistry();
    registry.getAlias('#pw', { role: '[REDACTED]' });
    registry.getAlias('#pw');
    expect(registry.getAliases()).toEqual({ el1: { selector: '#pw' } });
  });

  it('omits ariaLabel/role keys entirely when absent (clean Target shape)', () => {
    const registry = new SelectorAliasRegistry();
    registry.getAlias('#x');
    expect(registry.getAliases()).toEqual({ el1: { selector: '#x' } });
  });

  it('resolveTarget propagates ariaLabel/role from DomElementInfo into the alias target', () => {
    const recording = makeRichRecording();
    const registry = new SelectorAliasRegistry();
    resolveTarget(recording, 7, 2, registry, false);
    expect(registry.getAliases()).toEqual({
      el1: { selector: 'button#save', ariaLabel: 'Save', role: 'button' },
    });
  });

  it('resolveTarget drops [REDACTED] ariaLabel when propagating extras', () => {
    const recording = makeRichRecording();
    const registry = new SelectorAliasRegistry();
    resolveTarget(recording, 8, 2, registry, false);
    expect(registry.getAliases()).toEqual({ el1: { selector: 'input#pw', position: { x: 5, y: 5 } } });
  });

  it('resolveTarget splits two same-selector elements with different ariaLabel into separate aliases', () => {
    const recording: PageAgentRecording = {
      version: '1.0.0',
      meta: { startUrl: '', title: '', recordedAt: '', domain: '' },
      events: [],
      snapshots: [
        {
          timestamp: 1,
          url: '',
          selectorMap: {
            1: { index: 1, tagName: 'button', selector: 'button.row', ariaLabel: 'Save', boundingRect: { x: 0, y: 0, width: 1, height: 1 } },
            2: { index: 2, tagName: 'button', selector: 'button.row', ariaLabel: 'Delete', boundingRect: { x: 0, y: 0, width: 1, height: 1 } },
          },
        },
      ],
    };
    const registry = new SelectorAliasRegistry();
    resolveTarget(recording, 1, 2, registry, false);
    resolveTarget(recording, 2, 2, registry, false);
    expect(registry.getAliases()).toEqual({
      el1: { selector: 'button.row', ariaLabel: 'Save' },
      el2: { selector: 'button.row', ariaLabel: 'Delete' },
    });
  });

  it('buildTarget emits text from DomElementInfo.text', () => {
    const recording: PageAgentRecording = {
      version: '1.0.0',
      meta: { startUrl: '', title: '', recordedAt: '', domain: '' },
      events: [],
      snapshots: [{
        timestamp: 1, url: '',
        selectorMap: { 1: { index: 1, tagName: 'button', selector: 'button#go', text: 'Submit', boundingRect: { x: 0, y: 0, width: 10, height: 10 } } },
      }],
    };
    const registry = new SelectorAliasRegistry();
    resolveTarget(recording, 1, 2, registry, false);
    // position is SUPPRESSED because text is present (last-resort guard).
    expect(registry.getAliases()).toEqual({
      el1: { selector: 'button#go', text: 'Submit' },
    });
  });

  it('buildTarget emits position as bounding-rect center ONLY when no semantic disambiguator exists', () => {
    const recording: PageAgentRecording = {
      version: '1.0.0',
      meta: { startUrl: '', title: '', recordedAt: '', domain: '' },
      events: [],
      snapshots: [{
        timestamp: 1, url: '',
        // Canvas element with no text/ariaLabel/role — position is the only signal.
        selectorMap: { 1: { index: 1, tagName: 'canvas', selector: 'canvas#x', boundingRect: { x: 100, y: 200, width: 40, height: 60 } } },
      }],
    };
    const registry = new SelectorAliasRegistry();
    resolveTarget(recording, 1, 2, registry, false);
    expect(registry.getAliases().el1.position).toEqual({ x: 120, y: 230 });
    expect(registry.getAliases().el1.text).toBeUndefined();
    expect(registry.getAliases().el1.role).toBeUndefined();
    expect(registry.getAliases().el1.ariaLabel).toBeUndefined();
  });

  it('buildTarget does NOT emit position when ariaLabel is present (even if text/role absent)', () => {
    const recording: PageAgentRecording = {
      version: '1.0.0',
      meta: { startUrl: '', title: '', recordedAt: '', domain: '' },
      events: [],
      snapshots: [{
        timestamp: 1, url: '',
        selectorMap: { 1: { index: 1, tagName: 'input', selector: 'input#pw', ariaLabel: 'Password', boundingRect: { x: 0, y: 0, width: 100, height: 20 } } },
      }],
    };
    const registry = new SelectorAliasRegistry();
    resolveTarget(recording, 1, 2, registry, false);
    expect(registry.getAliases().el1.position).toBeUndefined();
    expect(registry.getAliases().el1.ariaLabel).toBe('Password');
  });

  it('buildTarget emits roleName only when role is also present', () => {
    const recording: PageAgentRecording = {
      version: '1.0.0',
      meta: { startUrl: '', title: '', recordedAt: '', domain: '' },
      events: [],
      snapshots: [{
        timestamp: 1, url: '',
        selectorMap: { 1: { index: 1, tagName: 'button', selector: 'b', role: 'button', text: 'Submit', boundingRect: { x: 0, y: 0, width: 1, height: 1 } } },
      }],
    };
    const registry = new SelectorAliasRegistry();
    resolveTarget(recording, 1, 2, registry, false);
    expect(registry.getAliases().el1).toMatchObject({ role: 'button', roleName: 'Submit', text: 'Submit' });
    expect(registry.getAliases().el1.position).toBeUndefined();
  });

  it('does not bind editable controls to transient typed text', () => {
    const recording: PageAgentRecording = {
      version: '1.0.0',
      meta: { startUrl: '', title: '', recordedAt: '', domain: '' },
      events: [],
      snapshots: [
        {
          timestamp: 1, url: '',
          selectorMap: { 7: {
            index: 7, tagName: 'input', selector: '#sb_form_q', role: 'combobox',
            ariaLabel: 'Enter your search here',
            boundingRect: { x: 0, y: 0, width: 100, height: 20 },
          } },
        },
        {
          timestamp: 2, url: '',
          selectorMap: { 7: {
            index: 7, tagName: 'input', selector: '#sb_form_q', role: 'combobox',
            ariaLabel: 'Enter your search here', text: 'h',
            boundingRect: { x: 0, y: 0, width: 100, height: 20 },
          } },
        },
      ],
    };
    const registry = new SelectorAliasRegistry();
    resolveTarget(recording, 7, 1, registry, false);
    resolveTarget(recording, 7, 2, registry, false);
    expect(registry.getAliases()).toEqual({
      el1: {
        selector: '#sb_form_q', role: 'combobox',
        ariaLabel: 'Enter your search here',
      },
    });
  });

  it('buildTarget does NOT emit roleName without role (avoids eerie combination)', () => {
    const recording: PageAgentRecording = {
      version: '1.0.0',
      meta: { startUrl: '', title: '', recordedAt: '', domain: '' },
      events: [],
      snapshots: [{
        timestamp: 1, url: '',
        selectorMap: { 1: { index: 1, tagName: 'button', selector: 'b', text: 'Submit', boundingRect: { x: 0, y: 0, width: 1, height: 1 } } },
      }],
    };
    const registry = new SelectorAliasRegistry();
    resolveTarget(recording, 1, 2, registry, false);
    expect(registry.getAliases().el1.roleName).toBeUndefined();
    expect(registry.getAliases().el1.text).toBe('Submit');
  });

  it('drops text="[REDACTED]" from target and dedup key', () => {
    const registry = new SelectorAliasRegistry();
    registry.getAlias('#pw', { text: '[REDACTED]' });
    registry.getAlias('#pw');
    expect(registry.getAliases()).toEqual({ el1: { selector: '#pw' } });
  });

  it('compositeKey folds in text so same-selector different-text split', () => {
    const registry = new SelectorAliasRegistry();
    registry.getAlias('button.row', { text: 'Save', boundingRect: { x: 0, y: 0, width: 1, height: 1 } });
    registry.getAlias('button.row', { text: 'Delete', boundingRect: { x: 0, y: 0, width: 1, height: 1 } });
    expect(Object.keys(registry.getAliases())).toEqual(['el1', 'el2']);
  });

  it('compositeKey 5px quantization tolerates boundingRect jitter (alias churn guard)', () => {
    const registry = new SelectorAliasRegistry();
    // Two captures of the same button — center drifted from (50,50) to (52,51) due to scroll.
    registry.getAlias('#btn', { text: 'OK', boundingRect: { x: 40, y: 40, width: 20, height: 20 } });     // center 50,50
    registry.getAlias('#btn', { text: 'OK', boundingRect: { x: 42, y: 41, width: 20, height: 20 } });     // center 52,51
    expect(Object.keys(registry.getAliases())).toEqual(['el1']); // collapsed, not churned
  });
});

describe('resolveElementInfo', () => {
  it('resolves element info from the latest snapshot', () => {
    const recording = makeRecording();
    const info = resolveElementInfo(recording, 5, 2);
    expect(info).toMatchObject({ index: 5, tagName: 'button', selector: 'button#submit.btn' });
  });

  it('returns null when no snapshot matches the timestamp', () => {
    const recording = makeRecording();
    recording.snapshots = [];
    expect(resolveElementInfo(recording, 5, 2)).toBeNull();
  });

  it('returns null when the index is not in the snapshot', () => {
    const recording = makeRecording();
    expect(resolveElementInfo(recording, 99, 2)).toBeNull();
  });
});
