declare module 'diff' {
  export interface Change {
    value: string;
    added?: boolean;
    removed?: boolean;
  }
  export function diffJson(oldObj: object, newObj: object): Change[];
}
