import { renderResultMarkdown, previewLimit } from './result-markdown.js';

const escapeHTML = (value) => String(value ?? '').replace(/[&<>"']/g, (char) => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
})[char]);

export function renderLoopList(loops, selected, detail) {
  if (!loops.length) return '';
  return loops.map((loop) => {
    const activity = detail?.loop.id === loop.id ? loopActivity(detail) : null;
    const label = activity?.title || (loop.activeVersion ? 'Active agent' : 'Open loop');
    const running = activity && ['generating', 'running', 'optimizing', 'project-running'].includes(activity.phase);
    return `<button class="loop-list-item ${loop.id === selected ? 'selected' : ''}" data-loop="${escapeHTML(loop.id)}" aria-pressed="${loop.id === selected}">
    <span class="loop-list-icon ${running ? 'spinning' : ''}" aria-hidden="true">↻</span><span><strong>${escapeHTML(loop.name)}</strong>
    <small>${escapeHTML(label)}</small></span><span aria-hidden="true">›</span></button>`;
  }).join('');
}

export function selectedLoopVersion(detail, selectedVersion) {
  const mode = detail.loop.mode || 'analysis';
  const versions = detail.versions.filter((item) => (item.mode || 'analysis') === mode);
  return versions.find((item) => item.id === selectedVersion)
    || versions.find((item) => item.id === detail.loop.activeVersion)
    || versions.at(-1);
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
  const jobs = (detail.jobs || []).filter((job) => !job.closedAt);
  const rounds = detail.rounds || [];
  const generation = jobs.find((job) => job.kind === 'generate' && busyJob(job));
  if (generation) return { phase: 'generating', title: generation.status === 'queued' ? 'Agent creation queued' : 'Creating agent', note: generation.status === 'queued' ? 'Waiting for a worker.' : '', job: generation };
  const round = rounds.find(liveRound);
  if (round) return { phase: 'optimizing', title: 'Optimizing', note: [round.pass ? `Pass ${round.pass}/${round.maxPasses || 4}` : '', round.stage || (round.status === 'designing' ? 'Designing candidates' : 'Evaluating candidates')].filter(Boolean).join(' · '), round };
  const run = jobs.find((job) => job.kind === 'run' && busyJob(job));
  if (run) return { phase: 'running', title: run.status === 'queued' ? 'Run queued' : 'Agent running', note: run.project?.name || '', job: run };
  const task = detail.projectTasks?.find((item) => item.status !== 'closed' && item.card && ['starting','running','paused'].includes(item.card.status));
  if(task) return {phase:'project-running',title:task.card.status==='paused'?'Project run paused':'Project run active',note:task.projectName,task};
  const latest = jobs.find((job) => job.kind === 'generate' || job.kind === 'run');
  if (latest && ['failed', 'canceled', 'interrupted'].includes(latest.status)) return { phase: 'failed', title: latest.status === 'failed' ? (latest.kind === 'generate' ? 'Agent creation failed' : 'Run failed') : (latest.kind === 'generate' ? 'Agent creation stopped' : 'Run stopped'), note: latest.error || 'Attempt incomplete.', job: latest };
  if (selectedLoopVersion(detail)) return { phase: 'ready', title: latest?.kind === 'run' ? 'Run complete' : 'Ready', note: '' };
  return { phase: 'empty', title: 'Generate a workflow for this execution type', note: '' };
}

