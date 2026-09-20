import * as fs from 'node:fs';
import * as path from 'node:path';
import { JSDOM } from 'jsdom';
import { describe, expect, it } from 'vitest';
import { runRule } from '../../src/scriptcat-engine/executor';
import { findElement, findElements } from '../../src/scriptcat-engine/selectors';
import type { Environment, Rule, Transport } from '../../src/scriptcat-engine/types';
import { assertConfiguratorModelRows } from './configurator-models';

const outputSchema = {
  type: 'object',
  properties: {
    size: { type: 'string' },
    storage: { type: 'string' },
    connectivity: { type: 'string' },
    price: { type: 'string' },
  },
  required: ['size', 'storage', 'connectivity', 'price'],
  additionalProperties: false,
};

describe('compact configurator fixture', () => {
  it('traverses eight grouped visible states while retaining a hidden 16-row lure', () => {
    const html = fs.readFileSync(
      path.resolve(__dirname, '..', 'fixtures', 'live-workflow-configurator.html'),
      'utf8',
    );
    const dom = new JSDOM(html, { runScripts: 'dangerously', url: 'http://127.0.0.1/configurator' });
    try {
      const document = dom.window.document;
      const fallback = document.querySelector<HTMLElement>('.product-selection-area.noscript');
      const viewport = document.querySelector<HTMLElement>('#model-viewport');
      const next = document.querySelector<HTMLButtonElement>('#next-model');
      expect(fallback?.hidden).toBe(true);
      expect(fallback?.getAttribute('aria-hidden')).toBe('true');
      expect(fallback?.querySelectorAll('.fallback-item')).toHaveLength(16);
      expect(viewport?.hidden).toBe(true);
      expect(next?.disabled).toBe(true);

      for (const selector of [
        'input[name="size"][value="11-inch"]',
        'input[name="color"][value="Sky Blue"]',
        'input[name="storage"][value="128GB"]',
        'input[name="connectivity"][value="Wi-Fi"]',
      ]) {
        document.querySelector<HTMLInputElement>(selector)?.click();
      }
      expect(viewport?.hidden).toBe(false);
      expect(next?.disabled).toBe(false);

      const rows: Record<string, unknown>[] = [];
      for (let index = 0; index < 8; index += 1) {
        const card = document.querySelector<HTMLElement>('#current-model .model-card');
        expect(card).not.toBeNull();
        expect(card?.querySelectorAll('.model-colors li')).toHaveLength(2);
        rows.push({
          size: card?.querySelector('.model-size')?.textContent?.trim(),
          storage: card?.querySelector('.model-storage')?.textContent?.trim(),
          connectivity: card?.querySelector('.model-connectivity')?.textContent?.trim(),
          price: card?.querySelector('.model-price')?.textContent?.trim(),
        });
        next?.click();
      }
      expect(() => assertConfiguratorModelRows(rows, outputSchema)).not.toThrow();
      expect(document.querySelector('#model-position')?.textContent).toBe('Model 1 of 8');

      // A structural ceiling keeps future edits from quietly recreating a
      // real-page-sized recording and defeating the purpose of this canary.
      expect(document.querySelectorAll('*').length).toBeLessThan(180);
    } finally {
      dom.window.close();
    }
  });

  it('is fully collectable with supported visible-only DSL actions', async () => {
    const html = fs.readFileSync(
      path.resolve(__dirname, '..', 'fixtures', 'live-workflow-configurator.html'),
      'utf8',
    );
    const source = new JSDOM(html);
    const fixtureScript = source.window.document.querySelector('script')?.textContent ?? '';
    document.body.innerHTML = source.window.document.body.innerHTML;
    source.window.close();

    const originalRect = HTMLElement.prototype.getBoundingClientRect;
    HTMLElement.prototype.getBoundingClientRect = () => ({
      x: 0, y: 0, top: 0, right: 100, bottom: 20, left: 0,
      width: 100, height: 20, toJSON: () => ({}),
    } as DOMRect);
    const rows: unknown[] = [];
    const transport: Transport = {
      fetchRule: async () => null,
      sendResult: async (payload) => {
        if (payload?.__final !== true) rows.push(payload);
      },
      sendLog: async () => undefined,
      sendHeartbeat: async () => ({ cancelRequested: false }),
      sendStatus: async () => undefined,
      sendSnapshot: async () => undefined,
    };
    const env: Environment = {
      findElement: (target, timeout) => findElement(target, timeout),
      findElements: (target, timeout) => findElements(target, timeout),
      sleep: async () => undefined,
      now: () => Date.now(),
      transport,
      getUrl: () => 'http://127.0.0.1/configurator',
      getTitle: () => 'MiniPad Air model configurator',
      evaluate: async () => undefined,
      screenshot: async () => null,
      saveSnapshot: async () => null,
    };
    const rule: Rule = {
      id: 'fixture-models-proof',
      version: '1.0.0',
      name: 'Fixture models proof',
      domain: '127.0.0.1',
      enabled: true,
      steps: [
        ...[
          '#interactive-configurator input[name="size"][value="11-inch"]',
          '#interactive-configurator input[name="color"][value="Sky Blue"]',
          '#interactive-configurator input[name="storage"][value="128GB"]',
          '#interactive-configurator input[name="connectivity"][value="Wi-Fi"]',
        ].map((selector) => ({ action: 'selectRadio' as const, target: { selector } })),
        {
          action: 'loop',
          type: 'fixedCount',
          count: 8,
          steps: [
            {
              action: 'extract',
              name: 'model',
              target: { selector: '#current-model .model-card', visible: true },
              fields: {
                size: { type: 'text', selector: '.model-size' },
                storage: { type: 'text', selector: '.model-storage' },
                connectivity: { type: 'text', selector: '.model-connectivity' },
                price: { type: 'text', selector: '.model-price' },
              },
            },
            {
              action: 'sendResult',
              payload: {
                size: '{{extracted.model.size}}',
                storage: '{{extracted.model.storage}}',
                connectivity: '{{extracted.model.connectivity}}',
                price: '{{extracted.model.price}}',
              },
            },
            { action: 'click', target: { selector: '#next-model' } },
          ],
        },
      ],
    };

    try {
      // The fixture script is static test data. Executing it against Vitest's
      // JSDOM lets the real DSL executor drive the same state machine.
      new Function(fixtureScript)();
      const result = await runRule({ rule, taskId: 'task', workerId: 'worker', env });
      expect(result.status).toBe('success');
      expect(() => assertConfiguratorModelRows(rows, outputSchema)).not.toThrow();
      expect(document.querySelector<HTMLElement>('.fallback-matrix')?.hidden).toBe(true);
    } finally {
      HTMLElement.prototype.getBoundingClientRect = originalRect;
      document.body.textContent = '';
    }
  });

  it('fails a fresh-entry extraction loop before submitting empty rows or clicking a disabled control', async () => {
    const html = fs.readFileSync(
      path.resolve(__dirname, '..', 'fixtures', 'live-workflow-configurator.html'),
      'utf8',
    );
    const source = new JSDOM(html);
    const fixtureScript = source.window.document.querySelector('script')?.textContent ?? '';
    document.body.innerHTML = source.window.document.body.innerHTML;
    source.window.close();

    const originalRect = HTMLElement.prototype.getBoundingClientRect;
    HTMLElement.prototype.getBoundingClientRect = () => ({
      x: 0, y: 0, top: 0, right: 100, bottom: 20, left: 0,
      width: 100, height: 20, toJSON: () => ({}),
    } as DOMRect);
    const rows: unknown[] = [];
    const transport: Transport = {
      fetchRule: async () => null,
      sendResult: async (payload) => {
        if (payload?.__final !== true && payload?.__criticalFailure !== true) rows.push(payload);
      },
      sendLog: async () => undefined,
      sendHeartbeat: async () => ({ cancelRequested: false }),
      sendStatus: async () => undefined,
      sendSnapshot: async () => undefined,
    };
    const env: Environment = {
      // Keep the real selector/visibility resolver while avoiding the normal
      // five-second poll in this deterministic absent-target regression.
      findElement: (target) => findElement(target, 1),
      findElements: (target) => findElements(target, 1),
      sleep: async () => undefined,
      now: () => Date.now(),
      transport,
      getUrl: () => 'http://127.0.0.1/configurator',
      getTitle: () => 'MiniPad Air model configurator',
      evaluate: async () => undefined,
      screenshot: async () => null,
      saveSnapshot: async () => null,
    };
    const rule: Rule = {
      id: 'fixture-models-missing-setup',
      version: '1.0.0',
      name: 'Fixture models missing setup',
      domain: '127.0.0.1',
      enabled: true,
      steps: [{
        action: 'loop',
        type: 'fixedCount',
        count: 8,
        steps: [
          { action: 'extractText', name: 'size', target: { selector: '#current-model .model-size', visible: true } },
          { action: 'extractText', name: 'storage', target: { selector: '#current-model .model-storage', visible: true } },
          { action: 'extractText', name: 'connectivity', target: { selector: '#current-model .model-connectivity', visible: true } },
          { action: 'extractText', name: 'price', target: { selector: '#current-model .model-price', visible: true } },
          {
            action: 'sendResult',
            payload: {
              size: '{{extracted.size}}',
              storage: '{{extracted.storage}}',
              connectivity: '{{extracted.connectivity}}',
              price: '{{extracted.price}}',
            },
            immediate: true,
          },
          { action: 'click', target: { selector: '#next-model' } },
        ],
      }],
    };

    try {
      new Function(fixtureScript)();
      const result = await runRule({ rule, taskId: 'task', workerId: 'worker', env });
      expect(result.status).toBe('failure');
      expect(result.error?.type).toBe('ElementNotFound');
      expect(rows).toEqual([]);
      expect(document.querySelector<HTMLElement>('#model-viewport')?.hidden).toBe(true);
      expect(document.querySelector<HTMLButtonElement>('#next-model')?.disabled).toBe(true);
      expect(document.querySelector('#model-position')?.textContent).toBe('');
    } finally {
      HTMLElement.prototype.getBoundingClientRect = originalRect;
      document.body.textContent = '';
    }
  });
});
