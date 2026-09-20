import * as crypto from 'node:crypto';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import type { DomNode, DomSnapshot, PageAgentRecording } from '../../src/rule-generator/types';
import {
  assertWindowsPrivatePath,
  secureWindowsPrivatePath,
} from '../qualification/windows-private-storage';
import { assertBooksRecording } from './books-category-matrix';
import { assertQuotesRecording } from './quotes-by-tag';
import {
  assertQuotesHumanChooseRecording,
  assertQuotesHumanInputRecording,
} from './quotes-human-generalization';

const FORMAT = 'aegiscrawler.live-workflow-recording.v1';
const MAX_ARTIFACT_BYTES = 128 * 1024 * 1024;
const MAX_REUSABLE_ACTIONS = 20;
type ReusableRecordingScenario =
  | 'baidu-search'
  | 'books-category-matrix'
  | 'quotes-by-tag'
  | 'quotes-human-choose-requirement'
  | 'quotes-human-input-requirement';

export type QuotesReusableRecordingScenario = Exclude<
  ReusableRecordingScenario,
  'baidu-search' | 'books-category-matrix'
>;

interface RecordingEnvelope {
  schema: typeof FORMAT;
  algorithm: 'aes-256-gcm';
  iv: string;
  tag: string;
  ciphertext: string;
}

interface RecordingPayload {
  schema: typeof FORMAT;
  scenario: ReusableRecordingScenario;
  savedAt: string;
  recording: PageAgentRecording;
}

export interface ReusableRecordingPaths {
  directory: string;
  artifact: string;
  keyFile: string;
}

function reusableRecordingPaths(
  scenario: ReusableRecordingScenario,
  homeDir = os.homedir(),
): ReusableRecordingPaths {
  const directory = path.join(path.resolve(homeDir), '.aegiscrawler', 'qualification-recordings');
  return {
    directory,
    artifact: path.join(directory, `${scenario}.recording.enc.json`),
    keyFile: path.join(directory, `${scenario}.recording.key`),
  };
}

export function reusableBaiduRecordingPaths(homeDir = os.homedir()): ReusableRecordingPaths {
  return reusableRecordingPaths('baidu-search', homeDir);
}

export function reusableBooksRecordingPaths(homeDir = os.homedir()): ReusableRecordingPaths {
  return reusableRecordingPaths('books-category-matrix', homeDir);
}

export function reusableQuotesRecordingPaths(
  homeDir = os.homedir(),
  scenario: QuotesReusableRecordingScenario = 'quotes-by-tag',
): ReusableRecordingPaths {
  return reusableRecordingPaths(scenario, homeDir);
}

function assertExpectedPaths(
  paths: ReusableRecordingPaths,
  scenario: ReusableRecordingScenario,
): void {
  const directory = path.resolve(paths.directory);
  if (path.basename(directory) !== 'qualification-recordings'
      || path.basename(path.dirname(directory)) !== '.aegiscrawler'
      || path.resolve(path.dirname(paths.artifact)) !== directory
      || path.basename(paths.artifact) !== `${scenario}.recording.enc.json`
      || path.resolve(path.dirname(paths.keyFile)) !== directory
      || path.basename(paths.keyFile) !== `${scenario}.recording.key`) {
    throw new Error('reusable recording paths must use the dedicated qualification directory layout');
  }
}

function assertPrivateDirectoryLayout(directory: string): void {
  const parent = fs.lstatSync(path.dirname(directory));
  if (!parent.isDirectory() || parent.isSymbolicLink()) {
    throw new Error('reusable recording parent must be a non-symlink .aegiscrawler directory');
  }
  if (process.platform !== 'win32' && (parent.mode & 0o777) !== 0o700) {
    throw new Error('reusable recording parent must use mode 0700');
  }
  assertWindowsPrivatePath(path.dirname(directory), 'directory');
}

function assertPrivateRegularFile(file: string, label: string): fs.Stats {
  const stat = fs.lstatSync(file);
  if (!stat.isFile() || stat.isSymbolicLink()) throw new Error(`${label} must be a regular non-symlink file`);
  if (process.platform !== 'win32' && (stat.mode & 0o777) !== 0o600) {
    throw new Error(`${label} must use mode 0600`);
  }
  assertWindowsPrivatePath(file, 'file');
  return stat;
}

