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
      this.selectedIndex = 0;
      this.value = '';
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
    API: { diffCard: async () => ({ diff: '' }), cardCommits: async () => [] },
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
  assert.equal(button.title, 'src/sub/file6.js — Modified');
  assert.equal(button.children[0].textContent, 'file6.js');
  assert.equal(button.children[1].textContent, 'M');
  assert.equal(button.dataset.change, 'modified');
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
  assert.equal(h.buttons()[0].children[0].textContent, '<img onerror="bad">');
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

test('commit selector lists commits safely and navigates oldest first', async () => {
  const h = diffHarness();
  const commits = [{ hash: 'a'.repeat(40), subject: '<script>first</script>' }, { hash: 'b'.repeat(40), subject: 'Second' }];
  h.context.API.cardCommits = async () => commits;
  const calls = [];
  h.context.API.diffCard = async (card, hash) => { calls.push([card, hash]); return { diff: '' }; };
  await h.context.openDiffDialog('a', 'A');
  const selector = h.get('diff-commit-select');
  assert.equal(selector.disabled, false);
  assert.equal(selector.children[0].textContent, '1. aaaaaaa <script>first</script>');
  assert.equal(selector.children[0].innerHTML, '');
  assert.equal(h.get('diff-commit-status').textContent, '2 commits · oldest first');
  assert.equal(h.get('diff-commit-prev').disabled, true);
  assert.equal(h.get('diff-commit-next').disabled, false);
  selector.selectedIndex = 1;
  selector.value = commits[0].hash;
  await h.context.loadDiffSelection();
  h.context.updateDiffCommitNavigation();
  assert.deepEqual(calls.at(-1), ['a', commits[0].hash]);
  assert.equal(h.get('diff-commit-prev').disabled, true);
  assert.equal(h.get('diff-commit-next').disabled, false);
  h.get('diff-commit-next').listeners.click();
  assert.equal(selector.selectedIndex, 2);
  assert.equal(h.get('diff-commit-next').disabled, true);
  h.get('diff-commit-prev').listeners.click();
  assert.equal(selector.selectedIndex, 1);
  selector.value = '';
  await h.context.loadDiffSelection();
  assert.deepEqual(calls.at(-1), ['a', '']);
});

test('late commit lists cannot populate a closed or different card', async () => {
  const h = diffHarness();
  const resolve = [];
  h.context.API.cardCommits = () => new Promise((done) => resolve.push(done));
  await h.context.openDiffDialog('a', 'A');
  await h.context.openDiffDialog('b', 'B');
  resolve[0]([{ hash: 'a'.repeat(40), subject: 'Stale' }]);
  await Promise.resolve();
  assert.equal(h.get('diff-commit-select').children.length, 0);
  h.get('diff-dialog').listeners.close();
  resolve[1]([{ hash: 'b'.repeat(40), subject: 'Closed' }]);
  await Promise.resolve();
  assert.equal(h.get('diff-commit-select').children.length, 0);
});

test('late diffs cannot overwrite a newer commit selection', async () => {
  const h = diffHarness();
  const resolve = [];
  h.context.API.diffCard = () => new Promise((done) => resolve.push(done));
  const opened = h.context.openDiffDialog('a', 'A');
  h.get('diff-commit-select').value = 'a'.repeat(40);
  const selected = h.context.loadDiffSelection();
  resolve[1]({ diff: sampleDiff(1) });
  await selected;
  h.frames.shift()();
  const current = h.get('diff-container').innerHTML;
  resolve[0]({ diff: '' });
  await opened;
  assert.equal(h.get('diff-container').innerHTML, current);
});

test('empty commits and commit list failures do not hide the combined diff', async () => {
  const h = diffHarness();
  h.context.API.cardCommits = async () => { throw new Error('Unavailable'); };
  await h.context.openDiffDialog('a', 'A');
  assert.equal(h.get('diff-commit-status').textContent, 'Could not load commits');
  assert.match(h.get('diff-container').innerHTML, /No changes/);
  h.get('diff-commit-select').value = 'a'.repeat(40);
  await h.context.loadDiffSelection();
  assert.match(h.get('diff-container').innerHTML, /This commit has no file changes/);
});

