/// <reference types="vitest/globals" />
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { findElement } from './selectors';

describe('structured-target replay integration', () => {
  let originalElementFromPoint: any;
  let originalCssEscape: any;

  beforeEach(() => {
    originalElementFromPoint = document.elementFromPoint;
    // jsdom does not implement CSS.escape; the engine's ariaLabel/role
    // branches use it to build selectors. Mirror the polyfill convention
    // from selectors.test.ts:9-24.
    originalCssEscape = (globalThis as any).CSS?.escape;
    if (typeof (globalThis as any).CSS === 'undefined') {
      (globalThis as any).CSS = {};
    }
    (globalThis as any).CSS.escape = (s: string) => s.replace(/"/g, '\\"');
  });

  afterEach(() => {
    document.body.innerHTML = '';
    document.elementFromPoint = originalElementFromPoint;
    if (originalCssEscape) {
      (globalThis as any).CSS.escape = originalCssEscape;
    }
  });

  it('4-field Target from recording (selector + text + ariaLabel + role) resolves on happy path', async () => {
    // Common case: button with text. Converter emits {selector, ariaLabel,
    // role, roleName, text} — NO position (last-resort guard suppressed it).
    document.body.innerHTML = '<button id="go" aria-label="Submit" role="button">Submit</button>';
    const target = {
      selector: '#go',
      ariaLabel: 'Submit',
      role: 'button',
      roleName: 'Submit',
      text: 'Submit',
    };
    const el = await findElement(target, 200);
    expect((el as Element).id).toBe('go');
  });

  it('selector drift falls through to text branch (no position to short-circuit)', async () => {
    // Recording captured text but the page restyled and selector drifted.
    // Without position, engine runs full 5-branch fallback → text branch wins.
    document.body.innerHTML = '<button id="real">Submit</button>';
    const target = {
      selector: '.btn-primary',   // stale
      text: 'Submit',
    };
    const el = await findElement(target, 200);
    expect((el as Element).id).toBe('real');
  });

  it('selector drift falls through to ariaLabel branch', async () => {
    document.body.innerHTML = '<button id="real" aria-label="Submit">OK</button>';
    const target = {
      selector: '.stale',
      ariaLabel: 'Submit',
      text: 'OK',
    };
    const el = await findElement(target, 200);
    expect((el as Element).id).toBe('real');
  });

  it('selector drift falls through to role branch with roleName disambiguation', async () => {
    // Engine role branch builds [role="button"][aria-label="Save"] — so the
    // DOM needs aria-label, not text content. (roleName at the engine layer
    // matches aria-label, not visible text.)
    document.body.innerHTML = '<div role="button" id="a" aria-label="Save">Save</div><div role="button" id="b" aria-label="Delete">Delete</div>';
    const target = {
      selector: '.stale',
      role: 'button',
      roleName: 'Save',
    };
    const el = await findElement(target, 200);
    expect((el as Element).id).toBe('a');
  });

  it('position-only Target (rare canvas/SVG case) uses elementFromPoint', async () => {
    // No semantic attrs captured — converter emitted position only.
    document.body.innerHTML = '<canvas id="cv" width="100" height="100"></canvas>';
    document.elementFromPoint = () => document.getElementById('cv');
    const target = { position: { x: 50, y: 50 } };
    const el = await findElement(target, 200);
    expect((el as Element).id).toBe('cv');
  });

  it('all branches fail verify → throws ElementVerificationFailed (not silent wrong-element)', async () => {
    // At least one branch must produce a candidate that fails verify.
    // Text branch matches "Save" (button text), then verify rejects because
    // ariaLabel is "Delete" not "Save". sawUnverified → throws.
    document.body.innerHTML = '<button id="x" aria-label="Delete">Save</button>';
    await expect(findElement({
      selector: '.stale',
      ariaLabel: 'Save',
      text: 'Save',
      role: 'button',
      roleName: 'Save',
    }, 200)).rejects.toThrow('ElementVerificationFailed');
  });
});
