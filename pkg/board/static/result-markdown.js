import MarkdownIt from './vendor/markdown-it/markdown-it.mjs';

// The zero preset exposes just the table rule in the paragraph terminator chain.
const [tableRule] = new MarkdownIt('zero').enable('table').block.ruler.getRules('paragraph');
const tableCellLimit = 4096;

function boundedTable(state, start, end, silent) {
  const scope = `${state.level}:${state.blkIndent}`;
  const rejected = state.env.rejectedTables.get(scope);
  if (rejected && start >= rejected.start && start < rejected.end) return false;
  if (!tableRule(state, start, end, true)) return false;
  if (silent) return true;

  const lineText = (line) => state.src.slice(state.bMarks[line] + state.tShift[line], state.eMarks[line]);
  const columns = lineText(start + 1).split('|').filter((cell) => cell.trim()).length;
  let cells = columns;
  let tableEnd = start + 2;
  const available = tableCellLimit - state.env.tableCells;
  const terminators = state.md.block.ruler.getRules('blockquote');
  const parent = state.parentType;
  state.parentType = 'table';
  try {
    for (let line = start + 2; line < end; line++) {
      if (state.sCount[line] < state.blkIndent || state.sCount[line] - state.blkIndent >= 4 || !lineText(line).trim()) break;
      if (terminators.some((rule) => rule(state, line, end, true))) break;
      cells += columns;
      tableEnd = line + 1;
    }
  } finally {
    state.parentType = parent;
  }
  if (cells > available) {
    state.env.tablesAsText = true;
    state.env.rejectedTables.set(scope, { start, end: tableEnd });
    return false;
  }
  const accepted = tableRule(state, start, end, false);
  if (accepted) state.env.tableCells += cells;
  return accepted;
}

const markdown = new MarkdownIt({ html: false, linkify: false, typographer: false, maxNesting: 20 });
markdown.disable('image');
markdown.block.ruler.at('table', boundedTable, { alt: ['paragraph', 'reference'] });
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
  const env = { tableCells: 0, tablesAsText: false, rejectedTables: new Map() };
  const html = markdown.render(preview, env);
  const result = { html, truncated, tablesAsText: env.tablesAsText };
  if (cache.size === cacheLimit) cache.delete(cache.keys().next().value);
  cache.set(key, result);
  return result;
}
