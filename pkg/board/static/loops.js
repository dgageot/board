const escapeHTML = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
})[char]);

export function renderLoopList(loops, selected) {
  if (!loops.length) return '<p class="loop-empty-list">Your reusable agents will live here.</p>';
  return loops.map((loop) => `<button class="loop-list-item ${loop.id === selected ? 'selected' : ''}" data-loop="${escapeHTML(loop.id)}" aria-pressed="${loop.id === selected}">
    <span class="loop-list-icon" aria-hidden="true">↻</span><span><strong>${escapeHTML(loop.name)}</strong>
    <small>${loop.activeVersion ? 'Active version' : 'Draft · ready to build'}</small></span><span aria-hidden="true">›</span></button>`).join('');
}

export function renderLoopDetail(detail, selectedVersion) {
  const { loop, versions } = detail;
  const version = versions.find((item) => item.id === selectedVersion) || versions[0];
  return `<div class="loop-detail-heading"><div><div class="loop-eyebrow">REUSABLE INTELLIGENCE</div><h2>${escapeHTML(loop.name)}</h2></div>
    <span class="loop-badge">${versions.length} ${versions.length === 1 ? 'version' : 'versions'}</span></div>
    <div class="loop-contract"><section><h3>The task</h3><p>${escapeHTML(loop.description)}</p></section>
    <section><h3>What good looks like</h3><p>${escapeHTML(loop.criteria)}</p></section></div>
    <section class="loop-panel"><div class="loop-section-heading"><div><h3>Agent versions</h3><p>Immutable snapshots. Experiment without losing what worked.</p></div>
      <button class="btn btn-ghost" data-action="import">Import YAML</button></div>
      ${version ? `<div class="loop-version-tabs">${versions.map((item) => `<button class="btn ${item.id === version.id ? 'btn-primary' : 'btn-ghost'}" data-version="${escapeHTML(item.id)}">v${item.number}${item.id === loop.activeVersion ? ' · active' : ''}</button>`).join('')}</div>
      <p class="loop-rationale">${escapeHTML(version.rationale || 'Imported configuration')}</p>
      <pre class="loop-code"><code>${escapeHTML(version.yaml)}</code></pre>
      <div class="loop-section-heading"><span class="loop-muted">Saved ${escapeHTML(new Date(version.createdAt).toLocaleString())}</span>
      <a class="btn btn-ghost" href="/api/loops/${encodeURIComponent(loop.id)}/versions/${encodeURIComponent(version.id)}/export" download="agent.yaml">↓ Export agent</a></div>` :
      `<div class="loop-empty-inline"><span aria-hidden="true">◇</span><h3>A good agent starts with a clear task.</h3><p>Your contract is saved. Import an agent to create the first version.</p></div>`}
    </section>
    <section class="loop-panel"><h3>Example input</h3><p class="loop-example">${escapeHTML(loop.example)}</p></section>`;
}

export function initLoops(api) {
  const byID = (id) => document.getElementById(id);
  let detail = null;
  let selectedVersion = '';
  let sequence = 0;

  function notice(message = '', error = false) {
    byID('loops-notice').textContent = message;
    byID('loops-notice').classList.toggle('error', error);
  }

  async function refreshLoops() {
    if (!location.hash.startsWith('#loops')) return;
    const request = ++sequence;
    const id = location.hash.split('/')[1] || '';
    try {
      const [list, nextDetail] = await Promise.all([api('/loops'), id ? api(`/loops/${encodeURIComponent(id)}`) : null]);
      if (request !== sequence) return;
      detail = nextDetail;
      byID('loops-list').innerHTML = renderLoopList(list, id);
      byID('loops-count').textContent = `${list.length} ${list.length === 1 ? 'loop' : 'loops'}`;
      byID('loop-detail').hidden = !detail;
      byID('loops-welcome').hidden = !!detail;
      if (detail) byID('loop-detail').innerHTML = renderLoopDetail(detail, selectedVersion);
    } catch (error) {
      if (request === sequence) notice(error.message, true);
    }
  }

  function route() {
    const active = location.hash.startsWith('#loops');
    document.body.classList.toggle('loops-mode', active);
    byID('board').hidden = active;
    byID('loops-workspace').hidden = !active;
    byID('workspace-board').setAttribute('aria-current', active ? 'false' : 'page');
    byID('workspace-loops').setAttribute('aria-current', active ? 'page' : 'false');
    if (active) refreshLoops();
    else sequence++;
  }

  byID('loops-list').addEventListener('click', (event) => {
    const button = event.target.closest('[data-loop]');
    if (button) { selectedVersion = ''; location.hash = `loops/${button.dataset.loop}`; }
  });
  byID('loop-detail').addEventListener('click', (event) => {
    const version = event.target.closest('[data-version]');
    if (version && detail) {
      selectedVersion = version.dataset.version;
      byID('loop-detail').innerHTML = renderLoopDetail(detail, selectedVersion);
    }
    if (event.target.closest('[data-action="import"]')) byID('loop-import-dialog').showModal();
  });
  for (const button of document.querySelectorAll('[data-new-loop]')) button.addEventListener('click', () => {
    byID('loop-new-dialog').showModal();
    byID('loop-name').focus();
  });
  async function submit(form, action) {
    const button = form.querySelector('[type="submit"]');
    if (button.disabled) return;
    button.disabled = true;
    const status = form.querySelector('[role="status"]');
    status.textContent = '';
    try { await action(); form.closest('dialog').close(); form.reset(); notice(); }
    catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; }
  }
  byID('loop-new-form').addEventListener('submit', (event) => {
    event.preventDefault();
    submit(event.target, async () => {
      const loop = await api('/loops', { method: 'POST', body: JSON.stringify({
        name: byID('loop-name').value, description: byID('loop-task').value,
        criteria: byID('loop-criteria').value, example: byID('loop-example').value,
      }) });
      selectedVersion = '';
      location.hash = `loops/${loop.id}`;
    });
  });
  byID('loop-import-form').addEventListener('submit', (event) => {
    event.preventDefault();
    const id = detail?.loop.id;
    if (!id) return;
    submit(event.target, async () => {
      const version = await api(`/loops/${id}/versions`, { method: 'POST', body: JSON.stringify({
        yaml: byID('loop-yaml').value, rationale: byID('loop-rationale').value,
      }) });
      selectedVersion = version.id;
      await refreshLoops();
    });
  });
  window.addEventListener('hashchange', route);
  window.addEventListener('board-refresh', refreshLoops);
  route();
}
