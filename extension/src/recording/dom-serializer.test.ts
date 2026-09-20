import { describe, it, expect, beforeEach, vi } from 'vitest';
import type { DomNode } from '../../../src/rule-generator/types';
import {
  REDACTED_VALUE,
  REMOVED_URL_VALUE,
  SemanticDomLimitError,
  serializeDom,
  serializeDomWithReport,
  estimateNodeCount,
} from './dom-serializer';

describe('serializeDom', () => {
  beforeEach(() => {
    document.body.innerHTML = '';
  });

  it('serializes a simple element tree', () => {
    document.body.innerHTML = '<div id="app"><button>Click</button></div>';
    const tree = serializeDom(document.body);
    expect(tree).not.toBeNull();
    expect(tree!.tagName).toBe('body');
    const app = tree!.children!.find((c) => c.tagName === 'div');
    expect(app).toBeDefined();
    expect(app!.attributes).toContainEqual({ name: 'id', value: 'app' });
    expect(app!.children!.some((c) => c.tagName === 'button')).toBe(true);
  });

  it('truncates text content', () => {
    document.body.innerHTML = '<p>Long text</p>';
    const tree = serializeDom(document.body, { maxTextLength: 4 });
    const p = tree!.children!.find((c) => c.tagName === 'p');
    expect(p).toBeDefined();
    const textNode = p!.children!.find((c) => c.type === 'text');
    expect(textNode).toBeDefined();
    expect(textNode!.text).toBe('Long');
  });

  it('respects maxDepth', () => {
    document.body.innerHTML = '<div><span><b>x</b></span></div>';
    const tree = serializeDom(document.body, { maxDepth: 2 });
    const div = tree!.children![0];
    expect(div.children).toBeDefined();
    const span = div.children![0];
    // depth 2 means we do not recurse into the <b> inside <span>
    expect(span.children).toBeUndefined();
  });

  it('respects maxNodes by pruning children', () => {
    document.body.innerHTML = '<ul>' + '<li>1</li>'.repeat(10) + '</ul>';
    const tree = serializeDom(document.body, { maxNodes: 5 });
    expect(estimateNodeCount(tree!)).toBeLessThanOrEqual(5);
  });

  it('serializes iframes as placeholders', () => {
    document.body.innerHTML = '<iframe id="frame" src="https://example.com/"></iframe>';
    const tree = serializeDom(document.documentElement);
    const body = tree!.children!.find((c) => c.tagName === 'body');
    const frameNode = body!.children!.find((c) => c.tagName === 'iframe');
    expect(frameNode).toBeDefined();
    expect(frameNode!.framePlaceholder).toBe(true);
    expect(frameNode!.frameSrc).toBe('https://example.com/');
    expect(frameNode!.children).toBeUndefined();
  });

  it('records omitted iframe fallback content before returning its placeholder', () => {
    document.body.innerHTML = `
      <iframe id="content-frame">fallback secret</iframe>
      <iframe id="markup-frame">   </iframe>
    `;
    const tree = serializeDom(document.body)!;
    const byID = (id: string) => tree.children?.find((node) =>
      node.attributes?.some((attribute) => attribute.name === 'id' && attribute.value === id));

    expect(byID('content-frame')).toMatchObject({
      framePlaceholder: true,
      sanitization: {
        markupAltered: true,
        contentOmitted: true,
      },
    });
    expect(byID('markup-frame')).toMatchObject({
      framePlaceholder: true,
      sanitization: {
        markupAltered: true,
      },
    });
    expect(byID('markup-frame')?.sanitization?.contentOmitted).toBeUndefined();
  });

  it('does not descend into same-origin iframes directly', () => {
    document.body.innerHTML = '<iframe id="frame" src="about:blank"></iframe>';
    const iframe = document.getElementById('frame') as HTMLIFrameElement;
    if (iframe.contentDocument) {
      iframe.contentDocument.body.innerHTML = '<button>In frame</button>';
    }
    const tree = serializeDom(document.documentElement);
    const body = tree!.children!.find((c) => c.tagName === 'body');
    const frameNode = body!.children!.find((c) => c.tagName === 'iframe');
    expect(frameNode).toBeDefined();
    expect(frameNode!.framePlaceholder).toBe(true);
    expect(frameNode!.children).toBeUndefined();
  });

  it('skips script, style, noscript, link, meta and template tags', () => {
    document.body.innerHTML =
      '<div>' +
      '<script>alert(1)</script>' +
      '<style>.a{}</style>' +
      '<noscript>no js</noscript>' +
      '<link rel="stylesheet" href="x.css">' +
      '<meta charset="utf-8">' +
      '<template><span>inert</span></template>' +
      '<p>keep</p>' +
      '</div>';
    const tree = serializeDom(document.body);
    const div = tree!.children!.find((c) => c.tagName === 'div');
    expect(div).toBeDefined();
    const childTags = div!.children!.map((c) => c.tagName).filter(Boolean);
    expect(childTags).toEqual(['p']);
  });

  it('records safe rendered-state evidence including hidden ancestors', () => {
    document.body.innerHTML = `
      <section id="visible"><span id="visible-child">visible</span></section>
      <section id="hidden-attr" hidden><span id="hidden-child">hidden</span></section>
      <section id="aria-hidden" aria-hidden="true"><span id="aria-child">hidden</span></section>
      <section id="display-none" style="display:none"><span id="display-child">hidden</span></section>
      <section id="visibility-hidden" style="visibility:hidden"><span id="visibility-child">hidden</span></section>
      <section id="transparent" style="opacity:0"><span id="transparent-child">hidden</span></section>
      <input id="hidden-input" type="hidden">
    `;

    const tree = serializeDom(document.body)!;
    const byID = (id: string): DomNode | undefined => {
      const visit = (node: DomNode): DomNode | undefined => {
        if (node.attributes?.some((attribute) => attribute.name === 'id' && attribute.value === id)) {
          return node;
        }
        for (const child of node.children ?? []) {
          const match = visit(child);
          if (match) return match;
        }
        return undefined;
      };
      return visit(tree);
    };

    expect(byID('visible')?.rendered).toBeUndefined();
    expect(byID('visible-child')?.rendered).toBeUndefined();
    for (const id of ['hidden-attr', 'hidden-child', 'aria-hidden', 'aria-child', 'display-none', 'display-child', 'visibility-hidden', 'visibility-child', 'transparent', 'transparent-child', 'hidden-input']) {
      expect(byID(id)?.rendered, id).toBe(false);
    }
  });

  it('evaluates computed rendered state once per traversed element', () => {
    document.body.innerHTML = '<main><article><span>visible</span></article></main>';
    const getComputedStyle = vi.spyOn(window, 'getComputedStyle');

    const tree = serializeDom(document.body);

    expect(tree).not.toBeNull();
    // html is checked once as the root ancestor; body/main/article/span are
    // checked once each. Descendants never rescan their ancestor chain.
    expect(getComputedStyle).toHaveBeenCalledTimes(5);
    getComputedStyle.mockRestore();
  });

  it('inherits non-rendered state across a shadow-root capture boundary', () => {
    const host = document.createElement('div');
    host.hidden = true;
    document.body.appendChild(host);
    const shadow = host.attachShadow({ mode: 'open' });
    const child = document.createElement('span');
    child.textContent = 'shadow value';
    shadow.appendChild(child);

    expect(serializeDom(child)?.rendered).toBe(false);
  });

  it('ignores non-text non-element nodes such as comments', () => {
    const container = document.createElement('div');
    container.appendChild(document.createTextNode('text'));
    container.appendChild(document.createComment('ignored comment'));
    const span = document.createElement('span');
    span.textContent = 'after';
    container.appendChild(span);

    const tree = serializeDom(container);
    expect(tree).not.toBeNull();
    const children = tree!.children!;
    expect(children).toHaveLength(2);
    expect(children[0].type).toBe('text');
    expect(children[0].text).toBe('text');
    expect(children[1].type).toBe('element');
    expect(children[1].tagName).toBe('span');
  });

  it('records bounded local provenance for removed content and transformed markup', () => {
    document.body.innerHTML = `
      <div id="content" class="stable css-abcdef" style="color:red" onclick="unsafe()"><span>safe</span><script>token=live-secret</script></div>
      <div id="markup-only" class="stable css-fedcba" onmouseover="unsafe()">safe<!-- framework marker --></div>
    `;

    const tree = serializeDom(document.body)!;
    const byID = (id: string) => tree.children?.find((node) =>
      node.attributes?.some((attribute) => attribute.name === 'id' && attribute.value === id));
    const content = byID('content');
    const markupOnly = byID('markup-only');

    expect(JSON.stringify(tree)).not.toContain('live-secret');
    expect(content?.sanitization).toEqual({
      markupAltered: true,
      contentOmitted: true,
      alteredAttributes: ['class'],
    });
    expect(markupOnly?.sanitization).toEqual({
      markupAltered: true,
      alteredAttributes: ['class'],
    });
  });

  it('returns null for a null root', () => {
    expect(serializeDom(null)).toBeNull();
  });

  it('returns null when the node budget is already exhausted', () => {
    expect(serializeDom(document.body, { maxNodes: 0 })).toBeNull();
  });

  it('filters out disallowed attributes', () => {
    document.body.innerHTML = '<div id="allowed" data-secret="hidden">text</div>';
    const tree = serializeDom(document.body);
    const div = tree!.children!.find((c) => c.tagName === 'div');
    expect(div).toBeDefined();
    expect(div!.attributes).toContainEqual({ name: 'id', value: 'allowed' });
    expect(div!.attributes!.some((a) => a.name === 'data-secret')).toBe(false);
  });

  it('skips whitespace-only text nodes', () => {
    document.body.innerHTML = '<p>   </p>';
    const tree = serializeDom(document.body);
    const p = tree!.children!.find((c) => c.tagName === 'p');
    expect(p).toBeDefined();
    expect(p!.children).toBeUndefined();
    expect(p!.sanitization).toEqual({ markupAltered: true });
  });

  it('marks non-empty text omitted by non-strict limits as content provenance', () => {
    document.body.innerHTML = '<p>meaningful output</p>';

    const nodeLimited = serializeDom(document.body, { maxNodes: 2 })!;
    expect(nodeLimited.children?.[0].sanitization).toEqual({
      markupAltered: true,
      contentOmitted: true,
    });

    const depthLimited = serializeDom(document.body, { maxDepth: 1 })!;
    expect(depthLimited.children?.[0].sanitization).toEqual({
      markupAltered: true,
      contentOmitted: true,
    });
  });

  it('redacts live secret form state and credential-like text before serialization', () => {
    document.body.innerHTML = `
      <input id="password" type="password" value="hunter2">
      <input id="normal" value="public-search">
      <p>email alice@example.com token=live-secret</p>
    `;
    const password = document.getElementById('password') as HTMLInputElement;
    password.value = 'changed-secret';

    const { domTree, report } = serializeDomWithReport(document.body, { strictLimits: true });
    const serialized = JSON.stringify(domTree);
    expect(serialized).not.toContain('hunter2');
    expect(serialized).not.toContain('changed-secret');
    expect(serialized).not.toContain('alice@example.com');
    expect(serialized).not.toContain('live-secret');
    expect(serialized).toContain(REDACTED_VALUE);
    expect(serialized).toContain('public-search');
    expect(report.redactionCount).toBeGreaterThanOrEqual(3);
  });

  it('preserves semantic form state while redacting autocomplete secrets', () => {
    document.body.innerHTML = `
      <input id="otp" autocomplete="one-time-code" placeholder="secret hint" value="123456">
      <input name="username" value="account-alias">
      <input id="checked" type="checkbox" checked>
      <select><option selected>chosen</option></select>
      <button disabled>disabled</button>
      <div id="classes" class="abcdefabcdef css-abcdef" aria-description="semantic label"></div>
    `;

    const tree = serializeDom(document.body)!;
    const serialized = JSON.stringify(tree);
    expect(serialized).not.toContain('123456');
    expect(serialized).not.toContain('secret hint');
    expect(serialized).not.toContain('account-alias');
    expect(serialized).toContain(REDACTED_VALUE);
    expect(serialized).toContain('"name":"checked","value":"true"');
    expect(serialized).toContain('"name":"selected","value":"true"');
    expect(serialized).toContain('"name":"disabled","value":"true"');
    expect(serialized).toContain('aria-description');

    const classNode = tree.children?.find((node) =>
      node.attributes?.some((attribute) => attribute.name === 'id' && attribute.value === 'classes'));
    expect(classNode?.attributes?.some((attribute) => attribute.name === 'class')).toBe(false);
  });

  it('uses an empty frame source when an iframe has no src attribute', () => {
    document.body.innerHTML = '<iframe title="embedded content"></iframe>';
    const tree = serializeDom(document.body)!;
    expect(tree.children?.[0]).toMatchObject({
      tagName: 'iframe',
      framePlaceholder: true,
      frameSrc: '',
    });
  });

  it('checks every node kind before deciding a depth boundary is semantic', () => {
    document.body.appendChild(document.createComment('ignored'));
    document.body.appendChild(document.createTextNode('   '));
    document.body.appendChild(document.createElement('script'));
    document.body.appendChild(document.createTextNode('keep'));

    const tree = serializeDom(document.body, { maxDepth: 0 })!;
    expect(tree.children).toBeUndefined();
    expect(tree.sanitization).toEqual({
      markupAltered: true,
      contentOmitted: true,
    });
  });

  it('removes unsafe URL payloads and redacts secret query parameters', () => {
    document.body.innerHTML = `
      <a id="data" href="data:text/html,secret">bad</a>
      <a id="query" href="https://example.com/?token=abc123&ok=yes">query</a>
    `;
    const tree = serializeDom(document.body)!;
    const serialized = JSON.stringify(tree);
    expect(serialized).toContain(REMOVED_URL_VALUE);
    expect(serialized).not.toContain('abc123');
    expect(serialized).toContain('ok=yes');
  });

  it('throws instead of silently truncating strict semantic captures', () => {
    document.body.innerHTML = '<div><span>complete text</span></div>';
    expect(() => serializeDom(document.body, { maxNodes: 2, strictLimits: true }))
      .toThrow(SemanticDomLimitError);
    expect(() => serializeDom(document.body, { maxDepth: 0, strictLimits: true }))
      .toThrow('semantic DOM depth limit exceeded');
    expect(() => serializeDom(document.body, { maxTextLength: 4, strictLimits: true }))
      .toThrow('semantic DOM text limit exceeded');
  });

  it('keeps complete semantic text when no explicit limit is configured', () => {
    const text = 'meaningful collection output '.repeat(100);
    document.body.textContent = text;
    const tree = serializeDom(document.body)!;
    expect(tree.children?.[0].text).toBe(text.trim());
  });
});
