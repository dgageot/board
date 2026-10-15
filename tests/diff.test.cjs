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
const context = vm.createContext({ TextDecoder });
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

test('nested, renamed, deleted and quoted paths retain their exact names', (t) => {
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'board-diff-paths-'));
  t.after(() => fs.rmSync(repo, { recursive: true, force: true }));
  git(repo, 'init', '-q');
  const names = ['src/sub/file.js', 'space name.txt', 'quoted"name.txt', 'café.txt', 'tab\tname.txt', 'prefix-\uFEFFname.txt', 'gone.txt', 'before.txt'];
  fs.mkdirSync(path.join(repo, 'src/sub'), { recursive: true });
  for (const name of names) fs.writeFileSync(path.join(repo, name), 'before\n');
  git(repo, 'add', '.');
  git(repo, '-c', 'user.name=Test', '-c', 'user.email=test@example.com', 'commit', '-qm', 'base');
  for (const name of names.slice(0, -2)) fs.writeFileSync(path.join(repo, name), 'after\n');
  fs.rmSync(path.join(repo, 'gone.txt'));
  git(repo, 'mv', 'before.txt', 'after.txt');
  const files = context.parseDiffFiles(git(repo, 'diff', '--find-renames', 'HEAD'));
  assert.deepEqual(Array.from(files, (file) => file.name).sort(), [...names.slice(0, -1), 'after.txt'].sort());
});

test('binary and mode-only paths containing the header separator stay intact', () => {
  const name = 'has b/binary.bin';
  const files = context.parseDiffFiles(`diff --git a/${name} b/${name}\nBinary files a/${name} and b/${name} differ\n`);
  assert.equal(files[0].name, name);
  const mode = 'has b/script.sh';
  assert.equal(context.parseDiffFiles(`diff --git a/${mode} b/${mode}\nold mode 100644\nnew mode 100755\n`)[0].name, mode);
});

function diffHarness(preference = null, storageFails = false) {
  const elements = new Map(), frames = [], stored = new Map();
  if (preference !== null) stored.set('diff-explorer', preference);
  class Element {
    constructor(tag = 'div') {
      this.tag = tag;
      this.children = [];
      this.dataset = {};
      this.attributes = {};
      this.listeners = {};
      this.innerHTML = '';
    }
    get innerHTML() { return this.html; }
    set innerHTML(value) { this.html = value; this.children = []; }
    addEventListener(type, listener) { this.listeners[type] = listener; }
    setAttribute(name, value) { this.attributes[name] = value; }
    removeAttribute(name) { delete this.attributes[name]; }
    append(...children) { this.children.push(...children); }
    insertAdjacentHTML(_, html) {
      this.innerHTML += html;
      for (const match of html.matchAll(/id="(diff-file-\d+)"/g)) {
        const file = new Element('details');
        file.summary = new Element('summary');
        elements.set(match[1], file);
      }
    }
    querySelector() { return this.summary; }
    closest() { return this; }
    scrollIntoView(options) { this.scrolled = options; }
    focus(options) { this.focused = options; }
    showModal() { this.open = true; }
    close() { this.open = false; this.listeners.close?.(); }
  }
  const getElementById = (id) => {
    if (!elements.has(id) && !/^diff-file-\d+$/.test(id)) elements.set(id, new Element());
    return elements.get(id);
  };
  const context = vm.createContext({
    TextDecoder,
    document: {
      getElementById,
      createElement: (tag) => new Element(tag),
      querySelectorAll: () => buttons().filter((button) => button.attributes['aria-current']),
    },
    localStorage: {
      getItem: (key) => stored.get(key) ?? null,
      setItem(key, value) {
        if (storageFails) throw new Error('Storage unavailable');
        stored.set(key, value);
      },
    },
    requestAnimationFrame: (callback) => frames.push(callback),
    cards: [],
    API: { diffCard: async () => ({ diff: '' }) },
  });
  function buttons() {
    const collect = (node) => node.children.flatMap((child) => child.tag === 'button' ? [child] : collect(child));
    return collect(getElementById('diff-file-tree'));
  }
  const start = source.indexOf('const DIFF_AUTO_COLLAPSE_LINES');
  const end = source.indexOf('// --- Utils ---', start);
  vm.runInContext(`${source.slice(utilsStart, utilsEnd)}\n${source.slice(start, end)}`, context);
  return { context, stored, frames, buttons, get: getElementById };
}

const sampleDiff = (count) => Array.from({ length: count }, (_, i) =>
  `diff --git a/src/sub/file${i}.js b/src/sub/file${i}.js\n--- a/src/sub/file${i}.js\n+++ b/src/sub/file${i}.js\n@@ -1 +1 @@\n-old\n+new\n`
).join('');

test('explorer shares folders and navigates to unique diff targets in batches', () => {
  const h = diffHarness();
  h.context.renderDiffInto(h.get('diff-container'), sampleDiff(7), 0);
  assert.equal(h.buttons().length, 0);
  h.frames.shift()();
  assert.equal(h.buttons().length, 5);
  assert.equal(h.get('diff-file-tree').children.length, 1);
  assert.equal(h.get('diff-file-tree').children[0].children[0].textContent, 'src');
  assert.equal(h.frames.length, 1);
  h.frames.shift()();
  assert.equal(h.buttons().length, 7);
  const button = h.buttons()[6];
  assert.equal(button.title, 'src/sub/file6.js');
  assert.equal(button.textContent, 'file6.js');
  h.get('diff-file-tree').listeners.click({ target: button });
  assert.equal(button.attributes['aria-current'], 'true');
  assert.equal(h.get('diff-file-6').open, true);
  assert.equal(h.get('diff-file-6').scrolled.block, 'start');
  assert.equal(h.get('diff-file-6').summary.focused.preventScroll, true);
  h.get('diff-file-tree').listeners.click({ target: h.buttons()[0] });
  assert.equal(button.attributes['aria-current'], undefined);
});

