const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

const staticDir = path.join(__dirname, '../pkg/board/static');
const html = fs.readFileSync(path.join(staticDir, 'index.html'), 'utf8');
const source = fs.readFileSync(path.join(staticDir, 'app.js'), 'utf8');
const templatesSource = fs.readFileSync(path.join(staticDir, 'task-templates.js'), 'utf8');

function harness(hash = '') {
  class Element {
    constructor() {
      this.listeners = new Map();
      this.children = [];
      this.queries = new Map();
      this.attributes = {};
      this.dataset = {};
      this.style = { setProperty() {} };
      this.classList = { add() {}, remove() {} };
      this.hidden = false;
      this.value = '';
      this.innerHTML = '';
    }
    set innerHTML(value) { this.html = value; this.children = []; this.queries.clear(); }
    get innerHTML() { return this.html; }
    addEventListener(name, callback) {
      if (!this.listeners.has(name)) this.listeners.set(name, []);
      this.listeners.get(name).push(callback);
    }
    fire(name, event = {}) { for (const callback of this.listeners.get(name) || []) callback(event); }
    setAttribute(name, value) { this.attributes[name] = value; }
    getAttribute(name) { return this.attributes[name]; }
    removeAttribute(name) { delete this.attributes[name]; }
    replaceChildren() { this.children = []; this.innerHTML = ''; }
    appendChild(child) { this.children.push(child); }
    querySelector(selector) {
      // Static children (e.g. the submit button's .btn-label) exist in the real
      // HTML; only content rendered by the app can be checked for a class.
      if (selector.startsWith('.') && this.innerHTML && !this.innerHTML.includes(selector.slice(1))) return null;
      if (!this.queries.has(selector)) this.queries.set(selector, new Element());
      return this.queries.get(selector);
    }
    querySelectorAll() { return []; }
    showModal() { this.open = true; }
    close() { this.open = false; this.fire('close'); }
    focus() { this.focused = true; }
  }
  const elements = new Map(Array.from(html.matchAll(/\bid="([^"]+)"/g), ([, id]) => [id, new Element()]));
  const get = (id) => {
    assert.ok(elements.has(id), `app requested missing element #${id}`);
    return elements.get(id);
  };
  const document = new Element();
  document.getElementById = get;
  document.documentElement = new Element();
  document.createElement = () => new Element();
  document.querySelector = (selector) => {
    assert.equal(selector, 'dialog[open]');
    return [...elements.entries()].find(([id, element]) => id.endsWith('-dialog') && element.open)?.[1];
  };
  document.querySelectorAll = (selector) => selector === 'dialog'
    ? [...elements.entries()].filter(([id]) => id.endsWith('-dialog')).map(([, element]) => element)
    : [];
  const requests = [], events = [], stored = new Map();
  const responses = {
    '/api/browse': { path: '/home/test' },
    '/api/cards': [],
    '/api/projects': [{ id: 'project', name: 'Example', repoPath: '/repo', agent: '/agent.yaml' }],
    '/api/columns': [{ id: 'todo', name: 'To do' }],
    '/api/task-templates': [],
  };
  const context = vm.createContext({
    document, window: { matchMedia: () => ({ matches: false }) }, location: { hash },
    init: () => Promise.resolve(),
    localStorage: { getItem: (key) => stored.get(key) ?? null, setItem: (key, value) => stored.set(key, value) },
    fetch: async (url) => {
      requests.push(url);
      assert.ok(Object.hasOwn(responses, url), `unexpected request: ${url}`);
      return { ok: true, status: 200, json: async () => responses[url] };
    },
    EventSource: class { constructor(url) { this.url = url; events.push(this); } },
    clearTimeout() {}, requestAnimationFrame() {},
  });
  // Only the terminal library is mocked; the task templates module is loaded
  // for real, then all board startup and event wiring executes.
  const terminalImport = /^import \{ init, Terminal as GhosttyTerminal, FitAddon \} from 'ghostty-web';\n/m;
  const templatesImport = /^import \{ initTaskTemplates \} from '\.\/task-templates\.js';\n/m;
  assert.match(source, terminalImport);
  assert.match(source, templatesImport);
  vm.runInContext(templatesSource.replaceAll('export function ', 'function '), context);
  vm.runInContext(source.replace(terminalImport, '').replace(templatesImport, ''), context);
  return { context, document, get, requests, events, responses };
}

const flush = () => new Promise((resolve) => setImmediate(resolve));

for (const hash of ['', '#board', '#loops/old-bookmark']) {
  test(`board starts and refreshes without loops at ${hash || 'the root URL'}`, async () => {
    const h = harness(hash);
    await flush();
    assert.deepEqual(h.requests, ['/api/browse', '/api/cards', '/api/projects', '/api/columns']);
    assert.equal(h.get('board').hidden, false);
    assert.equal(h.get('board').children.length, 1);
    assert.match(h.get('board').children[0].innerHTML, /To do/);
    assert.match(h.get('board-summary').innerHTML, /<strong>0<\/strong> tasks/);
    assert.equal(h.events.length, 1);
    assert.equal(h.events[0].url, '/api/events');

    h.responses['/api/cards'] = [{ id: 'task', title: 'Ready task', column: 'todo', status: 'waiting' }];
    await h.events[0].onmessage();
    const cards = h.get('board').children[0].querySelector('.column-body').children;
    assert.equal(cards.length, 1);
    assert.match(cards[0].innerHTML, /Ready task/);
    assert.match(h.get('board-summary').innerHTML, /<strong>1<\/strong> ready/);

    const event = {
      key: 'n', target: { matches: () => false },
      preventDefault() { this.prevented = true; },
    };
    h.document.fire('keydown', event);
    assert.equal(event.prevented, true);
    assert.equal(h.get('new-task-dialog').open, true);
    assert.match(h.get('task-project').innerHTML, /Example/);
    assert.equal(h.get('task-prompt').focused, true);
    await flush();
    assert.equal(h.requests.at(-1), '/api/task-templates');
    assert.match(h.get('task-template').innerHTML, /Custom task/);
    assert.equal(h.get('task-template').disabled, false);
  });
}

test('new-task shortcut still respects typing, modifiers and open dialogs', async () => {
  const h = harness();
  await flush();
  for (const overrides of [{ key: 'x' }, { metaKey: true }, { ctrlKey: true }, { altKey: true }, { target: { matches: () => true } }]) {
    h.document.fire('keydown', {
      key: 'n', target: { matches: () => false },
      preventDefault() { assert.fail('shortcut should be ignored'); }, ...overrides,
    });
    assert.ok(!h.get('new-task-dialog').open);
  }
  h.get('projects-dialog').showModal();
  h.document.fire('keydown', {
    key: 'N', target: { matches: () => false },
    preventDefault() { assert.fail('shortcut should be ignored while a dialog is open'); },
  });
  assert.ok(!h.get('new-task-dialog').open);
  h.get('projects-dialog').close();
  h.get('btn-new-task').fire('click');
  assert.equal(h.get('new-task-dialog').open, true);
});

test('board page has no loops UI or asset dependencies and retains hidden styling', () => {
  assert.equal((html.match(/<main\b/g) || []).length, 1);
  assert.doesNotMatch(html + source, /workspace-nav|loops|result-markdown|markdown-it|board-refresh/);
  assert.match(html, /<main id="board" aria-label="Task board"><\/main>/);
  for (const match of html.matchAll(/(?:src|href)="([^"#]+\.(?:js|css))"/g)) {
    assert.ok(fs.existsSync(path.join(staticDir, match[1])), `missing asset: ${match[1]}`);
  }
  const css = fs.readFileSync(path.join(staticDir, 'style.css'), 'utf8');
  assert.match(css, /\[hidden\]\s*\{\s*display: none !important;/);
});
