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
  const { loop, versions, jobs = [], rounds = [], feedback = [] } = detail;
  const version = versions.find((item) => item.id === selectedVersion) || versions[0];
  return `<div class="loop-detail-heading"><div><div class="loop-eyebrow">REUSABLE INTELLIGENCE</div><h2>${escapeHTML(loop.name)}</h2></div>
    <span class="loop-badge">${versions.length} ${versions.length === 1 ? 'version' : 'versions'}</span></div>
    <div class="loop-contract"><section><h3>The task</h3><p>${escapeHTML(loop.description)}</p></section>
    <section><h3>What good looks like</h3><p>${escapeHTML(loop.criteria)}</p></section></div>
    <section class="loop-panel"><div class="loop-section-heading"><div><h3>Agent versions</h3><p>Immutable snapshots. Experiment without losing what worked.</p></div>
      <div class="loop-actions"><button class="btn btn-ghost" data-action="import">Import YAML</button><button class="btn btn-primary" data-action="generate" ${jobs.some((job) => job.kind === 'generate' && ['queued', 'running'].includes(job.status)) ? 'disabled' : ''}>✦ Generate agent</button></div></div>
      ${version ? `<div class="loop-version-tabs">${versions.map((item) => `<button class="btn ${item.id === version.id ? 'btn-primary' : 'btn-ghost'}" data-version="${escapeHTML(item.id)}">v${item.number}${item.id === loop.activeVersion ? ' · active' : ''}</button>`).join('')}</div>
      <p class="loop-rationale">${escapeHTML(version.rationale || 'Imported configuration')}</p>
      <pre class="loop-code"><code>${escapeHTML(version.yaml)}</code></pre>
      <div class="loop-section-heading"><span class="loop-muted">Saved ${escapeHTML(new Date(version.createdAt).toLocaleString())}</span>
      <div class="loop-actions"><button class="btn btn-ghost" data-action="promote" ${version.id === loop.activeVersion ? 'disabled' : ''}>${version.id === loop.activeVersion ? '✓ Active' : 'Use this version'}</button><a class="btn btn-ghost" href="/api/loops/${encodeURIComponent(loop.id)}/versions/${encodeURIComponent(version.id)}/export" download="agent.yaml">↓ Export agent</a></div></div>` :
      `<div class="loop-empty-inline"><span aria-hidden="true">◇</span><h3>A good agent starts with a clear task.</h3><p>Your contract is saved. Generate a specialized agent, or import one you already have.</p></div>`}
    </section>
    <section class="loop-panel"><div class="loop-section-heading"><div><h3>Try this agent</h3><p>Each run starts fresh. Model calls use your configured provider credentials.</p></div>
      <button class="btn btn-primary" data-action="run" ${version ? '' : 'disabled'}>▷ Run ${version ? `v${version.number}` : ''}</button></div>
      <p class="loop-example">${escapeHTML(loop.example)}</p></section>
    <section class="loop-panel"><div class="loop-section-heading"><div><h3>Make the next version better</h3><p>Test two hypotheses alongside the baseline. Your active agent stays unchanged.</p></div>
      <button class="btn btn-primary" data-action="optimize" ${version ? '' : 'disabled'}>↻ Optimize</button></div>
      ${rounds.map((round) => renderRound(round, jobs, versions, loop.activeVersion)).join('')}
      ${!rounds.length ? '<p class="loop-hint">Feedback → candidate agents → repeated tests → independent judgment → your decision.</p>' : ''}</section>
    <section class="loop-panel"><div class="loop-section-heading"><div><h3>Activity & results</h3><p>Execution success is not a quality score. Inspect each result before trusting it.</p></div></div>
      ${jobs.length ? jobs.map((job) => renderJob(job, loop.id, feedback)).join('') : '<div class="loop-empty-inline"><p>No runs yet. Generate an agent, then try it on your example.</p></div>'}</section>`;
}

