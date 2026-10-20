const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const source = fs.readFileSync(path.join(__dirname, '../pkg/board/static/loops.js'), 'utf8');
const modulePromise = import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

test('loop lists escape untrusted names and expose selection', async () => {
  const { renderLoopList } = await modulePromise;
  const html = renderLoopList([{ id: 'a', name: '<script>bad</script>' }], 'a');
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /aria-pressed="true"/);
});

test('loop inspector preserves YAML safely and marks active versions', async () => {
  const { renderLoopDetail } = await modulePromise;
  const html = renderLoopDetail({
    loop: { id: 'loop', name: 'Notes', description: 'Summarize', criteria: '<b>accurate</b>', example: 'fix', activeVersion: 'v1' },
    versions: [{ id: 'v1', number: 1, yaml: 'instruction: </code><script>bad</script>', rationale: 'First', createdAt: '2026-10-20T00:00:00Z' }],
  }, 'v1');
  assert.match(html, /v1 · active/);
  assert.match(html, /&lt;\/code&gt;&lt;script&gt;/);
  assert.match(html, /versions\/v1\/export/);
});

test('empty loops offer a first version without inventing metrics', async () => {
  const { renderLoopDetail } = await modulePromise;
  const html = renderLoopDetail({ loop: { id: 'a', name: 'Notes', description: 'Task', criteria: 'Good', example: 'Input' }, versions: [] });
  assert.match(html, /Import YAML/);
  assert.match(html, /0 versions/);
  assert.doesNotMatch(html, /\$0|100%/);
});
