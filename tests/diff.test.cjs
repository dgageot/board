const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../pkg/board/static/app.js'), 'utf8');
const start = source.indexOf('function renderDiffFile(');
const end = source.indexOf('document.getElementById("close-diff")', start);
assert.ok(start >= 0 && end > start);
const context = vm.createContext({});
const utilsStart = source.indexOf('const ESC_CHARS =');
const utilsEnd = source.indexOf('// formatCost', utilsStart);
vm.runInContext(`const DIFF_AUTO_COLLAPSE_LINES = 500, DIFF_MAX_LINES_PER_FILE = 5000;\n${source.slice(utilsStart, utilsEnd)}\n${source.slice(start, end)}`, context);

function git(repo, ...args) {
  return execFileSync('git', ['-c', 'commit.gpgsign=false', '-c', 'core.filemode=true', ...args], {
    cwd: repo, encoding: 'utf8', timeout: 10000,
  });
}

test('binary, rename, mode-only and empty-file changes remain visible', (t) => {
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'board-diff-'));
  t.after(() => fs.rmSync(repo, { recursive: true, force: true }));
  git(repo, 'init', '-q');
  fs.writeFileSync(path.join(repo, 'binary.bin'), Buffer.from([0, 1, 2]));
  fs.writeFileSync(path.join(repo, 'before.txt'), 'hello\n');
  fs.writeFileSync(path.join(repo, 'mode.sh'), 'echo hello\n', { mode: 0o644 });
  git(repo, 'add', '.');
  git(repo, '-c', 'user.name=Test', '-c', 'user.email=test@example.com', 'commit', '-qm', 'base');
  fs.writeFileSync(path.join(repo, 'binary.bin'), Buffer.from([0, 1, 3]));
  git(repo, 'mv', 'before.txt', 'after.txt');
  fs.chmodSync(path.join(repo, 'mode.sh'), 0o755);
  fs.writeFileSync(path.join(repo, 'empty.txt'), '');
  git(repo, 'add', '--intent-to-add', 'empty.txt');
  const files = context.parseDiffFiles(git(repo, 'diff', '--find-renames', 'HEAD'));
  assert.equal(files.length, 4);
  const html = files.map(context.renderDiffFile).join('');
  for (const name of ['binary.bin', 'after.txt', 'mode.sh', 'empty.txt']) assert.ok(html.includes(name));
  for (const metadata of ['Binary files', 'rename from', 'old mode', 'new file mode']) assert.ok(html.includes(metadata));
  assert.match(context.renderDiffStats(files), /4 files changed/);
});

test('text hunks and empty diffs retain their behavior', () => {
  assert.equal(context.parseDiffFiles('').length, 0);
  const files = context.parseDiffFiles('diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n');
  assert.equal(files.length, 1);
  assert.equal(files[0].hunks[0].lines.length, 2);
  assert.match(context.renderDiffFile(files[0]), /diff-add/);
  assert.match(context.renderDiffFile(files[0]), /diff-del/);
});

test('metadata is escaped before rendering', () => {
  const files = context.parseDiffFiles('diff --git a/a b/a\nrename from <script>\nrename to a\n');
  const html = context.renderDiffFile(files[0]);
  assert.ok(html.includes('&lt;script&gt;'));
  assert.ok(!html.includes('<script>'));
});