function formatMetricCost(cost) { return cost == null ? 'Cost unknown' : `$${cost.toFixed(cost < 1 ? 4 : 2)}`; }

export function renderJob(job, loopID, feedback = []) {
  const busy = ['queued', 'running'].includes(job.status);
  const metrics = job.metrics || {};
  return `<details class="loop-job" ${busy || job.status === 'failed' ? 'open' : ''} data-job="${escapeHTML(job.id)}"><summary>
    <span class="loop-status ${escapeHTML(job.status)}">${escapeHTML(job.status)}</span><strong>${({ generate: '✦ Agent design', proposal: '✦ Candidate design', test: '▷ Evaluation run', judge: '◇ Quality judge' })[job.kind] || '▷ Agent run'}</strong>
    <span class="loop-job-metrics">${formatMetricCost(metrics.cost)} · ${((metrics.durationMs || 0) / 1000).toFixed(1)}s</span></summary>
    ${job.error ? `<p class="loop-job-error">${escapeHTML(job.error)}</p>` : ''}
    <div class="loop-job-facts"><span>${escapeHTML(metrics.model || 'Model pending')}</span><span>${metrics.calls || 0} calls</span><span>${metrics.inputTokens || 0} in / ${metrics.outputTokens || 0} out</span></div>
    ${job.kind !== 'generate' ? `<details><summary>Input</summary><pre class="loop-output">${escapeHTML(job.input)}</pre></details>` : ''}
    <pre class="loop-output">${escapeHTML(job.output || (busy ? 'Waiting for the agent…' : 'No output was produced.'))}</pre>
    <div class="loop-actions"><a class="btn btn-ghost btn-small" href="/api/loops/${encodeURIComponent(loopID)}/jobs/${encodeURIComponent(job.id)}/events" download>Raw events ↓</a>
    <a class="btn btn-ghost btn-small" href="/api/loops/${encodeURIComponent(loopID)}/jobs/${encodeURIComponent(job.id)}/stderr" download>Process log ↓</a>
    ${!busy && ['run', 'test'].includes(job.kind) ? `<button class="btn btn-ghost btn-small" data-feedback="${escapeHTML(job.id)}">＋ Give feedback</button>` : ''}
    ${busy ? `<button class="btn btn-ghost btn-small" data-cancel="${escapeHTML(job.id)}">Cancel</button>` : ''}</div>
    ${feedback.filter((item) => item.jobId === job.id).map((item) => `<p class="loop-feedback-note">${item.rating === 'good' ? '✓' : '✕'} ${escapeHTML(item.note || item.rating)}</p>`).join('')}</details>`;
}

export function summarizeCandidate(round, candidate, jobs) {
  const expected = round.inputs.length * round.repeats;
  const keys = new Set(candidate.trials.map((trial) => `${trial.case}/${trial.repeat}`));
  const complete = !candidate.error && expected > 0 && candidate.trials.length === expected && keys.size === expected;
  const runs = candidate.trials.map((trial) => jobs.find((job) => job.id === trial.jobId));
  const priced = complete && runs.every((job) => job?.metrics?.cost != null);
  const passed = candidate.trials.filter((trial) => trial.passed).length;
  return { expected, passed, qualified: complete && passed === expected,
    cost: priced ? runs.reduce((sum, job) => sum + job.metrics.cost, 0) / expected : null,
    duration: complete && runs.every((job) => job?.status === 'succeeded') ? runs.reduce((sum, job) => sum + job.metrics.durationMs, 0) / expected : null };
}

