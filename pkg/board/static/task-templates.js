// Only named placeholders are expanded; all other text is left untouched.
const placeholderPattern = /\{\{\s*([A-Za-z_][A-Za-z0-9_-]*)\s*\}\}/g;

export function taskArguments(prompt) {
  return [...new Set([...prompt.matchAll(placeholderPattern)].map((match) => match[1]))];
}

export function expandTask(prompt, values) {
  return prompt.replace(placeholderPattern, (placeholder, name) => values.get(name) ?? placeholder);
}

export function initTaskTemplates({ api, esc }) {
  const byID = (id) => document.getElementById(id);
  const picker = byID('task-template');
  const prompt = byID('task-prompt');
  const argumentsContainer = byID('task-arguments');
  const editor = byID('tasks-dialog');
  const form = byID('template-form');
  const list = byID('template-list');
  let templates = [];
  let pickerTemplates = [];
  let selectedTemplate = null;
  let customPrompt = prompt.value;
  let pickerLoad = 0;
  let editorLoad = 0;
  let editingID = '';
  let editorPending = false;
  // A save or delete in flight; unlike a load it must survive closing the dialog.
  let editorMutating = false;

  function argumentInputs() {
    return [...argumentsContainer.querySelectorAll('input')];
  }

  function updatePreview() {
    if (!selectedTemplate) return;
    const values = new Map(argumentInputs().map((input) => [input.dataset.argument, input.value]));
    prompt.value = expandTask(selectedTemplate.prompt, values);
  }

  function selectTemplate() {
    if (!selectedTemplate) customPrompt = prompt.value;
    selectedTemplate = pickerTemplates.find((template) => template.id === picker.value) || null;
    prompt.readOnly = !!selectedTemplate;
    byID('task-prompt-label').textContent = selectedTemplate ? 'Prompt preview' : 'Prompt';
    argumentsContainer.innerHTML = selectedTemplate ? taskArguments(selectedTemplate.prompt).map((name, index) =>
      `<label for="task-argument-${index}">${esc(name)}<input id="task-argument-${index}" data-argument="${esc(name)}" required autocomplete="off"></label>`
    ).join('') : '';
    if (selectedTemplate) updatePreview();
    else prompt.value = customPrompt;
  }

  picker.addEventListener('change', selectTemplate);
  argumentsContainer.addEventListener('input', (event) => {
    event.target.setCustomValidity('');
    updatePreview();
  });
  const newTaskDialog = byID('new-task-dialog');
  // close events are queued and may arrive after a reopen; only a dialog
  // that is really closed drops its in-flight load.
  newTaskDialog.addEventListener('close', () => {
    if (!newTaskDialog.open) pickerLoad++;
  });

  async function loadPicker() {
    const request = ++pickerLoad;
    picker.disabled = true;
    byID('task-template-status').textContent = '';
    try {
      const loaded = await api('/task-templates');
      if (request !== pickerLoad) return;
      templates = loaded;
      // Keep an in-progress prompt stable even if another tab edits its template.
      pickerTemplates = selectedTemplate ? templates.filter((t) => t.id !== selectedTemplate.id).concat(selectedTemplate) : templates;
      picker.innerHTML = '<option value="">Custom task</option>' + pickerTemplates.map((template) =>
        `<option value="${esc(template.id)}">${esc(template.name)}</option>`
      ).join('');
      picker.value = selectedTemplate?.id || '';
      if (!templates.length) byID('task-template-status').textContent = 'Save predefined tasks with the Tasks button in the header.';
    } catch (error) {
      if (request === pickerLoad) byID('task-template-status').textContent = `Could not load predefined tasks: ${error.message}`;
    } finally {
      if (request === pickerLoad) picker.disabled = false;
    }
  }

  function getPrompt() {
    for (const input of argumentInputs()) {
      input.setCustomValidity(input.value.trim() ? '' : 'Enter a value for this argument.');
      if (!input.reportValidity()) return '';
    }
    updatePreview();
    return prompt.value.trim();
  }

  // Focus the first missing argument when a template is selected, otherwise
  // the prompt; the preview textarea is read-only and useless to type into.
  function focus() {
    const target = argumentInputs().find((input) => !input.value.trim()) || argumentInputs()[0];
    (selectedTemplate && target ? target : prompt).focus();
  }

  function resetPicker() {
    selectedTemplate = null;
    prompt.value = '';
    picker.value = '';
    selectTemplate();
  }

  function setEditorPending(pending) {
    editorPending = pending;
    byID('template-fields').disabled = pending;
    byID('template-delete').disabled = pending || !editingID;
  }

  function fillEditor(template) {
    editingID = template?.id || '';
    list.value = editingID;
    byID('template-name').value = template?.name || '';
    byID('template-prompt').value = template?.prompt || '';
    byID('template-delete').disabled = !editingID;
    byID('template-submit').textContent = editingID ? 'Save changes' : 'Add task';
    byID('template-status').textContent = '';
    updateArgumentHint();
  }

  function updateArgumentHint() {
    const names = taskArguments(byID('template-prompt').value);
    byID('template-arguments').textContent = names.length
      ? `Arguments asked when starting this task: ${names.join(', ')}`
      : 'No arguments: this task runs as written.';
  }
  byID('template-prompt').addEventListener('input', updateArgumentHint);

  function renderList(selectedID) {
    list.innerHTML = '<option value="">New predefined task…</option>' + templates.map((template) =>
      `<option value="${esc(template.id)}">${esc(template.name)}</option>`
    ).join('');
    fillEditor(templates.find((template) => template.id === selectedID));
  }

  byID('btn-tasks').addEventListener('click', async () => {
    // A pending save or delete keeps its disabled form visible instead of
    // locking the user out; a pending load is simply superseded.
    if (editorMutating) {
      editor.showModal();
      return;
    }
    const request = ++editorLoad;
    editor.showModal();
    setEditorPending(true);
    byID('template-status').textContent = 'Loading…';
    try {
      const loaded = await api('/task-templates');
      if (request !== editorLoad) return;
      templates = loaded;
      renderList(templates.some((t) => t.id === editingID) ? editingID : templates[0]?.id);
    } catch (error) {
      if (request === editorLoad) byID('template-status').textContent = error.message;
    } finally {
      if (request === editorLoad) setEditorPending(false);
    }
  });

  // Closing mid-load drops that load so the next open starts fresh. A queued
  // close event for a dialog that has already reopened is ignored.
  editor.addEventListener('close', () => {
    if (editor.open) return;
    editorLoad++;
    if (!editorMutating) setEditorPending(false);
  });

  list.addEventListener('change', () => {
    if (!editorPending) fillEditor(templates.find((template) => template.id === list.value));
  });
  byID('template-new').addEventListener('click', () => {
    if (editorPending) return;
    fillEditor(null);
    byID('template-name').focus();
  });

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (editorPending) return;
    const name = byID('template-name').value.trim();
    const text = byID('template-prompt').value;
    if (!name || !text.trim()) {
      byID('template-status').textContent = 'Name and task text are required.';
      return;
    }
    editorMutating = true;
    setEditorPending(true);
    byID('template-status').textContent = '';
    try {
      const saved = await api(`/task-templates${editingID ? '/' + encodeURIComponent(editingID) : ''}`, {
        method: editingID ? 'PUT' : 'POST', body: JSON.stringify({ name, prompt: text }),
      });
      const index = templates.findIndex((template) => template.id === saved.id);
      if (index === -1) templates.push(saved);
      else templates[index] = saved;
      renderList(saved.id);
      byID('template-status').textContent = 'Saved.';
    } catch (error) {
      byID('template-status').textContent = error.message;
    } finally {
      editorMutating = false;
      setEditorPending(false);
    }
  });

  byID('template-delete').addEventListener('click', async () => {
    if (editorPending || !editingID) return;
    if (!confirm('Delete this predefined task? Existing tasks will not be affected.')) return;
    editorMutating = true;
    setEditorPending(true);
    byID('template-status').textContent = '';
    try {
      await api(`/task-templates/${encodeURIComponent(editingID)}`, { method: 'DELETE' });
      templates = templates.filter((template) => template.id !== editingID);
      renderList(templates[0]?.id);
    } catch (error) {
      byID('template-status').textContent = error.message;
    } finally {
      editorMutating = false;
      setEditorPending(false);
    }
  });

  return { loadPicker, getPrompt, resetPicker, focus };
}
