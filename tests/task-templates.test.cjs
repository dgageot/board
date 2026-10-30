const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

const file = path.join(__dirname, '../pkg/board/static/task-templates.js');
const source = fs.readFileSync(file, 'utf8');

function harness(api = async () => []) {
  const elements = new Map();
  function element(id) {
    if (!elements.has(id)) elements.set(id, {
      value: '', textContent: '', disabled: false, readOnly: false, dataset: {}, listeners: {},
      addEventListener(event, callback) { this.listeners[event] = callback; },
      showModal() { this.open = true; }, focus() { this.focused = true; },
      setCustomValidity(message) { this.validation = message; },
      reportValidity() { return !this.validation; },
      querySelectorAll() { return this.inputs || []; },
      get innerHTML() { return this.html || ''; },
      set innerHTML(html) {
        this.html = html;
        this.inputs = [...html.matchAll(/<input id="([^"]+)" data-argument="([^"]+)"/g)].map((match) => {
          const input = element(match[1]);
          input.dataset.argument = match[2];
          input.value = '';
          return input;
        });
      },
    });
    return elements.get(id);
  }
  const context = vm.createContext({
    document: { getElementById: element },
    confirm: () => true,
  });
  vm.runInContext(source.replaceAll('export function ', 'function '), context);
  const esc = (text) => String(text).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[c]);
  const controller = context.initTaskTemplates({ api, esc });
  return {
    context, element, controller,
    select(id) {
      element('task-template').value = id;
      element('task-template').listeners.change();
    },
    input(index, value) {
      const input = element(`task-argument-${index}`);
      input.value = value;
      element('task-arguments').listeners.input({ target: input });
    },
    submit() { return element('template-form').listeners.submit({ preventDefault() {} }); },
    openEditor() { return element('btn-tasks').listeners.click(); },
  };
}

const template = { id: 'review', name: 'Review', prompt: 'Review {{url}} for {{focus}}. Link: {{ url }}.' };

test('arguments are unique, ordered named placeholders; unrelated braces stay literal', () => {
  const { context } = harness();
  assert.deepEqual(Array.from(context.taskArguments('{{ x }} {{x}} {{second-name}} {{_third2}} {json} {{bad name}} {{1}}')), ['x', 'second-name', '_third2']);
  assert.deepEqual(Array.from(context.taskArguments('No arguments')), []);
});

test('expansion is literal and single-pass, including dollar signs, braces and HTML', () => {
  const { context } = harness();
  const values = new Map([['url', '$& {{focus}} <script>'], ['focus', 'a\nb']]);
  assert.equal(context.expandTask('{{url}} {{focus}} {{ url }} {{missing}}', values), '$& {{focus}} <script> a\nb $& {{focus}} <script> {{missing}}');
  assert.equal(context.expandTask('{{toString}} {{__proto__}}', new Map()), '{{toString}} {{__proto__}}');
});

test('selecting a template renders required arguments, previews all occurrences, and restores a custom draft', async () => {
  const h = harness(async () => [template]);
  h.element('task-prompt').value = 'My custom draft';
  await h.controller.loadPicker();
  h.select('review');
  assert.equal(h.element('task-prompt').readOnly, true);
  assert.equal(h.element('task-prompt-label').textContent, 'Prompt preview');
  assert.equal(h.element('task-arguments').inputs.length, 2);
  assert.match(h.element('task-arguments').innerHTML, /required/);
  assert.equal(h.controller.getPrompt(), '', 'missing arguments cannot be submitted');
  h.input(0, 'https://example.test/pull/1');
  h.input(1, 'security');
  assert.equal(h.controller.getPrompt(), 'Review https://example.test/pull/1 for security. Link: https://example.test/pull/1.');
  h.select('');
  assert.equal(h.element('task-prompt').value, 'My custom draft');
  assert.equal(h.element('task-prompt').readOnly, false);
  assert.equal(h.element('task-arguments').inputs.length, 0);
});

