const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { pathToFileURL } = require('node:url');
const { test } = require('node:test');
const { createHash } = require('node:crypto');
const modulePromise = import(pathToFileURL(path.join(__dirname, '../pkg/board/static/result-markdown.js')).href);

test('renders headings, lists, tables, code and emphasis', async () => {
  const { renderResultMarkdown } = await modulePromise;
  const { html } = renderResultMarkdown('# Result\n\n**Good** and `code`.\n\n- One\n- Two\n\n| File | Status |\n| --- | --- |\n| a.go | Done |\n\n```go\nfmt.Println("<tag>")\n```');
  for (const tag of ['<h1>Result</h1>', '<strong>Good</strong>', '<code>code</code>', '<ul>', '<table>', '<pre><code class="language-go">']) assert.ok(html.includes(tag), tag);
  assert.ok(html.includes('&lt;tag&gt;'));
});

test('untrusted output cannot embed HTML, images or active protocols', async () => {
  const { renderResultMarkdown } = await modulePromise;
  for (const source of [
    '<script>alert(1)</script>', '<img src=x onerror=alert(1)>', '<svg/onload=alert(1)>',
    '![tracking](https://evil.example/pixel)', '[click](javascript:alert%281%29)',
    '[click](jav&#x61;script:alert%281%29)', '[click](data:text/html,evil)',
    '[local](file:///etc/passwd)', '[command](vscode://file/a)',
  ]) {
    const { html } = renderResultMarkdown(source);
    assert.doesNotMatch(html, /<(script|img|svg|iframe)\b/i, source);
    assert.doesNotMatch(html, /href="(?:javascript|data|file|vscode):/i, source);
  }
  assert.match(renderResultMarkdown('[Docs](https://example.com/?a=1&b=2)').html, /href="https:\/\/example.com\/\?a=1&amp;b=2"/);
});

test('caches unchanged previews and bounds large output without losing raw source', async () => {
  const { renderResultMarkdown, previewLimit } = await modulePromise;
  const first = renderResultMarkdown('## Cached');
  assert.equal(renderResultMarkdown('## Cached'), first);
  const source = 'x'.repeat(previewLimit + 100);
  const rendered = renderResultMarkdown(source);
  assert.equal(rendered.truncated, true);
  assert.equal(rendered.html, `<p>${'x'.repeat(previewLimit)}</p>\n`);
  assert.equal(source.length, previewLimit + 100);
});

test('unfinished streaming fences are repaired by the next full snapshot', async () => {
  const { renderResultMarkdown } = await modulePromise;
  assert.match(renderResultMarkdown('```go\nvar x = 1').html, /<pre><code/);
  assert.match(renderResultMarkdown('```go\nvar x = 1\n```\n\nDone.').html, /<\/code><\/pre>\n<p>Done\.<\/p>/);
});

test('vendored Markdown bundle matches its recorded digest', () => {
  const dir = path.join(__dirname, '../pkg/board/static/vendor/markdown-it');
  const manifest = JSON.parse(fs.readFileSync(path.join(dir, 'provenance.json')));
  const entry = manifest['markdown-it'];
  assert.equal(createHash('sha256').update(fs.readFileSync(path.join(dir, entry.file))).digest('hex'), entry.sha256);
});

test('sparse tables cannot amplify short input into unbounded implicit cells', async () => {
  const { renderResultMarkdown } = await modulePromise;
  const table = 'a|'.repeat(256) + '\n' + '-|'.repeat(256) + '\n' + 'x\n'.repeat(256) + '\n';
  const source = table.repeat(4);
  const result = renderResultMarkdown(source);
  assert.equal(result.tablesAsText, true);
  assert.doesNotMatch(result.html, /<table>/);
  assert.ok(result.html.length < source.length * 4);
});
