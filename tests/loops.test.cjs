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
    versions: [{ id: 'v1', mode: 'project', number: 1, yaml: 'instruction: </code><script>bad</script>', rationale: 'First', createdAt: '2026-10-20T00:00:00Z' }],
  }, 'v1', '', 'agent');
  assert.match(html, /v1 · next run/);
  assert.match(html, /&lt;\/code&gt;&lt;script&gt;/);
  assert.match(html, /versions\/v1\/export/);
});

test('empty loops offer a first version without inventing metrics', async () => {
  const { renderLoopDetail } = await modulePromise;
  const html = renderLoopDetail({ loop: { id: 'a', name: 'Notes', description: 'Task', criteria: 'Good', example: 'Input' }, versions: [] });
  assert.match(html, /data-action="generate"/);
  assert.match(html, /No version/);
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
  const detail = { loop: { activeVersion: 'v1' }, versions: [{id:'v3',mode:'project'},{id:'v2',mode:'project'},{id:'v1',mode:'project'}] };
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
    querySelector: () => null,
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
    versions:[{id:'v1',mode:'project',number:1,yaml:'agents: {}',createdAt:'2026-10-20T00:00:00Z'}],
    jobs:[{id:'j1',kind:'run',status:'running',output:'First',metrics:{}}],
  };
}

test('live inspection preserves collapsed runs, nested scroll and keyboard focus', async () => {
  const { updateLoopInspector } = await modulePromise;
  const { container, state, doc } = inspectorHarness();
  const detail = inspectorFixture();
  detail.loadedJob=detail.jobs[0];
  updateLoopInspector(container, detail, 'v1', '', 'settings', 'j1');
  state.elements.find((element) => element.dataset.inspect === 'job-j1').open = false;
  const output = state.elements.find((element) => element.dataset.inspect === 'output-j1');
  output.scrollTop = 50;
  output.scrollLeft = 12;
  container.scrollTop = 200;

  detail.jobs[0].output = 'More output';
  updateLoopInspector(container, detail, 'v1', '', 'settings', 'j1');
  assert.equal(state.elements.find((element) => element.dataset.inspect === 'job-j1').open, false);
  assert.equal(state.elements.find((element) => element.dataset.inspect === 'output-j1').scrollTop, 50);
  assert.equal(state.elements.find((element) => element.dataset.inspect === 'output-j1').scrollLeft, 12);
  assert.equal(container.scrollTop, 200);

  assert.equal(state.writes, 2);
  updateLoopInspector(container, detail, 'v1', '', 'settings', 'j1');
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
  updateLoopInspector(container,detail,'v1','','settings');
  assert.equal(state.buttons.length,2);
  doc.activeElement=state.buttons.find((button)=>button.dataset.focus==='inspect-second-v1');
  detail.jobs[0].output='Changed output';
  updateLoopInspector(container,detail,'v1','','settings');
  assert.equal(doc.activeElement,state.buttons.find((button)=>button.dataset.focus==='inspect-second-v1'));
  assert.equal(doc.activeElement.dataset.focus,'inspect-second-v1');
});