test('file names are assigned as text, never HTML', () => {
  const h = diffHarness();
  const directories = new Map([['', h.get('diff-file-tree')]]);
  h.context.appendDiffTreeFile(directories, { name: '<script>/<img onerror="bad">' }, 9);
  const folder = h.get('diff-file-tree').children[0];
  assert.equal(folder.children[0].textContent, '<script>');
  assert.equal(h.buttons()[0].textContent, '<img onerror="bad">');
  assert.equal(h.buttons()[0].innerHTML, '');
});

test('visibility is remembered and toggling still works when persistence fails', () => {
  const h = diffHarness('false');
  assert.equal(h.get('diff-explorer').hidden, true);
  const toggle = h.get('diff-toggle-explorer');
  assert.equal(toggle.attributes['aria-expanded'], 'false');
  toggle.listeners.click();
  assert.equal(h.get('diff-explorer').hidden, false);
  assert.equal(toggle.attributes['aria-expanded'], 'true');
  assert.equal(h.stored.get('diff-explorer'), 'true');
  toggle.listeners.click();
  assert.equal(h.stored.get('diff-explorer'), 'false');
  const unavailable = diffHarness(null, true);
  unavailable.get('diff-toggle-explorer').listeners.click();
  assert.equal(unavailable.get('diff-explorer').hidden, true);
});

test('closing a diff clears the explorer and cancels outstanding render batches', () => {
  const h = diffHarness();
  h.context.renderDiffInto(h.get('diff-container'), sampleDiff(7), 0);
  h.frames.shift()();
  h.get('diff-dialog').listeners.close();
  h.frames.shift()();
  assert.equal(h.get('diff-container').innerHTML, '');
  assert.equal(h.get('diff-file-tree').innerHTML, '');
  assert.equal(h.buttons().length, 0);
  assert.equal(h.frames.length, 0);
});

test('opening an empty or failed diff clears stale explorer contents', async () => {
  const h = diffHarness();
  h.get('diff-file-tree').innerHTML = 'stale';
  await h.context.openDiffDialog('a', 'A');
  assert.equal(h.get('diff-file-tree').innerHTML, '');
  assert.match(h.get('diff-container').innerHTML, /No changes/);
  h.context.API.diffCard = async () => { throw new Error('Unavailable'); };
  h.get('diff-file-tree').innerHTML = 'stale';
  await h.context.openDiffDialog('b', 'B');
  assert.equal(h.get('diff-file-tree').innerHTML, '');
  assert.match(h.get('diff-container').innerHTML, /Error: Unavailable/);
});

test('HTML escaping preserves every replacement and coerces values only once', () => {
  const replacements = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
  const expected = (value) => String(value ?? '').replace(/[&<>"']/g, (character) => replacements[character]);
  const samples = [null, undefined, 0, false, '', 'plain text', 'café 😀', ...Object.keys(replacements)];
  for (const left of ['plain', '&', '<', '>', '"', "'", '😀']) {
    for (const right of ['text', '&', '<', '>', '"', "'", '😀']) {
      samples.push(left + right, left.repeat(10) + 'plain' + right.repeat(10));
    }
  }
  for (const sample of samples) assert.equal(context.esc(sample), expected(sample));
  let calls = 0;
  assert.equal(context.esc({ toString() { calls++; return '<&>'; } }), '&lt;&amp;&gt;');
  assert.equal(calls, 1);
});

test('cached diff counts include all hunks and preserve collapse and truncation boundaries', () => {
  for (const count of [0, 499, 500, 501, 5000, 5001]) {
    const lines = Array.from({ length: count }, (_, index) => '+- '[index % 3] + `line ${index}\n`).join('');
    const file = context.parseDiffFiles(`diff --git a/file b/file\n--- a/file\n+++ b/file\n@@ -1 +1 @@\n${lines}`)[0];
    assert.equal(file.added, Math.ceil(count / 3));
    assert.equal(file.removed, Math.floor((count + 1) / 3));
    assert.equal(file.lineCount, count);
    const html = context.renderDiffFile(file);
    assert.equal(/class="diff-file" open/.test(html), count <= 500);
    assert.equal(html.includes('not shown'), count > 5000);
    assert.match(html, new RegExp(`class="diff-file-adds">\\+${file.added}<`));
    assert.match(context.renderDiffStats([file]), new RegExp(`class="diff-file-dels">-${file.removed}<`));
  }
  const files = context.parseDiffFiles('diff --git a/a b/a\n@@ -1 +1 @@\n-old\n+new\n@@ -10 +10 @@\n more\n+added\ndiff --git a/b b/b\nold mode 100644\nnew mode 100755\n');
  assert.equal(files[0].added, 2);
  assert.equal(files[0].removed, 1);
  assert.equal(files[0].lineCount, 4);
  assert.equal(files[1].added, 0);
  assert.equal(files[1].removed, 0);
  assert.equal(files[1].lineCount, 0);
});