function prepareDirectory(directory: string): void {
  const parentExisted = fs.existsSync(path.dirname(directory));
  fs.mkdirSync(path.dirname(directory), { recursive: true, mode: 0o700 });
  if (!parentExisted) secureWindowsPrivatePath(path.dirname(directory), 'directory');
  assertPrivateDirectoryLayout(directory);
  let created = false;
  try {
    fs.mkdirSync(directory, { mode: 0o700 });
    created = true;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'EEXIST') throw error;
  }
  if (created) secureWindowsPrivatePath(directory, 'directory');
  const stat = fs.lstatSync(directory);
  if (!stat.isDirectory() || stat.isSymbolicLink()) {
    throw new Error('reusable recording directory must be a non-symlink directory');
  }
  if (process.platform !== 'win32' && (stat.mode & 0o777) !== 0o700) {
    throw new Error('reusable recording directory must use mode 0700');
  }
  assertWindowsPrivatePath(directory, 'directory');
}

function assertExistingDirectory(directory: string): void {
  assertPrivateDirectoryLayout(directory);
  const stat = fs.lstatSync(directory);
  if (!stat.isDirectory() || stat.isSymbolicLink()) {
    throw new Error('reusable recording directory must be a non-symlink directory');
  }
  if (process.platform !== 'win32' && (stat.mode & 0o777) !== 0o700) {
    throw new Error('reusable recording directory must use mode 0700');
  }
  assertWindowsPrivatePath(directory, 'directory');
}

function loadOrCreateKey(paths: ReusableRecordingPaths): Buffer {
  if (!fs.existsSync(paths.keyFile)) {
    fs.writeFileSync(paths.keyFile, crypto.randomBytes(32).toString('hex'), { mode: 0o600, flag: 'wx' });
    secureWindowsPrivatePath(paths.keyFile, 'file');
  }
  assertPrivateRegularFile(paths.keyFile, 'reusable recording key file');
  const encoded = fs.readFileSync(paths.keyFile, 'utf8').trim();
  if (!/^[0-9a-f]{64}$/i.test(encoded)) throw new Error('reusable recording key must be 32-byte hex');
  return Buffer.from(encoded, 'hex');
}

function readExistingKey(paths: ReusableRecordingPaths): Buffer {
  assertPrivateRegularFile(paths.keyFile, 'reusable recording key file');
  const encoded = fs.readFileSync(paths.keyFile, 'utf8').trim();
  if (!/^[0-9a-f]{64}$/i.test(encoded)) throw new Error('reusable recording key must be 32-byte hex');
  return Buffer.from(encoded, 'hex');
}

function assertScenarioURL(
  value: string,
  label: string,
  scenario: ReusableRecordingScenario,
): void {
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error(`${label} must be a valid scenario HTTP(S) URL`);
  }
  const hostname = url.hostname.toLowerCase();
  const valid = scenario === 'baidu-search'
    ? ['http:', 'https:'].includes(url.protocol)
      && (hostname === 'baidu.com' || hostname.endsWith('.baidu.com'))
    : scenario === 'books-category-matrix'
      ? url.protocol === 'https:'
        && hostname === 'books.toscrape.com'
        && !url.port
        && !url.username
        && !url.password
        && !url.search
        && !url.hash
        && new Set([
          '/',
          '/catalogue/category/books/mystery_3/index.html',
          '/catalogue/category/books/mystery_3/page-2.html',
        ]).has(url.pathname)
      : url.protocol === 'https:' && url.origin === 'https://quotes.toscrape.com';
  if (!valid) {
    throw new Error(`${label} must be a valid ${scenario} URL`);
  }
}

function normalizedDOMText(node: DomNode): string {
  return [node.text ?? '', ...(node.children ?? []).map(normalizedDOMText)]
    .join(' ')
    .replace(/\s+/g, ' ')
    .trim();
}

function domAttribute(node: DomNode, name: string): string {
  return node.attributes?.find((attribute) => attribute.name.toLowerCase() === name)?.value ?? '';
}

function domClassTokens(node: DomNode): string[] {
  return domAttribute(node, 'class').split(/\s+/).filter(Boolean);
}

function domDescendants(node: DomNode): DomNode[] {
  return [node, ...(node.children ?? []).flatMap(domDescendants)];
}