function loopAppHarness(overrides = {}) {
  const vm = require('node:vm');
  const elements = new Map();
  const handlers = new Map();
  const calls = [];
  const fixture = (id) => ({loop:{id,name:id,description:'Task',criteria:'Good',example:'Input',projectId:'project-first'},versions:[
    {id:`${id}-v2`,mode:'project',number:2,yaml:'agents: {}',createdAt:'2026-10-20T00:00:00Z'},
    {id:`${id}-v1`,mode:'project',number:1,yaml:'agents: {}',createdAt:'2026-10-20T00:00:00Z'},
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
    window:{addEventListener:(name,callback)=>handlers.set(name,callback),confirm:()=>true},
    location:{hash:'#board'},crypto:require('node:crypto').webcrypto,setTimeout:overrides.setTimeout||setTimeout,clearTimeout,console, ...markdownModule,
  });
  const api=async(path,options)=>{
    calls.push({path,options});
    if (overrides.api) { const result = overrides.api(path,options); if(result !== undefined) return result; }
    if(path.endsWith('/tasks') && !options?.method) return [];
    if(path==='/projects') return [{id:'project-first',name:'<Project one>',repoPath:'/repo/one',agent:'project-agent'},{id:'project-second',name:'Project two',repoPath:'/repo/two',agent:'second-agent'}];
    if(path==='/loops') return [fixture('first').loop,fixture('second').loop];
    if(options?.method==='POST') return {id:'imported'};
    return fixture(path.endsWith('/overview')?path.split('/').at(-2):path.split('/').at(-1));
  };
  context.api=api;
  vm.runInContext(source.replace(/^import .*;\n/gm,'').replace(/^export /gm,'')+'\ninitLoops(api);',context);
  return {
    calls,element,
    async refresh(){handlers.get('board-refresh')();await new Promise(setImmediate);},
    async click(selector,dataset) {
      await element('loop-detail').handlers.click({target:{closest:(value)=>value===selector?{dataset,disabled:false,textContent:'Close'}:null}});
      await new Promise(setImmediate);
    },
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
      assert.match(element('loop-detail').innerHTML,new RegExp(`<h2>${id}</h2>`));
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
 h.element('loop-mode').value='analysis';h.element('loop-name').value='Review';h.element('loop-task').value='Review code';h.element('loop-criteria').value='Precise';h.element('loop-first-project').value='project-first';
 await h.submit('loop-new-form');
 const request=h.calls.find((call)=>call.path==='/loops' && call.options?.method==='POST');
 assert.deepEqual(JSON.parse(request.options.body),{name:'Review',description:'Review code',criteria:'Precise',projectId:'project-first',mode:'project'});
});

test('newly created loop clearly shows automatic agent creation without another generate action', async () => {
  const { renderLoopDetail } = await modulePromise;
  const detail={loop:{id:'new',name:'Review',description:'Review code',criteria:'Accurate'},versions:[],jobs:[{id:'generation',kind:'generate',status:'running',metrics:{}}]};
  const html=renderLoopDetail(detail,'');
  assert.match(html,/Creating agent/);
  assert.match(html,/role="status" aria-live="polite"/);
  assert.doesNotMatch(html,/data-action="generate"/);
  assert.match(html,/data-cancel="generation"/);
  assert.match(html,/data-loop-panel="agent"/);
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
  assert.equal((html.match(/data-result-body="j1"/g)||[]).length,1);
});

test('failed generation offers retry while a ready agent offers a run', async () => {
  const { renderLoopDetail }=await modulePromise;
  const detail={loop:{id:'new',name:'Review',description:'Task',criteria:'Good'},versions:[],jobs:[{id:'generation',kind:'generate',status:'failed',error:'Provider unavailable',metrics:{}}]};
  assert.match(renderLoopDetail(detail,''),/Agent creation failed/);
  assert.match(renderLoopDetail(detail,''),/Retry creation/);
  detail.jobs[0].status='succeeded';
  detail.versions=[{id:'v1',mode:'project',number:1,yaml:'agents: {}'}];
  const html=renderLoopDetail(detail,'v1');
  assert.match(html,/data-action="project-task"/);
  assert.match(html,/data-action="project-task"/);
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
  const detail={loop:{id:'l',name:'Loop',description:'Task',criteria:'Good'},versions:[{id:'import',mode:'project',number:1,yaml:'agents: {}'}],jobs:[{id:'g',kind:'generate',status,error:'Stopped',metrics:{}}]};
  const html=renderLoopDetail(detail,'import');
  assert.match(html,/data-action="project-task"/);
  assert.doesNotMatch(html,/data-action="generate"/);
 }
});