function renderActivity(activity, version) {
  let action = '';
  if (activity.job && busyJob(activity.job)) action = `<button class="btn btn-ghost" data-cancel="${escapeHTML(activity.job.id)}">Stop</button>`;
  else if (activity.task) action = `<button class="btn btn-ghost" data-task-terminal="${escapeHTML(activity.task.card.id)}">Open agent</button>`;
  else if (activity.round) action = `<button class="btn btn-ghost" data-cancel-round="${escapeHTML(activity.round.id)}">Stop</button>`;
  else if (!version) action = `<button class="btn btn-primary" data-action="generate">${activity.phase === 'failed' ? 'Retry creation' : 'Create agent'}</button>`;
  else action = '';
  const running = ['generating', 'running', 'optimizing', 'project-running'].includes(activity.phase);
  return `<section class="loop-activity ${activity.phase}" aria-label="Current activity"><div class="loop-activity-mark ${running ? 'spinning' : ''}" aria-hidden="true">${running ? '↻' : activity.phase === 'failed' ? '!' : activity.phase === 'empty' ? '○' : '✓'}</div>
    <div class="loop-activity-copy"><h3 role="status" aria-live="polite">${escapeHTML(activity.title)}</h3>${activity.note ? `<p>${escapeHTML(activity.note)}</p>` : ''}</div>${action}</section>`;
}

function renderResult(job, loopID, feedback, sourceView = false) {
  const failed = ['failed', 'canceled', 'interrupted'].includes(job.status);
  const title = busyJob(job) ? 'Live output' : failed ? 'Incomplete result' : 'Result';
  const output = job.output || '';
  const markdown = sourceView ? null : renderResultMarkdown(output);
  const truncated = output.length > previewLimit;
  const content = !output ? `<p class="loop-muted">${busyJob(job) ? 'Waiting for the first response…' : 'No output.'}</p>`
    : sourceView ? `<pre class="loop-source-text">${escapeHTML(output.slice(0, previewLimit))}</pre>` : markdown.html;
  return `<section class="loop-panel loop-result"><div class="loop-section-heading"><h3>${title}</h3><span class="loop-muted">${formatMetricCost(job.metrics?.cost)} · ${((job.metrics?.durationMs || 0) / 1000).toFixed(1)}s</span></div>
    <p class="loop-result-status"><span class="loop-status ${escapeHTML(job.status)}">${escapeHTML(job.status)}</span>${failed ? ' Partial output' : ''}</p>
    ${job.error ? `<p class="loop-job-error">${escapeHTML(job.error)}</p>` : ''}
    ${job.project ? `<p class="loop-project-context">${escapeHTML(job.project.name)} · ${escapeHTML(job.project.revision.slice(0, 8))}</p>` : ''}
    <div class="loop-result-toolbar">${job.closedAt ? '<span class="loop-muted">Closed</span>' : `<button class="btn btn-ghost btn-small" data-close-run="${escapeHTML(job.id)}" title="Close run and keep its results">Close</button>`}<button class="btn btn-ghost btn-small" data-source="${escapeHTML(job.id)}" aria-pressed="${sourceView}" ${!output ? 'disabled' : ''}>${sourceView ? 'Rendered' : 'Source'}</button><button class="btn btn-ghost btn-small" data-copy-result="${escapeHTML(job.id)}" ${!output ? 'disabled' : ''}>Copy</button></div>
    <div class="loop-prose" data-result-body="${escapeHTML(job.id)}" data-inspect="latest-output-${escapeHTML(job.id)}">${content}</div>
    ${truncated ? '<p class="loop-hint">Preview limited to 64K characters. Copy includes the full result.</p>' : ''}
    ${markdown?.tablesAsText ? '<p class="loop-hint">Large tables shown as text.</p>' : ''}
    ${!busyJob(job) ? `<button class="btn btn-ghost" data-feedback="${escapeHTML(job.id)}">Feedback</button>` : ''}
    ${feedback.filter((item) => item.jobId === job.id).map((item) => `<p class="loop-feedback-note">${item.rating === 'good' ? '✓' : '✕'} ${escapeHTML(item.note || item.rating)}</p>`).join('')}
    <details class="loop-secondary" data-inspect="result-details-${escapeHTML(job.id)}"><summary>Run details & logs</summary><p class="loop-muted">${escapeHTML(job.metrics?.model || 'Model not reported')} · ${job.metrics?.calls || 0} calls · ${job.metrics?.inputTokens || 0} input / ${job.metrics?.outputTokens || 0} output tokens</p>
    <pre class="loop-output" data-inspect="latest-input-${escapeHTML(job.id)}">${escapeHTML(job.input)}</pre>
    <div class="loop-actions"><a class="btn btn-ghost btn-small" href="/api/loops/${encodeURIComponent(loopID)}/jobs/${encodeURIComponent(job.id)}/events" download>Raw events ↓</a><a class="btn btn-ghost btn-small" href="/api/loops/${encodeURIComponent(loopID)}/jobs/${encodeURIComponent(job.id)}/stderr" download>Process log ↓</a></div></details></section>`;
}

