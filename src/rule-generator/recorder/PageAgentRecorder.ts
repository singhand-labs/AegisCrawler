import type { PageAgentRecording, PageAgentEvent, DomElementInfo } from '../types';
import { effectiveRole } from '../../rule-engine/aria-roles';

export interface RecorderOptions {
  protocolVersion?: '1.0.0' | '2.0.0';
  captureSnapshotBeforeEachEvent?: boolean;
  maxEvents?: number;
  maxSnapshots?: number;
  maxDurationMs?: number;
  maxRecordingBytes?: number;
  warningThreshold?: number;
  selectorIndexTTLMs?: number;
  maxDomTreeDepth?: number;
  maxDomTreeNodes?: number;
  maxDomTreeTextLength?: number;
  /** When true, the recorder will not persist state to sessionStorage. */
  disableCrossPagePersistence?: boolean;
  /**
   * H-6: when true, reject synthetic DOM events (event.isTrusted === false).
   * Prevents hostile pages from polluting recordings via dispatchEvent.
   * Default false for backward compatibility; production should enable.
   */
  enforceIsTrusted?: boolean;
}

export interface PageControllerLike {
  /** PageAgent PageController 标准方法 */
  getCurrentUrl?(): Promise<string> | string;
  getBrowserState?(): Promise<any>;
  updateTree?(): Promise<any>;
  clickElement(index: number): Promise<any>;
  inputText(index: number, text: string): Promise<any>;
  selectOption(index: number, optionText: string): Promise<any>;
  scroll(options: { down: boolean; numPages: number; pixels?: number; index?: number }): Promise<any>;
  scrollHorizontally(options: { right: boolean; pixels: number; index?: number }): Promise<any>;
  executeJavascript(script: string): Promise<any>;
}

/**
 * Records interactions made through a PageController-like object.
 *
 * Warning: the recorder mutates the injected controller's methods in place
 * to intercept calls. Call {@link detach} to restore the original methods.
 */
export class PageAgentRecorder {
  private recording: PageAgentRecording;
  private controller: PageControllerLike;
  private options: RecorderOptions;
  private originals = new Map<keyof PageControllerLike, any>();

  constructor(controller: PageControllerLike, options: RecorderOptions = {}) {
    this.controller = controller;
    this.options = { captureSnapshotBeforeEachEvent: true, ...options };
    this.recording = this.createEmptyRecording();
    this.attachHooks();
  }

  private createEmptyRecording(): PageAgentRecording {
    return {
      version: this.options.protocolVersion ?? '1.0.0',
      meta: {
        startUrl: typeof window !== 'undefined' ? window.location.href : '',
        title: typeof document !== 'undefined' ? document.title : '',
        recordedAt: new Date().toISOString(),
        domain: typeof window !== 'undefined' ? window.location.hostname : '',
      },
      events: [],
      snapshots: [],
    };
  }

  private attachHooks() {
    const controller = this.controller as any;
    if (controller.__pageAgentRecorderAttached === true) {
      throw new Error('A PageAgentRecorder is already attached to this controller; call detach() on the existing recorder first.');
    }

    const methods: Array<{ name: keyof PageControllerLike; buildArgs: (args: any[]) => PageAgentEvent }> = [
      {
        name: 'clickElement',
        buildArgs: (args) => ({ type: 'click', index: args[0], timestamp: Date.now() }),
      },
      {
        name: 'inputText',
        buildArgs: (args) => ({ type: 'inputText', index: args[0], text: args[1], timestamp: Date.now() }),
      },
      {
        name: 'selectOption',
        buildArgs: (args) => ({ type: 'selectOption', index: args[0], optionText: args[1], timestamp: Date.now() }),
      },
      {
        name: 'scroll',
        buildArgs: (args) => ({
          type: 'scroll',
          direction: args[0].down ? 'down' : 'up',
          amount: args[0].pixels ?? args[0].numPages,
          unit: args[0].pixels ? 'pixels' : 'pages',
          index: args[0].index,
          timestamp: Date.now(),
        }),
      },
      {
        name: 'scrollHorizontally',
        buildArgs: (args) => ({
          type: 'scroll',
          direction: args[0].right ? 'right' : 'left',
          amount: args[0].pixels,
          unit: 'pixels',
          index: args[0].index,
          timestamp: Date.now(),
        }),
      },
      {
        name: 'executeJavascript',
        buildArgs: (args) => ({ type: 'executeJavascript', script: args[0], timestamp: Date.now() }),
      },
    ];

    for (const { name, buildArgs } of methods) {
      const original = (this.controller as any)[name].bind(this.controller);
      this.originals.set(name, original);
      (this.controller as any)[name] = async (...args: any[]) => {
        if (this.options.captureSnapshotBeforeEachEvent) {
          try {
            await this.captureSnapshot();
          } catch (err) {
            console.warn(`[PageAgentRecorder] snapshot capture failed for ${name}:`, err);
          }
        }
        const result = await original(...args);
        this.recording.events.push(buildArgs(args));
        return result;
      };
    }

    (this.controller as any).__pageAgentRecorderAttached = true;
  }

  detach() {
    for (const [name, original] of this.originals) {
      (this.controller as any)[name] = original;
    }
    this.originals.clear();
    delete (this.controller as any).__pageAgentRecorderAttached;
  }

  private async captureSnapshot() {
    const controller = this.controller as any;

    if (typeof controller.updateTree === 'function') {
      await controller.updateTree();
    }

    let rawMap: Map<number, any> | undefined;
    if (typeof controller.getBrowserState === 'function') {
      const state = await controller.getBrowserState();
      rawMap = state?.selectorMap;
    } else if (controller.selectorMap) {
      rawMap = controller.selectorMap;
    }

    const selectorMap: Record<number, DomElementInfo> = {};
    if (rawMap) {
      for (const [index, node] of rawMap.entries()) {
        const el = node.ref as Element | undefined;
        selectorMap[index] = {
          index,
          tagName: el?.tagName?.toLowerCase() ?? node.tagName ?? 'div',
          selector: this.inferSelector(el),
          text: el?.textContent?.trim() ?? node.text,
          ariaLabel: el?.getAttribute?.('aria-label') ?? node.ariaLabel,
          role: el ? effectiveRole(el) : node.role,
          placeholder: el?.getAttribute?.('placeholder') ?? node.placeholder,
          name: el?.getAttribute?.('name') ?? node.name,
          boundingRect: el?.getBoundingClientRect
            ? (() => {
                const r = el.getBoundingClientRect();
                return { x: r.x, y: r.y, width: r.width, height: r.height };
              })()
            : node.boundingRect ?? { x: 0, y: 0, width: 0, height: 0 },
        };
      }
    }

    this.recording.snapshots.push({
      timestamp: Date.now(),
      url: typeof window !== 'undefined' ? window.location.href : '',
      selectorMap,
    });
  }

  private inferSelector(el?: Element): string {
    if (!el) return '*';
    if (el.id) return `#${el.id}`;
    const testId = el.getAttribute('data-testid');
    if (testId) return `[data-testid="${testId}"]`;
    const classes = Array.from(el.classList).join('.');
    if (classes) return `${el.tagName.toLowerCase()}.${classes}`;
    return el.tagName.toLowerCase();
  }

  getRecording(): PageAgentRecording {
    return this.recording;
  }

  toJSON(): string {
    return JSON.stringify(this.recording, null, 2);
  }
}
