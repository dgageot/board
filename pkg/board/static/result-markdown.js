import MarkdownIt from './vendor/markdown-it/markdown-it.mjs';

const markdown = new MarkdownIt({ html: false, linkify: false, typographer: false, maxNesting: 20 });
markdown.disable('image');
const validateLink = markdown.validateLink.bind(markdown);
markdown.validateLink = (url) => {
  const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(url);
  return (!scheme || ['http', 'https', 'mailto'].includes(scheme[1].toLowerCase())) && validateLink(url);
};

// Bound parsing work and cached output independently of session length.
export const previewLimit = 64 * 1024;
const cacheLimit = 8;
const cache = new Map();

export function renderResultMarkdown(source) {
  source = String(source ?? '');
  const truncated = source.length > previewLimit;
  const preview = source.slice(0, previewLimit);
  const key = `${truncated ? '1' : '0'}${preview}`;
  const cached = cache.get(key);
  if (cached) return cached;
  const result = { html: markdown.render(preview), truncated };
  if (cache.size === cacheLimit) cache.delete(cache.keys().next().value);
  cache.set(key, result);
  return result;
}