export function renderLoopDetail(detail, selectedVersion, sourceJob = '', panel = 'runs', selectedRun = '') {
  const { loop, versions, jobs = [], rounds = [], feedback = [], projectTasks = [] } = detail;
  const version = selectedLoopVersion(detail, selectedVersion);
  const projectMode = loop.mode === 'project';
  const activity = loopActivity(detail);
  const inProgress = ['generating', 'running', 'optimizing', 'project-running'].includes(activity.phase);
  const blocked = !projectMode && rounds.find((round) => round.status === 'blocked' && round.outcome === 'capability_blocked');
  const runs = [...jobs.filter((job) => job.kind === 'run').map((job) => ({id: job.id, type: 'analysis', time: job.createdAt || '', job})),
    ...projectTasks.map((task) => ({id: task.id, type: 'project', time: task.createdAt || '', task}))].sort((a,b) => b.time.localeCompare(a.time));
  const selected = runs.find((run) => run.id === selectedRun) || runs.find((run) => run.type==='analysis' && busyJob(run.job) || run.type==='project' && ['starting','running','paused'].includes(run.task.card?.status)) || runs.find((run) => run.type === 'analysis' ? !run.job.closedAt : run.task.status !== 'closed') || runs[0];
  const runList = `<div class="loop-run-list" aria-label="Run history">${runs.map((run) => {
    const status = run.type === 'analysis' ? run.job.closedAt ? 'closed' : run.job.status : run.task.status === 'closed' ? 'closed' : run.task.card?.status || run.task.status;
    const title = run.type === 'analysis' ? run.job.project?.name || 'Analysis' : run.task.projectName;
    return `<button class="loop-run-row ${selected?.id === run.id ? 'selected' : ''}" data-select-run="${escapeHTML(run.id)}" aria-pressed="${selected?.id === run.id}"><span><strong>${escapeHTML(title)}</strong><small>${run.type === 'analysis' ? 'Analysis' : 'Project work'} · ${escapeHTML(run.time ? new Date(run.time).toLocaleString() : '')}</small></span><span class="loop-status ${escapeHTML(status)}">${escapeHTML(status)}</span></button>`;
  }).join('')}</div>`;
  let result = '<div class="loop-empty-inline">No runs yet.</div>';
  if (selected?.type === 'project') result = renderProjectTask(selected.task);
  else if (selected?.job) result = (detail.loadedJob?.id === selected.id || selected.job.output !== undefined && selected.job.outputSize === undefined) ? renderResult(detail.loadedJob?.id===selected.id?detail.loadedJob:selected.job, loop.id, feedback, sourceJob === selected.id) : '<div class="loop-empty-inline">Loading result…</div>';
  return `<div class="loop-detail-heading"><div><h2>${escapeHTML(loop.name)}</h2><p class="loop-mode-label">${projectMode ? 'Project work · configured agent + versioned workflow' : 'Analysis · read-only project snapshot'}</p></div><div class="loop-actions"><span class="loop-badge">${version ? `v${version.number}` : 'No version'}</span>${version ? `<button class="btn btn-primary" data-action="${projectMode ? 'project-task' : 'run'}" ${inProgress || blocked ? 'disabled' : ''}>Run</button>` : ''}</div></div>
    ${inProgress || activity.phase === 'failed' || !version ? renderActivity(activity, version) : ''}
    ${blocked && !inProgress ? `<section class="loop-panel loop-recovery"><div class="loop-section-heading"><div><h3>This task needs project work</h3><p class="loop-muted">Read-only analysis cannot satisfy its requirements.</p></div><button class="btn btn-primary" data-mode="project">Switch to project work</button></div><details class="loop-secondary" data-inspect="capability-reason"><summary>Why it is blocked</summary><p class="loop-example">${escapeHTML(blocked.error)}</p></details></section>` : ''}
    <nav class="loop-tabs aria-label="Loop views">${[['runs','Runs'],['agent','Agent'],['optimize','Optimize'],['settings','Settings']].map(([id,label])=>`<button class="${panel===id?'selected':''}" data-loop-panel="${id}" aria-pressed="${panel===id}">${label}</button>`).join('')}</nav>
    ${panel === 'runs' ? `${runList}${result}` : ''}
    ${panel === 'agent' ? `<section class="loop-panel"><div class="loop-section-heading"><h3>${projectMode?'Workflow instruction':'Agent configuration'}</h3><button class="btn btn-ghost" data-action="generate" ${inProgress?'disabled':''}>Generate new version</button></div><div class="loop-version-tabs">${versions.filter((item)=>(item.mode||'analysis')===(loop.mode||'analysis')).map((item)=>`<button class="btn ${item.id===version?.id?'btn-primary':'btn-ghost'}" data-version="${escapeHTML(item.id)}" data-focus="version-tab-${escapeHTML(item.id)}">v${item.number}${item.id===loop.activeVersion?' · active':''}</button>`).join('')}</div>${version?`<p class="loop-rationale">${escapeHTML(version.rationale)}</p><pre class="loop-code" data-inspect="config-${escapeHTML(version.id)}"><code>${escapeHTML(version.yaml)}</code></pre><div class="loop-actions"><button class="btn btn-ghost" data-action="promote">Use version</button><a class="btn btn-ghost" href="/api/loops/${encodeURIComponent(loop.id)}/versions/${encodeURIComponent(version.id)}/export" download="agent.yaml">Export YAML</a></div>`:'<p class="loop-hint">Generate a workflow for this execution type.</p>'}</section>` : ''}
    ${panel === 'optimize' ? projectMode ? '<section class="loop-panel"><h3>Project optimization is not yet available</h3><p class="loop-hint">Project runs use real tools and may publish changes. They are not replayed as unattended optimization trials. Review the run’s diff and actual CI results.</p></section>' : `<section class="loop-panel"><div class="loop-section-heading"><h3>Analysis optimization</h3><button class="btn btn-primary" data-action="optimize" ${inProgress||!version?'disabled':''}>Optimize</button></div><p class="loop-hint">Small edits · up to 4 passes · uses provider credits.</p><button class="btn btn-ghost btn-small" data-action="optimize-settings" ${inProgress||!version?'disabled':''}>Custom settings</button>${rounds.map((round)=>`<details class="loop-secondary" data-inspect="experiment-${escapeHTML(round.id)}" ${liveRound(round)||['blocked','exhausted','failed'].includes(round.status)?'open':''}><summary>${escapeHTML(roundLabel(round))}</summary>${renderRound(round,jobs,versions,loop.activeVersion)}</details>`).join('')}</section>` : ''}
    ${panel === 'settings' ? `<section class="loop-panel"><h3>Task</h3><p class="loop-example">${escapeHTML(loop.description)}</p><h3>Success criteria</h3><p class="loop-example">${escapeHTML(loop.criteria)}</p><div class="loop-actions"><button class="btn btn-ghost" data-mode="${projectMode?'analysis':'project'}">Switch to ${projectMode?'analysis':'project work'}</button><button class="btn btn-ghost" data-action="import">Import analysis agent</button></div></section>${renderSpend(jobs)}<details class="loop-secondary loop-panel" data-inspect="history"><summary>Technical activity · ${jobs.length} attempts</summary>${jobs.map((job)=>`<button class="loop-run-row" data-select-job="${escapeHTML(job.id)}">${escapeHTML(job.kind)} · ${escapeHTML(job.status)}</button>`).join('')}</details>${detail.loadedJob&&selectedRun===detail.loadedJob.id?renderJob(detail.loadedJob,loop.id,feedback):''}` : ''}`;
}

