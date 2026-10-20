const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { test } = require('node:test');
const { pathToFileURL } = require('node:url');
const markdownModule = require('../pkg/board/static/result-markdown.js');
const source = fs.readFileSync(path.join(__dirname, '../pkg/board/static/loops.js'), 'utf8');
const modulePromise = import(pathToFileURL(path.join(__dirname, '../pkg/board/static/loops.js')).href);

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
  const detail = {loop:{id:'first',name:'Same',description:'Task',criteria:'Good',example:'Input',projectId:'project-first'},versions:[]};
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
  doc.activeElement=state.buttons.find((button)=>button.dataset.focus==='inspect-second-v1');
  detail.jobs[0].output='Changed output';
  updateLoopInspector(container,detail,'v1');
  assert.equal(doc.activeElement,state.buttons.find((button)=>button.dataset.focus==='inspect-second-v1'));
  assert.equal(doc.activeElement.dataset.focus,'inspect-second-v1');
});

function loopAppHarness(overrides = {}) {
  const vm = require('node:vm');
  const elements = new Map();
  const handlers = new Map();
  const calls = [];
  const fixture = (id) => ({loop:{id,name:id,description:'Task',criteria:'Good',example:'Input',projectId:'project-first'},versions:[
    {id:`${id}-v2`,number:2,yaml:'agents: {}',createdAt:'2026-10-20T00:00:00Z'},
    {id:`${id}-v1`,number:1,yaml:'agents: {}',createdAt:'2026-10-20T00:00:00Z'},
  ]});
  const document = {activeElement:null};
  const element = (id) => {
    if (!elements.has(id)) elements.set(id, {
      id, value:'', hidden:false, disabled:false, dataset:{}, innerHTML:'', scrollTop:0, ownerDocument:document,
      handlers:{}, classList:{toggle() {}}, setAttribute(){},
      addEventListener(name,callback){this.handlers[name]=callback;},
      querySelectorAll:()=>[],
      querySelector:(selector)=>element(`${id}:${selector}`),
      contains:()=>false, focus(){}, scrollIntoView(){this.scrolled=true;},
      showModal(){this.open=true;}, close(){this.open=false;}, reset(){},
      closest:()=>element('dialog'),
    });
    return elements.get(id);
  };
  const context = vm.createContext({
    document:Object.assign(document,{getElementById:element,body:element('body'),querySelectorAll:()=>[]}),
    window:{addEventListener:(name,callback)=>handlers.set(name,callback)},
    location:{hash:'#board'},setTimeout,clearTimeout,console, ...markdownModule,
  });
  const api=async(path,options)=>{
    calls.push({path,options});
    if (overrides.api) { const result = overrides.api(path,options); if(result !== undefined) return result; }
    if(path==='/projects') return [{id:'project-first',name:'<Project one>',repoPath:'/repo/one'},{id:'project-second',name:'Project two',repoPath:'/repo/two'}];
    if(path==='/loops') return [fixture('first').loop,fixture('second').loop];
    if(options?.method==='POST') return {id:'imported'};
    return fixture(path.split('/').at(-1));
  };
  context.api=api;
  vm.runInContext(source.replace(/^import .*;\n/gm,'').replace(/^export /gm,'')+'\ninitLoops(api);',context);
  return {
    calls,element,
    async action(name) {
      await element('loop-detail').handlers.click({target:{closest:(selector)=>selector===`[data-action="${name}"]` ? {} : null}});
      await new Promise(setImmediate);
    },
    async submit(form) {
      element(form).handlers.submit({preventDefault(){},target:element(form)});
      await new Promise(setImmediate);
    },
    async navigate(id) {
      context.location.hash=`#loops/${id}`;
      handlers.get('hashchange')();
      await new Promise(setImmediate);
      assert.equal(element('loops-notice').textContent || '', '', 'navigation rendered successfully');
      assert.match(element('loop-detail').innerHTML, new RegExp(`${id}-v1`));
    },
    async selectVersion(id) {
      await element('loop-detail').handlers.click({target:{closest:(selector)=>selector==='[data-version]'?{dataset:{version:id}}:null}});
    },
    async importOpen() {
      await element('loop-detail').handlers.click({target:{closest:(selector)=>selector==='[data-action="import"]'? {}:null}});
    },
    async importSubmit() {
      element('loop-import-form').handlers.submit({preventDefault(){},target:element('loop-import-form')});
      await new Promise(setImmediate);
    },
  };
}