interface BookEvidenceNode {
  node: DomNode;
  rendered: boolean;
  contentComplete: boolean;
}

interface BookRenderedNode {
  node: DomNode;
  rendered: boolean;
}

function bookRenderedDescendants(
  node: DomNode,
  parentRendered = true,
): BookRenderedNode[] {
  const rendered = parentRendered && node.rendered !== false;
  return [
    { node, rendered },
    ...(node.children ?? []).flatMap((child) =>
      bookRenderedDescendants(child, rendered)),
  ];
}

function bookEvidenceDescendants(
  node: DomNode,
  parentRendered = true,
  parentContentComplete = true,
): BookEvidenceNode[] {
  const rendered = parentRendered && node.rendered !== false;
  const contentComplete = parentContentComplete && node.sanitization?.contentOmitted !== true;
  return [
    { node, rendered, contentComplete },
    ...(node.children ?? []).flatMap((child) =>
      bookEvidenceDescendants(child, rendered, contentComplete)),
  ];
}

function bookAttributeIsExact(node: DomNode, name: string): boolean {
  const altered = node.sanitization?.alteredAttributes ?? [];
  const normalized = name.toLowerCase();
  return !altered.some((candidate) =>
    candidate === '*' || candidate.toLowerCase() === normalized);
}

function bookTextSourceIsComplete(source: BookEvidenceNode): boolean {
  return source.rendered
    && source.contentComplete
    && bookEvidenceDescendants(
      source.node,
      source.rendered,
      source.contentComplete,
    ).every((candidate) => candidate.rendered && candidate.contentComplete)
    && Boolean(normalizedDOMText(source.node));
}

function hasSemanticFrameBoundary(node: DomNode): boolean {
  return domDescendants(node).some((candidate) =>
    candidate.tagName?.toLowerCase() === 'iframe'
    || candidate.frameOrigin !== undefined
    || candidate.frameSrc !== undefined
    || candidate.framePlaceholder !== undefined
    || candidate.frameMatched !== undefined
    || candidate.frameStatus !== undefined
    || candidate.frameError !== undefined);
}

function hasExactBooksRootFrame(snapshot: DomSnapshot): boolean {
  const frames = snapshot.capture?.frames;
  if (!Array.isArray(frames) || frames.length !== 1) return false;
  const [root] = frames;
  return root.frameId === 0
    && root.parentFrameId === -1
    && root.url === snapshot.url
    && root.status === 'captured'
    && !root.error;
}

function hasCompleteBookRowEvidence(snapshot: DomSnapshot): boolean {
  if (!snapshot.domTree) return false;
  return bookRenderedDescendants(snapshot.domTree).some((rowSource) => {
    const row = rowSource.node;
    if (!rowSource.rendered || row.sanitization?.contentOmitted === true
        || row.type !== 'element' || row.tagName?.toLowerCase() !== 'article'
        || !domClassTokens(row).includes('product_pod')
        || !bookAttributeIsExact(row, 'class')) {
      return false;
    }
    const descendants = bookEvidenceDescendants(
      row,
      rowSource.rendered,
      true,
    );
    const link = descendants.find(({ node, rendered, contentComplete }) =>
      rendered
      && contentComplete
      && node.type === 'element'
      && node.tagName?.toLowerCase() === 'a'
      && bookAttributeIsExact(node, 'title')
      && bookAttributeIsExact(node, 'href')
      && domAttribute(node, 'title').trim()
      && domAttribute(node, 'href').trim());
    const price = descendants.find((source) =>
      source.node.type === 'element'
      && domClassTokens(source.node).includes('price_color')
      && bookAttributeIsExact(source.node, 'class')
      && bookTextSourceIsComplete(source));
    const availability = descendants.find((source) =>
      source.node.type === 'element'
      && domClassTokens(source.node).includes('availability')
      && bookAttributeIsExact(source.node, 'class')
      && bookTextSourceIsComplete(source));
    const rating = descendants.find(({ node, rendered, contentComplete }) =>
      rendered
      && contentComplete
      && node.type === 'element'
      && bookAttributeIsExact(node, 'class')
      && domClassTokens(node).includes('star-rating')
      && domClassTokens(node).length >= 2);
    return Boolean(link && price && availability && rating);
  });
}