export function renderProjectTask(task) {
  const card = task.card;
  const closed = task.status === 'closed';
  const label = closed ? 'Closed' : task.status === 'closing' ? 'Closing — retry if needed' : !card ? 'Card removed' : task.status !== 'launched' ? task.status === 'reserved' || task.status === 'launch_uncertain' ? 'Launch unconfirmed — inspect before retrying' : 'Launch failed' : ({ starting: 'Starting', running: 'Running', waiting: 'Idle — verify results', paused: 'Paused', error: 'Needs attention' })[card.status] || card.status;
  return `<div class="loop-project-task"><div class="loop-section-heading"><strong>${escapeHTML(task.projectName)}</strong><span class="loop-muted">${escapeHTML(label)}</span></div>
    <p class="loop-project-context">${escapeHTML(task.agent)}${card ? ` · ${escapeHTML(card.branch)}` : ''}</p>
    ${task.error ? `<p class="loop-job-error">${escapeHTML(task.error)}</p>` : ''}
    <div class="loop-actions">${card && !closed && task.status !== 'closing' ? `<button class="btn btn-ghost btn-small" data-task-terminal="${escapeHTML(card.id)}">Open agent</button>` : ''}${card ? `<button class="btn btn-ghost btn-small" data-task-diff="${escapeHTML(card.id)}">Diff</button>` : ''}${!closed ? `<button class="btn btn-ghost btn-small" data-close-task="${escapeHTML(task.id)}">${task.status === 'closing' ? 'Retry close' : 'Close'}</button>` : ''}</div></div>`;
}

