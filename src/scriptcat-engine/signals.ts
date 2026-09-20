import type { ExecutionResult } from './types';

export class FlowControlSignal extends Error {
  constructor(public readonly signal: 'break' | 'continue') {
    super(`FlowControl:${signal}`);
  }
}

export class ExitSignal extends Error {
  constructor(
    public readonly status: ExecutionResult['status'],
    public readonly message: string = '',
  ) {
    super(`Exit:${status}:${message}`);
  }
}
