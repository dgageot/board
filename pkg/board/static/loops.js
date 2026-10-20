import { renderResultMarkdown, previewLimit } from './result-markdown.js';

const escapeHTML = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
})[char]);

export function renderLoopList(loops, selected, detail) {
  if (!loops.length) return '';
  return loops.map((loop) => {
    const activity = detail?.loop.id === loop.id ? loopActivity(detail) : null;
    const label = activity?.title || (loop.activeVersion ? 'Active agent' : 'Open loop');
    const running = activity && ['generating', 'running', 'optimizing'].includes(activity.phase);
    return `<button class="loop-list-item ${loop.id === selected ? 'selected' : ''}" data-loop="${escapeHTML(loop.id)}" aria-pressed="${loop.id === selected}">
    <span class="loop-list-icon ${running ? 'spinning' : ''}" aria-hidden="true">↻</span><span><strong>${escapeHTML(loop.name)}</strong>
    <small>${escapeHTML(label)}</small></span><span aria-hidden="true">›</span></button>`;
  }).join('');
}

export function selectedLoopVersion(detail, selectedVersion) {
  return detail.versions.find((item) => item.id === selectedVersion)
    || detail.versions.find((item) => item.id === detail.loop.activeVersion)
    || detail.versions.at(-1);
}

export function summarizeSpend(jobs) {
  const groups = {
    execution: { label: 'Runs', cost: 0, unknown: 0, count: 0 },
    optimization: { label: 'Design & experiments', cost: 0, unknown: 0, count: 0 },
    judging: { label: 'Quality judging', cost: 0, unknown: 0, count: 0 },
  };
  for (const job of jobs) {
    const group = job.kind === 'run' ? groups.execution : job.kind === 'judge' ? groups.judging : groups.optimization;
    group.count++;
    const cost = job.metrics?.cost;
    if (Number.isFinite(cost) && cost >= 0) group.cost += cost;
    else group.unknown++;
  }
  return groups;
}

function renderSpend(jobs) {
  return `<section class="loop-spend" aria-label="Recorded spend">${Object.values(summarizeSpend(jobs)).map((group) => `<div class="loop-spend-card">
    <span>${group.label}</span><strong>${group.count === 0 ? '—' : group.unknown === group.count ? 'Unknown' : formatMetricCost(group.cost)}</strong>
    <small>${group.count} ${group.count === 1 ? 'attempt' : 'attempts'}${group.unknown ? ` · ${group.unknown} unpriced${group.unknown < group.count ? ' · subtotal only' : ''}` : ''}</small>
  </div>`).join('')}</section>`;
}

const busyJob = (job) => job.status === 'queued' || job.status === 'running';
const liveRound = (round) => round.status === 'designing' || round.status === 'testing';

export function loopActivity(detail) {
  const jobs = detail.jobs || [];
  const rounds = detail.rounds || [];
  const generation = jobs.find((job) => job.kind === 'generate' && busyJob(job));
  if (generation) return { phase: 'generating', title: generation.status === 'queued' ? 'Agent creation queued' : 'Creating agent', note: generation.status === 'queued' ? 'Waiting for a worker.' : '', job: generation };
  const round = rounds.find(liveRound);
  if (round) return { phase: 'optimizing', title: 'Optimizing', note: [round.pass ? `Pass ${round.pass}/${round.maxPasses || 4}` : '', round.stage || (round.status === 'designing' ? 'Designing candidates' : 'Evaluating candidates')].filter(Boolean).join(' · '), round };
  const run = jobs.find((job) => job.kind === 'run' && busyJob(job));
  if (run) return { phase: 'running', title: run.status === 'queued' ? 'Run queued' : 'Agent running', note: run.project?.name || '', job: run };
  const latest = jobs.find((job) => job.kind === 'generate' || job.kind === 'run');
  if (latest && ['failed', 'canceled', 'interrupted'].includes(latest.status)) return { phase: 'failed', title: latest.status === 'failed' ? (latest.kind === 'generate' ? 'Agent creation failed' : 'Run failed') : (latest.kind === 'generate' ? 'Agent creation stopped' : 'Run stopped'), note: latest.error || 'Attempt incomplete.', job: latest };
  if (detail.versions.length) return { phase: 'ready', title: latest?.kind === 'run' ? 'Run complete' : 'Ready', note: '' };
  return { phase: 'empty', title: 'No agent', note: '' };
}

function renderActivity(activity, version) {
  let action = '';
  if (activity.job && busyJob(activity.job)) action = `<button class="btn btn-ghost" data-cancel="${escapeHTML(activity.job.id)}">Stop</button>`;
  else if (activity.round) action = `<button class="btn btn-ghost" data-cancel-round="${escapeHTML(activity.round.id)}">Stop</button>`;
  else if (!version) action = `<button class="btn btn-primary" data-action="generate">${activity.phase === 'failed' ? 'Retry creation' : 'Create agent'}</button>`;
  else action = `<button class="btn btn-primary" data-action="run">${activity.phase === 'failed' ? 'Retry analysis' : 'Run read-only'}</button>`;
  const running = ['generating', 'running', 'optimizing'].includes(activity.phase);
  return `<section class="loop-activity ${activity.phase}" aria-label="Current activity"><div class="loop-activity-mark ${running ? 'spinning' : ''}" aria-hidden="true">${running ? '↻' : activity.phase === 'failed' ? '!' : '✓'}</div>
    <div class="loop-activity-copy"><h3 role="status" aria-live="polite">${escapeHTML(activity.title)}</h3>${activity.note ? `<p>${escapeHTML(activity.note)}</p>` : ''}</div>${action}</section>`;
}

