const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../pkg/board/static/app.js'), 'utf8');
const start = source.indexOf('// prIcon renders');
const end = source.indexOf('async function handleCardAction(', start);
assert.ok(start >= 0 && end > start);

class Link {
  constructor(url, content = '') {
    this.href = url;
    this.title = url;
    this.innerHTML = content;
    this.className = 'card-pr';
    this.dataset = {};
    this.classList = {
      add: (name) => { this.className = [...new Set([...this.className.split(' '), name])].join(' '); },
      remove: (name) => { this.className = this.className.split(' ').filter((value) => value !== name).join(' '); },
    };
  }
  getAttribute(name) { return this[name]; }
  removeAttribute(name) { delete this[name]; }
  replaceChildren() { this.innerHTML = ''; }
  insertAdjacentHTML(_, html) { this.innerHTML += html; }
  querySelector(selector) {
    if (selector !== '.card-pr-icon') return null;
    const link = this;
    return { set outerHTML(html) { link.innerHTML = link.innerHTML.replace(/<svg class="card-pr-icon".*?<\/svg>/, html); } };
  }
}

function harness() {
  const requests = [];
  const terminalLink = new Link();
  const context = vm.createContext({
    columns: [{ id: 'done' }], cards: [], activeCardId: null,
    API: { prStatus(id) {
      return new Promise((resolve, reject) => requests.push({ id, resolve, reject }));
    } },
    document: {
      getElementById: (id) => id === 'terminal-pr' ? terminalLink : {},
      createElement: () => ({
        dataset: {}, addEventListener() {},
        set innerHTML(html) {
          const match = html.match(/<a class="card-pr" href="([^"]*)"[^>]*>(.*?)<\/a>/s);
          if (match) this.link = new Link(match[1], match[2]);
        },
        querySelector() { return this.link; },
      }),
    },
    formatCost: () => '', icon: () => '', handleCardAction() {},
  });
  const utilsStart = source.indexOf('const ESC_CHARS =');
  const utilsEnd = source.indexOf('// --- Keyboard shortcuts ---', utilsStart);
  const terminalStart = source.indexOf('function renderTerminalCardMeta()');
  const terminalEnd = source.indexOf('async function openTerminal(', terminalStart);
  vm.runInContext(source.slice(utilsStart, utilsEnd) + source.slice(start, end) + source.slice(terminalStart, terminalEnd), context);
  return {
    context, requests, terminalLink,
    link: (url) => new Link(url, context.prLinkContent(url)),
    render: (card) => context.renderCard({ status: 'done', ...card }, 'done').querySelector('.card-pr'),
    flush: () => new Promise((resolve) => setImmediate(resolve)),
  };
}

const card = { id: 'a', prUrl: 'https://github.com/org/repo/pull/1' };

function assertStatus(link, status, approved = false) {
  assert.equal(link.className, `card-pr card-pr-${status}`);
  assert.equal((link.innerHTML.match(/class="card-pr-approved"/g) || []).length, approved ? 1 : 0);
  assert.match(link.innerHTML, /PR #1/);
  assert.equal(link.title.includes(' — Approved'), approved);
}

test('card rerenders retain the last status and approval while refreshing', async () => {
  const h = harness();
  const first = h.render(card);
  assert.equal(first.className, 'card-pr');
  h.requests[0].resolve({ status: 'success', approved: true });
  await h.flush();
  assertStatus(first, 'success', true);

  const next = h.render(card);
  assertStatus(next, 'success', true);
  assert.equal(h.requests.length, 2, 'cached statuses still refresh');
  h.requests[1].resolve({ status: 'pending', approved: false });
  await h.flush();
  assertStatus(next, 'pending');
  assert.equal(next.title, `${card.prUrl} — CI running`);
});

test('rapid rerenders share a pending request and all receive its result', async () => {
  const h = harness();
  const first = h.render(card);
  const next = h.render(card);
  assert.equal(h.requests.length, 1);
  h.requests[0].resolve({ status: 'merged', approved: true });
  await h.flush();
  assertStatus(first, 'merged');
  assertStatus(next, 'merged');
});

test('errors and empty responses preserve cached status and allow retries', async () => {
  const h = harness();
  h.render(card);
  h.requests[0].resolve({ status: 'failure', approved: true });
  await h.flush();

  const failed = h.render(card);
  h.requests[1].reject(new Error('offline'));
  await h.flush();
  assertStatus(failed, 'failure', true);

  const empty = h.render(card);
  h.requests[2].resolve({ status: '', approved: false });
  await h.flush();
  assertStatus(empty, 'failure', true);

  const recovered = h.render(card);
  h.requests[3].resolve({ status: 'open', approved: false });
  await h.flush();
  assertStatus(recovered, 'open');
});

test('first-load failures keep the default icon and can be retried', async () => {
  const h = harness();
  const first = h.render(card);
  h.requests[0].reject(new Error('offline'));
  await h.flush();
  assert.equal(first.className, 'card-pr');
  assert.equal(first.title, card.prUrl);
  const next = h.render(card);
  h.requests[1].resolve({ status: 'draft', approved: false });
  await h.flush();
  assertStatus(next, 'draft');
});

test('changing a card PR URL does not reuse the previous PR status', async () => {
  const h = harness();
  h.render(card);
  h.requests[0].resolve({ status: 'merged', approved: false });
  await h.flush();
  const next = h.render({ ...card, prUrl: 'https://github.com/other/repo/pull/1' });
  assert.equal(next.className, 'card-pr');
  h.requests[1].resolve({ status: 'open', approved: false });
  await h.flush();
  assertStatus(next, 'open');
});

test('stale responses cannot overwrite a link switched to another PR', async () => {
  const h = harness();
  const link = h.link(card.prUrl);
  const first = h.context.loadPRStatus(card.id, link);
  link.href = 'https://github.com/other/repo/pull/1';
  const next = h.context.loadPRStatus(card.id, link);
  h.requests[1].resolve({ status: 'open', approved: false });
  await next;
  h.requests[0].resolve({ status: 'merged', approved: false });
  await first;
  assertStatus(link, 'open');
  assert.equal(link.title, `${link.href} — Waiting for review`);
});

test('repeated updates replace status classes, tooltips and approval markers', async () => {
  const h = harness();
  const link = h.link(card.prUrl);
  for (const [status, approved] of [['success', true], ['success', true], ['failure', false], ['merged', true]]) {
    const loading = h.context.loadPRStatus(card.id, link);
    h.requests.at(-1).resolve({ status, approved });
    await loading;
    assertStatus(link, status, approved && status !== 'merged');
    assert.equal((link.title.match(/ — /g) || []).length, approved && status !== 'merged' ? 2 : 1);
  }
});

test('the terminal immediately reuses a card status and shares its refresh', async () => {
  const h = harness();
  h.context.cards = [card];
  h.context.activeCardId = card.id;
  h.render(card);
  h.requests[0].resolve({ status: 'success', approved: true });
  await h.flush();
  const next = h.render(card);
  h.context.renderTerminalCardMeta();
  assertStatus(h.terminalLink, 'success', true);
  assert.equal(h.requests.length, 2);
  h.requests[1].resolve({ status: 'merged', approved: true });
  await h.flush();
  assertStatus(next, 'merged');
  assertStatus(h.terminalLink, 'merged');
});
