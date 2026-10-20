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
  const candidate = { versionId: 'v', trials: [trial, { ...trial, jobId: 'b', judgeId: 'j2', case: 1 }] };
  const summary = summarizeCandidate(round, candidate, [{id:'a',kind:'test',versionId:'v',status:'succeeded',metrics:{cost:0.1,durationMs:200}}, {id:'b',kind:'test',versionId:'v',status:'succeeded',metrics:{cost:0.2,durationMs:400}}, {id:'j',kind:'judge',versionId:'v',status:'succeeded'}, {id:'j2',kind:'judge',versionId:'v',status:'succeeded'}]);
  assert.equal(summary.qualified, true);
  assert.ok(Math.abs(summary.cost - .15) < 1e-9);
  assert.equal(summary.duration, 300);
});

test('experiments never offer promotion for failing candidates', async () => {
  const { renderRound } = await modulePromise;
  const html = renderRound({id:'r',status:'completed',inputs:['a'],repeats:1,candidates:[{label:'<candidate>',versionId:'v',trials:[{jobId:'j',case:0,repeat:0,passed:false,reason:'<failed>'}]}]}, [], [{id:'v',number:2}], '');
  assert.match(html, /data-promote="v"[^>]* disabled/);
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

test('missing, failed and interrupted persisted jobs never qualify in the UI', async () => {
 const { summarizeCandidate } = await modulePromise;
 const round={inputs:['a'],repeats:1};
 const candidate={versionId:'v',trials:[{jobId:'r',judgeId:'j',case:0,repeat:0,passed:true}]};
 const jobs=[{id:'r',versionId:'v',kind:'test',status:'succeeded'},{id:'j',versionId:'v',kind:'judge',status:'succeeded'}];
 assert.equal(summarizeCandidate(round,candidate,[]).qualified,false);
 for(const status of ['failed','running','interrupted']) {
   assert.equal(summarizeCandidate(round,candidate,[jobs[0],{...jobs[1],status}]).qualified,false);
 }
 assert.equal(summarizeCandidate(round,candidate,jobs).qualified,true);
 candidate.trials[0].case=99;
 assert.equal(summarizeCandidate(round,candidate,jobs).qualified,false);
});

test('spend separates run, optimization and judge costs including failed attempts', async () => {
  const { summarizeSpend } = await modulePromise;
  const result = summarizeSpend([
    {kind:'run',status:'failed',metrics:{cost:.1}},
    {kind:'run',status:'running',metrics:{cost:null}},
    {kind:'proposal',metrics:{cost:.3}},
    {kind:'test',metrics:{cost:.2}},
    {kind:'judge',metrics:{cost:.05}},
    {kind:'judge',metrics:{cost:0}},
  ]);
  assert.deepEqual(result.execution,{label:'Runs',cost:.1,unknown:1,count:2});
  assert.equal(result.optimization.cost,.5);
  assert.equal(result.judging.cost,.05);
  assert.equal(result.judging.unknown,0);
});

test('relative metrics avoid inventing improvements for missing or zero baselines', async () => {
  const { metricDelta } = await modulePromise;
  assert.equal(metricDelta(.05,.1,'cost'),'50% lower cost');
  assert.equal(metricDelta(200,100,'duration'),'100% slower');
  assert.equal(metricDelta(100,100,'duration'),'Similar to baseline');
  for (const baseline of [0,null,undefined,NaN]) assert.equal(metricDelta(.1,baseline,'cost'),'');
  for (const value of [null,undefined,NaN,-1]) assert.equal(metricDelta(value,.1,'cost'),'');
});

test('comparisons show observed deltas only after both quality gates pass', async () => {
  const { renderRound } = await modulePromise;
  const candidates = ['base','candidate'].map((versionId,index) => ({versionId,label:versionId,trials:[{jobId:`run${index}`,judgeId:`judge${index}`,case:0,repeat:0,passed:true}]}));
  const round = {id:'r',baseline:'base',status:'completed',inputs:['input'],repeats:1,candidates};
  const jobs = candidates.flatMap((candidate,index) => [
    {id:`run${index}`,kind:'test',versionId:candidate.versionId,status:'succeeded',metrics:{cost:index ? .05 : .1,durationMs:index ? 500 : 1000}},
    {id:`judge${index}`,kind:'judge',versionId:candidate.versionId,status:'succeeded'},
  ]);
  const versions = candidates.map((candidate,index) => ({id:candidate.versionId,number:index+1}));
  assert.match(renderRound(round,jobs,versions,''),/50% lower cost/);
  assert.match(renderRound(round,jobs,versions,''),/50% faster/);
  candidates[1].trials[0].passed=false;
  const failed = renderRound(round,jobs,versions,'');
  assert.doesNotMatch(failed,/50% lower cost|50% faster/);
  assert.match(failed,/Quality gate not met/);
});

function inspectorHarness() {
  const doc = { activeElement: null };
  const state = { elements: [], buttons: [], writes: 0 };
  const container = {
    ownerDocument: doc, scrollTop: 0,
    contains: (element) => state.buttons.includes(element),
    querySelectorAll: (selector) => selector === '[data-inspect]' ? state.elements : state.buttons,
    set innerHTML(html) {
      state.writes++;
      state.elements = [...html.matchAll(/<(details|pre)[^>]*data-inspect="([^"]+)"[^>]*>/g)].map((match) => ({
        tagName: match[1].toUpperCase(), dataset: { inspect: match[2] }, open: /\sopen[\s>]/.test(match[0]), scrollTop: 0, scrollLeft: 0,
      }));
      state.buttons = [...html.matchAll(/<button[^>]*data-version="([^"]+)"[^>]*>/g)].map((match) => {
        const button = { tagName: 'BUTTON', dataset: { version: match[1], focus: match[0].match(/data-focus="([^"]+)"/)?.[1] }, getAttribute: () => null, focus() { doc.activeElement = button; } };
        return button;
      });
    },
  };
  return { container, state, doc };
}