function renderResult(job, loopID, feedback, sourceView = false) {
  const failed = ['failed', 'canceled', 'interrupted'].includes(job.status);
  const title = busyJob(job) ? 'Live output' : failed ? 'Incomplete result' : 'Latest result';
  const output = job.output || '';
  const markdown = sourceView ? null : renderResultMarkdown(output);
  const truncated = output.length > previewLimit;
  const content = !output ? `<p class="loop-muted">${busyJob(job) ? 'Waiting for the first response…' : 'No output.'}</p>`
    : sourceView ? `<pre class="loop-source-text">${escapeHTML(output.slice(0, previewLimit))}</pre>` : markdown.html;
  return `<section class="loop-panel loop-result"><div class="loop-section-heading"><h3>${title}</h3><span class="loop-muted">${formatMetricCost(job.metrics?.cost)} · ${((job.metrics?.durationMs || 0) / 1000).toFixed(1)}s</span></div>
    <p class="loop-result-status"><span class="loop-status ${escapeHTML(job.status)}">${escapeHTML(job.status)}</span>${failed ? ' Partial output' : ''}</p>
    ${job.error ? `<p class="loop-job-error">${escapeHTML(job.error)}</p>` : ''}
    ${job.project ? `<p class="loop-project-context">${escapeHTML(job.project.name)} · ${escapeHTML(job.project.revision.slice(0, 8))}</p>` : ''}
    <div class="loop-result-toolbar"><button class="btn btn-ghost btn-small" data-source="${escapeHTML(job.id)}" aria-pressed="${sourceView}" ${!output ? 'disabled' : ''}>${sourceView ? 'Rendered' : 'Source'}</button><button class="btn btn-ghost btn-small" data-copy-result="${escapeHTML(job.id)}" ${!output ? 'disabled' : ''}>Copy</button></div>
    <div class="loop-prose" data-result-body="${escapeHTML(job.id)}" data-inspect="latest-output-${escapeHTML(job.id)}">${content}</div>
    ${truncated ? '<p class="loop-hint">Preview limited to 64K characters. Copy includes the full result.</p>' : ''}
    ${markdown?.tablesAsText ? '<p class="loop-hint">Large tables shown as text.</p>' : ''}
    ${!busyJob(job) ? `<button class="btn btn-ghost" data-feedback="${escapeHTML(job.id)}">Feedback</button>` : ''}
    ${feedback.filter((item) => item.jobId === job.id).map((item) => `<p class="loop-feedback-note">${item.rating === 'good' ? '✓' : '✕'} ${escapeHTML(item.note || item.rating)}</p>`).join('')}
    <details class="loop-secondary" data-inspect="result-details-${escapeHTML(job.id)}"><summary>Run details & logs</summary><p class="loop-muted">${escapeHTML(job.metrics?.model || 'Model not reported')} · ${job.metrics?.calls || 0} calls · ${job.metrics?.inputTokens || 0} input / ${job.metrics?.outputTokens || 0} output tokens</p>
    <pre class="loop-output" data-inspect="latest-input-${escapeHTML(job.id)}">${escapeHTML(job.input)}</pre>
    <div class="loop-actions"><a class="btn btn-ghost btn-small" href="/api/loops/${encodeURIComponent(loopID)}/jobs/${encodeURIComponent(job.id)}/events" download>Raw events ↓</a><a class="btn btn-ghost btn-small" href="/api/loops/${encodeURIComponent(loopID)}/jobs/${encodeURIComponent(job.id)}/stderr" download>Process log ↓</a></div></details></section>`;
}