test('explicit version inspection reveals configuration instead of only changing hidden YAML', async () => {
 const h=loopAppHarness();
 await h.navigate('first');
 await h.selectVersion('first-v2');
 assert.match(h.element('loop-detail').innerHTML,/Agent instruction/);
 assert.match(h.element('loop-detail').innerHTML,/data-version="first-v2"/);
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
 assert.match(ready,/data-action="project-task"/);
 assert.doesNotMatch(ready,/data-loop-panel="optimize"|data-mode=/);
 assert.doesNotMatch(ready,/whenever you’re ready|Improve this agent|immutable snapshots|REUSABLE INTELLIGENCE/);
 detail.jobs=[{id:'g',kind:'generate',status:'failed',error:'Provider unavailable',metrics:{}}];
 detail.loadedJob=detail.jobs[0];
 assert.match(renderLoopDetail(detail,'v1','','settings',detail.jobs[0].id),/Provider unavailable/);
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

function richInspectorHarness() {
  const { container, state, doc } = inspectorHarness();
  const setter = Object.getOwnPropertyDescriptor(container, 'innerHTML').set;
  let body = null;
  function richBody(id, content) {
    const block = { scrollTop: 0, scrollLeft: 0, content };
    return {
      dataset: { resultBody: id }, ownerDocument: doc, childNodes: [block],
      querySelectorAll() { return this.childNodes; },
      replaceChildren(...children) { this.childNodes = children; },
    };
  }
  doc.createElement = () => ({ childNodes: [], append(...children) { this.childNodes.push(...children); } });
  container.querySelector = () => body;
  Object.defineProperty(container, 'innerHTML', { set(html) {
    setter.call(container, html);
    const id = html.match(/data-result-body="([^"]+)"/)?.[1];
    const content = html.match(/data-inspect="latest-output-[^"]+">([\s\S]*?)<\/div>/)?.[1] || '';
    body = id ? richBody(id, content) : null;
  }});
  return { container, state, body: () => body };
}

test('metadata updates reuse rich result nodes and changed output preserves code scrolling', async () => {
  const { updateLoopInspector } = await modulePromise;
  const h = richInspectorHarness();
  const detail = inspectorFixture();
  detail.jobs[0].output = '```js\nconst x = 1;\n```';
  updateLoopInspector(h.container, detail, 'v1');
  const block = h.body().childNodes[0];
  block.scrollLeft = 500;
  detail.jobs[0].metrics.durationMs = 100;
  updateLoopInspector(h.container, detail, 'v1');
  assert.equal(h.body().childNodes[0], block);
  assert.equal(h.body().childNodes[0].scrollLeft, 500);
  detail.jobs[0].output = '```js\nconst x = 2;\n```';
  updateLoopInspector(h.container, detail, 'v1');
  assert.notEqual(h.body().childNodes[0], block);
  assert.equal(h.body().childNodes[0].scrollLeft, 500);
});

test('empty finished results replace the live waiting placeholder', async () => {
 const {updateLoopInspector}=await modulePromise;
 for(const status of ['succeeded','failed','canceled']) {
  const h=richInspectorHarness(), detail=inspectorFixture();
  detail.jobs[0].output='';
  updateLoopInspector(h.container,detail,'v1');
  assert.match(h.body().childNodes[0].content,/Waiting for the first response/);
  detail.jobs[0].status=status;
  updateLoopInspector(h.container,detail,'v1');
  assert.match(h.body().childNodes[0].content,/No output/);
  assert.doesNotMatch(h.body().childNodes[0].content,/Waiting/);
 }
});

test('iterative optimization reports blocked and exhausted outcomes instead of success', async () => {
 const {roundLabel,renderRound}=await modulePromise;
 assert.match(roundLabel({status:'blocked'}),/runtime cannot meet/);
 assert.match(roundLabel({status:'exhausted'}),/not met/);
 assert.match(roundLabel({status:'completed',outcome:'unchanged'}),/Kept the passing baseline/);
 const html=renderRound({id:'r',status:'blocked',inputs:['input'],repeats:2,candidates:[],error:'Cannot create signed commits in read-only mode'},[],[],'');
 assert.match(html,/Cannot create signed commits/);
 assert.doesNotMatch(html,/Passing improvement found/);
});

test('optimizer progress includes pass number and current stage', async () => {
 const {loopActivity}=await modulePromise;
 const detail=inspectorFixture();
 detail.jobs=[];
 detail.rounds=[{status:'testing',pass:3,maxPasses:4,stage:'Evaluating candidates'}];
 assert.equal(loopActivity(detail).note,'Pass 3/4 · Evaluating candidates');
});

test('iterative candidates need confirmation and only the winner is offered for promotion', async () => {
 const {summarizeCandidate,renderRound}=await modulePromise;
 const trial={jobId:'run',judgeId:'judge',case:0,repeat:0,passed:true,reason:'Correct'};
 const candidate={versionId:'v2',pass:1,label:'Candidate',trials:[trial]};
 const round={id:'round',status:'completed',outcome:'improved',maxPasses:4,winner:'v2',inputs:['input'],repeats:1,candidates:[candidate]};
 const jobs=[{id:'run',kind:'test',status:'succeeded',versionId:'v2',metrics:{cost:.1,durationMs:100}},{id:'judge',kind:'judge',status:'succeeded',versionId:'v2'}];
 assert.equal(summarizeCandidate(round,candidate,jobs).qualified,false);
 candidate.confirmation=[{...trial,jobId:'confirm',judgeId:'confirm-judge'}];
 jobs.push({...jobs[0],id:'confirm'},{...jobs[1],id:'confirm-judge'});
 assert.equal(summarizeCandidate(round,candidate,jobs).qualified,true);
 const html=renderRound(round,jobs,[{id:'v2',mode:'project',number:2}],'');
 assert.match(html,/Recommended: v2/);
 assert.match(html,/Confirmation · input 1/);
 round.winner='other';
 assert.match(renderRound(round,jobs,[{id:'v2',mode:'project',number:2}],''),/data-promote="v2"[^>]*disabled/);
});

test('custom model settings preserve blank choices without substituting auto', async () => {
 for(const models of [['openai/test',''],['','openai/test'],['','']]) {
  const h=loopAppHarness();await h.navigate('first');await h.action('optimize-settings');
  h.element('loop-test-project').value='project-first';h.element('loop-test-inputs').value='input';h.element('loop-test-repeats').value='2';
  h.element('loop-model-one').value=models[0];h.element('loop-model-two').value=models[1];
  await h.submit('loop-optimize-form');
  const request=h.calls.find((call)=>call.path==='/loops/first/optimize');
  assert.deepEqual(JSON.parse(request.options.body).models,models.some(Boolean)?models:[]);
 }
});

test('project tasks require fresh explicit consent and submit the reviewed agent', async () => {
 const h=loopAppHarness();await h.navigate('first');await h.action('project-task');
 assert.equal(h.element('loop-project-task-dialog').open,true);
 assert.equal(h.element('loop-task-consent').checked,false);
 await h.submit('loop-project-task-form');
 assert.equal(h.calls.filter((call)=>call.path.endsWith('/tasks') && call.options?.method==='POST').length,0);
 h.element('loop-task-consent').checked=true;
 await h.submit('loop-project-task-form');
 const request=h.calls.find((call)=>call.path==='/loops/first/tasks' && call.options?.method==='POST');
 assert.ok(request);
 const input=JSON.parse(request.options.body);
 assert.equal(input.confirmed,true);assert.equal(input.agent,'project-agent');assert.equal(input.repoPath,'/repo/one');assert.ok(input.requestId);
});

test('idle Board task status never claims verified success', async () => {
 const {renderProjectTask}=await modulePromise;
 const html=renderProjectTask({status:'launched',projectName:'Repo',agent:'agent',card:{id:'c',branch:'b',status:'waiting'}});
 assert.match(html,/Idle — verify results/);
 assert.doesNotMatch(html,/succeeded|CI passed/);
 assert.match(html,/data-task-terminal="c"/);
});

test('Run always starts actual project execution', async () => {
 const {renderLoopDetail}=await modulePromise;
 const detail=inspectorFixture();detail.jobs=[];
 const html=renderLoopDetail(detail,'v1');
 assert.match(html,/Next run:/);
 detail.loop.mode='project';detail.versions[0].mode='project';
 assert.match(renderLoopDetail(detail,'v1'),/data-action="project-task"/);
});

test('closing a result uses the close endpoint and does not delete evidence', async () => {
 const h=loopAppHarness();await h.navigate('first');
 await h.click('[data-close-run]',{closeRun:'run-one'});
 const request=h.calls.find((call)=>call.path==='/loops/first/runs/run-one/close');
 assert.equal(request.options.method,'POST');
 assert.equal(h.calls.filter((call)=>call.options?.method==='DELETE').length,0);
});

test('closed results leave the active view but remain in history and accounting', async () => {
 const {renderLoopDetail,loopActivity,summarizeSpend}=await modulePromise;
 const detail=inspectorFixture();
 detail.jobs[0].status='succeeded';detail.jobs[0].closedAt='2026-10-20T00:00:00Z';detail.jobs[0].metrics.cost=.1;
 assert.equal(loopActivity(detail).title,'Ready');
 const html=renderLoopDetail(detail,'v1');
 assert.match(html,/data-select-run="j1"/);
 assert.match(html,/closed/);
 assert.equal(summarizeSpend(detail.jobs).execution.cost,.1);
});

test('closed project tasks retain diff access without offering a live terminal', async () => {
 const {renderProjectTask,renderLoopDetail}=await modulePromise;
 const task={id:'task',status:'closed',projectName:'Repo',agent:'agent',card:{id:'card',branch:'branch',status:'closed'}};
 const html=renderProjectTask(task);
 assert.match(html,/Closed/);assert.match(html,/data-task-diff="card"/);
 assert.doesNotMatch(html,/data-task-terminal|data-close-task/);
 const detail=inspectorFixture();detail.projectTasks=[task];
 assert.match(renderLoopDetail(detail,'v1'),/data-select-run=/);
});

test('workflow selection uses only matching execution type', async () => {
 const {selectedLoopVersion,renderLoopDetail}=await modulePromise;
 const detail={loop:{id:'l',name:'Work',mode:'project'},versions:[{id:'a',mode:'analysis',number:2},{id:'p',mode:'project',number:1,yaml:'agents: {}'}],jobs:[]};
 assert.equal(selectedLoopVersion(detail,'a').id,'p');
 const html=renderLoopDetail(detail,'p');
 assert.match(html,/data-action="project-task"/);
 assert.doesNotMatch(html,/data-action="run"/);
 const optimization=renderLoopDetail(detail,'p','','optimize');
 assert.doesNotMatch(optimization,/Project optimization is not yet available/);
 assert.doesNotMatch(optimization,/data-action="optimize"/);
});

test('run history lets a closed result be selected without losing the current execution', async () => {
 const {renderLoopDetail}=await modulePromise;
 const detail=inspectorFixture();
 detail.jobs=[{id:'new',kind:'run',status:'running',output:'Current'},{id:'old',kind:'run',status:'succeeded',closedAt:'yesterday',output:'Archived result'}];
 detail.loadedJob=detail.jobs[1];
 const html=renderLoopDetail(detail,'v1','','runs','old');
 assert.match(html,/Archived result/);
 assert.match(html,/Agent running/);
 assert.match(html,/data-select-run="old" aria-pressed="true"/);
 assert.match(html,/data-select-run="new"/);
});

test('Runs view is focused while advanced controls remain in their tabs', async () => {
 const {renderLoopDetail}=await modulePromise;
 const detail=inspectorFixture();
 const runs=renderLoopDetail(detail,'v1');
 assert.match(runs,/data-loop-panel="agent"/);
 assert.match(runs,/data-loop-panel="settings"/);
 assert.doesNotMatch(runs,/<pre class="loop-code"/);
 const agent=renderLoopDetail(detail,'v1','','agent');
 assert.match(agent,/<pre class="loop-code"/);
 assert.match(agent,/Generate new version/);
 const settings=renderLoopDetail(detail,'v1','','settings');
 assert.doesNotMatch(settings,/data-mode=/);
 assert.match(settings,/Import instruction/);
});

test('slow selected-result requests are coalesced across refreshes', async () => {
 let resolve;const pending=new Promise(r=>resolve=r);
 const summary={loop:{id:'first',name:'first',mode:'analysis'},versions:[{id:'first-v1',mode:'project',number:1}],jobs:[{id:'run',kind:'run',status:'succeeded',outputSize:10,createdAt:'now'}],rounds:[],feedback:[]};
 const h=loopAppHarness({api:(path)=>path==='/loops/first/overview'?summary:path==='/loops/first/jobs/run'?pending:undefined});
 await h.navigate('first');await h.refresh();await h.refresh();
 assert.equal(h.calls.filter(c=>c.path==='/loops/first/jobs/run').length,1);
 resolve({...summary.jobs[0],output:'Loaded result'});await new Promise(setImmediate);
 assert.match(h.element('loop-detail').innerHTML,/Loaded result/);
});

test('technical-job selection does not replace Runs selection', async () => {
 const summary={loop:{id:'first',name:'first'},versions:[{id:'first-v1',mode:'project',number:1}],jobs:[{id:'run',kind:'run',status:'succeeded',outputSize:3},{id:'design',kind:'generate',status:'succeeded',outputSize:3}],rounds:[],feedback:[]};
 const h=loopAppHarness({api:(path)=>path==='/loops/first/overview'?summary:path==='/loops/first/jobs/run'?{...summary.jobs[0],output:'Run answer'}:path==='/loops/first/jobs/design'?{...summary.jobs[1],output:'Design answer'}:undefined});
 await h.navigate('first');
 await h.click('[data-loop-panel]',{loopPanel:'settings'});
 await h.click('[data-select-run], [data-select-job]',{selectJob:'design'});
 await h.click('[data-loop-panel]',{loopPanel:'runs'});
 assert.match(h.element('loop-detail').innerHTML,/Run answer/);
 assert.doesNotMatch(h.element('loop-detail').innerHTML,/Design answer/);
});

test('newly submitted run becomes the selected result', async () => {
 const summary={loop:{id:'first',name:'first'},versions:[{id:'first-v1',mode:'project',number:1}],jobs:[{id:'old',kind:'run',status:'succeeded',outputSize:3}],rounds:[],feedback:[]};
 const h=loopAppHarness({api:(path,opts)=>{
  if(path==='/loops/first/overview')return summary;
  if(path==='/loops/first/runs'&&opts?.method==='POST'){summary.jobs.unshift({id:'new',kind:'run',status:'succeeded',outputSize:3});return{id:'new'};}
  if(path.startsWith('/loops/first/jobs/'))return{...summary.jobs.find(j=>j.id===path.split('/').at(-1)),output:path.endsWith('/new')?'New answer':'Old answer'};
 }});
 await h.navigate('first');await h.action('run');h.element('loop-run-project').value='project-first';await h.submit('loop-run-form');await new Promise(setImmediate);
 assert.match(h.element('loop-detail').innerHTML,/New answer/);
 assert.doesNotMatch(h.element('loop-detail').innerHTML,/Old answer/);
});

test('returning to a pending result renders its response without requiring another refresh', async () => {
 let resolve;const pending=new Promise(r=>resolve=r);
 const summary={loop:{id:'first',name:'first'},versions:[{id:'first-v1',mode:'project',number:1}],jobs:[{id:'a',kind:'run',status:'succeeded',outputSize:3},{id:'b',kind:'run',status:'succeeded',outputSize:3}],rounds:[]};
 const h=loopAppHarness({api:(path)=>path==='/loops/first/overview'?summary:path==='/loops/first/jobs/a'?pending:path==='/loops/first/jobs/b'?{...summary.jobs[1],output:'B answer'}:undefined});
 await h.navigate('first');
 await h.click('[data-select-run], [data-select-job]',{selectRun:'b'});
 await h.click('[data-select-run], [data-select-job]',{selectRun:'a'});
 resolve({...summary.jobs[0],output:'A answer'});await new Promise(setImmediate);
 assert.match(h.element('loop-detail').innerHTML,/A answer/);
});

test('legacy conversion completing after navigation leaves the other loop selection intact', async () => {
 let resolve;const pending=new Promise(r=>resolve=r);
 const h=loopAppHarness({api:(path,opts)=>path==='/loops/first/mode'&&opts?.method==='PUT'?pending:undefined});
 await h.navigate('first');
 const changing=h.action('generate');
 await new Promise(setImmediate);
 await h.navigate('second');await h.selectVersion('second-v2');
 resolve({mode:'project'});await changing;
 assert.match(h.element('loop-detail').innerHTML,/btn-primary" data-version="second-v2"/);
});

test('legacy blocked assessments do not show obsolete mode controls', async () => {
 const {renderLoopDetail}=await modulePromise;const detail=inspectorFixture();detail.jobs=[];
 detail.rounds=[{status:'blocked',outcome:'capability_blocked',error:'Needs file writes',inputs:[],candidates:[]}];
 const html=renderLoopDetail(detail,'v1');
 assert.doesNotMatch(html,/Last optimization was blocked/);
 assert.doesNotMatch(html,/data-mode=/);
 assert.doesNotMatch(html,/data-action="run" disabled/);
});

test('old blocked assessments do not override newer completed experiments', async () => {
 const {renderLoopDetail}=await modulePromise;const detail=inspectorFixture();detail.jobs=[];
 detail.rounds=[{status:'completed',outcome:'unchanged'},{status:'blocked',outcome:'capability_blocked',error:'Missing file'}];
 const html=renderLoopDetail(detail,'v1');assert.doesNotMatch(html,/Last optimization was blocked/);
 assert.match(html,/<nav class="loop-tabs" aria-label="Loop views">/);
});

test('actual runs are chronological and show frozen metrics, judge and untested next version', async () => {
 const {renderLoopDetail}=await modulePromise;
 const detail=inspectorFixture();detail.loop.activeVersion='v2';detail.versions.unshift({id:'v2',mode:'project',number:2});
 detail.jobs=[{id:'design',kind:'run_revision',status:'succeeded',metrics:{cost:99}}];
 detail.projectTasks=[{id:'later',createdAt:'2026-10-21T12:00:00Z',versionId:'v2',projectName:'Repo two',status:'launched',review:{state:'processing',cost:null,durationMs:null,outcome:'normal'}},
 {id:'earlier',createdAt:'2026-10-21T11:00:00Z',versionId:'v1',projectName:'Repo one',status:'closed',card:{status:'closed',cost:200},review:{state:'ready',cost:.12,durationMs:65000,outcome:'normal',nextVersion:'v2',assessment:{quality:'failed',efficiency:'inefficient',changeImpact:'hurt',summary:'Tests missing',improvements:[]}}}];
 const html=renderLoopDetail(detail,'v2');
 assert.ok(html.indexOf('data-select-run="earlier"')<html.indexOf('data-select-run="later"'));
 assert.match(html,/\$0\.1200/);assert.match(html,/1m 5s/);assert.match(html,/failed · inefficient · hurt/);
 assert.match(html,/v2 · run recorded/);assert.match(html,/Repo one/);assert.match(html,/Repo two/);
 assert.doesNotMatch(html,/data-select-run="design"|\$200|\$99/);
});

test('automatic preparation follows the next pointer unless an old version was explicitly selected', async () => {
 const summary={loop:{id:'first',name:'first',mode:'project',activeVersion:'v1'},versions:[{id:'v1',mode:'project',number:1}],jobs:[],rounds:[]};
 const h=loopAppHarness({api:(path)=>path==='/loops/first/overview'?summary:undefined});
 await h.navigate('first');
 summary.versions.unshift({id:'v2',mode:'project',number:2});summary.loop.activeVersion='v2';
 await h.refresh();assert.match(h.element('loop-detail').innerHTML,/Next run: v2/);
 await h.selectVersion('v1');
 summary.versions.unshift({id:'v3',mode:'project',number:3});summary.loop.activeVersion='v3';
 await h.refresh();assert.match(h.element('loop-detail').innerHTML,/Next run: v1/);
});

test('idle task keeps polling while automatic review is pending', async () => {
 let scheduled=0;
 const h=loopAppHarness({setTimeout:()=>{scheduled++;return 1;},api:(path)=>path==='/loops/first/tasks'?[{id:'t',card:{status:'waiting'},review:{state:'processing',lastSeq:10}}]:path.endsWith('/evidence')?{events:[]}:undefined});
 await h.navigate('first');assert.ok(scheduled>0);
 assert.match(h.element('loop-detail').innerHTML,/Judging and preparing next version/);
});

test('preparation retry never calls the task execution endpoint', async () => {
 const h=loopAppHarness();await h.navigate('first');
 await h.click('[data-retry-learning]',{retryLearning:'run-one'});
 const requests=h.calls.filter(c=>c.options?.method==='POST');
 assert.deepEqual(requests.map(c=>c.path),['/loops/first/tasks/run-one/prepare']);
});

test('legacy snapshot agents remain inspectable but cannot be launched as task agents', async () => {
 const {renderLoopDetail}=await modulePromise;
 const detail={loop:{id:'l',name:'Old',mode:'analysis'},versions:[{id:'old',mode:'analysis',number:1,yaml:'instruction: report'}]};
 const html=renderLoopDetail(detail);
 assert.match(html,/Create a task agent/);assert.match(html,/data-action="generate"/);
 assert.doesNotMatch(html,/data-action="project-task"|data-mode=|data-loop-panel="optimize"/);
 assert.match(renderLoopDetail(detail,'','','settings'),/Legacy agent versions/);
 assert.match(renderLoopDetail(detail,'','','settings'),/instruction: report/);
});

test('project run evidence renders safe Markdown with source and copy', async () => {
 const {renderProjectTask}=await modulePromise;
 const task={id:'task',loopId:'loop',projectName:'Repo',status:'closed',review:{state:'failed',finishedAt:'now',complete:false,error:'<error>'}};
 const evidence={taskID:'task',events:[{type:'agent_choice',content:'# Done\n\n**Result** <script>bad</script>'}]};
 const html=renderProjectTask(task,[],evidence);
 assert.match(html,/<h1>Done<\/h1>/);assert.match(html,/<strong>Result<\/strong>/);
 assert.doesNotMatch(html,/<script>/);assert.match(html,/data-copy-result="task"/);assert.match(html,/data-source="task"/);
 assert.match(html,/Evidence incomplete/);assert.match(html,/Retry preparation/);
 assert.match(renderProjectTask(task,[],evidence,true),/loop-source-text/);
});

test('a delayed active-run response triggers a fresh read after final preparation', async () => {
 let resolve;const pending=new Promise(r=>resolve=r);let requests=0;
 const task={id:'t',loopId:'first',status:'launched',card:{status:'waiting'},review:{state:'observing',lastSeq:3}};
 const h=loopAppHarness({api:(path)=>path==='/loops/first/tasks'?[task]:path.endsWith('/tasks/t/evidence')?(++requests===1?pending:{events:[{type:'agent_choice',content:'Final output'}]}):undefined});
 await h.navigate('first');task.review={state:'ready',lastSeq:8};await h.refresh();
 assert.equal(requests,1);
 resolve({events:[{type:'agent_choice',content:'Partial output'}]});await new Promise(setImmediate);await new Promise(setImmediate);
 assert.equal(requests,2);assert.match(h.element('loop-detail').innerHTML,/Final output/);assert.doesNotMatch(h.element('loop-detail').innerHTML,/Partial output/);
});

test('recorded result contains only the final root message with its chunks combined', async () => {
 const {renderProjectTask}=await modulePromise;
 const task={id:'t',status:'closed'};
 const evidence={taskID:'t',events:[{type:'stream_started',session_id:'root'},
 {type:'agent_choice',session_id:'root',message_id:'plan',content:'I will inspect.'},
 {type:'agent_choice',session_id:'child',message_id:'child',content:'Child report'},
 {type:'agent_choice',session_id:'root',message_id:'answer',content:'# Final'},
 {type:'agent_choice',session_id:'child',message_id:'child',content:' unrelated'},
 {type:'agent_choice',session_id:'root',message_id:'answer',content:' answer'}]};
 const html=renderProjectTask(task,[],evidence);
 assert.match(html,/<h1>Final answer<\/h1>/);assert.doesNotMatch(html,/I will inspect|Child report|unrelated/);
});

test('late project evidence cannot replace the selected technical job', async () => {
 let resolve;const pending=new Promise(r=>resolve=r);
 const summary={loop:{id:'first',name:'first',mode:'project'},versions:[{id:'v1',mode:'project',number:1}],jobs:[{id:'judge',kind:'run_judge',status:'succeeded',outputSize:12}],rounds:[]};
 const h=loopAppHarness({api:(path)=>path==='/loops/first/overview'?summary:path==='/loops/first/tasks'?[{id:'t',status:'closed',review:{state:'ready',lastSeq:4}}]:path.endsWith('/tasks/t/evidence')?pending:path.endsWith('/jobs/judge')?{...summary.jobs[0],output:'Selected technical result'}:undefined});
 await h.navigate('first');await h.click('[data-loop-panel]',{loopPanel:'settings'});await h.click('[data-select-run], [data-select-job]',{selectJob:'judge'});
 assert.match(h.element('loop-detail').innerHTML,/Selected technical result/);
 resolve({events:[]});await new Promise(setImmediate);
 assert.match(h.element('loop-detail').innerHTML,/Selected technical result/);
});