function inspectorFixture() {
  return {
    loop:{id:'loop',name:'Notes',description:'Summarize',criteria:'Accurate',example:'input'},
    versions:[{id:'v1',number:1,yaml:'agents: {}',createdAt:'2026-10-20T00:00:00Z'}],
    jobs:[{id:'j1',kind:'run',status:'running',output:'First',metrics:{}}],
  };
}

test('live inspection preserves collapsed runs, nested scroll and keyboard focus', async () => {
  const { updateLoopInspector } = await modulePromise;
  const { container, state, doc } = inspectorHarness();
  const detail = inspectorFixture();
  updateLoopInspector(container, detail, 'v1');
  state.elements.find((element) => element.dataset.inspect === 'job-j1').open = false;
  const output = state.elements.find((element) => element.dataset.inspect === 'output-j1');
  output.scrollTop = 50;
  output.scrollLeft = 12;
  container.scrollTop = 200;
  doc.activeElement = state.buttons[0];
  detail.jobs[0].output = 'More output';
  updateLoopInspector(container, detail, 'v1');
  assert.equal(state.elements.find((element) => element.dataset.inspect === 'job-j1').open, false);
  assert.equal(state.elements.find((element) => element.dataset.inspect === 'output-j1').scrollTop, 50);
  assert.equal(state.elements.find((element) => element.dataset.inspect === 'output-j1').scrollLeft, 12);
  assert.equal(container.scrollTop, 200);
  assert.equal(doc.activeElement, state.buttons[0]);
  assert.equal(state.writes, 2);
  updateLoopInspector(container, detail, 'v1');
  assert.equal(state.writes, 2, 'unchanged refresh does not replace the DOM');
});

test('switching loops resets inspection rather than leaking the previous scroll position', async () => {
  const { updateLoopInspector } = await modulePromise;
  const { container } = inspectorHarness();
  const detail = inspectorFixture();
  updateLoopInspector(container, detail, 'v1');
  container.scrollTop = 300;
  detail.loop.id = 'another-loop';
  updateLoopInspector(container, detail, 'v1');
  assert.equal(container.scrollTop, 0);
});

test('identically rendered draft loops still reset the inspection context', async () => {
  const { updateLoopInspector } = await modulePromise;
  const { container, state } = inspectorHarness();
  const detail = {loop:{id:'first',name:'Same',description:'Task',criteria:'Good',example:'Input'},versions:[]};
  updateLoopInspector(container,detail,'');
  container.scrollTop=300;
  detail.loop.id='second';
  updateLoopInspector(container,detail,'');
  assert.equal(container.scrollTop,0);
  assert.equal(state.writes,2);
});

test('live refresh restores the exact experiment control instead of a matching version tab', async () => {
  const { updateLoopInspector } = await modulePromise;
  const { container, state, doc } = inspectorHarness();
  const detail=inspectorFixture();
  const candidate={versionId:'v1',label:'Baseline',trials:[]};
  detail.rounds=['first','second'].map((id)=>({id,baseline:'v1',status:'testing',inputs:['input'],repeats:1,candidates:[candidate]}));
  updateLoopInspector(container,detail,'v1');
  assert.equal(state.buttons.length,3);
  doc.activeElement=state.buttons[2];
  detail.jobs[0].output='Changed output';
  updateLoopInspector(container,detail,'v1');
  assert.equal(doc.activeElement,state.buttons[2]);
  assert.equal(doc.activeElement.dataset.focus,'inspect-second-v1');
});