export function renderLoopDetail(detail, selectedVersion, sourceJob = '') {
  const { loop, versions, jobs = [], rounds = [], feedback = [], projectTasks = [] } = detail;
  const version = selectedLoopVersion(detail, selectedVersion);
  const activity = loopActivity(detail);
  const latestRun = activity.phase === 'running' ? activity.job : jobs.find((job) => job.kind === 'run');
  const inProgress = ['generating', 'running', 'optimizing'].includes(activity.phase);
  return `<div class="loop-detail-heading"><div><h2>${escapeHTML(loop.name)}</h2></div>
    <span class="loop-badge">${version ? `v${version.number}${version.id === loop.activeVersion ? ' · active' : ''}` : 'No version'}</span></div>
    ${renderActivity(activity, version)}
    <section class="loop-panel"><div class="loop-section-heading"><div><h3>Project execution</h3><p class="loop-muted">For file changes, tests and commits. Uses the project’s Board agent.</p></div><button class="btn btn-ghost" data-action="project-task">Run as Board task</button></div>
    ${projectTasks.map((task) => renderProjectTask(task)).join('')}</section>
    ${latestRun ? renderResult(latestRun, loop.id, feedback, sourceJob === latestRun.id) : ''}
    <details class="loop-secondary loop-panel" data-inspect="task"><summary>Task</summary><div class="loop-contract"><section><h3>Task</h3><p>${escapeHTML(loop.description)}</p></section><section><h3>Success criteria</h3><p>${escapeHTML(loop.criteria)}</p></section></div></details>
    ${version ? `<section class="loop-panel"><div class="loop-section-heading"><h3>Optimization</h3><button class="btn btn-primary" data-action="optimize" ${inProgress ? 'disabled' : ''}>Optimize</button></div>
    <p class="loop-hint">Small edits · up to 4 passes · quality checked first · uses provider credits. <button class="btn btn-ghost btn-small" data-action="optimize-settings" ${inProgress ? 'disabled' : ''}>Custom settings</button></p>
    ${rounds.map((round) => `<details class="loop-secondary" data-inspect="experiment-${escapeHTML(round.id)}" ${liveRound(round) || ['blocked', 'exhausted', 'failed'].includes(round.status) ? 'open' : ''}><summary>${escapeHTML(roundLabel(round))}</summary>${renderRound(round, jobs, versions, loop.activeVersion)}</details>`).join('')}</section>` : ''}
    <details class="loop-secondary loop-panel" data-inspect="agent-config"><summary>Agent${version ? ` · v${version.number}` : ''}</summary>
      <div class="loop-section-heading"><p class="loop-muted">${versions.length} ${versions.length === 1 ? 'version' : 'versions'}</p><button class="btn btn-ghost" data-action="import">Import YAML</button></div>
      ${version ? `<div class="loop-version-tabs">${versions.map((item) => `<button class="btn ${item.id === version.id ? 'btn-primary' : 'btn-ghost'}" data-version="${escapeHTML(item.id)}" data-focus="version-tab-${escapeHTML(item.id)}">v${item.number}${item.id === loop.activeVersion ? ' · active' : ''}</button>`).join('')}</div>
      <p class="loop-rationale">${escapeHTML(version.rationale || 'Imported configuration')}</p><pre class="loop-code" data-inspect="config-${escapeHTML(version.id)}"><code>${escapeHTML(version.yaml)}</code></pre>
      <div class="loop-actions"><button class="btn btn-ghost" data-action="promote" ${version.id === loop.activeVersion ? 'disabled' : ''}>${version.id === loop.activeVersion ? '✓ Active' : 'Use this version'}</button><a class="btn btn-ghost" href="/api/loops/${encodeURIComponent(loop.id)}/versions/${encodeURIComponent(version.id)}/export" download="agent.yaml">Export YAML ↓</a></div>` : ''}
    </details>
    <details class="loop-secondary loop-panel" data-inspect="history"><summary>History · ${jobs.length} ${jobs.length === 1 ? 'attempt' : 'attempts'}</summary>
      ${jobs.length ? jobs.map((job) => renderJob(job, loop.id, feedback)).join('') : '<p class="loop-hint">No attempts.</p>'}</details>
    <details class="loop-secondary loop-panel" data-inspect="spend"><summary>Costs</summary>${renderSpend(jobs)}</details>`;
}

export function renderProjectTask(task) {
  const card = task.card;
  const label = !card ? 'Card removed' : task.status !== 'launched' ? task.status === 'reserved' || task.status === 'launch_uncertain' ? 'Launch unconfirmed — inspect before retrying' : 'Launch failed' : ({ starting: 'Starting', running: 'Running', waiting: 'Idle — verify results', paused: 'Paused', error: 'Needs attention' })[card.status] || card.status;
  return `<div class="loop-project-task"><div class="loop-section-heading"><strong>${escapeHTML(task.projectName)}</strong><span class="loop-muted">${escapeHTML(label)}</span></div>
    <p class="loop-project-context">${escapeHTML(task.agent)}${card ? ` · ${escapeHTML(card.branch)}` : ''}</p>
    ${task.error ? `<p class="loop-job-error">${escapeHTML(task.error)}</p>` : ''}
    ${card ? `<div class="loop-actions"><button class="btn btn-ghost btn-small" data-task-terminal="${escapeHTML(card.id)}">Open agent</button><button class="btn btn-ghost btn-small" data-task-diff="${escapeHTML(card.id)}">Diff</button></div>` : ''}</div>`;
}

function formatMetricCost(cost) { return cost == null ? 'Cost unknown' : `$${cost.toFixed(cost < 1 ? 4 : 2)}`; }