function booksSnapshotPage(snapshot: DomSnapshot): 0 | 1 | 2 {
  const url = new URL(snapshot.url);
  if (url.pathname === '/') return 0;
  return url.pathname.endsWith('/page-2.html') ? 2 : 1;
}

function assertReusableRecording(
  recording: PageAgentRecording,
  scenario: ReusableRecordingScenario,
): void {
  if (recording.version !== '2.0.0' || recording.termination?.complete !== true) {
    throw new Error('reusable recording must be a complete semantic recording v2');
  }
  if (recording.meta.semanticDomVersion !== '1'
      || recording.meta.sanitizationVersion !== 'extension-v2'
      || !recording.meta.endedAt) {
    throw new Error('reusable recording must retain extension-v2 sanitization provenance');
  }
  if (!Array.isArray(recording.events) || recording.events.length === 0
      || recording.events.length > MAX_REUSABLE_ACTIONS || !Array.isArray(recording.snapshots)) {
    throw new Error(`reusable recording must contain between 1 and ${MAX_REUSABLE_ACTIONS} actions`);
  }
  if (recording.events.some((event) => event.type === 'executeJavascript')) {
    throw new Error('reusable recording must not contain executeJavascript actions');
  }
  if (recording.snapshots.length !== recording.events.length + 2) {
    throw new Error('reusable recording must contain one initial, one pre-action per event, and one final snapshot');
  }
  const domain = recording.meta.domain?.toLowerCase() ?? '';
  if (scenario === 'baidu-search') {
    if (domain !== 'baidu.com' && !domain.endsWith('.baidu.com')) {
      throw new Error('reusable recording is not scoped to baidu.com');
    }
  } else if (scenario === 'books-category-matrix') {
    if (domain !== 'books.toscrape.com') {
      throw new Error('reusable recording is not scoped to books.toscrape.com');
    }
    assertBooksRecording(recording);
    const indexedClicks = recording.events.flatMap((event, index) =>
      event.type === 'click' ? [{ event, index }] : []);
    if (indexedClicks.length !== 2
        || recording.events[0]?.type !== 'click'
        || recording.events.some((event) => event.type !== 'click' && event.type !== 'scroll')) {
      throw new Error('reusable Books recording must contain exactly two clicks and optional scrolls only');
    }
    const clickTargets = indexedClicks.map(({ event, index }) => {
      const snapshot = recording.snapshots[index + 1];
      const target = snapshot?.selectorMap[event.index];
      if (!snapshot || snapshot.phase !== 'before-action' || snapshot.actionIndex !== index
          || !target || target.tagName.toLowerCase() !== 'a' || !target.selector.trim()
          || target.boundingRect.width <= 0 || target.boundingRect.height <= 0) {
        throw new Error(`reusable Books click ${index} is not bound to one recorded anchor target`);
      }
      return { snapshot, target };
    });
    if (clickTargets[0].snapshot.url !== 'https://books.toscrape.com/') {
      throw new Error('reusable Books first click must start from the catalogue root');
    }
    let nextPage: URL;
    try {
      nextPage = new URL(clickTargets[1].snapshot.url);
    } catch {
      throw new Error('reusable Books Next click snapshot URL is invalid');
    }
    if (nextPage.origin !== 'https://books.toscrape.com'
        || nextPage.pathname !== '/catalogue/category/books/mystery_3/index.html') {
      throw new Error('reusable Books second click must start from Mystery page 1');
    }
    for (const { event, index } of recording.events.map((event, index) => ({ event, index }))) {
      const before = recording.snapshots[index + 1];
      const after = recording.snapshots[index + 2];
      if (event.type === 'scroll' && before.url !== after.url) {
        throw new Error(`reusable Books scroll ${index} must not change the page URL`);
      }
    }
    if (booksSnapshotPage(recording.snapshots[indexedClicks[0].index + 2]) !== 1
        || booksSnapshotPage(recording.snapshots[indexedClicks[1].index + 2]) !== 2) {
      throw new Error('reusable Books clicks must commit root to Mystery page 1 to page 2');
    }
    if (recording.snapshots.some((snapshot) => snapshot.capture?.redactionCount !== 0)) {
      throw new Error('reusable Books recording must contain zero redacted DOM values');
    }
    if (recording.snapshots.some((snapshot) =>
      !Number.isSafeInteger(snapshot.capture?.removedNodeCount)
      || (snapshot.capture?.removedNodeCount ?? -1) < 0)) {
      throw new Error('reusable Books recording contains an invalid removed-node audit count');
    }
    if (recording.snapshots.some((snapshot) =>
      !hasExactBooksRootFrame(snapshot)
      || Boolean(snapshot.domTree && hasSemanticFrameBoundary(snapshot.domTree)))) {
      throw new Error('reusable Books recording must be frame-free');
    }
    const evidencedPages = new Set(recording.snapshots
      .filter(hasCompleteBookRowEvidence)
      .map(booksSnapshotPage));
    if (!evidencedPages.has(1) || !evidencedPages.has(2)) {
      throw new Error('reusable Books recording must retain complete product evidence on both Mystery pages');
    }
  } else {
    if (domain !== 'quotes.toscrape.com') {
      throw new Error('reusable recording is not scoped to quotes.toscrape.com');
    }
    if (scenario === 'quotes-human-choose-requirement') {
      assertQuotesHumanChooseRecording(recording);
    } else if (scenario === 'quotes-human-input-requirement') {
      assertQuotesHumanInputRecording(recording);
    } else {
      assertQuotesRecording(recording);
    }
  }
  assertScenarioURL(recording.meta.startUrl, 'reusable recording start URL', scenario);
  recording.events.forEach((event, index) => {
    if (event.type === 'navigate') {
      assertScenarioURL(event.url, `reusable recording event ${index} URL`, scenario);
    }
  });
  recording.snapshots.forEach((snapshot, index) => {
    const expectedPhase = index === 0
      ? 'initial'
      : index === recording.snapshots.length - 1 ? 'final' : 'before-action';
    if (snapshot.phase !== expectedPhase || snapshot.sequence !== index) {
      throw new Error(`reusable recording snapshot ${index} has invalid phase or sequence`);
    }
    if (expectedPhase === 'before-action' && snapshot.actionIndex !== index - 1) {
      throw new Error(`reusable recording snapshot ${index} is not linked to its action`);
    }
    if (!snapshot.domTree || snapshot.capture?.status !== 'complete') {
      throw new Error(`reusable recording snapshot ${index} is not a complete semantic DOM capture`);
    }
    if (!Array.isArray(snapshot.capture.frames)
        || snapshot.capture.frames.some((frame) => frame.status !== 'captured')) {
      throw new Error(`reusable recording snapshot ${index} contains an unavailable frame`);
    }
    if (scenario === 'books-category-matrix' && !hasExactBooksRootFrame(snapshot)) {
      throw new Error('reusable Books recording must be frame-free');
    }
    assertScenarioURL(snapshot.url, `reusable recording snapshot ${index} URL`, scenario);
  });
}

