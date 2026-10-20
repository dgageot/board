const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const source = fs.readFileSync(path.join(__dirname, '../pkg/board/static/loops.js'), 'utf8');
const modulePromise = import(`data:text/javascript;base64,${Buffer.from(source).toString('base64')}`);

test('loop lists escape untrusted names and expose selection', async () => {
  const { renderLoopList } = await modulePromise;
  const html = renderLoopList([{ id: 'a', name: '<script>bad</script>' }], 'a');
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /aria-pressed="true"/);
});

test('loop inspector preserves YAML safely and marks active versions', async () => {
  const { renderLoopDetail } = await modulePromise;
  const html = renderLoopDetail({
    loop: { id: 'loop', name: 'Notes', description: 'Summarize', criteria: '<b>accurate</b>', example: 'fix', activeVersion: 'v1' },
    versions: [{ id: 'v1', number: 1, yaml: 'instruction: </code><script>bad</script>', rationale: 'First', createdAt: '2026-10-20T00:00:00Z' }],
  }, 'v1');
  assert.match(html, /v1 · active/);
  assert.match(html, /&lt;\/code&gt;&lt;script&gt;/);
  assert.match(html, /versions\/v1\/export/);
});

test('empty loops offer a first version without inventing metrics', async () => {
  const { renderLoopDetail } = await modulePromise;
  const html = renderLoopDetail({ loop: { id: 'a', name: 'Notes', description: 'Task', criteria: 'Good', example: 'Input' }, versions: [] });
  assert.match(html, /Import YAML/);
  assert.match(html, /0 versions/);
  assert.doesNotMatch(html, /\$0|100%/);
});

test('job metrics distinguish unpriced calls from free execution and escape output', async () => {
  const { renderJob } = await modulePromise;
  const html = renderJob({ id: 'job', kind: 'run', status: 'failed', input: '<input>', output: '<script>x</script>', error: '<failure>', metrics: { cost: null, durationMs: 1500, calls: 1, model: '<model>' } }, 'loop');
  assert.match(html, /Cost unknown/);
  assert.match(html, /1\.5s/);
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>|\$0/);
  assert.match(html, /jobs\/job\/events/);
});

test('comparison fails closed on incomplete, failed and duplicate evidence', async () => {
  const { summarizeCandidate } = await modulePromise;
  const round = { inputs: ['a','b'], repeats: 1 };
  const trial = { jobId: 'a', judgeId: 'j', case: 0, repeat: 0, passed: true };
  assert.equal(summarizeCandidate(round, { trials: [trial] }, []).qualified, false);
  assert.equal(summarizeCandidate(round, { trials: [trial,trial] }, []).qualified, false);
  const candidate = { trials: [trial, { ...trial, jobId: 'b', case: 1 }] };
  const summary = summarizeCandidate(round, candidate, [{id:'a',status:'succeeded',metrics:{cost:0.1,durationMs:200}}, {id:'b',status:'succeeded',metrics:{cost:0.2,durationMs:400}}]);
  assert.equal(summary.qualified, true);
  assert.ok(Math.abs(summary.cost - .15) < 1e-9);
  assert.equal(summary.duration, 300);
});

test('experiments never offer promotion for failing candidates', async () => {
  const { renderRound } = await modulePromise;
  const html = renderRound({id:'r',status:'completed',inputs:['a'],repeats:1,candidates:[{label:'<candidate>',versionId:'v',trials:[{jobId:'j',case:0,repeat:0,passed:false,reason:'<failed>'}]}]}, [], [{id:'v',number:2}], '');
  assert.match(html, /data-promote="v" disabled/);
  assert.match(html, /&lt;failed&gt;/);
  assert.match(html, /not a guarantee/);
});

test('active version remains the default when unpromoted candidates arrive', async () => {
  const { selectedLoopVersion } = await modulePromise;
  const detail = { loop: { activeVersion: 'v1' }, versions: [{id:'v3'},{id:'v2'},{id:'v1'}] };
  assert.equal(selectedLoopVersion(detail, '').id, 'v1');
  assert.equal(selectedLoopVersion(detail, 'v2').id, 'v2');
  assert.equal(selectedLoopVersion(detail, 'removed').id, 'v1');
  detail.loop.activeVersion = '';
  assert.equal(selectedLoopVersion(detail, '').id, 'v1');
});