export function renderJob(job, loopID, feedback = []) {
  const busy = ['queued', 'running'].includes(job.status);
  const metrics = job.metrics || {};
  return `<details class="loop-job" ${busy || job.status === 'failed' ? 'open' : ''} data-job="${escapeHTML(job.id)}" data-inspect="job-${escapeHTML(job.id)}"><summary>
    <span class="loop-status ${escapeHTML(job.status)}">${escapeHTML(job.status)}</span><strong>${({ generate: '✦ Agent design', proposal: '✦ Candidate design', capability: '◇ Capability check', test: '▷ Evaluation run', judge: '◇ Quality judge' })[job.kind] || '▷ Agent run'}</strong>
    <span class="loop-job-metrics">${formatMetricCost(metrics.cost)} · ${((metrics.durationMs || 0) / 1000).toFixed(1)}s</span></summary>
    ${job.project ? `<p class="loop-project-context"><strong>${escapeHTML(job.project.name)}</strong> · ${escapeHTML(job.project.revision.slice(0, 8))}<br><span>${escapeHTML(job.project.path)} · ${job.project.files.length} files included · ${job.project.omitted} omitted</span></p>` : ''}
    ${job.error ? `<p class="loop-job-error">${escapeHTML(job.error)}</p>` : ''}
    <div class="loop-job-facts"><span>${escapeHTML(metrics.model || 'Model pending')}</span><span>${metrics.calls || 0} calls</span><span>${metrics.inputTokens || 0} in / ${metrics.outputTokens || 0} out</span></div>
    ${job.kind !== 'generate' ? `<details data-inspect="input-${escapeHTML(job.id)}"><summary>Input</summary><pre class="loop-output" data-inspect="input-text-${escapeHTML(job.id)}">${escapeHTML(job.input)}</pre></details>` : ''}
    <pre class="loop-output" data-inspect="output-${escapeHTML(job.id)}">${escapeHTML(job.output || (busy ? 'Waiting for the agent…' : 'No output.'))}</pre>
    <div class="loop-actions"><a class="btn btn-ghost btn-small" href="/api/loops/${encodeURIComponent(loopID)}/jobs/${encodeURIComponent(job.id)}/events" download>Raw events ↓</a>
    <a class="btn btn-ghost btn-small" href="/api/loops/${encodeURIComponent(loopID)}/jobs/${encodeURIComponent(job.id)}/stderr" download>Process log ↓</a>
    ${!busy && ['run', 'test'].includes(job.kind) ? `<button class="btn btn-ghost btn-small" data-feedback="${escapeHTML(job.id)}">＋ Feedback</button>` : ''}
    ${busy ? `<button class="btn btn-ghost btn-small" data-cancel="${escapeHTML(job.id)}">Cancel</button>` : ''}</div>
    ${feedback.filter((item) => item.jobId === job.id).map((item) => `<p class="loop-feedback-note">${item.rating === 'good' ? '✓' : '✕'} ${escapeHTML(item.note || item.rating)}</p>`).join('')}</details>`;
}

export function summarizeCandidate(round, candidate, jobs) {
  const expected = round.inputs.length * round.repeats;
  const keys = new Set(candidate.trials.map((trial) => `${trial.case}/${trial.repeat}`));
  const complete = !candidate.error && expected > 0 && candidate.trials.length === expected && keys.size === expected
    && candidate.trials.every((trial) => {
      const run = jobs.find((job) => job.id === trial.jobId);
      const judge = jobs.find((job) => job.id === trial.judgeId);
      return Number.isInteger(trial.case) && trial.case >= 0 && trial.case < round.inputs.length
        && Number.isInteger(trial.repeat) && trial.repeat >= 0 && trial.repeat < round.repeats
        && run?.status === 'succeeded' && run.kind === 'test' && run.versionId === candidate.versionId
        && judge?.status === 'succeeded' && judge.kind === 'judge' && judge.versionId === candidate.versionId;
    });
  const runs = candidate.trials.map((trial) => jobs.find((job) => job.id === trial.jobId));
  const priced = complete && runs.every((job) => job?.metrics?.cost != null);
  const passed = candidate.trials.filter((trial) => trial.passed).length;
  const confirmationRequired = round.maxPasses && candidate.pass > 0;
  const confirmation = candidate.confirmation || [];
  const confirmed = !confirmationRequired || (confirmation.length === expected
    && new Set(confirmation.map((trial) => `${trial.case}/${trial.repeat}`)).size === expected
    && confirmation.every((trial) => trial.passed
      && trial.case >= 0 && trial.case < round.inputs.length && trial.repeat >= 0 && trial.repeat < round.repeats
      && jobs.some((job) => job.id === trial.jobId && job.status === 'succeeded' && job.kind === 'test' && job.versionId === candidate.versionId)
      && jobs.some((job) => job.id === trial.judgeId && job.status === 'succeeded' && job.kind === 'judge' && job.versionId === candidate.versionId)));
  return { expected, passed, qualified: complete && passed === expected && confirmed,
    cost: priced ? runs.reduce((sum, job) => sum + job.metrics.cost, 0) / expected : null,
    duration: complete && runs.every((job) => job?.status === 'succeeded' && Number.isFinite(job.metrics?.durationMs)) ? runs.reduce((sum, job) => sum + job.metrics.durationMs, 0) / expected : null };
}

export function metricDelta(value, baseline, dimension) {
  if (!Number.isFinite(value) || value < 0 || !Number.isFinite(baseline) || baseline <= 0) return '';
  const change = Math.round((value / baseline - 1) * 100);
  if (change === 0) return 'Similar to baseline';
  const direction = dimension === 'cost' ? (change < 0 ? 'lower cost' : 'higher cost') : (change < 0 ? 'faster' : 'slower');
  return `${Math.abs(change)}% ${direction}`;
}