function saveReusableRecording(
  paths: ReusableRecordingPaths,
  source: PageAgentRecording,
  scenario: ReusableRecordingScenario,
): void {
  assertExpectedPaths(paths, scenario);
  const recording = structuredClone(source);
  delete recording.meta.serverRecordingId;
  assertReusableRecording(recording, scenario);
  prepareDirectory(paths.directory);
  const key = loadOrCreateKey(paths);
  const payload: RecordingPayload = {
    schema: FORMAT,
    scenario,
    savedAt: new Date().toISOString(),
    recording,
  };
  const iv = crypto.randomBytes(12);
  const cipher = crypto.createCipheriv('aes-256-gcm', key, iv);
  cipher.setAAD(Buffer.from(`${FORMAT}\0${scenario}`, 'utf8'));
  const ciphertext = Buffer.concat([cipher.update(JSON.stringify(payload), 'utf8'), cipher.final()]);
  if (ciphertext.length === 0 || ciphertext.length > MAX_ARTIFACT_BYTES) {
    throw new Error('reusable recording artifact has an invalid size');
  }
  const envelope: RecordingEnvelope = {
    schema: FORMAT,
    algorithm: 'aes-256-gcm',
    iv: iv.toString('base64'),
    tag: cipher.getAuthTag().toString('base64'),
    ciphertext: ciphertext.toString('base64'),
  };
  const temporary = `${paths.artifact}.tmp-${process.pid}-${Date.now()}`;
  try {
    fs.writeFileSync(temporary, JSON.stringify(envelope), { mode: 0o600, flag: 'wx' });
    secureWindowsPrivatePath(temporary, 'file');
    fs.renameSync(temporary, paths.artifact);
    fs.chmodSync(paths.artifact, 0o600);
  } finally {
    if (fs.existsSync(temporary)) fs.unlinkSync(temporary);
  }
}

