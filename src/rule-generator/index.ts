export * from './types';
export { convert } from './converter/PageAgentToDslConverter';
export { writeYaml } from './output/yaml-writer';
export * from './intent/intent-templates';
export * from './intent/enhance';
export { expandSnapshotReferences } from './converter/snapshot-refs';
export { applyJsonPatch } from './converter/json-patch';
export type { JsonPatchOp } from './converter/json-patch';

import type { PageAgentRecording, ConvertOptions } from './types';
import { convert } from './converter/PageAgentToDslConverter';
import { writeYaml } from './output/yaml-writer';

export function convertToYaml(recording: PageAgentRecording, options?: ConvertOptions): string {
  return writeYaml(convert(recording, options));
}