export function roundLabel(round) {
  if (round.status === 'blocked') return 'Blocked: runtime cannot meet the requirements';
  if (round.status === 'exhausted') return 'Requirements not met within the attempt limit';
  if (round.outcome === 'improved') return 'Passing improvement found';
  if (round.outcome === 'unchanged') return 'Kept the passing baseline';
  if (liveRound(round)) return round.pass ? `Optimizing · pass ${round.pass}/${round.maxPasses || 4}` : 'Checking baseline';
  return `Experiment · ${round.status}`;
}

export function renderRound(round, jobs, versions, active) {
  const busy = ['designing', 'testing'].includes(round.status);
  const baseline = round.candidates.find((candidate) => candidate.versionId === round.baseline) || round.candidates[0];
  const baseMetrics = baseline ? summarizeCandidate(round, baseline, jobs) : null;
  return `<div class="loop-experiment"><div class="loop-section-heading"><h4>${escapeHTML(roundLabel(round))}</h4>
    ${busy ? `<button class="btn btn-ghost btn-small" data-cancel-round="${escapeHTML(round.id)}">Stop</button>` : ''}</div>
    ${round.project ? `<p class="loop-project-context"><strong>${escapeHTML(round.project.name)}</strong> · ${escapeHTML(round.project.revision.slice(0, 8))} · shared snapshot</p>` : ''}
    <p class="loop-hint">${round.inputs.length} inputs × ${round.repeats} repetitions. AI-judged; not a guarantee. Winners get fresh confirmation runs. Cost and time are exploratory measurements.</p>
    ${round.error ? `<p class="loop-job-error">${escapeHTML(round.error)}</p>` : ''}
    ${round.winner ? `<p class="loop-hint">${round.outcome === 'unchanged' ? 'Retained' : 'Recommended'}: v${versions.find((item) => item.id === round.winner)?.number || '?'} · active version unchanged</p>` : ''}
    <div class="loop-table-scroll"><table class="loop-comparison"><thead><tr><th>Version</th><th>Quality checks</th><th>Cost / run</th><th>Time / run</th><th></th></tr></thead><tbody>
    ${round.candidates.map((candidate) => {
      const summary = summarizeCandidate(round, candidate, jobs);
      const version = versions.find((item) => item.id === candidate.versionId);
      const comparable = round.status === 'completed' && summary.qualified && baseMetrics?.qualified && candidate !== baseline;
      const costDelta = comparable ? metricDelta(summary.cost, baseMetrics.cost, 'cost') : '';
      const timeDelta = comparable ? metricDelta(summary.duration, baseMetrics.duration, 'duration') : '';
      const result = candidate.error ? 'Design failed' : summary.qualified ? 'All checks passed' : candidate.trials.length < summary.expected ? 'Incomplete evidence' : round.maxPasses && summary.passed === summary.expected ? 'Confirmation required or failed' : 'Quality gate not met';
      return `<tr><td><strong>${escapeHTML(candidate.label)}</strong><small>${version ? `v${version.number}` : candidate.error ? 'No version created' : 'Pending'}</small>${candidate.decision ? `<small>${escapeHTML(candidate.decision)}</small>` : ''}</td>
      <td><span class="${summary.qualified ? 'loop-good' : ''}">${summary.passed} / ${summary.expected}</span><small>${result}</small>${candidate.error ? `<small>${escapeHTML(candidate.error)}</small>` : ''}</td>
      <td>${formatMetricCost(summary.cost)}${costDelta ? `<small>${costDelta}</small>` : ''}</td><td>${summary.duration == null ? '—' : `${(summary.duration / 1000).toFixed(1)}s`}${timeDelta ? `<small>${timeDelta}</small>` : ''}</td>
      <td>${version ? `<button class="btn btn-ghost btn-small" data-version="${escapeHTML(version.id)}" data-focus="inspect-${escapeHTML(round.id)}-${escapeHTML(version.id)}">Inspect</button><button class="btn btn-ghost btn-small" data-promote="${escapeHTML(version.id)}" data-focus="promote-${escapeHTML(round.id)}-${escapeHTML(version.id)}" ${!summary.qualified || round.status !== 'completed' || (round.maxPasses && round.winner !== version.id) || active === version.id ? 'disabled' : ''}>${active === version.id ? '✓ Active' : 'Use version'}</button>` : ''}</td></tr>`;
    }).join('')}</tbody></table></div>
    <details class="loop-verdicts" data-inspect="verdicts-${escapeHTML(round.id)}"><summary>Quality evidence</summary>${round.candidates.map((candidate) => `<h4>${escapeHTML(candidate.label)}</h4>${candidate.trials.map((trial) => `<p><span class="${trial.passed ? 'loop-good' : 'loop-bad'}">${trial.passed ? '✓' : '✕'}</span> Input ${trial.case + 1}, repeat ${trial.repeat + 1}: ${escapeHTML(trial.reason)}</p>`).join('')}${(candidate.confirmation || []).map((trial) => `<p><span class="${trial.passed ? 'loop-good' : 'loop-bad'}">${trial.passed ? '✓' : '✕'}</span> Confirmation · input ${trial.case + 1}, repeat ${trial.repeat + 1}: ${escapeHTML(trial.reason)}</p>`).join('')}`).join('')}</details>
    </div>`;
}

