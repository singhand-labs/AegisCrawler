/// <reference types="vitest/globals" />
import { findElement, findElements, resolveTarget, evaluateXPath, findByText, isElementVisible } from './selectors';
import { classifyError } from './executor-utils';

describe('selectors', () => {
  let originalCssEscape: any;
  let originalElementFromPoint: any;

  beforeEach(() => {
    originalCssEscape = (globalThis as any).CSS?.escape;
    if (typeof (globalThis as any).CSS === 'undefined') {
      (globalThis as any).CSS = {};
    }
    (globalThis as any).CSS.escape = (s: string) => s.replace(/"/g, '\\"');
    originalElementFromPoint = document.elementFromPoint;
  });

  afterEach(() => {
    document.body.innerHTML = '';
    document.body.removeAttribute('style');
    document.elementFromPoint = originalElementFromPoint;
    if (originalCssEscape) {
      (globalThis as any).CSS.escape = originalCssEscape;
    }
  });

  it('finds element by CSS selector', async () => {
    document.body.innerHTML = '<div id="a">A</div><div id="b">B</div>';
    const el = await findElement({ selector: '#b' });
    expect(el?.id).toBe('b');
  });

  it('finds element by CSS selector with index', async () => {
    document.body.innerHTML = '<span>1</span><span>2</span><span>3</span>';
    const el = await findElement({ selector: 'span', index: 1 });
    expect(el?.textContent).toBe('2');
  });

  it('finds multiple elements', async () => {
    document.body.innerHTML = '<span>1</span><span>2</span>';
    const els = await findElements({ selector: 'span' });
    expect(els).toHaveLength(2);
  });

  it('finds multiple elements by XPath', async () => {
    document.body.innerHTML = '<ul><li>a</li><li>b</li></ul>';
    const els = await findElements({ xpath: '//li' });
    expect(els).toHaveLength(2);
  });

  it('finds elements by text with visible filter', async () => {
    document.body.innerHTML = '<span id="v">target</span><span style="display:none">target</span>';
    const visible = document.getElementById('v')!;
    visible.getBoundingClientRect = () => ({ width: 10, height: 10, top: 0, left: 0, right: 10, bottom: 10, x: 0, y: 0 } as DOMRect);
    const els = await findElements({ text: 'target', visible: true });
    expect(els).toHaveLength(1);
  });

  it('finds element by XPath', async () => {
    document.body.innerHTML = '<div id="x"><p>target</p></div>';
    const el = await findElement({ xpath: '//p[text()="target"]' });
    expect(el?.textContent).toBe('target');
  });

  it('finds element by exact text', async () => {
    document.body.innerHTML = '<button>Click me</button><button>Cancel</button>';
    const el = await findElement({ text: 'Cancel' });
    expect(el?.textContent).toBe('Cancel');
  });

  it('finds element by aria-label', async () => {
    document.body.innerHTML = '<input aria-label="Search box" />';
    const el = await findElement({ ariaLabel: 'Search box' });
    expect(el?.tagName).toBe('INPUT');
  });

  it('finds element by role', async () => {
    document.body.innerHTML = '<div role="button" id="rb">Submit</div>';
    const el = await findElement({ role: 'button' });
    expect(el?.id).toBe('rb');
  });

  it('finds element by role and roleName', async () => {
    document.body.innerHTML = '<div role="button" aria-label="Close">X</div><div role="button" aria-label="Open">O</div>';
    const el = await findElement({ role: 'button', roleName: 'Open' });
    expect(el?.textContent).toBe('O');
  });

  it('filters invisible elements when visible=true', async () => {
    document.body.innerHTML = '<div id="visible">visible</div><div id="hidden" style="display:none">hidden</div>';
    const visible = document.getElementById('visible')!;
    visible.getBoundingClientRect = () => ({ width: 100, height: 100, top: 0, left: 0, right: 100, bottom: 100, x: 0, y: 0 } as DOMRect);
    const el = await findElement({ selector: 'div', visible: true });
    expect(el?.id).toBe('visible');
  });

  it('skips an earlier hidden selector candidate instead of accepting the first match', async () => {
    document.body.innerHTML = [
      '<div id="hidden" aria-hidden="true">hidden</div>',
      '<div id="visible">visible</div>',
    ].join('');
    for (const el of Array.from(document.querySelectorAll('div'))) {
      el.getBoundingClientRect = () => ({ width: 100, height: 20 } as DOMRect);
    }

    const el = await findElement({ selector: 'div', visible: true }, 100);

    expect(el?.id).toBe('visible');
  });

  it('rejects descendants of non-rendered ancestors despite non-zero geometry', () => {
    document.body.innerHTML = [
      '<section id="hidden-attr" hidden><span id="a">a</span></section>',
      '<section id="aria-hidden" aria-hidden="true"><span id="b">b</span></section>',
      '<section id="display-none" style="display:none"><span id="c">c</span></section>',
      '<section id="visibility-hidden" style="visibility:hidden"><span id="d">d</span></section>',
      '<section id="transparent" style="opacity:0"><span id="e">e</span></section>',
      '<input id="hidden-input" type="hidden">',
    ].join('');
    for (const id of ['a', 'b', 'c', 'd', 'e', 'hidden-input']) {
      const el = document.getElementById(id)!;
      el.getBoundingClientRect = () => ({ width: 100, height: 20 } as DOMRect);
      expect(isElementVisible(el), id).toBe(false);
    }
  });

  it('accepts a display-contents semantic container only through a visible descendant', () => {
    document.body.innerHTML = [
      '<main id="visible-main" style="display:contents"><section><h1>Guide</h1></section></main>',
      '<main id="hidden-main" style="display:contents"><section hidden><h1>Hidden</h1></section></main>',
      '<main id="empty-main" style="display:contents"></main>',
      '<main id="zero-main"><h1>Ordinary zero box</h1></main>',
    ].join('');
    const visibleHeading = document.querySelector('#visible-main h1')!;
    visibleHeading.getBoundingClientRect = () => ({ width: 100, height: 20 } as DOMRect);
    const hiddenHeading = document.querySelector('#hidden-main h1')!;
    hiddenHeading.getBoundingClientRect = () => ({ width: 100, height: 20 } as DOMRect);

    expect(isElementVisible(document.getElementById('visible-main')!)).toBe(true);
    expect(isElementVisible(document.getElementById('hidden-main')!)).toBe(false);
    expect(isElementVisible(document.getElementById('empty-main')!)).toBe(false);
    expect(isElementVisible(document.getElementById('zero-main')!)).toBe(false);
  });

  it('filters every hidden candidate in findElements', async () => {
    document.body.innerHTML = [
      '<div hidden><span class="item">hidden attribute</span></div>',
      '<div aria-hidden="true"><span class="item">aria hidden</span></div>',
      '<div style="display:none"><span class="item">computed hidden</span></div>',
      '<span class="item" id="visible">visible</span>',
    ].join('');
    for (const el of Array.from(document.querySelectorAll('.item'))) {
      el.getBoundingClientRect = () => ({ width: 10, height: 10 } as DOMRect);
    }

    const els = await findElements({ selector: '.item', visible: true }, 100);

    expect(els.map((el) => el.id)).toEqual(['visible']);
  });

  it('finds element by position', async () => {
    const fake = document.createElement('div');
    fake.id = 'pos';
    document.body.innerHTML = '<div id="pos" style="position:absolute;top:0;left:0;width:100px;height:100px;"></div>';
    document.elementFromPoint = () => fake;
    const el = await findElement({ position: { x: 50, y: 50 } });
    expect(el?.id).toBe('pos');
  });

  it('finds element inside iframe by index', async () => {
    document.body.innerHTML = '<iframe id="f"></iframe>';
    const iframe = document.getElementById('f') as HTMLIFrameElement;
    const doc = iframe.contentDocument!;
    doc.open();
    doc.write('<html><body><p id="inside">inside</p></body></html>');
    doc.close();
    const el = await findElement({ selector: '#inside', frame: 0 });
    expect(el?.id).toBe('inside');
  });

  it('finds element inside shadow DOM', async () => {
    document.body.innerHTML = '<div id="host"></div>';
    const host = document.getElementById('host')!;
    const shadow = host.attachShadow({ mode: 'open' });
    shadow.innerHTML = '<span id="shadow-el">shadow</span>';
    const el = await findElement({ selector: '#shadow-el', shadowPath: ['#host'] });
    expect(el?.id).toBe('shadow-el');
  });

  it('returns null after timeout when element is missing', async () => {
    const start = Date.now();
    const el = await findElement({ selector: '#missing' }, 200);
    expect(el).toBeNull();
    expect(Date.now() - start).toBeGreaterThanOrEqual(150);
  });

  it('returns empty array after timeout for findElements', async () => {
    const els = await findElements({ selector: '.missing' }, 200);
    expect(els).toEqual([]);
  });

  describe('resolveTarget multiple', () => {
    it('resolves multiple CSS selectors', () => {
      document.body.innerHTML = '<span>a</span><span>b</span>';
      const els = resolveTarget({ selector: 'span' }, document, true);
      expect(Array.isArray(els)).toBe(true);
      expect((els as Element[])).toHaveLength(2);
    });

    it('resolves multiple XPath matches', () => {
      document.body.innerHTML = '<ul><li>a</li><li>b</li></ul>';
      const els = resolveTarget({ xpath: '//li' }, document, true);
      expect((els as Element[])).toHaveLength(2);
    });

    it('resolves multiple text matches', () => {
      document.body.innerHTML = '<button>x</button><button>x</button>';
      const els = resolveTarget({ text: 'x' }, document, true);
      expect((els as Element[])).toHaveLength(2);
    });

    it('filters multiple by visibility', () => {
      document.body.innerHTML = '<span id="v">a</span><span style="display:none">b</span>';
      const visible = document.getElementById('v')!;
      visible.getBoundingClientRect = () => ({ width: 10, height: 10 } as DOMRect);
      const els = resolveTarget({ selector: 'span', visible: true }, document, true);
      expect((els as Element[])).toHaveLength(1);
    });

    it('finds element by position', () => {
      const fake = document.createElement('div');
      fake.id = 'pos';
      document.body.innerHTML = '<div id="pos"></div>';
      document.elementFromPoint = () => fake;
      const el = resolveTarget({ position: { x: 1, y: 2 } }, document, false);
      expect((el as Element).id).toBe('pos');
    });

    it('resolves inside iframe document passed as root', () => {
      document.body.innerHTML = '<iframe id="f"></iframe>';
      const iframe = document.getElementById('f') as HTMLIFrameElement;
      const doc = iframe.contentDocument!;
      doc.open();
      doc.write('<html><body><p id="inside">inside</p></body></html>');
      doc.close();
      const els = resolveTarget({ selector: '#inside' }, doc, true);
      expect((els as Element[])).toHaveLength(1);
    });

    it('falls back to document when frame missing', () => {
      document.body.innerHTML = '<p id="p">p</p>';
      const el = resolveTarget({ selector: '#p', frame: 5 }, document, false);
      expect((el as Element).id).toBe('p');
    });

    it('returns empty array for multiple with no matches', () => {
      const els = resolveTarget({ selector: '.missing' }, document, true);
      expect((els as Element[])).toEqual([]);
    });

    it('returns null for single with no matches', () => {
      const el = resolveTarget({ selector: '.missing' }, document, false);
      expect(el).toBeNull();
    });

    it('resolves single by aria-label', () => {
      document.body.innerHTML = '<input aria-label="Search" />';
      const el = resolveTarget({ ariaLabel: 'Search' }, document, false);
      expect((el as Element).tagName).toBe('INPUT');
    });

    it('resolves single by role with name', () => {
      document.body.innerHTML = '<div role="button" aria-label="Close">X</div><div role="button">Y</div>';
      const el = resolveTarget({ role: 'button', roleName: 'Close' }, document, false);
      expect((el as Element).textContent).toBe('X');
    });

    it('filters invisible single element', () => {
      document.body.innerHTML = '<div id="v">v</div><div id="h" style="display:none">h</div>';
      const visible = document.getElementById('v')!;
      visible.getBoundingClientRect = () => ({ width: 10, height: 10 } as DOMRect);
      const hidden = document.getElementById('h')!;
      hidden.getBoundingClientRect = () => ({ width: 0, height: 0 } as DOMRect);
      const el = resolveTarget({ selector: 'div', visible: true }, document, false);
      expect((el as Element).id).toBe('v');
    });

    it('filters a hidden position target', () => {
      document.body.innerHTML = '<div id="hidden" hidden></div>';
      const hidden = document.getElementById('hidden')!;
      hidden.getBoundingClientRect = () => ({ width: 10, height: 10 } as DOMRect);
      document.elementFromPoint = () => hidden;

      expect(resolveTarget({ position: { x: 1, y: 2 }, visible: true }, document, false)).toBeNull();
    });

    it('resolves single by XPath', () => {
      document.body.innerHTML = '<div id="x"><p>target</p></div>';
      const el = resolveTarget({ xpath: '//p[text()="target"]' }, document, false);
      expect((el as Element).textContent).toBe('target');
    });

    it('resolves single by text', () => {
      document.body.innerHTML = '<button>Click me</button><button>Cancel</button>';
      const el = resolveTarget({ text: 'Cancel' }, document, false);
      expect((el as Element).textContent).toBe('Cancel');
    });

    it('resolves single selector with index', () => {
      document.body.innerHTML = '<span>1</span><span>2</span><span>3</span>';
      const el = resolveTarget({ selector: 'span', index: 1 }, document, false);
      expect((el as Element).textContent).toBe('2');
    });

    it('resolves single by role without roleName', () => {
      document.body.innerHTML = '<div role="button" id="rb">Submit</div>';
      const el = resolveTarget({ role: 'button' }, document, false);
      expect((el as Element).id).toBe('rb');
    });

    it('returns empty array for multiple text with no matches', () => {
      const els = resolveTarget({ text: 'missing' }, document, true);
      expect((els as Element[])).toEqual([]);
    });

    it('evaluates XPath in snapshot mode', () => {
      document.body.innerHTML = '<ul><li>a</li><li>b</li></ul>';
      const el = evaluateXPath('//li', document, false);
      expect(el?.textContent).toBe('a');
    });

    it('returns null for empty XPath snapshot', () => {
      const el = evaluateXPath('//missing', document, false);
      expect(el).toBeNull();
    });

    it('returns null for empty single XPath', () => {
      const el = evaluateXPath('//missing', document, true);
      expect(el).toBeNull();
    });

    it('falls back to document when shadow host has no shadow root', async () => {
      document.body.innerHTML = '<div id="host"><p id="regular">regular</p></div>';
      const el = await findElement({ selector: '#regular', shadowPath: ['#host'] });
      expect(el?.id).toBe('regular');
    });
  });

  describe('findByText', () => {
    it('finds all text matches in multiple mode', () => {
      document.body.innerHTML = '<span>a</span><div>a</div>';
      const el = findByText(document.body, 'a', true);
      expect(el?.tagName).toBe('SPAN');
    });

    it('returns null when no text matches', () => {
      document.body.innerHTML = '<span>a</span>';
      const el = findByText(document.body, 'b', false);
      expect(el).toBeNull();
    });

    it('breaks after first match in single mode', () => {
      document.body.innerHTML = '<span>a</span><div>a</div>';
      const el = findByText(document.body, 'a', false);
      expect(el?.tagName).toBe('SPAN');
    });
  });

  it('finds multiple elements by text', async () => {
    document.body.innerHTML = '<span>a</span><div style="display:none">a</div>';
    const visible = document.querySelector('span')!;
    visible.getBoundingClientRect = () => ({ width: 10, height: 10 } as DOMRect);
    const els = await findElements({ text: 'a', visible: true });
    expect(els).toHaveLength(1);
  });

  it('returns empty array for findElements text with no matches', async () => {
    const els = await findElements({ text: 'missing' }, 100);
    expect(els).toEqual([]);
  });

  describe('Unit 1: fallback chain (selector → xpath → text → ariaLabel → role)', () => {
    it('resolves via ariaLabel fallback when selector drifts', () => {
      document.body.innerHTML = '<button id="real" aria-label="Submit">OK</button>';
      const el = resolveTarget({ selector: '.missing', ariaLabel: 'Submit' }, document, false);
      expect((el as Element).id).toBe('real');
    });

    it('resolves via ariaLabel fallback in findElement', async () => {
      document.body.innerHTML = '<button id="real" aria-label="Submit">OK</button>';
      const el = await findElement({ selector: '.missing', ariaLabel: 'Submit' });
      expect(el?.id).toBe('real');
    });

    it('resolves via role fallback when selector and ariaLabel absent', () => {
      document.body.innerHTML = '<div role="button" id="rb">X</div>';
      const el = resolveTarget({ selector: '.missing', role: 'button' }, document, false);
      expect((el as Element).id).toBe('rb');
    });

    it('tries selector first when both selector and ariaLabel match the same element', () => {
      document.body.innerHTML = '<button id="btn" class="primary" aria-label="Submit">OK</button>';
      const el = resolveTarget({ selector: '.primary', ariaLabel: 'Submit' }, document, false);
      expect((el as Element).id).toBe('btn');
    });

    it('advances chain when selector throws DOMException (malformed)', () => {
      // 'a[b' is malformed — querySelector throws. Chain should advance.
      document.body.innerHTML = '<button id="real" aria-label="Submit">OK</button>';
      const el = resolveTarget({ selector: 'a[b', ariaLabel: 'Submit' }, document, false);
      expect((el as Element).id).toBe('real');
    });

    it('queryAll gains ariaLabel branch (currently-absent recovery)', () => {
      document.body.innerHTML = '<li aria-label="row">1</li><li aria-label="row">2</li><li aria-label="row">3</li>';
      const els = resolveTarget({ selector: '.missing', ariaLabel: 'row' }, document, true);
      expect((els as Element[])).toHaveLength(3);
    });

    it('queryAll gains role branch', () => {
      document.body.innerHTML = '<div role="listitem">1</div><div role="listitem">2</div>';
      const els = resolveTarget({ selector: '.missing', role: 'listitem' }, document, true);
      expect((els as Element[])).toHaveLength(2);
    });

    it('queryAll role branch does NOT match implicit-role elements', () => {
      // Per R8 note: [role="listitem"] only matches explicit role attribute.
      // Implicit-role <li> elements do not match — chain returns [].
      document.body.innerHTML = '<ul><li>a</li><li>b</li></ul>';
      const els = resolveTarget({ selector: '.missing', role: 'listitem' }, document, true);
      expect((els as Element[])).toEqual([]);
    });

    it('queryAll advances chain when selector throws DOMException', () => {
      document.body.innerHTML = '<div role="button">1</div><div role="button">2</div>';
      const els = resolveTarget({ selector: 'a[b', role: 'button' }, document, true);
      expect((els as Element[])).toHaveLength(2);
    });

    it('queryAll uses selector when present and matches (with consistent ariaLabel)', () => {
      // Post-Unit-2, R-verify requires selector-matched elements to also satisfy
      // any ariaLabel/role declared on the Target. Give the spans matching
      // aria-labels so selector-wins semantics are preserved under verify.
      document.body.innerHTML = '<span aria-label="whatever">a</span><span aria-label="whatever">b</span>';
      const els = resolveTarget({ selector: 'span', ariaLabel: 'whatever' }, document, true);
      expect((els as Element[])).toHaveLength(2);
    });

    it('single-field {selector} Target resolves identically to today (regression guard)', () => {
      document.body.innerHTML = '<div id="x">x</div>';
      const el = resolveTarget({ selector: '#x' }, document, false);
      expect((el as Element).id).toBe('x');
    });

    it('single-field {selector} that misses returns null (no fallback possible)', () => {
      document.body.innerHTML = '<div id="x">x</div>';
      const el = resolveTarget({ selector: '.missing' }, document, false);
      expect(el).toBeNull();
    });

    it('xpath single-field Target resolves (backward compat for hand-authored rules)', () => {
      document.body.innerHTML = '<div id="x"><p>target</p></div>';
      const el = resolveTarget({ xpath: '//p[text()="target"]' }, document, false);
      expect((el as Element).textContent).toBe('target');
    });

    it('falls back from drifted selector to xpath in single mode', () => {
      document.body.innerHTML = '<div id="x"><p>target</p></div>';
      const el = resolveTarget({ selector: '.missing', xpath: '//p[text()="target"]' }, document, false);
      expect((el as Element).textContent).toBe('target');
    });

    it('falls back from drifted selector to text in single mode', () => {
      document.body.innerHTML = '<button>Cancel</button>';
      const el = resolveTarget({ selector: '.missing', text: 'Cancel' }, document, false);
      expect((el as Element).textContent).toBe('Cancel');
    });
  });

  describe('Unit 1: findElement/findElements poll-loop resilience', () => {
    it('findElement continues polling when queryOne throws (does not short-circuit)', async () => {
      // queryOne will throw DOMException on the malformed selector 'a[b'.
      // findElement must catch this and continue polling, not propagate.
      // Use a short timeout and verify we get null after timeout (not a throw).
      const el = await findElement({ selector: 'a[b' }, 150);
      expect(el).toBeNull();
    });

    it('findElements continues polling when queryAll throws (does not short-circuit)', async () => {
      const els = await findElements({ selector: 'a[b' }, 150);
      expect(els).toEqual([]);
    });

    it('findElement resolves element that appears mid-poll (wait-for-element contract)', async () => {
      document.body.innerHTML = '';
      const late = document.createElement('div');
      late.id = 'late';
      // Append after ~50ms so the first poll misses it.
      setTimeout(() => document.body.appendChild(late), 50);
      const el = await findElement({ selector: '#late' }, 500);
      expect(el?.id).toBe('late');
    });
  });

  it('finds element inside iframe by string selector', async () => {
    document.body.innerHTML = '<iframe id="f"></iframe>';
    const iframe = document.getElementById('f') as HTMLIFrameElement;
    const doc = iframe.contentDocument!;
    doc.open();
    doc.write('<html><body><p id="inside">inside</p></body></html>');
    doc.close();
    const el = await findElement({ selector: '#inside', frame: '#f' });
    expect(el?.id).toBe('inside');
  });

  describe('Unit 2: R-verify disambiguation', () => {
    it('happy path: selector match with matching ariaLabel is returned', () => {
      document.body.innerHTML = '<button id="x" aria-label="Save">OK</button>';
      const el = resolveTarget({ selector: '#x', ariaLabel: 'Save' }, document, false);
      expect((el as Element).id).toBe('x');
    });

    it('S7 wrong-element guard: selector matches but ariaLabel differs, no other branch matches → throws ElementVerificationFailed', () => {
      document.body.innerHTML = '<button id="x" aria-label="Delete">Del</button>';
      expect(() => resolveTarget({ selector: '#x', ariaLabel: 'Save' }, document, false))
        .toThrow('ElementVerificationFailed');
    });

    it('S2 drift recovery: selector matches wrong element, ariaLabel branch recovers elsewhere', () => {
      document.body.innerHTML = '<button id="x" aria-label="Delete">Del</button><button id="y" aria-label="Save">OK</button>';
      const el = resolveTarget({ selector: '#x', ariaLabel: 'Save' }, document, false);
      expect((el as Element).id).toBe('y');
    });

    it('uses declared text to disambiguate the exact broad Books Next selector shape', () => {
      document.body.innerHTML = [
        '<ul>',
        '  <li><a id="catalogue-root" href="/">Books</a></li>',
        '  <li><a id="next" href="/page-2.html">next</a></li>',
        '</ul>',
      ].join('');

      const el = resolveTarget({
        selector: 'li > a',
        text: 'next',
        role: 'link',
        roleName: 'next',
      }, document, false);

      expect((el as Element).id).toBe('next');
    });

    it('returns genuine absence when broad selector candidates lack the authoritative text', () => {
      document.body.innerHTML = '<ul><li><a href="/">Books</a></li><li><a href="/previous">previous</a></li></ul>';

      expect(resolveTarget({
        selector: 'li > a',
        text: 'next',
        role: 'link',
      }, document, false)).toBeNull();
    });

    it('does not use position, selector, XPath, aria, or role as alternatives to authoritative text', () => {
      document.body.innerHTML = '<a id="wrong" role="link" aria-label="next">Books</a>';
      const wrong = document.getElementById('wrong')!;
      document.elementFromPoint = () => wrong;

      expect(resolveTarget({
        position: { x: 1, y: 2 },
        selector: '#wrong',
        xpath: '//*[@id="wrong"]',
        text: 'next',
        ariaLabel: 'next',
        role: 'link',
        roleName: 'next',
      }, document, false)).toBeNull();
    });

    it('keeps selector-identified ancestors compatible with nested declared text', () => {
      document.body.innerHTML = '<button id="save"><span>Save</span></button>';

      const el = resolveTarget({
        selector: '#save',
        text: 'Save',
        role: 'button',
      }, document, false);

      expect((el as Element).id).toBe('save');
    });

    it('fails verification when declared text exists but the candidate role differs', () => {
      document.body.innerHTML = '<button id="next">next</button>';

      expect(() => resolveTarget({
        selector: '#next',
        text: 'next',
        role: 'link',
      }, document, false)).toThrow('ElementVerificationFailed');
    });

    it('filters multiple selector candidates by declared text', () => {
      document.body.innerHTML = [
        '<a id="next-1">next</a>',
        '<a id="previous">previous</a>',
        '<a id="next-2">next</a>',
      ].join('');

      const els = resolveTarget({ selector: 'a', text: 'next', role: 'link' }, document, true);

      expect((els as Element[]).map((element) => element.id)).toEqual(['next-1', 'next-2']);
    });

    it('normalizes surrounding whitespace for declared text verification and fallback', () => {
      document.body.innerHTML = '<a id="next">  next  </a>';

      const selectorMatch = resolveTarget({ selector: 'a', text: ' next ' }, document, false);
      const textFallback = resolveTarget({ selector: '.missing', text: ' next ' }, document, false);

      expect((selectorMatch as Element).id).toBe('next');
      expect((textFallback as Element).id).toBe('next');
    });

    it('role verify derives implicit role for native <button>', () => {
      document.body.innerHTML = '<button id="x">Save</button>';
      const el = resolveTarget({ selector: '#x', role: 'button' }, document, false);
      expect((el as Element).id).toBe('x');
    });

    it('role verify accepts explicit role attribute', () => {
      document.body.innerHTML = '<div id="x" role="button">Go</div>';
      const el = resolveTarget({ selector: '#x', role: 'button' }, document, false);
      expect((el as Element).id).toBe('x');
    });

    it('role mismatch on selector match → throws when no recovery branch', () => {
      document.body.innerHTML = '<button id="x">Save</button>'; // implicit role: button, not link
      expect(() => resolveTarget({ selector: '#x', role: 'link' }, document, false))
        .toThrow('ElementVerificationFailed');
    });

    it('treats blank semantic discriminators as absent', () => {
      document.body.innerHTML = '<p id="semantic-result">semantic action complete</p>';
      const el = resolveTarget({
        selector: '#semantic-result',
        text: '  ',
        ariaLabel: '  ',
        role: '',
        roleName: '\t',
        frame: '  ',
        shadowPath: [' ', '\t'],
      }, document, false);
      expect((el as Element).id).toBe('semantic-result');
    });

    it('xpath branch yield is verified against ariaLabel', () => {
      document.body.innerHTML = '<button id="x" aria-label="Delete">Del</button>';
      expect(() => resolveTarget({ xpath: '//button', ariaLabel: 'Save' }, document, false))
        .toThrow('ElementVerificationFailed');
    });

    it('text branch yield is verified against ariaLabel', () => {
      // text "Save" matches button content, but aria-label is "Delete" not "Save"
      document.body.innerHTML = '<button aria-label="Delete">Save</button>';
      expect(() => resolveTarget({ text: 'Save', ariaLabel: 'Save' }, document, false))
        .toThrow('ElementVerificationFailed');
    });

    it('queryAll filters selector matches by verify (keeps only verified subset)', () => {
      document.body.innerHTML = '<li id="a" aria-label="row">1</li><li id="b" aria-label="row">2</li><li id="c" aria-label="col">3</li>';
      const els = resolveTarget({ selector: 'li', ariaLabel: 'row' }, document, true);
      expect((els as Element[]).map((e) => e.id)).toEqual(['a', 'b']);
    });

    it('queryAll sawUnverified on selector advances to ariaLabel branch which recovers', () => {
      document.body.innerHTML = '<span>no-aria</span><span aria-label="row">r1</span><span aria-label="row">r2</span>';
      const els = resolveTarget({ selector: 'span', ariaLabel: 'row' }, document, true);
      expect((els as Element[]).map((e) => e.textContent)).toEqual(['r1', 'r2']);
    });

    it('queryAll all branches fail verify → throws ElementVerificationFailed', () => {
      document.body.innerHTML = '<li aria-label="col">1</li>'; // selector matches, verify fails
      expect(() => resolveTarget({ selector: 'li', ariaLabel: 'row' }, document, true))
        .toThrow('ElementVerificationFailed');
    });

    it('classifyError recognizes ElementVerificationFailed', () => {
      expect(classifyError(new Error('ElementVerificationFailed: ...'))).toBe('ElementVerificationFailed');
    });

    it('ElementVerificationFailed is NOT in the recoverable set', () => {
      const errorType = classifyError(new Error('ElementVerificationFailed: ...'));
      expect(['ElementNotFound', 'TimeoutError', 'NetworkError', 'RateLimited', 'Blocked', 'SessionExpired'])
        .not.toContain(errorType);
    });

    it('findElement re-throws ElementVerificationFailed after timeout (wrong-element, not missing)', async () => {
      document.body.innerHTML = '<button id="x" aria-label="Delete">Del</button>';
      await expect(findElement({ selector: '#x', ariaLabel: 'Save' }, 150))
        .rejects.toThrow('ElementVerificationFailed');
    });

    it('findElement does NOT throw when element is genuinely missing (returns null)', async () => {
      const el = await findElement({ selector: '#missing', ariaLabel: 'Save' }, 150);
      expect(el).toBeNull();
    });

    it('findElements re-throws ElementVerificationFailed after timeout', async () => {
      document.body.innerHTML = '<button id="x" aria-label="Delete">Del</button>';
      await expect(findElements({ selector: '#x', ariaLabel: 'Save' }, 150))
        .rejects.toThrow('ElementVerificationFailed');
    });

    it('role verify on <input type=checkbox> derives implicit "checkbox" role', () => {
      document.body.innerHTML = '<input id="cb" type="checkbox" />';
      const el = resolveTarget({ selector: '#cb', role: 'checkbox' }, document, false);
      expect((el as Element).id).toBe('cb');
    });

    it('role verify rejects <input type=text> when role claims "checkbox"', () => {
      document.body.innerHTML = '<input id="t" type="text" />';
      expect(() => resolveTarget({ selector: '#t', role: 'checkbox' }, document, false))
        .toThrow('ElementVerificationFailed');
    });
  });
});