export function renderRound(round, jobs, versions, active) {
  const busy = ['designing', 'testing'].includes(round.status);
  return `<div class="loop-experiment"><div class="loop-section-heading"><h4>Experiment · ${escapeHTML(round.status)}</h4>
    ${busy ? `<button class="btn btn-ghost btn-small" data-cancel-round="${escapeHTML(round.id)}">Stop experiment</button>` : ''}</div>
    <p class="loop-hint">${round.inputs.length} inputs × ${round.repeats} repetitions. AI-judged evidence, not a guarantee. Parallel-run timings include contention.</p>
    ${round.error ? `<p class="loop-job-error">${escapeHTML(round.error)}</p>` : ''}
    <div class="loop-table-scroll"><table class="loop-comparison"><thead><tr><th>Version</th><th>Quality checks</th><th>Cost / run</th><th>Time / run</th><th></th></tr></thead><tbody>
    ${round.candidates.map((candidate) => {
      const summary = summarizeCandidate(round, candidate, jobs);
      const version = versions.find((item) => item.id === candidate.versionId);
      return `<tr><td><strong>${escapeHTML(candidate.label)}</strong><small>${version ? `v${version.number}` : 'Design pending'}</small></td>
      <td><span class="${summary.qualified ? 'loop-good' : ''}">${summary.passed} / ${summary.expected}</span>${candidate.error ? `<small>${escapeHTML(candidate.error)}</small>` : ''}</td>
      <td>${formatMetricCost(summary.cost)}</td><td>${summary.duration == null ? '—' : `${(summary.duration / 1000).toFixed(1)}s`}</td>
      <td>${version ? `<button class="btn btn-ghost btn-small" data-version="${escapeHTML(version.id)}">Inspect</button><button class="btn btn-ghost btn-small" data-promote="${escapeHTML(version.id)}" ${!summary.qualified || round.status !== 'completed' || active === version.id ? 'disabled' : ''}>${active === version.id ? '✓ Active' : 'Use version'}</button>` : ''}</td></tr>`;
    }).join('')}</tbody></table></div>
    <details class="loop-verdicts"><summary>Quality evidence</summary>${round.candidates.map((candidate) => `<h4>${escapeHTML(candidate.label)}</h4>${candidate.trials.map((trial) => `<p><span class="${trial.passed ? 'loop-good' : 'loop-bad'}">${trial.passed ? '✓' : '✕'}</span> Input ${trial.case + 1}, repeat ${trial.repeat + 1}: ${escapeHTML(trial.reason)}</p>`).join('')}`).join('')}</details>
    </div>`;
}