const inspectorRenders = new WeakMap();

function focusIdentity(element) {
  if (!element) return '';
  if (element.dataset.focus) return `control:${element.dataset.focus}`;
  if (element.tagName === 'SUMMARY') return `summary:${element.parentElement.dataset.inspect || ''}`;
  for (const key of ['version', 'action', 'promote', 'cancel', 'feedback', 'cancelRound', 'source', 'copyResult']) {
    if (element.dataset[key]) return `${key}:${element.dataset[key]}`;
  }
  return element.getAttribute('href') || '';
}

export function updateLoopInspector(container, detail, selectedVersion, sourceJob = '') {
  const html = renderLoopDetail(detail, selectedVersion, sourceJob);
  const previous = inspectorRenders.get(container);
  if (previous?.loop === detail.loop.id && previous.html === html) return;
  const sameLoop = previous?.loop === detail.loop.id;
  const positions = new Map();
  if (sameLoop) {
    for (const element of container.querySelectorAll('[data-inspect]')) {
      positions.set(element.dataset.inspect, { open: element.open, top: element.scrollTop, left: element.scrollLeft });
    }
  }
  const activeElement = container.ownerDocument.activeElement;
  const focused = sameLoop && container.contains(activeElement) ? focusIdentity(activeElement) : '';
  const scroll = sameLoop ? container.scrollTop : 0;
  // Reuse unchanged rich content; keep code and table scrolling across updates.
  const body = container.querySelector('[data-result-body]');
  const latest = detail.jobs?.find((job) => job.id === body?.dataset.resultBody);
  const keepBody = sameLoop && body && latest?.output && previous.output === latest.output && previous.sourceJob === sourceJob;
  const bodyScroll = body ? [...body.querySelectorAll('pre, table')].map((element) => ({ top: element.scrollTop, left: element.scrollLeft })) : [];
  const parked = keepBody ? body.ownerDocument.createElement('div') : null;
  if (parked) {
    parked.append(...body.childNodes);
  }
  container.innerHTML = html;
  const nextBody = container.querySelector('[data-result-body]');
  if (nextBody && body?.dataset.resultBody === nextBody.dataset.resultBody) {
    if (parked) nextBody.replaceChildren(...parked.childNodes);
    [...nextBody.querySelectorAll('pre, table')].forEach((element, index) => {
      const saved = bodyScroll[index];
      if (saved) { element.scrollTop = saved.top; element.scrollLeft = saved.left; }
    });
  }
  inspectorRenders.set(container, { loop: detail.loop.id, html, sourceJob, output: detail.jobs?.find((job) => job.id === nextBody?.dataset.resultBody)?.output });
  for (const element of container.querySelectorAll('[data-inspect]')) {
    const saved = positions.get(element.dataset.inspect);
    if (!saved) continue;
    if (element.tagName === 'DETAILS') element.open = saved.open;
    element.scrollTop = saved.top;
    element.scrollLeft = saved.left;
  }
  if (focused) {
    const target = [...container.querySelectorAll('button, a, summary')].find((element) => focusIdentity(element) === focused);
    target?.focus({ preventScroll: true });
  }
  container.scrollTop = scroll;
}