function formatMetricCost(cost) { return cost == null ? 'Cost unknown' : `$${cost.toFixed(cost < 1 ? 4 : 2)}`; }

export function renderJob(job, loopID, feedback = []) {
  const busy = ['queued', 'running'].includes(job.status);
  const metrics = job.metrics || {};
  return `<details class="loop-job" ${busy || job.status === 'failed' ? 'open' : ''} data-job="${escapeHTML(job.id)}" data-inspect="job-${escapeHTML(job.id)}"><summary>
    <span class="loop-status ${escapeHTML(job.status)}">${escapeHTML(job.status)}</span>${job.closedAt ? '<span class="loop-muted">Closed</span>' : ''}<strong>${({ generate: '✦ Agent design', proposal: '✦ Candidate design', capability: '◇ Capability check', test: '▷ Evaluation run', judge: '◇ Quality judge' })[job.kind] || '▷ Agent run'}</strong>
    <span class="loop-job-metrics">${formatMetricCost(metrics.cost)} · ${((metrics.durationMs || 0) / 1000).toFixed(1)}s</span></summary>
    ${job.project ? `<p class="loop-project-context"><strong>${escapeHTML(job.project.name)}</strong> · ${escapeHTML(job.project.revision.slice(0, 8))}<br><span>${escapeHTML(job.project.path)} · ${job.project.files ? `${job.project.files.length} files included` : 'Committed snapshot'} · ${job.project.omitted} omitted</span></p>` : ''}
    ${job.error ? `<p class="loop-job-error">${escapeHTML(job.error)}</p>` : ''}
    <div class="loop-job-facts"><span>${escapeHTML(metrics.model || 'Model pending')}</span><span>${metrics.calls || 0} calls</span><span>${metrics.inputTokens || 0} in / ${metrics.outputTokens || 0} out</span></div>
    ${job.kind !== 'generate' ? `<details data-inspect="input-${escapeHTML(job.id)}"><summary>Input</summary><pre class="loop-output" data-inspect="input-text-${escapeHTML(job.id)}">${escapeHTML(job.input)}</pre></details>` : ''}
    <pre class="loop-output" data-inspect="output-${escapeHTML(job.id)}">${escapeHTML(job.output || (busy ? 'Waiting for the agent…' : 'No output.'))}</pre>
    <div class="loop-actions"><a class="btn btn-ghost btn-small" href="/api/loops/${encodeURIComponent(loopID)}/jobs/${encodeURIComponent(job.id)}/events" download>Raw events ↓</a>
    <a class="btn btn-ghost btn-small" href="/api/loops/${encodeURIComponent(loopID)}/jobs/${encodeURIComponent(job.id)}/stderr" download>Process log ↓</a>
    ${!busy && ['run', 'test'].includes(job.kind) ? `<button class="btn btn-ghost btn-small" data-feedback="${escapeHTML(job.id)}">＋ Feedback</button>` : ''}
    ${busy ? `<button class="btn btn-ghost btn-small" data-cancel="${escapeHTML(job.id)}">Cancel</button>` : ''}${job.kind === 'run' && !job.closedAt ? `<button class="btn btn-ghost btn-small" data-close-run="${escapeHTML(job.id)}">Close</button>` : ''}</div>
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
  for (const key of ['version', 'action', 'promote', 'cancel', 'feedback', 'cancelRound', 'source', 'copyResult', 'closeRun', 'closeTask', 'loopPanel', 'selectRun', 'selectJob', 'mode']) {
    if (element.dataset[key]) return `${key}:${element.dataset[key]}`;
  }
  return element.getAttribute('href') || '';
}

export function updateLoopInspector(container, detail, selectedVersion, sourceJob = '', panel = 'runs', selectedRun = '') {
  const html = renderLoopDetail(detail, selectedVersion, sourceJob, panel, selectedRun);
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
  const latest = detail.loadedJob && detail.loadedJob.id === body?.dataset.resultBody ? detail.loadedJob : detail.jobs?.find((job) => job.id === body?.dataset.resultBody);
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
  inspectorRenders.set(container, { loop: detail.loop.id, html, sourceJob, output: detail.loadedJob && detail.loadedJob.id === nextBody?.dataset.resultBody ? detail.loadedJob.output : detail.jobs?.find((job) => job.id === nextBody?.dataset.resultBody)?.output });
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
  let panel = 'runs';
  let selectedRun = '';
  let selectedJob = '';
  const jobLoads = new Map();
  const loadedJobs = new Map();
  let runLoad = 0;
  let refreshError = false;
  let taskLoop = '';
  let taskVersion = '';
  let taskRequestID = '';
  let taskProjects = [];
  let taskLoading = 0;
  let taskSubmitting = false;
  const projectLoads = new Map();
  const startingOptimizations = new Set();
  const closingRuns = new Set();

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
    const request=++sequence;
    const id=location.hash.split('/')[1]||'';
    try {
      const list=await api('/loops');
      if(request!==sequence)return;
      byID('loops-list').innerHTML=renderLoopList(list,id,detail?.loop.id===id?detail:null);
      byID('loops-count').textContent=`${list.length} ${list.length===1?'loop':'loops'}`;
      byID('loops-welcome-title').textContent=list.length?'Select a loop':'No loops yet';
      if(!id){detail=null;byID('loop-detail').hidden=true;byID('loops-welcome').hidden=false;return;}
      const [nextDetail,projectTasks]=await Promise.all([api(`/loops/${encodeURIComponent(id)}/overview`),api(`/loops/${encodeURIComponent(id)}/tasks`)]);
      if(request!==sequence)return;
      if(refreshError){notice();refreshError=false;}
      detail=nextDetail;detail.projectTasks=projectTasks;
      const selected=panel==='settings'?selectedJob:selectedRun;
      detail.loadedJob=loadedJobs.get(`${id}/${selected}`);
      selectedVersion=selectedLoopVersion(detail,selectedVersion)?.id||'';
      byID('loop-detail').hidden=false;byID('loops-welcome').hidden=true;
      byID('loops-list').innerHTML=renderLoopList(list,id,detail);
      updateLoopInspector(byID('loop-detail'),detail,selectedVersion,sourceJob,panel,selected);
      loadSelectedRun();
      if((projectTasks||[]).some(t=>t.status!=='closed'&&['starting','running','paused'].includes(t.card?.status)) || (detail.jobs||[]).some(busyJob) || (detail.rounds||[]).some(liveRound))refreshTimer=setTimeout(refreshLoops,1000);
    }catch(error){
      if(request!==sequence)return;
      refreshError=true;notice(error.message,true);
      if(error.status===404){detail=null;byID('loop-detail').hidden=true;byID('loops-welcome').hidden=false;}
      else refreshTimer=setTimeout(refreshLoops,3000);
    }
  }

  async function loadSelectedRun() {
    if(!detail || !['runs','settings'].includes(panel))return;
    const id=detail.loop.id;
    if(panel==='runs'&&!selectedRun){
      const candidates=[...(detail.jobs||[]).filter(j=>j.kind==='run').map(j=>({id:j.id,time:j.createdAt||'',closed:!!j.closedAt,busy:busyJob(j)})),...(detail.projectTasks||[]).map(t=>({id:t.id,time:t.createdAt||'',closed:t.status==='closed',busy:['starting','running','paused'].includes(t.card?.status)}))].sort((a,b)=>b.time.localeCompare(a.time));
      selectedRun=(candidates.find(r=>r.busy)||candidates.find(r=>!r.closed)||candidates[0])?.id||'';
    }
    const selection=panel==='settings'?selectedJob:selectedRun;
    const job=(detail.jobs||[]).find(j=>j.id===selection);
    if(!job)return;
    const key=`${id}/${job.id}`;
    const cached=loadedJobs.get(key);
    if(cached&&!busyJob(job)&&cached.status===job.status&&cached.closedAt===job.closedAt){detail.loadedJob=cached;updateLoopInspector(byID('loop-detail'),detail,selectedVersion,sourceJob,panel,selection);return;}
    if(jobLoads.has(key))return;
    const request=runLoad;
    jobLoads.set(key,true);
    try{
      const loaded=await api(`/loops/${id}/jobs/${job.id}`);
      if(loadedJobs.size>=8)loadedJobs.delete(loadedJobs.keys().next().value);
      loadedJobs.set(key,loaded);
      const current=panel==='settings'?selectedJob:selectedRun;
      if(detail?.loop.id!==id||current!==job.id)return;
      detail.loadedJob=loaded;
      updateLoopInspector(byID('loop-detail'),detail,selectedVersion,sourceJob,panel,current);
    }catch(error){if(request===runLoad&&detail?.loop.id===id)notice(error.message,true);}
    finally{jobLoads.delete(key);}
  }

  let routeLoop='';
  function route() {
    const id=location.hash.split('/')[1]||'';
    if(routeLoop!==id){routeLoop=id;selectedRun='';selectedJob='';selectedVersion='';runLoad++;detail=null;byID('loop-detail').hidden=true;}

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
    if (button) { selectedVersion = ''; selectedRun=''; panel='runs'; location.hash = `loops/${button.dataset.loop}`; }
  });
  byID('loop-detail').addEventListener('click', async (event) => {
    const tab=event.target.closest('[data-loop-panel]');
    if(tab&&detail){panel=tab.dataset.loopPanel;runLoad++;detail.loadedJob=loadedJobs.get(`${detail.loop.id}/${panel==='settings'?selectedJob:selectedRun}`);updateLoopInspector(byID('loop-detail'),detail,selectedVersion,sourceJob,panel,panel==='settings'?selectedJob:selectedRun);loadSelectedRun();return;}
    const run=event.target.closest('[data-select-run], [data-select-job]');
    if(run&&detail){if(run.dataset.selectJob){selectedJob=run.dataset.selectJob;}else{selectedRun=run.dataset.selectRun;}runLoad++;detail.loadedJob=null;updateLoopInspector(byID('loop-detail'),detail,selectedVersion,sourceJob,panel,panel==='settings'?selectedJob:selectedRun);loadSelectedRun();return;}
    const mode=event.target.closest('[data-mode]');
    if(mode&&detail){
      if(!window.confirm(`Switch to ${mode.dataset.mode==='project'?'project work':'read-only analysis'}? Generate a new matching workflow before running. Previous versions and runs are retained.`))return;
      const id=detail.loop.id;
      try{await api(`/loops/${id}/mode`,{method:'PUT',body:JSON.stringify({mode:mode.dataset.mode})});if(detail?.loop.id===id){selectedVersion='';panel='agent';await refreshLoops();}}catch(error){if(detail?.loop.id===id)notice(error.message,true);}return;
    }

    const closeRun = event.target.closest('[data-close-run]');
    const closeTask = event.target.closest('[data-close-task]');
    if ((closeRun || closeTask) && detail) {
      const button = closeRun || closeTask;
      const id = closeRun?.dataset.closeRun || closeTask.dataset.closeTask;
      const loopID = detail.loop.id;
      const key = `${loopID}/${closeRun ? 'run' : 'task'}/${id}`;
      if (closingRuns.has(key)) return;
      const job = detail.jobs?.find((item) => item.id === id);
      const question = closeTask ? 'Stop this Board task and close it? Its worktree, commits and history will be kept.' : 'Stop this run and close it? Its results will be kept in history.';
      if ((closeTask || (job && busyJob(job))) && !window.confirm(question)) return;
      closingRuns.add(key);
      const label = button.textContent;
      button.disabled = true;
      button.textContent = 'Closing…';
      try {
        await api(`/loops/${loopID}/${closeRun ? 'runs' : 'tasks'}/${encodeURIComponent(id)}/close`, { method: 'POST' });
        if (detail?.loop.id === loopID) notice('Run closed. Results kept in history.');
        await refreshLoops();
      } catch (error) { if (detail?.loop.id === loopID) notice(error.message, true); }
      finally { closingRuns.delete(key); button.disabled = false; button.textContent = label; }
      return;
    }
    const projectTask = event.target.closest('[data-action="project-task"]');
    if (projectTask && detail) {
      if (taskSubmitting) return;
      taskLoop = detail.loop.id;
      taskVersion = selectedLoopVersion(detail,selectedVersion)?.id || '';
      if(!taskVersion){notice('Generate a project workflow first.',true);return;}
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
      updateLoopInspector(byID('loop-detail'), detail, selectedVersion, sourceJob, panel, panel === 'settings' ? selectedJob : selectedRun);
      return;
    }
    const copy = event.target.closest('[data-copy-result]');
    if (copy && detail) {
      const job = detail.loadedJob?.id===copy.dataset.copyResult?detail.loadedJob:detail.jobs?.find((item) => item.id === copy.dataset.copyResult);
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
      panel='agent';
      updateLoopInspector(byID('loop-detail'), detail, selectedVersion, sourceJob, panel, panel === 'settings' ? selectedJob : selectedRun);
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
        criteria: byID('loop-criteria').value, mode:byID('loop-mode').value, projectId: byID('loop-first-project').value,
      }) });
      selectedVersion = ''; selectedRun='';panel='runs';
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
      const job=await api(`/loops/${id}/runs`, { method: 'POST', body: JSON.stringify({ version, projectId: byID('loop-run-project').value, input: byID('loop-run-input').value }) });
      if(detail?.loop.id===id){selectedRun=job.id;panel='runs';runLoad++;}
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
    const loopID = taskLoop, requestId = taskRequestID, versionId=taskVersion;
    taskSubmitting = true;
    byID('loop-task-project').disabled = true;
    submit(event.target, async () => {
      try {
        const task=await api(`/loops/${loopID}/tasks`, { method: 'POST', body: JSON.stringify({ requestId, versionId, projectId: project.id, repoPath: project.repoPath, agent: project.agent, confirmed: true }) });
        if(detail?.loop.id===loopID){selectedRun=task.id;panel='runs';runLoad++;}
        await refreshLoops();
      } finally { taskSubmitting = false; byID('loop-task-project').disabled = false; }
    });
  });
  window.addEventListener('hashchange', route);
  window.addEventListener('board-refresh', refreshLoops);
  route();
}
