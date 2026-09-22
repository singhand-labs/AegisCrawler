import type { DomElementInfo, PageMark, PageMarkRole } from '../../../src/rule-generator/types';

export interface PageMarkOverlayAdapter {
  buildMarkForElement(element: Element, role: PageMarkRole, note: string): PageMark;
  saveMark(mark: PageMark): PageMark;
  removeMark(id: string): boolean;
  listMarks(): PageMark[];
}

const ROLE_LABELS: Array<{ role: PageMarkRole; label: string }> = [
  { role: 'listItem', label: '列表项' },
  { role: 'field', label: '字段' },
  { role: 'nextPage', label: '下一页' },
  { role: 'input', label: '输入项' },
  { role: 'exclude', label: '排除' },
];

const MAX_NOTE_CHARS = 200;

function styleText(): string {
  return `
    :host { all: initial; color-scheme: light; }
    .toggle {
      position: fixed;
      right: 18px;
      bottom: 18px;
      z-index: 2147483647;
      padding: 8px 12px;
      border: 0;
      border-radius: 999px;
      background: #1677ff;
      color: #fff;
      font: 600 13px system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      box-shadow: 0 4px 14px rgba(0,0,0,.2);
      cursor: pointer;
    }
    .toggle.active { background: #fa8c16; }
    .highlight {
      position: fixed;
      z-index: 2147483646;
      pointer-events: none;
      border: 2px solid #1677ff;
      background: rgba(22,119,255,.12);
      border-radius: 4px;
      box-sizing: border-box;
      display: none;
    }
    .popover {
      position: fixed;
      z-index: 2147483647;
      width: 280px;
      box-sizing: border-box;
      padding: 12px;
      border: 1px solid #d9d9d9;
      border-radius: 10px;
      background: #fff;
      box-shadow: 0 8px 28px rgba(0,0,0,.24);
      font: 13px system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      color: #222;
      display: none;
    }
    .popover h3 { margin: 0 0 8px; font-size: 14px; }
    .selector { margin-bottom: 8px; color: #666; word-break: break-all; font: 12px ui-monospace, SFMono-Regular, Menlo, monospace; }
    .roles { display: flex; flex-wrap: wrap; gap: 6px; margin-bottom: 8px; }
    .role {
      padding: 5px 8px;
      border: 1px solid #d9d9d9;
      border-radius: 999px;
      background: #fff;
      cursor: pointer;
      font: inherit;
    }
    .role.selected { border-color: #1677ff; background: #e6f4ff; color: #0958d9; }
    textarea {
      box-sizing: border-box;
      width: 100%;
      min-height: 70px;
      padding: 7px;
      border: 1px solid #d9d9d9;
      border-radius: 6px;
      font: inherit;
      resize: vertical;
    }
    .footer { display: flex; gap: 8px; justify-content: flex-end; margin-top: 10px; }
    .footer button {
      padding: 6px 10px;
      border: 1px solid #d9d9d9;
      border-radius: 6px;
      background: #fff;
      cursor: pointer;
      font: inherit;
    }
    .footer .primary { border-color: #1677ff; background: #1677ff; color: #fff; }
    .footer .danger { color: #cf1322; }
    .badge {
      position: fixed;
      z-index: 2147483645;
      pointer-events: none;
      padding: 2px 6px;
      border-radius: 999px;
      background: #fa8c16;
      color: #fff;
      font: 11px system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      box-shadow: 0 2px 8px rgba(0,0,0,.2);
    }
  `;
}

export class PageMarkOverlay {
  private host: HTMLDivElement | null = null;
  private root: ShadowRoot | null = null;
  private toggle: HTMLButtonElement | null = null;
  private highlight: HTMLDivElement | null = null;
  private popover: HTMLDivElement | null = null;
  private roleButtons = new Map<PageMarkRole, HTMLButtonElement>();
  private noteInput: HTMLTextAreaElement | null = null;
  private selectorPreview: HTMLElement | null = null;
  private removeButton: HTMLButtonElement | null = null;
  private active = false;
  private selectedRole: PageMarkRole = 'field';
  private selectedElement: Element | null = null;
  private editingMarkId: string | null = null;
  private badges: HTMLDivElement[] = [];

  constructor(private readonly adapter: PageMarkOverlayAdapter) {}

