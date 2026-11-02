const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../pkg/board/static/app.js'), 'utf8');
const markup = fs.readFileSync(path.join(__dirname, '../pkg/board/static/index.html'), 'utf8');
const styles = fs.readFileSync(path.join(__dirname, '../pkg/board/static/style.css'), 'utf8');
const controlTags = new Map([...markup.matchAll(/<(input|select|textarea|button)\b[^>]*\bid="([^"]+)"/g)]
  .map((match) => [match[2], match[1]]));
controlTags.set('proj-edit', 'button');
controlTags.set('proj-delete', 'button');
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
      querySelectorAll: (selector) => selector.startsWith('#projects-dialog')
        ? [...elements].filter(([id]) => selector.includes(`#projects-dialog ${controlTags.get(id)}`)).map(([, control]) => control)
        : buttons,
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
  h.element('proj-instructions').value = '  Draft guidance\n\nKeep spaces.  ';
  h.select('a');
  h.element('proj-new').listeners.click();
  assert.equal(h.element('proj-name').value, 'Draft');
  assert.equal(h.element('proj-instructions').value, '  Draft guidance\n\nKeep spaces.  ');
});

test('creating a project selects its inspector and clears the creator', async () => {
  const h = harness();
  await h.open();
  h.element('proj-name').value = ' New project ';
  h.element('proj-repo').value = ' /repo ';
  h.element('proj-agent').value = '/agents/default.yaml';
  const instructions = '\n  Keep indentation.\n\n\tUse tests.  \n';
  h.element('proj-instructions').value = instructions;
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(JSON.stringify(h.calls), JSON.stringify([{ name: 'New project', repoPath: '/repo', agent: '/agents/default.yaml', instructions }]));
  assert.ok(h.element('project-inspector').innerHTML.includes(instructions));
  assert.equal(h.element('projects-count').textContent, '1 project');
  assert.match(h.element('project-inspector').innerHTML, /New project/);
  assert.equal(h.element('form').hidden, true);
  assert.equal(h.element('proj-name').value, '');
  assert.equal(h.element('proj-instructions').value, '');
  h.element('proj-new').listeners.click();
  assert.equal(h.element('proj-instructions').value, '');
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
  const h = harness([project('a'), { ...project('b'), instructions: 'Original\n  guidance.' }]);
  await h.open();
  h.select('b');
  h.element('proj-edit').listeners.click();
  assert.equal(h.element('proj-name').value, 'Project b');
  assert.equal(h.element('proj-repo').value, '/home/test/b');
  assert.equal(h.element('proj-agent').value, '/agents/default.yaml');
  assert.equal(h.element('proj-instructions').value, 'Original\n  guidance.');
  assert.equal(h.element('form').hidden, false);
  assert.equal(h.element('project-inspector').hidden, true);
  assert.equal(h.element('proj-cancel').hidden, false);
  assert.equal(h.element('proj-submit').textContent, 'Save changes');
  assert.equal(h.element('proj-name').focused, true);
  h.element('proj-name').value = ' Renamed ';
  h.element('proj-repo').value = ' /new/repo ';
  h.element('proj-agent').value = '/new/agent.yaml';
  const instructions = '  Updated guidance.\n\n\tKeep whitespace.\n';
  h.element('proj-instructions').value = instructions;
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(JSON.stringify(h.updates), JSON.stringify([{ id: 'b', name: 'Renamed', repoPath: '/new/repo', agent: '/new/agent.yaml', instructions }]));
  assert.ok(h.element('project-inspector').innerHTML.includes(instructions));
  assert.equal(h.calls.length, 0);
  assert.equal(h.element('projects-count').textContent, '2 projects');
  assert.match(h.element('project-inspector').innerHTML, /Renamed/);
  assert.equal(h.element('form').hidden, true);
  assert.deepEqual(h.alerts, []);
});