test('file changes distinguish additions and deletions from edits using git metadata', (t) => {
  const repo = fs.mkdtempSync(path.join(os.tmpdir(), 'board-diff-status-'));
  t.after(() => fs.rmSync(repo, { recursive: true, force: true }));
  git(repo, 'init', '-q');
  const initial = {
    'edited.txt': 'before\n',
    'only-additions.txt': 'first\n',
    'emptied.txt': 'before\n',
    'deleted.txt': 'gone\n',
    'deleted-empty.txt': '',
    'renamed.txt': 'same\n',
    'mode.sh': 'echo hello\n',
    'header-like.txt': 'before\n',
  };
  for (const [name, text] of Object.entries(initial)) fs.writeFileSync(path.join(repo, name), text, { mode: 0o644 });
  fs.writeFileSync(path.join(repo, 'deleted.bin'), Buffer.from([0, 1, 2]));
  git(repo, 'add', '.');
  git(repo, '-c', 'user.name=Test', '-c', 'user.email=test@example.com', 'commit', '-qm', 'base');
  fs.writeFileSync(path.join(repo, 'edited.txt'), 'after\n');
  fs.writeFileSync(path.join(repo, 'only-additions.txt'), 'first\nsecond\n');
  fs.writeFileSync(path.join(repo, 'emptied.txt'), '');
  for (const name of ['deleted.txt', 'deleted-empty.txt', 'deleted.bin']) fs.rmSync(path.join(repo, name));
  git(repo, 'mv', 'renamed.txt', 'renamed-new.txt');
  fs.chmodSync(path.join(repo, 'mode.sh'), 0o755);
  fs.writeFileSync(path.join(repo, 'header-like.txt'), '++ /dev/null\nnew file mode 100644\n');
  fs.writeFileSync(path.join(repo, 'added.txt'), 'new\n');
  fs.writeFileSync(path.join(repo, 'added-empty.txt'), '');
  fs.writeFileSync(path.join(repo, 'added.bin'), Buffer.from([0, 2, 3]));
  git(repo, 'add', '--intent-to-add', 'added.txt', 'added-empty.txt', 'added.bin');
  const files = context.parseDiffFiles(git(repo, 'diff', '--find-renames', '--no-rename-empty', 'HEAD'));
  const statuses = Object.fromEntries(Array.from(files, (file) => [file.name, file.change]));
  assert.deepEqual(statuses, {
    'added.bin': 'added', 'added-empty.txt': 'added', 'added.txt': 'added',
    'deleted.bin': 'deleted', 'deleted-empty.txt': 'deleted', 'deleted.txt': 'deleted',
    'edited.txt': 'modified', 'emptied.txt': 'modified', 'header-like.txt': 'modified',
    'mode.sh': 'modified', 'only-additions.txt': 'modified', 'renamed-new.txt': 'modified',
  });
});

test('explorer entries expose color status, a letter, and an accessible label', () => {
  const h = diffHarness();
  const directories = new Map([['', h.get('diff-file-tree')]]);
  for (const [index, change] of ['added', 'deleted', 'modified'].entries()) {
    h.context.appendDiffTreeFile(directories, { name: `dir/${change}.txt`, change }, index);
    const button = h.buttons()[index];
    const label = change[0].toUpperCase() + change.slice(1);
    assert.equal(button.dataset.change, change);
    assert.equal(button.attributes['aria-label'], `dir/${change}.txt — ${label}`);
    assert.equal(button.title, button.attributes['aria-label']);
    assert.equal(button.children[0].textContent, `${change}.txt`);
    assert.equal(button.children[1].textContent, label[0]);
    assert.equal(button.children[1].attributes['aria-hidden'], 'true');
  }
});
