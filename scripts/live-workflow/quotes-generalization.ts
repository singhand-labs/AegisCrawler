import {
  QUOTES_BY_TAG_ORIGIN,
  QUOTES_BY_TAG_REQUIREMENT,
  QUOTES_REPLAY_TAG,
  type QuotesContract,
} from './quotes-by-tag';

const INPUT_CONSTRAINTS = {
  pattern: '^[a-z0-9]+(?:-[a-z0-9]+)*$',
  maxLength: 64,
} as const;

export const QUOTES_ONE_PAGE_CONTRACT: QuotesContract = {
  inputName: 'tag',
  replayTag: QUOTES_REPLAY_TAG,
  taskTag: 'reading',
  outputMap: {
    quote: 'quote',
    author: 'author',
    author_url: 'author_url',
  },
};

export const QUOTES_ONE_PAGE_REQUIREMENT = QUOTES_BY_TAG_REQUIREMENT;
export const QUOTES_ONE_PAGE_TASK_PAGES = 1;

export const QUOTES_RENAMED_CONTRACT: QuotesContract = {
  inputName: 'topic',
  replayTag: QUOTES_REPLAY_TAG,
  taskTag: 'inspirational',
  outputMap: {
    text: 'quote',
    writer: 'author',
    profile_url: 'author_url',
  },
};

export const QUOTES_RENAMED_REQUIREMENT = {
  title: 'Collect every quotation for a topic',
  description: [
    'Navigate to the public Quotes to Scrape topic page using the required topic input.',
    'Use a fixedCount pagination loop with a positive count of at most 10 to collect every visible quotation in page order across all pages.',
    'After each page, stop the loop when no visible Next link remains; otherwise click Next and continue.',
    'Return exactly text, writer, and profile_url as strings.',
  ].join(' '),
  requiredInputs: [{
    name: 'topic',
    type: 'string',
    description: 'Lowercase quote topic slug',
    constraints: INPUT_CONSTRAINTS,
  }],
  optionalInputs: [],
  outputFields: [
    { name: 'text', type: 'string', description: 'Visible quotation text' },
    { name: 'writer', type: 'string', description: 'Visible writer name' },
    {
      name: 'profile_url',
      type: 'string',
      description: 'Same-origin writer detail URL',
    },
  ],
  sampleOutput: {
    text: 'A visible quotation.',
    writer: 'Example Writer',
    profile_url: `${QUOTES_BY_TAG_ORIGIN}/author/Example-Writer/`,
  },
};
export const QUOTES_RENAMED_TASK_PAGES = 2;

export const QUOTES_PROJECTION_CONTRACT: QuotesContract = {
  inputName: 'category',
  replayTag: QUOTES_REPLAY_TAG,
  taskTag: 'books',
  outputMap: {
    quote: 'quote',
    author: 'author',
  },
};

export const QUOTES_PROJECTION_REQUIREMENT = {
  title: 'Collect quote and author for a category',
  description: [
    'Navigate to the public Quotes to Scrape tag page using the required category input.',
    'Use a fixedCount pagination loop with a positive count of at most 10 to collect every visible quote in page order across all pages.',
    'After each page, stop the loop when no visible Next link remains; otherwise click Next and continue.',
    'Return exactly quote and author as strings; do not return the author URL.',
  ].join(' '),
  requiredInputs: [{
    name: 'category',
    type: 'string',
    description: 'Lowercase quote category slug',
    constraints: INPUT_CONSTRAINTS,
  }],
  optionalInputs: [],
  outputFields: [
    { name: 'quote', type: 'string', description: 'Visible quote text' },
    { name: 'author', type: 'string', description: 'Visible author name' },
  ],
  sampleOutput: {
    quote: 'A visible quotation.',
    author: 'Example Author',
  },
};
export const QUOTES_PROJECTION_TASK_PAGES = 2;