test('whitespace arguments are rejected without trimming meaningful replacement text', async () => {
  const h = harness(async () => [{ id: 'one', name: 'One', prompt: 'Start {{value}} end' }]);
  await h.controller.loadPicker();
  h.select('one');
  h.input(0, '  ');
  assert.equal(h.controller.getPrompt(), '');
  h.input(0, '  $&\n{{another}}  ');
  assert.equal(h.controller.getPrompt(), 'Start   $&\n{{another}}   end');
});

test('templates without arguments work and successful creation resets the picker to a blank custom task', async () => {
  const h = harness(async () => [{ id: 'plain', name: 'Plain', prompt: 'Run the tests' }]);
  await h.controller.loadPicker();
  h.select('plain');
  assert.equal(h.controller.getPrompt(), 'Run the tests');
  h.controller.resetPicker();
  assert.equal(h.controller.getPrompt(), '');
  assert.equal(h.element('task-prompt').readOnly, false);
  assert.equal(h.element('task-template').value, '');
  h.element('task-prompt').value = 'Next custom';
  assert.equal(h.controller.getPrompt(), 'Next custom');
});

test('failed template loading leaves custom creation available and shows the error', async () => {
  const h = harness(async () => { throw new Error('Offline'); });
  h.element('task-prompt').value = 'Custom';
  await h.controller.loadPicker();
  assert.match(h.element('task-template-status').textContent, /Offline/);
  assert.equal(h.element('task-template').disabled, false);
  assert.equal(h.controller.getPrompt(), 'Custom');
});

test('late picker responses cannot overwrite a newer request', async () => {
  let resolve;
  let calls = 0;
  const h = harness(() => ++calls === 1 ? new Promise((done) => { resolve = done; }) : Promise.resolve([template]));
  const first = h.controller.loadPicker();
  await h.controller.loadPicker();
  resolve([{ id: 'stale', name: 'Stale', prompt: 'Old' }]);
  await first;
  assert.match(h.element('task-template').innerHTML, /Review/);
  assert.doesNotMatch(h.element('task-template').innerHTML, /Stale/);
});

test('reopening preserves arguments and the original template snapshot after external edits or deletion', async () => {
  let templates = [template];
  const h = harness(async () => templates);
  await h.controller.loadPicker();
  h.select('review');
  h.input(0, 'url');
  h.input(1, 'focus');
  templates = [{ ...template, prompt: 'Changed {{new}}' }];
  await h.controller.loadPicker();
  assert.equal(h.controller.getPrompt(), 'Review url for focus. Link: url.');
  templates = [];
  await h.controller.loadPicker();
  assert.equal(h.element('task-template').value, 'review');
  assert.equal(h.controller.getPrompt(), 'Review url for focus. Link: url.');
});

test('retained picker choices resolve to the displayed snapshot even after an external deletion', async () => {
  let templates = [template];
  const h = harness(async () => templates);
  h.element('task-prompt').value = 'Custom draft';
  await h.controller.loadPicker();
  h.select('review');
  templates = [];
  await h.controller.loadPicker();
  h.select('');
  assert.equal(h.controller.getPrompt(), 'Custom draft');
  h.select('review');
  assert.equal(h.element('task-prompt').readOnly, true);
  h.input(0, 'url');
  h.input(1, 'focus');
  assert.equal(h.controller.getPrompt(), 'Review url for focus. Link: url.');
});

