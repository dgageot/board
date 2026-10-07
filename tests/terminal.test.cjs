const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../pkg/board/static/app.js'), 'utf8');
const start = source.indexOf('async function openTerminal(');
const end = source.indexOf('document.getElementById("close-terminal")', start);
assert.ok(start >= 0 && end > start);

function harness() {
  let ready;
  const ghosttyReady = new Promise((resolve) => { ready = resolve; });
  const frames = [], terminals = [], sockets = [], listeners = new Set();
  const observers = [];
  const timers = new Map();
  let fits = 0, timerId = 0;
  const dialog = { open: false, showModal() { this.open = true; } };
  const elements = {
    'terminal-dialog': dialog, 'terminal-container': { innerHTML: '' }, 'terminal-title': { textContent: '' },
  };
  class Terminal {
    constructor() { terminals.push(this); this.cols = 80; this.rows = 24; }
    loadAddon() {} open() {} attachCustomWheelEventHandler() {} onData() {} onResize() {} write() {}
    dispose() { this.disposed = true; }
  }
  class WebSocket {
    constructor(url) { this.url = url; sockets.push(this); }
    close() { this.closed = true; }
  }
  const context = vm.createContext({
    ghosttyReady, GhosttyTerminal: Terminal, FitAddon: class { fit() { fits++; } }, WebSocket,
    document: { getElementById: (id) => elements[id] },
    window: { addEventListener: (_, f) => listeners.add(f), removeEventListener: (_, f) => listeners.delete(f) },
    requestAnimationFrame: (f) => frames.push(f), isDark: () => true, renderTerminalCardMeta() {},
    setTimeout: (f) => { timers.set(++timerId, f); return timerId; },
    clearTimeout: (id) => timers.delete(id),
    ResizeObserver: class {
      constructor(callback) { this.callback = callback; observers.push(this); }
      observe() {} disconnect() { this.disconnected = true; }
    },
    location: { protocol: 'http:', host: 'localhost' },
  });
  const stateStart = source.indexOf('let activeTerm = null;');
  const stateEnd = source.indexOf('function renderTerminalCardMeta()', stateStart);
  assert.ok(stateStart >= 0 && stateEnd > stateStart);
  vm.runInContext(source.slice(stateStart, stateEnd) + source.slice(start, end), context);
  return {
    context, ready, terminals, sockets, listeners, observers, timers,
    get fits() { return fits; },
    flushTimers() { for (const [id, f] of timers) { timers.delete(id); f(); } },
    flush() { while (frames.length) frames.shift()(); },
    close() { dialog.open = false; context.closeTerminal(); },
  };
}

test('closing during WASM initialization cancels pending open', async () => {
  const h = harness();
  const opened = h.context.openTerminal('a', 'A', 'a');
  h.close(); h.ready(); await opened; h.flush();
  assert.equal(h.terminals.length, 0);
  assert.equal(h.sockets.length, 0);
  assert.equal(h.listeners.size, 0);
});

test('closing before the animation frame does not create a socket', async () => {
  const h = harness(); h.ready();
  await h.context.openTerminal('a', 'A', 'a');
  h.close(); h.flush();
  assert.equal(h.sockets.length, 0);
  assert.equal(h.terminals[0].disposed, true);
  assert.equal(h.listeners.size, 0);
  assert.equal(h.observers[0].disconnected, true);
});

test('overlapping opens only attach the newest session', async () => {
  const h = harness();
  const a = h.context.openTerminal('a', 'A', 'a');
  const b = h.context.openTerminal('b', 'B', 'b');
  h.ready(); await Promise.all([a, b]); h.flush();
  assert.equal(h.terminals.length, 1);
  assert.equal(h.sockets.length, 1);
  assert.match(h.sockets[0].url, /\/terminal\/b\?/);
  assert.equal(h.listeners.size, 1);
});

test('a normal open, close and reopen preserves cleanup and attachment', async () => {
  const h = harness(); h.ready();
  await h.context.openTerminal('a', 'A', 'a'); h.flush(); h.close();
  assert.equal(h.sockets[0].closed, true);
  assert.equal(h.sockets[0].onmessage, null);
  assert.equal(h.sockets[0].onclose, null);
  assert.equal(h.sockets[0].onerror, null);
  assert.equal(h.terminals[0].disposed, true);
  await h.context.openTerminal('b', 'B', 'b'); h.flush();
  assert.equal(h.sockets.length, 2);
  assert.match(h.sockets[1].url, /\/terminal\/b\?/);
  assert.equal(h.listeners.size, 1);
});

test('rapid pane resizes apply one trailing fit and cancel it on close', async () => {
  const h = harness(); h.ready();
  await h.context.openTerminal('a', 'A', 'a'); h.flush();
  assert.equal(h.fits, 1);
  h.observers[0].callback();
  h.observers[0].callback();
  assert.equal(h.timers.size, 1);
  h.flushTimers();
  assert.equal(h.fits, 2);
  h.observers[0].callback();
  h.close();
  assert.equal(h.timers.size, 0);
});
