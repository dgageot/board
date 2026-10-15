const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../pkg/board/static/app.js'), 'utf8');
const start = source.indexOf('// Projects dialog');
const end = source.indexOf('// --- Folder picker ---', start);
assert.ok(start >= 0 && end > start);

function harness(projects = [], api = {}) {
  const elements = new Map();
  const buttons = [];
  const element = (id) => {
    if (!elements.has(id)) elements.set(id, {
      innerHTML: '', textContent: '', value: '', hidden: false,
      get options() { return [...this.innerHTML.matchAll(/<option value="([^"]*)"/g)].map((match) => ({ value: match[1] })); },
      insertAdjacentHTML(_, html) { this.innerHTML += html; },
      listeners: {}, addEventListener(event, callback) { this.listeners[event] = callback; },
      querySelector: () => element('form'), querySelectorAll: () => [],
      showModal() { this.open = true; }, focus() { this.focused = true; },
    });
    return elements.get(id);
  };
  const calls = [], updates = [], alerts = [];
  const context = vm.createContext({
    projects, window: {},
    document: {
      getElementById: element,
      querySelector: () => element('form'),
      querySelectorAll: (selector) => selector.includes('input,') ? [...elements.values()] : buttons,
    },
    API: {
      listAgents: async () => ['/agents/default.yaml'],
      createProject: async (data) => {
        calls.push(data);
        const project = { id: 'new', ...data };
        projects.push(project);
        return project;
      },
      updateProject: async (id, data) => {
        updates.push({ id, ...data });
        const project = projects.find((p) => p.id === id);
        Object.assign(project, data);
        return project;
      },
      deleteProject: async (id) => projects.splice(projects.findIndex((p) => p.id === id), 1),
      ...api,
    },
    refresh: async () => {},
    alert: (message) => alerts.push(message),
    esc: (s) => String(s).replace(/[&<>"']/g, (c) => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
    })[c]),
    shortenPath: (s) => s.replace('/home/test', '~'),
    icon: () => '<svg></svg>',
  });
  vm.runInContext(source.slice(start, end), context);
  return {
    context, element, calls, updates, alerts,
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

test('editing pre-fills the project and saves changes without creating a duplicate', async () => {
  const h = harness([project('a'), project('b')]);
  await h.open();
  h.select('b');
  h.element('proj-edit').listeners.click();
  assert.equal(h.element('proj-name').value, 'Project b');
  assert.equal(h.element('proj-repo').value, '/home/test/b');
  assert.equal(h.element('proj-agent').value, '/agents/default.yaml');
  assert.equal(h.element('form').hidden, false);
  assert.equal(h.element('project-inspector').hidden, true);
  assert.equal(h.element('proj-cancel').hidden, false);
  assert.equal(h.element('proj-submit').textContent, 'Save changes');
  assert.equal(h.element('proj-name').focused, true);
  h.element('proj-name').value = ' Renamed ';
  h.element('proj-repo').value = ' /new/repo ';
  h.element('proj-agent').value = '/new/agent.yaml';
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(JSON.stringify(h.updates), JSON.stringify([{ id: 'b', name: 'Renamed', repoPath: '/new/repo', agent: '/new/agent.yaml' }]));
  assert.equal(h.calls.length, 0);
  assert.equal(h.element('projects-count').textContent, '2 projects');
  assert.match(h.element('project-inspector').innerHTML, /Renamed/);
  assert.equal(h.element('form').hidden, true);
  assert.deepEqual(h.alerts, []);
});

test('canceling an edit restores the new-project draft and leaves the project unchanged', async () => {
  const h = harness([project('a')]);
  await h.open();
  h.element('proj-new').listeners.click();
  h.element('proj-name').value = 'Draft';
  h.element('proj-repo').value = '/draft/repo';
  h.select('a');
  h.element('proj-edit').listeners.click();
  h.element('proj-name').value = 'Unsaved';
  h.element('proj-cancel').listeners.click();
  assert.match(h.element('project-inspector').innerHTML, /Project a/);
  assert.equal(h.updates.length, 0);
  h.element('proj-new').listeners.click();
  assert.equal(h.element('proj-name').value, 'Draft');
  assert.equal(h.element('proj-repo').value, '/draft/repo');
  assert.equal(h.element('proj-submit').textContent, 'Add project');
});

test('switching projects or creating a new one exits edit mode', async () => {
  const h = harness([project('a'), project('b')]);
  await h.open();
  h.element('proj-edit').listeners.click();
  h.element('proj-name').value = 'Unsaved';
  h.select('b');
  assert.equal(h.element('form').hidden, true);
  assert.match(h.element('project-inspector').innerHTML, /Project b/);
  h.element('proj-edit').listeners.click();
  assert.equal(h.element('proj-name').value, 'Project b');
  h.element('proj-new').listeners.click();
  assert.equal(h.element('proj-name').value, '');
  assert.equal(h.element('proj-submit').textContent, 'Add project');
  assert.equal(h.element('proj-cancel').hidden, true);
});

test('saved agents absent from discovery remain selectable while editing', async () => {
  const h = harness([{ ...project('a'), agent: '/custom/agent.yaml' }]);
  await h.open();
  h.element('proj-edit').listeners.click();
  assert.equal(h.element('proj-agent').value, '/custom/agent.yaml');
  assert.ok(h.element('proj-agent').options.some((option) => option.value === '/custom/agent.yaml'));
  await h.context.populateAgentSelect();
  assert.equal(h.element('proj-agent').value, '/custom/agent.yaml');
  assert.ok(h.element('proj-agent').options.some((option) => option.value === '/custom/agent.yaml'));
});

test('failed edits retain the entered values and re-enable saving', async () => {
  const h = harness([project('a')], { updateProject: async () => { throw new Error('Invalid repository'); } });
  await h.open();
  h.element('proj-edit').listeners.click();
  h.element('proj-name').value = 'Edited';
  h.element('proj-repo').value = '/invalid';
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.deepEqual(h.alerts, ['Invalid repository']);
  assert.equal(h.element('form').hidden, false);
  assert.equal(h.element('proj-name').value, 'Edited');
  assert.equal(h.element('proj-repo').value, '/invalid');
  assert.equal(h.element('proj-submit').disabled, false);
});

test('an in-flight save blocks duplicate submits and switching the edited project', async () => {
  let resolve, saves = 0;
  const h = harness([project('a'), project('b')], { updateProject: () => {
    saves++;
    return new Promise((done) => { resolve = done; });
  } });
  await h.open();
  h.element('proj-edit').listeners.click();
  const saving = h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(h.element('proj-submit').disabled, true);
  await h.element('form').listeners.submit({ preventDefault() {} });
  h.select('b');
  h.element('proj-new').listeners.click();
  h.element('proj-cancel').listeners.click();
  assert.equal(saves, 1);
  assert.equal(h.element('proj-name').value, 'Project a');
  resolve(project('a'));
  await saving;
  assert.equal(h.element('proj-submit').disabled, false);
  assert.match(h.element('project-inspector').innerHTML, /Project a/);
});

test('a successful edit remains visible and exits edit mode even if refresh fails', async () => {
  const h = harness([project('a')], { updateProject: async (id, data) => ({ id, ...data }) });
  await h.open();
  h.element('proj-edit').listeners.click();
  h.element('proj-name').value = 'Saved name';
  h.context.refresh = async () => { throw new Error('Refresh failed'); };
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.deepEqual(h.alerts, ['Refresh failed']);
  assert.equal(h.element('form').hidden, true);
  assert.match(h.element('project-inspector').innerHTML, /Saved name/);
  h.element('proj-edit').listeners.click();
  assert.equal(h.element('proj-name').value, 'Saved name');
});

test('the saved response updates the inspector without relying on a refresh result', async () => {
  const h = harness([project('a')], { updateProject: async (id, data) => ({ id, ...data }) });
  await h.open();
  h.element('proj-edit').listeners.click();
  h.element('proj-name').value = 'Saved name';
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.match(h.element('project-inspector').innerHTML, /Saved name/);
});

test('deletion blocks editing while pending and leaves a working creator', async () => {
  let resolve;
  const h = harness([project('a')], { deleteProject: () => new Promise((done) => { resolve = done; }) });
  await h.open();
  const deleting = h.element('proj-delete').listeners.click();
  h.element('proj-edit').listeners.click();
  assert.equal(h.element('form').hidden, true);
  resolve();
  await deleting;
  assert.equal(h.element('form').hidden, false);
  assert.equal(h.element('proj-submit').textContent, 'Add project');
  h.element('proj-name').value = 'Replacement';
  h.element('proj-repo').value = '/replacement';
  h.element('proj-agent').value = '/agent';
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(h.calls.length, 1);
  assert.equal(h.updates.length, 0);
});