  mount(): void {
    if (this.host || typeof document === 'undefined') return;
    this.host = document.createElement('div');
    this.host.setAttribute('data-aegis-page-mark-overlay', '');
    this.root = this.host.attachShadow({ mode: 'open' });
    const style = document.createElement('style');
    style.textContent = styleText();
    this.toggle = document.createElement('button');
    this.toggle.type = 'button';
    this.toggle.className = 'toggle';
    this.toggle.textContent = '标注采集意图';
    this.toggle.addEventListener('click', () => this.setActive(!this.active));
    this.highlight = document.createElement('div');
    this.highlight.className = 'highlight';
    this.popover = this.createPopover();
    this.root.append(style, this.highlight, this.popover, this.toggle);
    document.documentElement.appendChild(this.host);
    window.addEventListener('keydown', this.onKeyDown, true);
    document.addEventListener('mousemove', this.onMouseMove, true);
    document.addEventListener('click', this.onClick, true);
    document.addEventListener('scroll', this.refreshBadges, true);
    window.addEventListener('resize', this.refreshBadges, true);
    this.refreshBadges();
  }

  unmount(): void {
    this.setActive(false);
    window.removeEventListener('keydown', this.onKeyDown, true);
    document.removeEventListener('mousemove', this.onMouseMove, true);
    document.removeEventListener('click', this.onClick, true);
    document.removeEventListener('scroll', this.refreshBadges, true);
    window.removeEventListener('resize', this.refreshBadges, true);
    this.clearBadges();
    this.host?.remove();
    this.host = null;
    this.root = null;
  }

  refresh(): void {
    this.refreshBadges();
  }