test('canceling an edit restores the new-project draft and leaves the project unchanged', async () => {
  const saved = { ...project('a'), instructions: 'Saved instructions' };
  const h = harness([saved]);
  await h.open();
  h.element('proj-new').listeners.click();
  h.element('proj-name').value = 'Draft';
  h.element('proj-repo').value = '/draft/repo';
  h.element('proj-instructions').value = '  Draft\n\tinstructions.\n';
  h.select('a');
  h.element('proj-edit').listeners.click();
  h.element('proj-name').value = 'Unsaved';
  h.element('proj-instructions').value = 'Unsaved instructions';
  h.element('proj-cancel').listeners.click();
  assert.match(h.element('project-inspector').innerHTML, /Project a/);
  assert.equal(h.updates.length, 0);
  assert.equal(saved.instructions, 'Saved instructions');
  assert.match(h.element('project-inspector').innerHTML, /Saved instructions/);
  h.element('proj-new').listeners.click();
  assert.equal(h.element('proj-name').value, 'Draft');
  assert.equal(h.element('proj-repo').value, '/draft/repo');
  assert.equal(h.element('proj-instructions').value, '  Draft\n\tinstructions.\n');
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
  h.element('proj-instructions').value = '\n Unsaved\n\tinstructions.  ';
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.deepEqual(h.alerts, ['Invalid repository']);
  assert.equal(h.element('form').hidden, false);
  assert.equal(h.element('proj-name').value, 'Edited');
  assert.equal(h.element('proj-repo').value, '/invalid');
  assert.equal(h.element('proj-instructions').value, '\n Unsaved\n\tinstructions.  ');
  assert.equal(h.element('proj-instructions').disabled, false);
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
  assert.equal(h.element('proj-instructions').disabled, true);
  await h.element('form').listeners.submit({ preventDefault() {} });
  h.select('b');
  h.element('proj-new').listeners.click();
  h.element('proj-cancel').listeners.click();
  assert.equal(saves, 1);
  assert.equal(h.element('proj-name').value, 'Project a');
  resolve(project('a'));
  await saving;
  assert.equal(h.element('proj-submit').disabled, false);
  assert.equal(h.element('proj-instructions').disabled, false);
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

test('additional instructions are an optional multiline field with whitespace-preserving display', () => {
  assert.match(markup, /<label>Additional instructions\s*<textarea\b[^>]*\bid="proj-instructions"/);
  const textarea = markup.match(/<textarea\b[^>]*\bid="proj-instructions"[^>]*>/)?.[0];
  assert.ok(textarea);
  assert.ok(!textarea.includes('required'));
  assert.match(textarea, /rows="[2-9]\d*"/);
  assert.match(styles, /\.project-instructions\s*\{[^}]*white-space:\s*pre-wrap/);
});

test('instruction markup is escaped and multiline whitespace is preserved in the inspector', async () => {
  const instructions = '\n  <script>alert("x")</script>\n\n<img src=x onerror=alert(1)> & \'quoted\'\n';
  const h = harness([{ ...project('a'), instructions }]);
  await h.open();
  const html = h.element('project-inspector').innerHTML;
  assert.match(html, /<dt>Additional instructions<\/dt>/);
  assert.ok(html.includes('<dd class="project-instructions">\n  &lt;script&gt;alert(&quot;x&quot;)&lt;/script&gt;\n\n&lt;img src=x onerror=alert(1)&gt; &amp; &#39;quoted&#39;\n</dd>'));
  assert.ok(!html.includes('<script>') && !html.includes('<img'));
  h.element('proj-edit').listeners.click();
  assert.equal(h.element('proj-instructions').value, instructions);
});

test('legacy and empty projects show an empty state and edit with no instructions', async () => {
  const h = harness([project('legacy'), { ...project('empty'), instructions: '' }]);
  await h.open();
  for (const id of ['legacy', 'empty']) {
    h.select(id);
    assert.match(h.element('project-inspector').innerHTML, /<dd class="project-instructions">No additional instructions\.<\/dd>/);
    assert.doesNotMatch(h.element('project-inspector').innerHTML, /undefined|null/);
    h.element('proj-edit').listeners.click();
    assert.equal(h.element('proj-instructions').value, '');
    await h.element('form').listeners.submit({ preventDefault() {} });
    assert.equal(h.updates.at(-1).instructions, '');
  }
});

test('instructions can be explicitly cleared and stay empty when reopening the editor', async () => {
  const h = harness([{ ...project('a'), instructions: 'Old\ninstructions' }]);
  await h.open();
  h.element('proj-edit').listeners.click();
  h.element('proj-instructions').value = '';
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(h.updates[0].instructions, '');
  assert.match(h.element('project-inspector').innerHTML, /No additional instructions\./);
  assert.doesNotMatch(h.element('project-inspector').innerHTML, /Old\ninstructions/);
  h.element('proj-edit').listeners.click();
  assert.equal(h.element('proj-instructions').value, '');
});

test('new projects send empty and whitespace-only instructions verbatim', async () => {
  const h = harness();
  await h.open();
  for (const instructions of ['', ' \n\t  \n']) {
    h.element('proj-new').listeners.click();
    h.element('proj-name').value = 'New project';
    h.element('proj-instructions').value = instructions;
    await h.element('form').listeners.submit({ preventDefault() {} });
    assert.equal(h.calls.at(-1).instructions, instructions);
    if (instructions) {
      assert.ok(h.element('project-inspector').innerHTML.includes(`<dd class="project-instructions">${instructions}</dd>`));
    }
  }
});

test('saving an edit preserves the separate new-project instructions draft', async () => {
  const h = harness([{ ...project('a'), instructions: 'Original' }]);
  await h.open();
  h.element('proj-new').listeners.click();
  h.element('proj-instructions').value = '\n  New project draft\n';
  h.select('a');
  h.element('proj-edit').listeners.click();
  h.element('proj-instructions').value = 'Saved edit';
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(h.updates[0].instructions, 'Saved edit');
  h.element('proj-new').listeners.click();
  assert.equal(h.element('proj-instructions').value, '\n  New project draft\n');
});

test('switching projects discards unsaved instructions without leaking them into another editor', async () => {
  const h = harness([{ ...project('a'), instructions: 'Project a instructions' }, project('b')]);
  await h.open();
  h.element('proj-edit').listeners.click();
  h.element('proj-instructions').value = 'Unsaved instructions';
  h.select('b');
  h.element('proj-edit').listeners.click();
  assert.equal(h.element('proj-instructions').value, '');
  h.element('proj-instructions').value = 'Another unsaved edit';
  h.element('proj-new').listeners.click();
  assert.equal(h.element('proj-instructions').value, '');
  h.select('a');
  assert.match(h.element('project-inspector').innerHTML, /Project a instructions/);
  assert.equal(h.updates.length, 0);
});

test('a failed create preserves instructions and a retry saves them without duplicate submissions', async () => {
  let reject, attempts = 0;
  const submitted = [];
  const h = harness([], { createProject: (data) => {
    submitted.push(data);
    if (++attempts === 1) return new Promise((_, fail) => { reject = fail; });
    return Promise.resolve({ id: 'new', ...data });
  } });
  await h.open();
  const instructions = '\n  Keep this draft.\n\n';
  h.element('proj-name').value = 'New project';
  h.element('proj-instructions').value = instructions;
  const saving = h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(h.element('proj-instructions').disabled, true);
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(attempts, 1);
  reject(new Error('Create failed'));
  await saving;
  assert.deepEqual(h.alerts, ['Create failed']);
  assert.equal(h.element('form').hidden, false);
  assert.equal(h.element('proj-instructions').value, instructions);
  assert.equal(h.element('proj-instructions').disabled, false);
  await h.element('form').listeners.submit({ preventDefault() {} });
  assert.equal(attempts, 2);
  assert.ok(submitted.every((data) => data.instructions === instructions));
  assert.ok(h.element('project-inspector').innerHTML.includes(instructions));
  assert.equal(h.element('proj-instructions').value, '');
  assert.equal(h.element('proj-instructions').disabled, false);
});