export function initLoops(api) {
  const byID = (id) => document.getElementById(id);
  let detail = null;
  let selectedVersion = '';
  let sequence = 0;
  let refreshTimer = null;
  let feedbackJob = '';
  let optimizeVersion = '';
  let runVersion = '';
  let modalLoop = '';

  function notice(message = '', error = false) {
    byID('loops-notice').textContent = message;
    byID('loops-notice').classList.toggle('error', error);
  }

  async function refreshLoops() {
    if (!location.hash.startsWith('#loops')) return;
    clearTimeout(refreshTimer);
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
      if (detail) {
        const openJobs = [...byID('loop-detail').querySelectorAll('details[data-job][open]')].map((item) => item.dataset.job);
        byID('loop-detail').innerHTML = renderLoopDetail(detail, selectedVersion);
        for (const item of byID('loop-detail').querySelectorAll('details[data-job]')) {
          if (openJobs.includes(item.dataset.job)) item.open = true;
        }
        if ((detail.jobs || []).some((job) => ['queued', 'running'].includes(job.status)) || (detail.rounds || []).some((round) => ['designing', 'testing'].includes(round.status))) refreshTimer = setTimeout(refreshLoops, 1000);
      }
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
    else { sequence++; clearTimeout(refreshTimer); }
  }

  byID('loops-list').addEventListener('click', (event) => {
    const button = event.target.closest('[data-loop]');
    if (button) { selectedVersion = ''; location.hash = `loops/${button.dataset.loop}`; }
  });
  byID('loop-detail').addEventListener('click', async (event) => {
    const version = event.target.closest('[data-version]');
    if (version && detail) {
      selectedVersion = version.dataset.version;
      byID('loop-detail').innerHTML = renderLoopDetail(detail, selectedVersion);
    }
    if (event.target.closest('[data-action="import"]')) byID('loop-import-dialog').showModal();
    if (event.target.closest('[data-action="run"]') && detail) {
      modalLoop = detail.loop.id;
      runVersion = (detail.versions.find((item) => item.id === selectedVersion) || detail.versions[0])?.id;
      byID('loop-run-input').value = detail.loop.example;
      byID('loop-run-dialog').showModal();
    }
    if (event.target.closest('[data-action="optimize"]') && detail) {
      modalLoop = detail.loop.id;
      optimizeVersion = (detail.versions.find((item) => item.id === selectedVersion) || detail.versions[0])?.id;
      byID('loop-test-inputs').value = detail.loop.example;
      byID('loop-optimize-dialog').showModal();
    }
    const feedback = event.target.closest('[data-feedback]');
    if (feedback && detail) {
      modalLoop = detail.loop.id;
      feedbackJob = feedback.dataset.feedback;
      byID('loop-feedback-dialog').showModal();
    }
    const promote = event.target.closest('[data-promote], [data-action="promote"]');
    if (promote && detail) {
      const version = promote.dataset.promote || (detail.versions.find((item) => item.id === selectedVersion) || detail.versions[0])?.id;
      if (!version || !window.confirm('Make this the active version? Review the result and quality evidence first. The previous version remains available.')) return;
      promote.disabled = true;
      try {
        await api(`/loops/${detail.loop.id}/promote`, { method: 'POST', body: JSON.stringify({ version, expectedActive: detail.loop.activeVersion }) });
        await refreshLoops();
      } catch (error) { notice(error.message, true); }
      finally { promote.disabled = false; }
    }
    const stopRound = event.target.closest('[data-cancel-round]');
    if (stopRound && detail) {
      try { await api(`/loops/${detail.loop.id}/rounds/${stopRound.dataset.cancelRound}/cancel`, { method: 'POST' }); await refreshLoops(); }
      catch (error) { notice(error.message, true); }
    }
    const generate = event.target.closest('[data-action="generate"]');
    const cancel = event.target.closest('[data-cancel]');
    if (!detail || (!generate && !cancel)) return;
    const button = generate || cancel;
    button.disabled = true;
    try {
      await api(`/loops/${detail.loop.id}/${generate ? 'generate' : `jobs/${cancel.dataset.cancel}/cancel`}`, { method: 'POST' });
      await refreshLoops();
    } catch (error) { notice(error.message, true); }
    finally { button.disabled = false; }
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
  byID('loop-run-form').addEventListener('submit', (event) => {
    event.preventDefault();
    if (!detail) return;
    const version = runVersion;
    const id = modalLoop;
    if (!version || !id) return;
    submit(event.target, async () => {
      await api(`/loops/${id}/runs`, { method: 'POST', body: JSON.stringify({ version, input: byID('loop-run-input').value }) });
      await refreshLoops();
    });
  });
  byID('loop-feedback-form').addEventListener('submit', (event) => {
    event.preventDefault();
    const id = modalLoop, job = feedbackJob;
    submit(event.target, async () => {
      await api(`/loops/${id}/feedback`, { method: 'POST', body: JSON.stringify({ job, rating: byID('loop-rating').value, note: byID('loop-feedback-note').value }) });
      await refreshLoops();
    });
  });
  byID('loop-optimize-form').addEventListener('submit', (event) => {
    event.preventDefault();
    const id = modalLoop, version = optimizeVersion;
    submit(event.target, async () => {
      await api(`/loops/${id}/optimize`, { method: 'POST', body: JSON.stringify({ version,
        inputs: byID('loop-test-inputs').value.split(/\n---\n/).map((text) => text.trim()).filter(Boolean),
        repeats: Number(byID('loop-test-repeats').value),
        models: [byID('loop-model-one').value.trim(), byID('loop-model-two').value.trim()],
      }) });
      await refreshLoops();
    });
  });
  window.addEventListener('hashchange', route);
  window.addEventListener('board-refresh', refreshLoops);
  route();
}
