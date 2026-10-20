import MarkdownIt from './vendor/markdown-it/markdown-it.mjs';

function parser(tables) {
  const markdown = new MarkdownIt({ html: false, linkify: false, typographer: false, maxNesting: 20 });
  markdown.disable(tables ? ['image'] : ['image', 'table']);
  const validateLink = markdown.validateLink.bind(markdown);
  markdown.validateLink = (url) => {
    const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(url);
    return (!scheme || ['http', 'https', 'mailto'].includes(scheme[1].toLowerCase())) && validateLink(url);
  };
  return markdown;
}

const markdown = parser(true);
const withoutTables = parser(false);

// Bound parsing work and cached output independently of session length.
export const previewLimit = 64 * 1024;
const cacheLimit = 8;
const cache = new Map();

function tableBudgetExceeded(source) {
  let pipes = 0, lines = 1;
  for (const char of source) {
    if (char === '|') pipes++;
    if (char === '\n' || char === '\r') lines++;
  }
  // Upper bound for implicit cells, checked before the parser allocates tokens.
  return pipes > 0 && (pipes + 1) * lines > 4096;
}

export function renderResultMarkdown(source) {
  source = String(source ?? '');
  const truncated = source.length > previewLimit;
  const preview = source.slice(0, previewLimit);
  const key = `${truncated ? '1' : '0'}${preview}`;
  const cached = cache.get(key);
  if (cached) return cached;
  const tablesAsText = tableBudgetExceeded(preview);
  const result = { html: (tablesAsText ? withoutTables : markdown).render(preview), truncated, tablesAsText };
  if (cache.size === cacheLimit) cache.delete(cache.keys().next().value);
  cache.set(key, result);
  return result;
}