export function saveReusableBaiduRecording(
  paths: ReusableRecordingPaths,
  source: PageAgentRecording,
): void {
  saveReusableRecording(paths, source, 'baidu-search');
}

export function saveReusableBooksRecording(
  paths: ReusableRecordingPaths,
  source: PageAgentRecording,
): void {
  saveReusableRecording(paths, source, 'books-category-matrix');
}

export function saveReusableQuotesRecording(
  paths: ReusableRecordingPaths,
  source: PageAgentRecording,
  scenario: QuotesReusableRecordingScenario = 'quotes-by-tag',
): void {
  saveReusableRecording(paths, source, scenario);
}

function loadReusableRecording(
  paths: ReusableRecordingPaths,
  scenario: ReusableRecordingScenario,
): PageAgentRecording {
  assertExpectedPaths(paths, scenario);
  assertExistingDirectory(paths.directory);
  const key = readExistingKey(paths);
  const stat = assertPrivateRegularFile(paths.artifact, 'reusable recording artifact');
  if (stat.size <= 0 || stat.size > MAX_ARTIFACT_BYTES) throw new Error('reusable recording artifact has an invalid size');
  const envelope = JSON.parse(fs.readFileSync(paths.artifact, 'utf8')) as RecordingEnvelope;
  if (envelope.schema !== FORMAT || envelope.algorithm !== 'aes-256-gcm') {
    throw new Error('unsupported reusable recording artifact format');
  }
  const decodeCanonicalBase64 = (encoded: unknown, label: string, expectedBytes?: number): Buffer => {
    if (typeof encoded !== 'string') throw new Error(`${label} must be canonical base64`);
    const decoded = Buffer.from(encoded, 'base64');
    if (decoded.toString('base64') !== encoded || (expectedBytes !== undefined && decoded.length !== expectedBytes)) {
      throw new Error(`${label} must be canonical base64${expectedBytes ? ` encoding ${expectedBytes} bytes` : ''}`);
    }
    return decoded;
  };
  const iv = decodeCanonicalBase64(envelope.iv, 'reusable recording IV', 12);
  const tag = decodeCanonicalBase64(envelope.tag, 'reusable recording authentication tag', 16);
  const ciphertext = decodeCanonicalBase64(envelope.ciphertext, 'reusable recording ciphertext');
  if (ciphertext.length === 0) throw new Error('reusable recording ciphertext must not be empty');
  const decipher = crypto.createDecipheriv('aes-256-gcm', key, iv);
  decipher.setAAD(Buffer.from(`${FORMAT}\0${scenario}`, 'utf8'));
  decipher.setAuthTag(tag);
  const plaintext = Buffer.concat([
    decipher.update(ciphertext),
    decipher.final(),
  ]).toString('utf8');
  const payload = JSON.parse(plaintext) as RecordingPayload;
  if (payload.schema !== FORMAT || payload.scenario !== scenario) {
    throw new Error(`reusable recording payload does not match the ${scenario} scenario`);
  }
  if (typeof payload.savedAt !== 'string' || !Number.isFinite(Date.parse(payload.savedAt))) {
    throw new Error('reusable recording payload has an invalid save timestamp');
  }
  const recording = structuredClone(payload.recording);
  delete recording.meta.serverRecordingId;
  assertReusableRecording(recording, scenario);
  return recording;
}

export function loadReusableBaiduRecording(paths: ReusableRecordingPaths): PageAgentRecording {
  return loadReusableRecording(paths, 'baidu-search');
}

export function loadReusableBooksRecording(paths: ReusableRecordingPaths): PageAgentRecording {
  return loadReusableRecording(paths, 'books-category-matrix');
}

export function loadReusableQuotesRecording(
  paths: ReusableRecordingPaths,
  scenario: QuotesReusableRecordingScenario = 'quotes-by-tag',
): PageAgentRecording {
  return loadReusableRecording(paths, scenario);
}
