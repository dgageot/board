const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../pkg/board/static/app.js'), 'utf8');
const start = source.indexOf('// Projects dialog');
const end = source.indexOf('// --- Folder picker ---', start);
assert.ok(start >= 0 && end > start);

function harness(projects = []) {
  const elements = new Map();
  const buttons = [];
  const element = (id) => {
    if (!elements.has(id)) elements.set(id, {
      innerHTML: '', textContent: '', value: '', hidden: false,
      listeners: {}, addEventListener(event, callback) { this.listeners[event] = callback; },
      querySelector: () => element('form'), querySelectorAll: () => [],
      showModal() { this.open = true; }, focus() { this.focused = true; },
    });
    return elements.get(id);
  };
  const calls = [];
  const context = vm.createContext({
    projects, window: {},
    document: {
      getElementById: element,
      querySelector: () => element('form'),
      querySelectorAll: () => buttons,
    },
    API: {
      listAgents: async () => ['/agents/default.yaml'],
      createProject: async (data) => {
        calls.push(data);
        const project = { id: 'new', ...data };
        projects.push(project);
        return project;
      },
      deleteProject: async (id) => projects.splice(projects.findIndex((p) => p.id === id), 1),
    },
    refresh: async () => {},
    alert: (message) => assert.fail(message),
    esc: (s) => String(s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    })[c]),
    shortenPath: (s) => s.replace('/home/test', '~'),
    icon: () => '<svg></svg>',
  });
  vm.runInContext(source.slice(start, end), context);
  return {
    context, element, calls,
    open: () => element('btn-projects').listeners.click(),
    select(id) {
      const button = { dataset: { id }, setAttribute(_, value) { this.pressed = value; } };
      buttons.push(button);
      element('projects-list').listeners.click({ target: { closest: () => button } });
      return button;
    },
  };
}

const project = (id) => ({ id, name: `Project ${id}`, repoPath: `/home/test/${id}`, agent: '/agents/default.yaml' });

test('empty projects open the creator with an empty state and count', async () => {
  const h = harness();
  await h.open();
  assert.equal(h.element('projects-dialog').open, true);
  assert.equal(h.element('projects-count').textContent, '0 projects');
  assert.match(h.element('projects-list').innerHTML, /No projects yet/);
  assert.equal(h.element('project-inspector').hidden, true);
  assert.equal(h.element('form').hidden, false);
});

test('opening selects the first project; selecting another shows its full details', async () => {
  const h = harness([project('a'), project('b')]);
  await h.open();
  assert.equal(h.element('projects-count').textContent, '2 projects');
  assert.equal(h.element('form').hidden, true);
  assert.match(h.element('project-inspector').innerHTML, /Project a/);
  const list = h.element('projects-list').innerHTML;
  assert.match(list, /~\/a/);
  const button = h.select('b');
  assert.equal(button.pressed, 'true');
  assert.match(h.element('project-inspector').innerHTML, /\/home\/test\/b/);
  assert.match(h.element('project-inspector').innerHTML, /\/agents\/default.yaml/);
  assert.equal(h.element('projects-list').innerHTML, list, 'selection does not replace the focused list');
});

test('new project focuses the creator and selecting a project preserves the draft', async () => {
  const h = harness([project('a')]);
  await h.open();
  h.element('proj-new').listeners.click();
  assert.equal(h.element('form').hidden, false);
  assert.equal(h.element('proj-name').focused, true);
  h.element('proj-name').value = 'Draft';
  h.select('a');
  h.element('proj-new').listeners.click();
  assert.equal(h.element('proj-name').value, 'Draft');
});

test('creating a project selects its inspector and clears the creator', async () => {
  const h = harness();
  await h.open();
  h.element('proj-name').value = ' New project ';
  h.element('proj-repo').value = ' /repo ';
  h.element('proj-agent').value = '/agents/default.yaml';
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(JSON.stringify(h.calls), JSON.stringify([{ name: 'New project', repoPath: '/repo', agent: '/agents/default.yaml' }]));
  assert.equal(h.element('projects-count').textContent, '1 project');
  assert.match(h.element('project-inspector').innerHTML, /New project/);
  assert.equal(h.element('form').hidden, true);
  assert.equal(h.element('proj-name').value, '');
});

test('deleting the selected project selects the next or shows the empty creator', async () => {
  const h = harness([project('a'), project('b')]);
  await h.open();
  await h.element('proj-delete').listeners.click();
  assert.match(h.element('project-inspector').innerHTML, /Project b/);
  assert.equal(h.element('projects-count').textContent, '1 project');
  await h.element('proj-delete').listeners.click();
  assert.equal(h.element('project-inspector').hidden, true);
  assert.equal(h.element('form').hidden, false);
});

test('project names and paths are escaped in list and inspector', async () => {
  const h = harness([{ id: 'x', name: '<script>', repoPath: '/repo/"<img>', agent: '<agent>' }]);
  await h.open();
  for (const id of ['projects-list', 'project-inspector']) {
    const html = h.element(id).innerHTML;
    assert.ok(!html.includes('<script>') && !html.includes('<img>'));
    assert.match(html, /&lt;script&gt;/);
    assert.match(html, /&quot;&lt;img&gt;/);
  }
});