  private createPopover(): HTMLDivElement {
    const popover = document.createElement('div');
    popover.className = 'popover';
    popover.addEventListener('click', (event) => event.stopPropagation());
    const title = document.createElement('h3');
    title.textContent = '标注这个元素';
    this.selectorPreview = document.createElement('div');
    this.selectorPreview.className = 'selector';
    const roles = document.createElement('div');
    roles.className = 'roles';
    for (const { role, label } of ROLE_LABELS) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'role';
      button.textContent = label;
      button.addEventListener('click', () => this.selectRole(role));
      this.roleButtons.set(role, button);
      roles.appendChild(button);
    }
    this.noteInput = document.createElement('textarea');
    this.noteInput.maxLength = MAX_NOTE_CHARS;
    this.noteInput.placeholder = '用大白话说明你想采集什么，例如“商品标题字段”';
    const footer = document.createElement('div');
    footer.className = 'footer';
    const cancel = document.createElement('button');
    cancel.type = 'button';
    cancel.textContent = '取消';
    cancel.addEventListener('click', () => this.hidePopover());
    this.removeButton = document.createElement('button');
    this.removeButton.type = 'button';
    this.removeButton.className = 'danger';
    this.removeButton.textContent = '删除';
    this.removeButton.addEventListener('click', () => {
      if (this.editingMarkId) {
        this.adapter.removeMark(this.editingMarkId);
        this.refreshBadges();
      }
      this.hidePopover();
    });
    const save = document.createElement('button');
    save.type = 'button';
    save.className = 'primary';
    save.textContent = '保存';
    save.addEventListener('click', () => this.saveSelectedMark());
    footer.append(this.removeButton, cancel, save);
    popover.append(title, this.selectorPreview, roles, this.noteInput, footer);
    this.selectRole(this.selectedRole);
    return popover;
  }

  private setActive(active: boolean): void {
    this.active = active;
    this.toggle?.classList.toggle('active', active);
    if (this.toggle) this.toggle.textContent = active ? '退出标注' : '标注采集意图';
    if (!active) {
      this.hideHighlight();
      this.hidePopover();
    }
  }

  private onKeyDown = (event: KeyboardEvent): void => {
    if (event.altKey && !event.metaKey && !event.ctrlKey && !event.shiftKey && event.key.toLowerCase() === 'm') {
      event.preventDefault();
      event.stopPropagation();
      this.setActive(!this.active);
    }
    if (this.active && event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      this.hidePopover();
      this.setActive(false);
    }
  };

  private onMouseMove = (event: MouseEvent): void => {
    if (!this.active || this.isOverlayEvent(event) || this.popover?.style.display === 'block') return;
    const element = this.elementFromPoint(event.clientX, event.clientY);
    if (!element) {
      this.hideHighlight();
      return;
    }
    this.showHighlight(element);
  };

  private onClick = (event: MouseEvent): void => {
    if (!this.active || this.isOverlayEvent(event)) return;
    const element = this.elementFromPoint(event.clientX, event.clientY);
    if (!element) return;
    event.preventDefault();
    event.stopPropagation();
    event.stopImmediatePropagation();
    this.openPopover(element);
  };

  private isOverlayEvent(event: Event): boolean {
    const path = typeof event.composedPath === 'function' ? event.composedPath() : [];
    return Boolean(this.host && path.includes(this.host));
  }

  private elementFromPoint(x: number, y: number): Element | null {
    const element = document.elementFromPoint(x, y);
    if (!element || this.host?.contains(element)) return null;
    if (element === document.documentElement || element === document.body) return null;
    return element;
  }

  private showHighlight(element: Element): void {
    const rect = element.getBoundingClientRect();
    if (!this.highlight || rect.width <= 0 || rect.height <= 0) return;
    Object.assign(this.highlight.style, {
      display: 'block',
      left: `${Math.max(0, rect.left)}px`,
      top: `${Math.max(0, rect.top)}px`,
      width: `${rect.width}px`,
      height: `${rect.height}px`,
    });
  }

  private hideHighlight(): void {
    if (this.highlight) this.highlight.style.display = 'none';
  }

  private openPopover(element: Element): void {
    this.selectedElement = element;
    this.showHighlight(element);
    const info = this.adapter.buildMarkForElement(element, this.selectedRole, '').element as DomElementInfo;
    const existing = this.adapter.listMarks().find((mark) => mark.element.selector === info.selector || mark.element.stableSelector === info.stableSelector);
    this.editingMarkId = existing?.id ?? null;
    this.selectRole(existing?.role ?? 'field');
    if (this.noteInput) this.noteInput.value = existing?.note ?? '';
    if (this.removeButton) this.removeButton.style.display = existing ? 'inline-block' : 'none';
    if (this.selectorPreview) this.selectorPreview.textContent = info.stableSelector || info.selector;
    const rect = element.getBoundingClientRect();
    this.positionPopover(rect);
  }

  private positionPopover(rect: DOMRect): void {
    if (!this.popover) return;
    const width = 280;
    const left = Math.min(Math.max(8, rect.left), Math.max(8, window.innerWidth - width - 8));
    const below = rect.bottom + 8;
    const top = below + 220 > window.innerHeight ? Math.max(8, rect.top - 230) : below;
    Object.assign(this.popover.style, { display: 'block', left: `${left}px`, top: `${top}px` });
  }

  private hidePopover(): void {
    if (this.popover) this.popover.style.display = 'none';
    this.selectedElement = null;
    this.editingMarkId = null;
  }

  private selectRole(role: PageMarkRole): void {
    this.selectedRole = role;
    for (const [candidate, button] of this.roleButtons) {
      button.classList.toggle('selected', candidate === role);
    }
  }

  private saveSelectedMark(): void {
    if (!this.selectedElement) return;
    const note = (this.noteInput?.value ?? '').trim();
    if ([...note].length > MAX_NOTE_CHARS) return;
    const mark = this.adapter.buildMarkForElement(this.selectedElement, this.selectedRole, note);
    const saved = this.editingMarkId ? { ...mark, id: this.editingMarkId } : mark;
    this.adapter.saveMark(saved);
    this.refreshBadges();
    this.hidePopover();
  }

  private refreshBadges = (): void => {
    this.clearBadges();
    if (!this.root) return;
    for (const mark of this.adapter.listMarks()) {
      let element: Element | null = null;
      try {
        element = document.querySelector(mark.element.selector);
      } catch {
        element = null;
      }
      if (!element) continue;
      const rect = element.getBoundingClientRect();
      if (rect.width <= 0 || rect.height <= 0) continue;
      const badge = document.createElement('div');
      badge.className = 'badge';
      badge.textContent = ROLE_LABELS.find((item) => item.role === mark.role)?.label ?? mark.role;
      Object.assign(badge.style, {
        left: `${Math.max(0, rect.left)}px`,
        top: `${Math.max(0, rect.top - 18)}px`,
      });
      this.root.appendChild(badge);
      this.badges.push(badge);
    }
  };

  private clearBadges(): void {
    for (const badge of this.badges) badge.remove();
    this.badges = [];
  }
}