test('the editor creates, updates and deletes saved templates through the API', async () => {
  let templates = [];
  const calls = [];
  const h = harness(async (url, options) => {
    calls.push({ url, ...options });
    if (!options) return templates;
    if (options.method === 'DELETE') { templates = []; return null; }
    const saved = { id: 'saved', ...JSON.parse(options.body) };
    templates = [saved];
    return saved;
  });
  await h.openEditor();
  assert.equal(h.element('tasks-dialog').open, true);
  assert.equal(h.element('template-delete').disabled, true);
  h.element('template-name').value = ' New ';
  h.element('template-prompt').value = '  Text {{arg}}\n';
  await h.submit();
  assert.equal(calls[1].method, 'POST');
  assert.deepEqual(JSON.parse(calls[1].body), { name: 'New', prompt: '  Text {{arg}}\n' });
  assert.equal(h.element('template-status').textContent, 'Saved.');
  h.element('template-name').value = 'Edited';
  await h.submit();
  assert.equal(calls[2].url, '/task-templates/saved');
  assert.equal(calls[2].method, 'PUT');
  await h.element('template-delete').listeners.click();
  assert.equal(calls[3].method, 'DELETE');
  assert.equal(h.element('template-name').value, '');
  assert.equal(h.element('template-delete').disabled, true);
});

test('editor failures preserve the draft and unlock saving; pending saves block duplicate requests and switching', async () => {
  let reject;
  let saves = 0;
  const h = harness(async (_, options) => {
    if (!options) return [template];
    saves++;
    return new Promise((_, fail) => { reject = fail; });
  });
  await h.openEditor();
  h.element('template-name').value = 'Edited draft';
  const pending = h.submit();
  assert.equal(h.element('template-fields').disabled, true);
  await h.submit();
  h.element('template-new').listeners.click();
  assert.equal(h.element('template-name').value, 'Edited draft');
  assert.equal(saves, 1);
  reject(new Error('Save failed'));
  await pending;
  assert.equal(h.element('template-name').value, 'Edited draft');
  assert.equal(h.element('template-status').textContent, 'Save failed');
  assert.equal(h.element('template-fields').disabled, false);
});

test('saved template names and IDs are escaped in both selectors', async () => {
  const h = harness(async () => [{ id: '"><img>', name: '<script>', prompt: 'Do it' }]);
  await h.controller.loadPicker();
  await h.openEditor();
  for (const id of ['task-template', 'template-list']) {
    assert.doesNotMatch(h.element(id).innerHTML, /<script>|<img>/);
    assert.match(h.element(id).innerHTML, /&lt;script&gt;/);
    assert.match(h.element(id).innerHTML, /&quot;&gt;&lt;img&gt;/);
  }
});

test('focus targets the first missing argument for a template and the prompt otherwise', async () => {
  const h = harness(async () => [template]);
  await h.controller.loadPicker();
  h.controller.focus();
  assert.equal(h.element('task-prompt').focused, true);
  h.select('review');
  h.input(0, 'url');
  h.controller.focus();
  assert.equal(h.element('task-argument-1').focused, true);
});

test('the editor shows which arguments a task text asks for', async () => {
  const h = harness(async () => [template]);
  await h.openEditor();
  assert.match(h.element('template-arguments').textContent, /url, focus$/);
  h.element('template-prompt').value = 'Plain text';
  h.element('template-prompt').listeners.input();
  assert.match(h.element('template-arguments').textContent, /No arguments/);
});

test('a queued close event does not drop the picker load of a reopened New task dialog', async () => {
  let resolve;
  const h = harness(() => new Promise((done) => { resolve = done; }));
  h.element('new-task-dialog').open = true;
  const loading = h.controller.loadPicker();
  h.element('new-task-dialog').listeners.close();
  resolve([template]);
  await loading;
  assert.match(h.element('task-template').innerHTML, /Review/);
  assert.equal(h.element('task-template').disabled, false);
  h.element('new-task-dialog').open = false;
  const dropped = h.controller.loadPicker();
  h.element('new-task-dialog').listeners.close();
  resolve([]);
  await dropped;
  assert.match(h.element('task-template').innerHTML, /Review/, 'a load belonging to a closed dialog is dropped');
});