export function initLoops(api, boardActions = {}) {
  const byID = (id) => document.getElementById(id);
  let detail = null;
  let selectedVersion = '';
  let sequence = 0;
  let refreshTimer = null;
  let feedbackJob = '';
  let optimizeVersion = '';
  let runVersion = '';
  let modalLoop = '';
  let importLoop = '';
  let sourceJob = '';
  let taskLoop = '';
  let taskRequestID = '';
  let taskProjects = [];
  let taskLoading = 0;
  let taskSubmitting = false;
  const projectLoads = new Map();
  const startingOptimizations = new Set();

  function notice(message = '', error = false) {
    byID('loops-notice').textContent = message;
    byID('loops-notice').classList.toggle('error', error);
  }

  async function loadProjects(selectID, statusID, preferred) {
    const request = (projectLoads.get(selectID) || 0) + 1;
    projectLoads.set(selectID, request);
    const select = byID(selectID), status = byID(statusID);
    select.disabled = true;
    select.innerHTML = '<option value="">Loading projects…</option>';
    status.textContent = '';
    try {
      const projects = await api('/projects');
      if (projectLoads.get(selectID) !== request) return;
      select.innerHTML = '<option value="">Choose a project…</option>' + projects.map((project) => `<option value="${escapeHTML(project.id)}">${escapeHTML(project.name)} — ${escapeHTML(project.repoPath)}</option>`).join('');
      select.value = projects.some((project) => project.id === preferred) ? preferred : '';
      status.textContent = projects.length ? '' : 'No projects configured. Add one in Board → Projects.';
      select.disabled = projects.length === 0;
    } catch (error) {
      if (projectLoads.get(selectID) !== request) return;
      select.innerHTML = '<option value="">Projects unavailable</option>';
      status.textContent = error.message;
    }
  }

  function preferredProject() {
    return detail?.jobs?.find((job) => job.project?.id)?.project.id || detail?.loop.projectId || '';
  }

  async function refreshLoops() {
    if (!location.hash.startsWith('#loops')) return;
    clearTimeout(refreshTimer);
    const request = ++sequence;
    const id = location.hash.split('/')[1] || '';
    try {
      const [list, nextDetail, projectTasks] = await Promise.all([api('/loops'), id ? api(`/loops/${encodeURIComponent(id)}`) : null, id ? api(`/loops/${encodeURIComponent(id)}/tasks`) : []]);
      if (nextDetail) nextDetail.projectTasks = projectTasks;
      if (request !== sequence) return;
      detail = nextDetail;
      if (detail) selectedVersion = selectedLoopVersion(detail, selectedVersion)?.id || '';
      byID('loops-list').innerHTML = renderLoopList(list, id, detail);
      byID('loops-count').textContent = `${list.length} ${list.length === 1 ? 'loop' : 'loops'}`;
      byID('loop-detail').hidden = !detail;
      byID('loops-welcome').hidden = !!detail;
      byID('loops-welcome-title').textContent = list.length ? 'Select a loop' : 'No loops yet';
      if (detail) {
        updateLoopInspector(byID('loop-detail'), detail, selectedVersion, sourceJob);
        if ((detail.projectTasks || []).some((task) => task.card && ['starting', 'running', 'paused'].includes(task.card.status)) || (detail.jobs || []).some((job) => ['queued', 'running'].includes(job.status)) || (detail.rounds || []).some((round) => ['designing', 'testing'].includes(round.status))) refreshTimer = setTimeout(refreshLoops, 1000);
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
    const projectTask = event.target.closest('[data-action="project-task"]');
    if (projectTask && detail) {
      if (taskSubmitting) return;
      taskLoop = detail.loop.id;
      taskRequestID = crypto.randomUUID();
      byID('loop-task-consent').checked = false;
      taskProjects = [];
      byID('loop-task-agent').textContent = '';
      byID('loop-project-task-dialog').showModal();
      const request = ++taskLoading;
      const preferred = preferredProject();
      const select = byID('loop-task-project');
      select.disabled = true;
      select.innerHTML = '<option value="">Loading projects…</option>';
      try {
        const projects = await api('/projects');
        if (request !== taskLoading) return;
        taskProjects = projects;
        select.innerHTML = '<option value="">Choose a project…</option>' + projects.map((project) => `<option value="${escapeHTML(project.id)}">${escapeHTML(project.name)}</option>`).join('');
        select.value = projects.some((project) => project.id === preferred) ? preferred : '';
        select.disabled = false;
        byID('loop-task-project-status').textContent = projects.length ? '' : 'Add a project in Board → Projects.';
        showTaskAgent();
      } catch (error) { if (request === taskLoading) byID('loop-task-project-status').textContent = error.message; }
      return;
    }
    const terminal = event.target.closest('[data-task-terminal]');
    const diff = event.target.closest('[data-task-diff]');
    if ((terminal || diff) && detail) {
      const id = terminal?.dataset.taskTerminal || diff.dataset.taskDiff;
      const task = detail.projectTasks?.find((item) => item.card?.id === id);
      if (!task) return;
      try { await (terminal ? boardActions.openAgent?.(task.card) : boardActions.openDiff?.(task.card)); }
      catch (error) { notice(error.message, true); }
      return;
    }
    const source = event.target.closest('[data-source]');
    if (source && detail) {
      sourceJob = sourceJob === source.dataset.source ? '' : source.dataset.source;
      updateLoopInspector(byID('loop-detail'), detail, selectedVersion, sourceJob);
      return;
    }
    const copy = event.target.closest('[data-copy-result]');
    if (copy && detail) {
      const job = detail.jobs?.find((item) => item.id === copy.dataset.copyResult);
      if (!job || copy.disabled) return;
      const loopID = detail.loop.id;
      copy.disabled = true;
      try {
        await navigator.clipboard.writeText(job.output || '');
        if (detail?.loop.id === loopID) notice('Result copied.');
      } catch {
        if (detail?.loop.id === loopID) notice('Could not copy. Select the text in Source view instead.', true);
      } finally { copy.disabled = false; }
      return;
    }
    const version = event.target.closest('[data-version]');
    if (version && detail) {
      selectedVersion = version.dataset.version;
      updateLoopInspector(byID('loop-detail'), detail, selectedVersion, sourceJob);
      const config = byID('loop-detail').querySelector('[data-inspect="agent-config"]');
      if (config) {
        config.open = true;
        config.scrollIntoView({ block: 'nearest' });
      }
    }
    if (event.target.closest('[data-action="import"]') && detail) {
      importLoop = detail.loop.id;
      byID('loop-import-dialog').showModal();
    }
    if (event.target.closest('[data-action="run"]') && detail) {
      modalLoop = detail.loop.id;
      runVersion = selectedLoopVersion(detail, selectedVersion)?.id;
      byID('loop-run-input').value = '';
      byID('loop-run-dialog').showModal();
      loadProjects('loop-run-project', 'loop-run-project-status', preferredProject());
    }
    const optimize = event.target.closest('[data-action="optimize"]');
    if (optimize && detail) {
      const id = detail.loop.id;
      const version = selectedLoopVersion(detail, selectedVersion)?.id;
      if (!version || startingOptimizations.has(id)) return;
      startingOptimizations.add(id);
      optimize.disabled = true;
      optimize.textContent = 'Starting optimization…';
      notice('Starting optimization…');
      try {
        await api(`/loops/${id}/optimize/default`, { method: 'POST', body: JSON.stringify({ version }) });
        if (detail?.loop.id === id) notice();
        await refreshLoops();
      } catch (error) {
        if (detail?.loop.id === id) notice(error.message, true);
      } finally {
        startingOptimizations.delete(id);
        optimize.disabled = false;
        optimize.textContent = 'Optimize';
      }
      return;
    }
    if (event.target.closest('[data-action="optimize-settings"]') && detail) {
      modalLoop = detail.loop.id;
      optimizeVersion = selectedLoopVersion(detail, selectedVersion)?.id;
      byID('loop-test-inputs').value = detail.loop.description;
      byID('loop-optimize-dialog').showModal();
      loadProjects('loop-test-project', 'loop-test-project-status', preferredProject());
    }
    const feedback = event.target.closest('[data-feedback]');
    if (feedback && detail) {
      modalLoop = detail.loop.id;
      feedbackJob = feedback.dataset.feedback;
      byID('loop-feedback-dialog').showModal();
    }
    const promote = event.target.closest('[data-promote], [data-action="promote"]');
    if (promote && detail) {
      const version = promote.dataset.promote || selectedLoopVersion(detail, selectedVersion)?.id;
      if (!version || !window.confirm('Use this version? The previous version will remain available.')) return;
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
    loadProjects('loop-first-project', 'loop-first-project-status', '');
  });
  async function submit(form, action) {
    const button = form.querySelector('[type="submit"]');
    if (button.disabled) return;
    button.disabled = true;
    const originalLabel = button.textContent;
    if (form.id === 'loop-new-form') button.textContent = 'Starting agent creation…';
    const status = form.querySelector('[role="status"]');
    status.textContent = '';
    try { await action(); form.closest('dialog').close(); form.reset(); notice(); }
    catch (error) { status.textContent = error.message; }
    finally { button.disabled = false; button.textContent = originalLabel; }
  }
  byID('loop-new-form').addEventListener('submit', (event) => {
    event.preventDefault();
    submit(event.target, async () => {
      const loop = await api('/loops', { method: 'POST', body: JSON.stringify({
        name: byID('loop-name').value, description: byID('loop-task').value,
        criteria: byID('loop-criteria').value, projectId: byID('loop-first-project').value,
      }) });
      selectedVersion = '';
      location.hash = `loops/${loop.id}`;
    });
  });
  byID('loop-import-form').addEventListener('submit', (event) => {
    event.preventDefault();
    const id = importLoop;
    if (!id) return;
    submit(event.target, async () => {
      const version = await api(`/loops/${id}/versions`, { method: 'POST', body: JSON.stringify({
        yaml: byID('loop-yaml').value, rationale: byID('loop-rationale').value,
      }) });
      if (detail?.loop.id === id) selectedVersion = version.id;
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
      await api(`/loops/${id}/runs`, { method: 'POST', body: JSON.stringify({ version, projectId: byID('loop-run-project').value, input: byID('loop-run-input').value }) });
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
      await api(`/loops/${id}/optimize`, { method: 'POST', body: JSON.stringify({ version, projectId: byID('loop-test-project').value,
        inputs: byID('loop-test-inputs').value.split(/\n---\n/).map((text) => text.trim()).filter(Boolean),
        repeats: Number(byID('loop-test-repeats').value),
        models: [byID('loop-model-one').value.trim(), byID('loop-model-two').value.trim()].some(Boolean) ? [byID('loop-model-one').value.trim(), byID('loop-model-two').value.trim()] : [],
      }) });
      await refreshLoops();
    });
  });
  function showTaskAgent() {
    const project = taskProjects.find((item) => item.id === byID('loop-task-project').value);
    byID('loop-task-agent').textContent = project ? `${project.agent} · ${project.repoPath}` : '';
    byID('loop-task-consent').checked = false;
  }
  byID('loop-task-project').addEventListener('change', () => {
    if (taskSubmitting) return;
    taskRequestID = crypto.randomUUID();
    showTaskAgent();
  });
  byID('loop-project-task-form').addEventListener('submit', (event) => {
    event.preventDefault();
    if (taskSubmitting) return;
    const project = taskProjects.find((item) => item.id === byID('loop-task-project').value);
    if (!project || !byID('loop-task-consent').checked) return;
    const loopID = taskLoop, requestId = taskRequestID;
    taskSubmitting = true;
    byID('loop-task-project').disabled = true;
    submit(event.target, async () => {
      try {
        await api(`/loops/${loopID}/tasks`, { method: 'POST', body: JSON.stringify({ requestId, projectId: project.id, repoPath: project.repoPath, agent: project.agent, confirmed: true }) });
        await refreshLoops();
      } finally { taskSubmitting = false; byID('loop-task-project').disabled = false; }
    });
  });
  window.addEventListener('hashchange', route);
  window.addEventListener('board-refresh', refreshLoops);
  route();
}
