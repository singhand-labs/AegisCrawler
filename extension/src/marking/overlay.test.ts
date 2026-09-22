import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { PageMarkOverlay } from './overlay';
import type { PageMarkOverlayAdapter } from './overlay';
import type { DomElementInfo, PageMark, PageMarkRole } from '../../../src/rule-generator/types';

function elementInfo(element: Element): DomElementInfo {
  const rect = element.getBoundingClientRect();
  const selector = element.id ? `#${element.id}` : element.tagName.toLowerCase();
  return {
    index: 1,
    tagName: element.tagName.toLowerCase(),
    selector,
    stableSelector: selector,
    text: element.textContent?.trim(),
    boundingRect: { x: rect.x, y: rect.y, width: rect.width, height: rect.height },
  };
}

function createAdapter(marks: PageMark[] = []): PageMarkOverlayAdapter & {
  marks: PageMark[];
  saved: PageMark[];
  removed: string[];
} {
  const adapter = {
    marks,
    saved: [] as PageMark[],
    removed: [] as string[],
    buildMarkForElement(element: Element, role: PageMarkRole, note: string): PageMark {
      return {
        id: 'generated-mark',
        timestamp: 1,
        url: 'https://example.test/',
        role,
        note,
        element: elementInfo(element),
      };
    },
    saveMark(mark: PageMark): PageMark {
      const index = adapter.marks.findIndex((item) => item.id === mark.id);
      if (index >= 0) adapter.marks[index] = mark;
      else adapter.marks.push(mark);
      adapter.saved.push(mark);
      return mark;
    },
    removeMark(id: string): boolean {
      adapter.removed.push(id);
      const index = adapter.marks.findIndex((item) => item.id === id);
      if (index < 0) return false;
      adapter.marks.splice(index, 1);
      return true;
    },
    listMarks(): PageMark[] {
      return adapter.marks;
    },
  };
  return adapter;
}

function hostRoot(): ShadowRoot {
  const host = document.querySelector('[data-aegis-page-mark-overlay]') as HTMLDivElement | null;
  expect(host).not.toBeNull();
  expect(host!.shadowRoot).not.toBeNull();
  return host!.shadowRoot!;
}

function pointTo(element: Element): void {
  if (typeof document.elementFromPoint !== 'function') {
    Object.defineProperty(document, 'elementFromPoint', {
      configurable: true,
      value: () => null,
    });
  }
  vi.spyOn(document, 'elementFromPoint').mockReturnValue(element);
}

function clickAt(x = 12, y = 12): void {
  const target = document.elementFromPoint?.(x, y) ?? document;
  target.dispatchEvent(new MouseEvent('click', {
    clientX: x,
    clientY: y,
    bubbles: true,
    cancelable: true,
  }));
}

function mouseMoveAt(x = 12, y = 12): void {
  const target = document.elementFromPoint?.(x, y) ?? document;
  target.dispatchEvent(new MouseEvent('mousemove', { clientX: x, clientY: y, bubbles: true }));
}

function clickShadowButton(label: string): void {
  const button = Array.from(hostRoot().querySelectorAll<HTMLButtonElement>('button'))
    .find((item) => item.textContent === label);
  expect(button).toBeDefined();
  button!.click();
}

describe('PageMarkOverlay', () => {
  let target: HTMLButtonElement;

  beforeEach(() => {
    document.body.innerHTML = '<button id="product-title">Product title</button>';
    target = document.getElementById('product-title') as HTMLButtonElement;
    target.getBoundingClientRect = () => ({
      x: 10, y: 20, left: 10, top: 20, right: 130, bottom: 50,
      width: 120, height: 30,
      toJSON: () => ({}),
    } as DOMRect);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    document.querySelectorAll('[data-aegis-page-mark-overlay]').forEach((node) => node.remove());
    document.body.innerHTML = '';
  });

  it('toggles marking mode, suppresses page clicks, and saves a new mark', () => {
    const adapter = createAdapter();
    const overlay = new PageMarkOverlay(adapter);
    overlay.mount();
    pointTo(target);
    const pageClick = vi.fn();
    target.addEventListener('click', pageClick);

    clickShadowButton('标注采集意图');
    mouseMoveAt();
    clickAt();

    expect(pageClick).not.toHaveBeenCalled();
    const textarea = hostRoot().querySelector('textarea') as HTMLTextAreaElement;
    textarea.value = '商品标题字段';
    clickShadowButton('保存');

    expect(adapter.saved).toEqual([expect.objectContaining({
      id: 'generated-mark',
      role: 'field',
      note: '商品标题字段',
      element: expect.objectContaining({ selector: '#product-title' }),
    })]);
    overlay.unmount();
  });

  it('edits and deletes an existing mark for the same selector', () => {
    const existing: PageMark = {
      id: 'existing-mark',
      timestamp: 1,
      url: 'https://example.test/',
      role: 'field',
      note: '旧备注',
      element: elementInfo(target),
    };
    const adapter = createAdapter([existing]);
    const overlay = new PageMarkOverlay(adapter);
    overlay.mount();
    pointTo(target);

    clickShadowButton('标注采集意图');
    clickAt();
    const textarea = hostRoot().querySelector('textarea') as HTMLTextAreaElement;
    expect(textarea.value).toBe('旧备注');
    textarea.value = '不要采集广告区域';
    clickShadowButton('排除');
    clickShadowButton('保存');

    expect(adapter.saved.at(-1)).toMatchObject({
      id: 'existing-mark',
      role: 'exclude',
      note: '不要采集广告区域',
    });

    clickAt();
    clickShadowButton('删除');
    expect(adapter.removed).toEqual(['existing-mark']);
    expect(adapter.marks).toEqual([]);
    overlay.unmount();
  });

  it('mounts and unmounts without leaving host nodes behind', () => {
    const overlay = new PageMarkOverlay(createAdapter());
    overlay.mount();
    expect(document.querySelector('[data-aegis-page-mark-overlay]')).not.toBeNull();
    overlay.unmount();
    expect(document.querySelector('[data-aegis-page-mark-overlay]')).toBeNull();
  });
});