test('a queued close event does not unlock or discard the load of a reopened editor', async () => {
  let resolve;
  const h = harness((_, options) => options ? Promise.resolve(null) : new Promise((done) => { resolve = done; }));
  const loading = h.openEditor();
  assert.equal(h.element('tasks-dialog').open, true);
  h.element('tasks-dialog').listeners.close();
  assert.equal(h.element('template-fields').disabled, true, 'still loading: the form stays locked');
  resolve([template]);
  await loading;
  assert.equal(h.element('template-fields').disabled, false);
  assert.equal(h.element('template-name').value, 'Review');
});

test('closing the editor mid-load releases it; a pending save stays visible when reopened', async () => {
  let resolveLoad, resolveSave;
  let loads = 0;
  const h = harness((_, options) => {
    if (!options) return ++loads === 1 ? new Promise((done) => { resolveLoad = done; }) : Promise.resolve([template]);
    return new Promise((done) => { resolveSave = done; });
  });
  const first = h.openEditor();
  h.element('tasks-dialog').open = false;
  h.element('tasks-dialog').listeners.close();
  assert.equal(h.element('template-fields').disabled, false, 'closing mid-load releases the form');
  await h.openEditor();
  assert.equal(h.element('tasks-dialog').open, true);
  assert.equal(h.element('template-name').value, 'Review');
  resolveLoad([{ id: 'stale', name: 'Stale', prompt: 'Old' }]);
  await first;
  assert.equal(h.element('template-name').value, 'Review', 'a superseded load is ignored');

  h.element('template-name').value = 'Renamed';
  const saving = h.submit();
  h.element('tasks-dialog').open = false;
  h.element('tasks-dialog').listeners.close();
  assert.equal(h.element('template-fields').disabled, true, 'a pending save keeps the form locked');
  await h.openEditor();
  assert.equal(h.element('tasks-dialog').open, true, 'the pending editor can be reopened');
  assert.equal(h.element('template-name').value, 'Renamed');
  resolveSave({ ...template, name: 'Renamed' });
  await saving;
  assert.equal(h.element('template-fields').disabled, false);
  assert.equal(h.element('template-status').textContent, 'Saved.');
});

// Harness for the New task dialog section of app.js, with the template module stubbed.
function newTaskHarness({ createCard, getPrompt = () => 'Prompt' }) {
  const app = fs.readFileSync(path.join(__dirname, '../pkg/board/static/app.js'), 'utf8');
  const start = app.indexOf('// New task dialog');
  const end = app.indexOf('// Projects dialog', start);
  assert.ok(start >= 0 && end > start);
  const label = { textContent: 'Create task' };
  const classes = new Set();
  const submitBtn = {
    disabled: false, querySelector: () => label,
    classList: { add: (c) => classes.add(c), remove: (c) => classes.delete(c), has: (c) => classes.has(c) },
  };
  const form = { listeners: {}, addEventListener(event, callback) { this.listeners[event] = callback; } };
  const dialog = {
    open: false, listeners: {}, addEventListener(event, callback) { this.listeners[event] = callback; },
    showModal() { this.open = true; }, close() { this.open = false; this.listeners.close?.(); },
    querySelector: (selector) => selector === 'form' ? form : submitBtn,
  };
  const elements = { 'new-task-dialog': dialog, 'task-project': { value: 'p1', innerHTML: '' } };
  const calls = { reset: 0, focus: 0, loads: 0 };
  const alerts = [];
  const context = vm.createContext({
    projects: [{ id: 'p1', name: 'P' }], alerts,
    document: { getElementById: (id) => elements[id] },
    initTaskTemplates: () => ({
      loadPicker: async () => { calls.loads++; }, focus: () => { calls.focus++; },
      getPrompt, resetPicker: () => { calls.reset++; },
    }),
    api: () => {}, esc: (s) => s, API: { createCard }, alert: (message) => alerts.push(message),
  });
  vm.runInContext(app.slice(start, end), context);
  return { context, dialog, form, submitBtn, label, classes, calls, open: () => context.openNewTaskDialog(), submit: () => form.listeners.submit({ preventDefault() {} }), dispatchClose: () => dialog.listeners.close() };
}

