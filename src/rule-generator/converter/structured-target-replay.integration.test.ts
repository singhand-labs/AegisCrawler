/// <reference types="vitest/globals" />
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { convert } from './PageAgentToDslConverter';
import { resolveTarget, ElementVerificationError } from '../../scriptcat-engine/selectors';
import { BrowserEnvironment } from '../../worker/BrowserEnvironment';
import type { PageAgentRecording, DomElementInfo, PageAgentEvent } from '../types';
import type { Target } from '../../rule-engine/types/action';

/**
 * End-to-end coverage for the Structured-Target-with-Fallback pipeline:
 *
 *   recording (DomElementInfo with ariaLabel/role)
 *     → convert() emits `selectors: { el1: { selector, ariaLabel, role } }`
 *       → engine resolveTarget / BrowserEnvironment.findElement recovers
 *         under DOM drift via the fallback chain + R-verify.
 *
 * These tests cross three modules (rule-generator/converter,
 * scriptcat-engine/selectors, worker/BrowserEnvironment) and exercise the
 * interaction that unit-level tests in each module cannot prove alone.
 */

function makeClickRecording(info: DomElementInfo): PageAgentRecording {
  const events: PageAgentEvent[] = [{ type: 'click', index: info.index, timestamp: 100 }];
  return {
    version: '1.0.0',
    meta: { startUrl: 'https://example.com/', title: 'T', recordedAt: '2026-07-16T10:00:00Z', domain: 'example.com' },
    events,
    snapshots: [
      {
        timestamp: 1,
        url: 'https://example.com/',
        selectorMap: { [info.index]: info },
      },
    ],
  };
}

describe('structured-target replay under DOM drift (integration)', () => {
  let originalCssEscape: any;

  beforeEach(() => {
    // jsdom in this suite doesn't ship CSS.escape; selectors.ts uses it for
    // ariaLabel / role queries. Polyfill with a minimal escape.
    originalCssEscape = (globalThis as any).CSS?.escape;
    if (typeof (globalThis as any).CSS === 'undefined') (globalThis as any).CSS = {};
    (globalThis as any).CSS.escape = (s: string) => s.replace(/"/g, '\\"');
  });

  afterEach(() => {
    document.body.innerHTML = '';
    if (originalCssEscape) (globalThis as any).CSS.escape = originalCssEscape;
  });

  const recordedInfo: DomElementInfo = {
    index: 1,
    tagName: 'button',
    selector: 'button#save.primary',
    ariaLabel: 'Save',
    role: 'button',
    boundingRect: { x: 0, y: 0, width: 40, height: 20 },
  };

  it('convert() preserves ariaLabel and role on the emitted alias Target', () => {
    const recording = makeClickRecording(recordedInfo);
    const rule = convert(recording, { optimizeSelectors: false });
    expect(rule.selectors!).toEqual({
      el1: { selector: 'button#save.primary', ariaLabel: 'Save', role: 'button' },
    });
    expect(rule.steps[0]).toMatchObject({ action: 'click', target: { $ref: 'el1' } });
  });

  it('replay recovers via ariaLabel branch when the recorded selector has drifted away', () => {
    const recording = makeClickRecording(recordedInfo);
    const rule = convert(recording, { optimizeSelectors: false });
    const target: Target = rule.selectors!.el1;

    // Recorded selector is gone; a different element carries the same aria-label.
    document.body.innerHTML = '<button class="secondary" aria-label="Save">Save</button>';
    const resolved = resolveTarget(target, document, false);
    expect((resolved as Element)?.className).toBe('secondary');
  });

  it('replay recovers via role branch when both selector and ariaLabel have drifted', () => {
    // The converter carries role: 'button' from the recorded <button>.
    // After drift, the DOM has a different element with explicit role="button".
    const info: DomElementInfo = { ...recordedInfo, ariaLabel: undefined };
    const recording = makeClickRecording(info);
    const rule = convert(recording, { optimizeSelectors: false });
    const target: Target = rule.selectors!.el1;

    document.body.innerHTML = '<div id="recover" role="button">Go</div>';
    const resolved = resolveTarget(target, document, false);
    expect((resolved as Element)?.id).toBe('recover');
  });

  it('R-verify rejects a selector match whose ariaLabel differs, and throws ElementVerificationFailed at chain exhaustion', () => {
    const recording = makeClickRecording(recordedInfo);
    const rule = convert(recording, { optimizeSelectors: false });
    const target: Target = rule.selectors!.el1;

    // The recorded selector still resolves, but the element's aria-label has
    // changed (e.g. the page swapped "Save" for "Submit"). No other branch
    // matches. The engine MUST surface this as a verification failure rather
    // than silently clicking the wrong control.
    document.body.innerHTML = '<button id="save" class="primary" aria-label="Submit">Submit</button>';
    expect(() => resolveTarget(target, document, false)).toThrow(ElementVerificationError);
  });

  it('R-verify accepts the selector match when ariaLabel still matches (no drift)', () => {
    const recording = makeClickRecording(recordedInfo);
    const rule = convert(recording, { optimizeSelectors: false });
    const target: Target = rule.selectors!.el1;

    document.body.innerHTML = '<button id="save" class="primary" aria-label="Save">Save</button>';
    const resolved = resolveTarget(target, document, false);
    expect((resolved as Element)?.id).toBe('save');
  });

  it('BrowserEnvironment.findElement recovers via ariaLabel under drift (poll path)', async () => {
    const recording = makeClickRecording(recordedInfo);
    const rule = convert(recording, { optimizeSelectors: false });
    const target: Target = rule.selectors!.el1;

    document.body.innerHTML = '<button class="delta" aria-label="Save">Save</button>';
    const env = new BrowserEnvironment({ send: async () => undefined } as any, window);
    const el = await env.findElement(target, 200);
    expect((el as Element)?.className).toBe('delta');
  });

  it('BrowserEnvironment.findElement surfaces ElementVerificationFailed after timeout under wrong-element drift', async () => {
    const recording = makeClickRecording(recordedInfo);
    const rule = convert(recording, { optimizeSelectors: false });
    const target: Target = rule.selectors!.el1;

    document.body.innerHTML = '<button id="save" class="primary" aria-label="Delete">Delete</button>';
    const env = new BrowserEnvironment({ send: async () => undefined } as any, window);
    await expect(env.findElement(target, 150)).rejects.toThrow('ElementVerificationFailed');
  });

  it('[REDACTED] ariaLabel on the recording does NOT leak into the rule target', () => {
    const info: DomElementInfo = {
      ...recordedInfo,
      ariaLabel: '[REDACTED]',
      selector: 'input#password',
      tagName: 'input',
    };
    const recording = makeClickRecording(info);
    const rule = convert(recording, { optimizeSelectors: false });
    // The redacted sentinel must be stripped — otherwise every replay would
    // look for an element literally labelled "[REDACTED]" and fail verify.
    expect(rule.selectors!.el1).toEqual({ selector: 'input#password', role: 'button' });
    expect(rule.selectors!.el1).not.toHaveProperty('ariaLabel');
  });
});
