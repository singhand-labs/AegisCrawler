import { describe, expect, it } from 'vitest';
import { getImplicitRole, effectiveRole } from './aria-roles';

describe('getImplicitRole', () => {
  describe('native semantic HTML elements', () => {
    it('maps <button> to "button"', () => {
      expect(getImplicitRole('button')).toBe('button');
    });

    it('maps <a> to "link"', () => {
      expect(getImplicitRole('a')).toBe('link');
    });

    it('maps <nav> to "navigation"', () => {
      expect(getImplicitRole('nav')).toBe('navigation');
    });

    it('maps <main> to "main"', () => {
      expect(getImplicitRole('main')).toBe('main');
    });

    it('maps <aside> to "complementary"', () => {
      expect(getImplicitRole('aside')).toBe('complementary');
    });

    it('maps <form> to "form"', () => {
      expect(getImplicitRole('form')).toBe('form');
    });

    it('maps <img> to "image"', () => {
      expect(getImplicitRole('img')).toBe('image');
    });

    it('maps <ul> to "list"', () => {
      expect(getImplicitRole('ul')).toBe('list');
    });

    it('maps <ol> to "list"', () => {
      expect(getImplicitRole('ol')).toBe('list');
    });

    it('maps <li> to "listitem"', () => {
      expect(getImplicitRole('li')).toBe('listitem');
    });

    it('maps <table> to "table"', () => {
      expect(getImplicitRole('table')).toBe('table');
    });

    it('maps <select> to "combobox"', () => {
      expect(getImplicitRole('select')).toBe('combobox');
    });

    it('maps <textarea> to "textbox"', () => {
      expect(getImplicitRole('textarea')).toBe('textbox');
    });

    it.each(['h1', 'h2', 'h3', 'h4', 'h5', 'h6'])('maps <%s> to "heading"', (tag) => {
      expect(getImplicitRole(tag)).toBe('heading');
    });
  });

  describe('header / footer context-sensitivity', () => {
    // The WAI-ARIA spec says <header> → banner and <footer> → contentinfo
    // only when they are top-level (not nested inside sectioning content).
    // The bounded map ships the simpler default; the ancestor-walk
    // refinement is deferred (see plan Open Questions). R-verify cannot
    // catch over-derivation here because it uses the same map at verify
    // time — but ariaLabel remains the effective recovery path.
    it('maps <header> to "banner" by default', () => {
      expect(getImplicitRole('header')).toBe('banner');
    });

    it('maps <footer> to "contentinfo" by default', () => {
      expect(getImplicitRole('footer')).toBe('contentinfo');
    });
  });

  describe('input[type=...] branching', () => {
    it('maps <input type="checkbox"> to "checkbox"', () => {
      expect(getImplicitRole('input', 'checkbox')).toBe('checkbox');
    });

    it('maps <input type="radio"> to "radio"', () => {
      expect(getImplicitRole('input', 'radio')).toBe('radio');
    });

    it('maps <input type="button"> to "button"', () => {
      expect(getImplicitRole('input', 'button')).toBe('button');
    });

    it('maps <input type="submit"> to "button"', () => {
      expect(getImplicitRole('input', 'submit')).toBe('button');
    });

    it('maps <input type="text"> to "textbox"', () => {
      expect(getImplicitRole('input', 'text')).toBe('textbox');
    });

    it('maps <input type="email"> to "textbox"', () => {
      expect(getImplicitRole('input', 'email')).toBe('textbox');
    });

    it('maps <input type="search"> to "searchbox"', () => {
      expect(getImplicitRole('input', 'search')).toBe('searchbox');
    });

    it('maps <input type="password"> to "textbox"', () => {
      expect(getImplicitRole('input', 'password')).toBe('textbox');
    });

    it('maps bare <input> with no type to "textbox"', () => {
      expect(getImplicitRole('input')).toBe('textbox');
    });
  });

  describe('case-insensitivity', () => {
    it('normalizes uppercase tag names', () => {
      expect(getImplicitRole('BUTTON')).toBe('button');
      expect(getImplicitRole('Button')).toBe('button');
    });

    it('normalizes uppercase <A>', () => {
      expect(getImplicitRole('A')).toBe('link');
    });
  });

  describe('elements without a mapping', () => {
    it('returns undefined for <span>', () => {
      expect(getImplicitRole('span')).toBeUndefined();
    });

    it('returns undefined for <div>', () => {
      expect(getImplicitRole('div')).toBeUndefined();
    });

    it('returns undefined for <section>', () => {
      expect(getImplicitRole('section')).toBeUndefined();
    });

    it('returns undefined for <p>', () => {
      expect(getImplicitRole('p')).toBeUndefined();
    });

    it('returns undefined for empty string', () => {
      expect(getImplicitRole('')).toBeUndefined();
    });

    it('returns undefined for custom elements', () => {
      expect(getImplicitRole('my-widget')).toBeUndefined();
    });
  });
});

describe('effectiveRole', () => {
  function el(tag: string, attrs: Record<string, string> = {}): Element {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    return e;
  }

  it('prefers explicit role attribute over implicit', () => {
    expect(effectiveRole(el('button', { role: 'link' }))).toBe('link');
  });

  it('falls back to implicit role for native <button> (no explicit role)', () => {
    expect(effectiveRole(el('button'))).toBe('button');
  });

  it('falls back to implicit role for <a>', () => {
    expect(effectiveRole(el('a'))).toBe('link');
  });

  it('falls back to implicit role for <input type=checkbox>', () => {
    expect(effectiveRole(el('input', { type: 'checkbox' }))).toBe('checkbox');
  });

  it('falls back to implicit "textbox" for bare <input> with no type', () => {
    expect(effectiveRole(el('input'))).toBe('textbox');
  });

  it('returns undefined for <span> with no role (no implicit mapping)', () => {
    expect(effectiveRole(el('span'))).toBeUndefined();
  });

  it('returns undefined for <div> with no role', () => {
    expect(effectiveRole(el('div'))).toBeUndefined();
  });

  it('treats empty explicit role as absent and falls back to implicit', () => {
    expect(effectiveRole(el('button', { role: '' }))).toBe('button');
  });

  it('returns undefined for <input type=hidden> (no role either way)', () => {
    expect(effectiveRole(el('input', { type: 'hidden' }))).toBeUndefined();
  });

  it('uses uppercase tag names case-insensitively (document.createElement uppercases anyway)', () => {
    expect(effectiveRole(el('BUTTON'))).toBe('button');
  });
});