test('a queued close event from a previous opening does not re-enable a reopened dialog', async () => {
  const resolvers = [];
  const h = newTaskHarness({ createCard: () => new Promise((done) => resolvers.push(done)) });
  await h.open();
  h.submit();
  h.dialog.open = false; // closed, but the close event is still queued
  await h.open();
  assert.equal(h.submitBtn.disabled, false, 'opening resets the button itself');
  h.submit();
  assert.equal(h.submitBtn.disabled, true);
  h.dispatchClose();
  assert.equal(h.submitBtn.disabled, true, 'the stale close event is ignored');
  assert.ok(h.classes.has('btn-loading'));
  resolvers[1]({});
  await new Promise((done) => setImmediate(done));
  assert.equal(h.dialog.open, false);
  assert.equal(h.calls.reset, 1);
});

test('a creation started in an earlier opening cannot close or reset a reopened dialog', async () => {
  const resolvers = [];
  const h = newTaskHarness({ createCard: () => new Promise((done) => resolvers.push(done)) });
  await h.open();
  const first = h.submit();
  assert.equal(h.submitBtn.disabled, true);
  await h.submit();
  h.dialog.close();
  await h.open();
  assert.equal(h.submitBtn.disabled, false, 'closing resets the button');
  assert.equal(h.calls.focus, 2);
  const second = h.submit();
  assert.equal(h.submitBtn.disabled, true, 'the reopened dialog can submit again');
  assert.equal(resolvers.length, 2);
  resolvers[0]({});
  await first;
  assert.equal(h.dialog.open, true, 'the stale creation leaves the reopened dialog alone');
  assert.equal(h.calls.reset, 0);
  resolvers[1]({});
  await second;
  assert.equal(h.dialog.open, false);
  assert.equal(h.calls.reset, 1);
});

test('a failed creation re-enables the current dialog but not a reopened one', async () => {
  const rejecters = [];
  const h = newTaskHarness({ createCard: () => new Promise((_, fail) => rejecters.push(fail)) });
  await h.open();
  const pending = h.submit();
  rejecters[0](new Error('Boom'));
  await pending;
  assert.deepEqual(h.context.alerts, ['Boom']);
  assert.equal(h.submitBtn.disabled, false);
  assert.equal(h.label.textContent, 'Create task');
  assert.equal(h.dialog.open, true);

  const again = h.submit();
  h.dialog.close();
  await h.open();
  h.submitBtn.disabled = true;
  rejecters[1](new Error('Later'));
  await again;
  assert.equal(h.submitBtn.disabled, true, 'a stale failure does not touch the reopened dialog');
});

test('an empty or invalid prompt never sends a request', async () => {
  let created = 0;
  const h = newTaskHarness({ createCard: async () => { created++; }, getPrompt: () => '' });
  await h.open();
  await h.submit();
  assert.equal(created, 0);
  assert.equal(h.submitBtn.disabled, false);
});

test('Tasks follows Projects in the header and the picker is wired into new-task submission', () => {
  const html = fs.readFileSync(path.join(__dirname, '../pkg/board/static/index.html'), 'utf8');
  assert.ok(html.indexOf('id="btn-projects"') < html.indexOf('id="btn-tasks"'));
  assert.ok(html.indexOf('id="btn-tasks"') < html.indexOf('id="btn-columns"'));
  assert.match(html, /id="task-template"/);
  const app = fs.readFileSync(path.join(__dirname, '../pkg/board/static/app.js'), 'utf8');
  assert.match(app, /await taskTemplates\.loadPicker\(\)/);
  assert.match(app, /const prompt = taskTemplates\.getPrompt\(\)/);
  assert.match(app, /await API\.createCard\(\{ prompt, projectId \}\)/);
  assert.match(app, /taskTemplates\.resetPicker\(\)/);
});