test('an open import keeps its original target when navigation changes', async () => {
  const h=loopAppHarness();
  await h.navigate('first');
  await h.importOpen();
  await h.navigate('second');
  await h.selectVersion('second-v2');
  h.element('loop-yaml').value='agents: {root: {}}';
  await h.importSubmit();
  const request=h.calls.find((call)=>call.options?.method==='POST');
  assert.equal(request.path,'/loops/first/versions');
  assert.equal(JSON.parse(request.options.body).yaml,'agents: {root: {}}');
  assert.match(h.element('loop-detail').innerHTML, /btn-primary" data-version="second-v2"/);
  assert.equal(h.element('loops-notice').textContent || '', '');
});

test('each run submits its chosen project, independent of the first-run default', async () => {
 const h=loopAppHarness();
 await h.navigate('first');
 await h.action('run');
 assert.equal(h.element('loop-run-project').value,'project-first');
 assert.match(h.element('loop-run-project').innerHTML,/&lt;Project one&gt;/);
 h.element('loop-run-project').value='project-second';
 h.element('loop-run-input').value='';
 await h.submit('loop-run-form');
 const request=h.calls.find((call)=>call.path==='/loops/first/runs');
 assert.deepEqual(JSON.parse(request.options.body),{version:'first-v1',projectId:'project-second',input:''});
});

test('project list failure and empty projects are visible rather than silently using a directory', async () => {
 const empty=loopAppHarness({api:(path)=>path==='/projects'?[]:undefined});
 await empty.navigate('first'); await empty.action('run');
 assert.equal(empty.element('loop-run-project').disabled,true);
 assert.match(empty.element('loop-run-project-status').textContent,/No projects configured/);
 const failed=loopAppHarness({api:(path)=>path==='/projects'?Promise.reject(new Error('Cannot load projects')):undefined});
 await failed.navigate('first'); await failed.action('run');
 assert.equal(failed.element('loop-run-project').disabled,true);
 assert.match(failed.element('loop-run-project-status').textContent,/Cannot load projects/);
});

test('loop creation sends the first-run project instead of example text', async () => {
 const h=loopAppHarness();
 h.element('loop-name').value='Review';h.element('loop-task').value='Review code';h.element('loop-criteria').value='Precise';h.element('loop-first-project').value='project-first';
 await h.submit('loop-new-form');
 const request=h.calls.find((call)=>call.path==='/loops' && call.options?.method==='POST');
 assert.deepEqual(JSON.parse(request.options.body),{name:'Review',description:'Review code',criteria:'Precise',projectId:'project-first'});
});

test('newly created loop clearly shows automatic agent creation without another generate action', async () => {
  const { renderLoopDetail } = await modulePromise;
  const detail={loop:{id:'new',name:'Review',description:'Review code',criteria:'Accurate'},versions:[],jobs:[{id:'generation',kind:'generate',status:'running',metrics:{}}]};
  const html=renderLoopDetail(detail,'');
  assert.match(html,/Creating agent/);
  assert.match(html,/role="status" aria-live="polite"/);
  assert.doesNotMatch(html,/data-action="generate"/);
  assert.match(html,/data-cancel="generation"/);
  assert.match(html,/<details[^>]+data-inspect="agent-config"/);
});

test('running agent is distinct from agent generation and has visible live output', async () => {
  const { renderLoopDetail, loopActivity } = await modulePromise;
  const detail=inspectorFixture();
  const html=renderLoopDetail(detail,'v1');
  assert.equal(loopActivity(detail).phase,'running');
  assert.match(html,/Agent running/);
  assert.match(html,/Live output/);
  assert.match(html,/First/);
  assert.doesNotMatch(html,/data-action="generate"/);
  assert.equal((html.match(/data-inspect="job-j1"/g)||[]).length,1);
});

test('failed generation offers retry while a ready agent offers a run', async () => {
  const { renderLoopDetail }=await modulePromise;
  const detail={loop:{id:'new',name:'Review',description:'Task',criteria:'Good'},versions:[],jobs:[{id:'generation',kind:'generate',status:'failed',error:'Provider unavailable',metrics:{}}]};
  assert.match(renderLoopDetail(detail,''),/Agent creation failed/);
  assert.match(renderLoopDetail(detail,''),/Retry creation/);
  detail.jobs[0].status='succeeded';
  detail.versions=[{id:'v1',number:1,yaml:'agents: {}'}];
  const html=renderLoopDetail(detail,'v1');
  assert.match(html,/Ready/);
  assert.match(html,/data-action="run"/);
  assert.doesNotMatch(html,/Retry creation/);
});

test('selected loop sidebar reflects its current running activity', async () => {
 const { renderLoopList }=await modulePromise;
 const detail=inspectorFixture();
 const html=renderLoopList([detail.loop],detail.loop.id,detail);
 assert.match(html,/Agent running/);
 assert.match(html,/loop-list-icon spinning/);
 assert.doesNotMatch(html,/Draft/);
});

test('imported agents remain runnable after automatic generation fails or stops', async () => {
 const { renderLoopDetail }=await modulePromise;
 for(const status of ['failed','canceled']) {
  const detail={loop:{id:'l',name:'Loop',description:'Task',criteria:'Good'},versions:[{id:'import',number:1,yaml:'agents: {}'}],jobs:[{id:'g',kind:'generate',status,error:'Stopped',metrics:{}}]};
  const html=renderLoopDetail(detail,'import');
  assert.match(html,/data-action="run"/);
  assert.doesNotMatch(html,/data-action="generate"/);
 }
});

test('explicit version inspection reveals configuration instead of only changing hidden YAML', async () => {
 const h=loopAppHarness();
 await h.navigate('first');
 await h.selectVersion('first-v2');
 const config=h.element('loop-detail:[data-inspect="agent-config"]');
 assert.equal(config.open,true);
 assert.equal(config.scrolled,true);
});

test('live output and stop action refer to the same active run when a newer run has finished', async () => {
 const { renderLoopDetail }=await modulePromise;
 const detail=inspectorFixture();
 detail.jobs=[{id:'new',kind:'run',status:'succeeded',output:'New result',metrics:{}},{id:'old',kind:'run',status:'running',output:'Active output',metrics:{}}];
 const html=renderLoopDetail(detail,'v1');
 const main=html.slice(0,html.indexOf('data-inspect="task"'));
 assert.match(main,/data-cancel="old"/);
 assert.match(main,/Active output/);
 assert.doesNotMatch(main,/New result/);
});

test('partial failed output stays visibly marked when an experiment takes status priority', async () => {
 const { renderLoopDetail }=await modulePromise;
 const detail=inspectorFixture();
 detail.jobs=[{id:'failed',kind:'run',status:'failed',output:'Partial answer',error:'Provider unavailable',metrics:{}}];
 detail.rounds=[{id:'r',status:'designing',inputs:['a'],repeats:1,candidates:[]}];
 const html=renderLoopDetail(detail,'v1');
 const main=html.slice(0,html.indexOf('data-inspect="task"'));
 assert.match(main,/Optimizing/);
 assert.match(main,/Incomplete result/);
 assert.match(main,/Partial output/);
 assert.match(main,/Provider unavailable/);
});

test('Optimize immediately starts default optimization without opening settings', async () => {
 const h=loopAppHarness();
 await h.navigate('first');
 await h.action('optimize');
 const requests=h.calls.filter((call)=>call.path==='/loops/first/optimize/default');
 assert.equal(requests.length,1);
 assert.deepEqual(JSON.parse(requests[0].options.body),{version:'first-v1'});
 assert.notEqual(h.element('loop-optimize-dialog').open,true);
});

test('custom optimization settings remain a separate advanced action', async () => {
 const h=loopAppHarness();
 await h.navigate('first');
 await h.action('optimize-settings');
 assert.equal(h.element('loop-optimize-dialog').open,true);
 assert.equal(h.element('loop-test-project').value,'project-first');
 assert.equal(h.calls.filter((call)=>call.path.includes('optimize/default')).length,0);
});

test('double-clicking Optimize does not start a second experiment', async () => {
 let resolve;
 const pending=new Promise((r)=>{resolve=r;});
 const h=loopAppHarness({api:(path)=>path.endsWith('/optimize/default')?pending:undefined});
 await h.navigate('first');
 const first=h.action('optimize');
 await new Promise(setImmediate);
 await h.action('optimize');
 assert.equal(h.calls.filter((call)=>call.path.endsWith('/optimize/default')).length,1);
 resolve({id:'round'});
 await first;
});

test('loop status is concise without hiding failures or primary actions', async () => {
 const { renderLoopDetail, loopActivity }=await modulePromise;
 const detail=inspectorFixture();
 detail.jobs=[];
 assert.equal(loopActivity(detail).title,'Ready');
 assert.equal(loopActivity(detail).note,'');
 const ready=renderLoopDetail(detail,'v1');
 assert.match(ready,/data-action="run"/);
 assert.match(ready,/data-action="optimize"/);
 assert.match(ready,/uses provider credits/);
 assert.doesNotMatch(ready,/whenever you’re ready|Improve this agent|immutable snapshots|REUSABLE INTELLIGENCE/);
 detail.jobs=[{id:'g',kind:'generate',status:'failed',error:'Provider unavailable',metrics:{}}];
 assert.match(renderLoopDetail(detail,'v1'),/Provider unavailable/);
});

test('latest result renders safe Markdown with Source and Copy controls', async () => {
 const { renderLoopDetail }=await modulePromise;
 const detail=inspectorFixture();
 detail.jobs[0].output='# Findings\n\n- **One**\n- Two\n\n```go\nfunc main() {}\n```';
 const rendered=renderLoopDetail(detail,'v1');
 assert.match(rendered,/<h1>Findings<\/h1>/);
 assert.match(rendered,/<strong>One<\/strong>/);
 assert.match(rendered,/data-copy-result="j1"/);
 assert.match(rendered,/data-source="j1" aria-pressed="false"/);
 const raw=renderLoopDetail(detail,'v1','j1');
 assert.match(raw,/# Findings/);
 assert.doesNotMatch(raw,/<h1>Findings<\/h1>/);
 assert.match(raw,/data-source="j1" aria-pressed="true"/);
});
