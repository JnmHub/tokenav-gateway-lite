import { createServer } from 'node:http'
import { once } from 'node:events'
import { before, after, beforeEach, afterEach, test } from 'node:test'
import { readFile } from 'node:fs/promises'
import { extname } from 'node:path'
import { parse, compileScript } from 'vue/compiler-sfc'
import { createRenderer, h, reactive, nextTick } from 'vue'
import { createPinia } from 'pinia'
import { message, Modal } from 'ant-design-vue'
import assert from 'node:assert/strict'
import { setToken, resetRequestSession } from './index.js'
import { provideWindowContext } from '../core/windowContext.js'
import { quotaPath } from '../components/account/quotaApi.js'
import { readAccountTestStream } from './accountTestStream.js'
import { buildBulkHeaderPatch } from '../components/account/bulkHeaderPatch.js'

// 第二轮独立证据：实际组件 setup + 本机 HTTP，合成配置，不访问数据库/浏览器/秘密。
const store = new Map()
globalThis.localStorage = { getItem: k => store.get(k) ?? null, setItem: (k,v) => store.set(k,String(v)), removeItem: k => store.delete(k) }
globalThis.window = { location: { origin: '' }, dispatchEvent() {} }
globalThis.document = { hidden: false }
const nativeFetch = globalThis.fetch
const notes = []
for (const key of ['success','error','warning','info']) message[key] = text => notes.push({key,text})
Modal.confirm = options => ({ destroy() {}, options })
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r }); return { promise, resolve } }
const pauses = new Map(), arrivals = new Map(), records = []
let server, settings, config, policies, mounts = [], bulkWarning, omitSettings, omitConfig, failPolicy, failCompliance
const clone = v => JSON.parse(JSON.stringify(v))
function pause(path, method = 'GET') {
  const arrival = deferred(), release = deferred(), done = deferred()
  pauses.set(`${method} ${path}`, { arrival, release, done })
  return { seen: arrival.promise, release: release.resolve, done: done.promise }
}
function ok(res,data) { res.writeHead(200, {'Content-Type':'application/json'}); res.end(JSON.stringify({code:0,data})) }
function defaults() {
  return { version:'0',site_name:'合成站点',site_logo:'',site_subtitle:'',api_base_url:'',contact_info:'',doc_url:'',backend_mode_enabled:true,hide_ccs_import_button:false,table_default_page_size:20,table_page_size_options:[10,20,50],
    channel_monitor_enabled:true,channel_monitor_mode:'v1',channel_monitor_default_interval_seconds:60,channel_monitor_hide_throughput:true,risk_control_enabled:true,cyber_session_block_enabled:false,cyber_session_block_ttl_seconds:3600,cyber_policy_user_allowlist:'',
    totp_enabled:true,turnstile_enabled:false,turnstile_site_key:'',turnstile_secret_key_configured:false,api_key_acl_trust_forwarded_ip:false,forwarded_client_ip_headers:[],
    gateway_custom_error_message:'',enable_model_fallback:false,fallback_model_anthropic:'',fallback_model_openai:'',fallback_model_gemini:'',fallback_model_antigravity:'',enable_identity_patch:false,identity_patch_prompt:'',
    min_claude_code_version:'',max_claude_code_version:'',antigravity_user_agent_version:'',openai_codex_user_agent:'',openai_codex_client_version:'',openai_codex_client_version_synced:'',openai_codex_version_auto_sync_enabled:true,
    allow_ungrouped_key_scheduling:false,enable_fingerprint_unification:false,enable_metadata_passthrough:false,enable_cch_signing:false,enable_anthropic_cache_ttl_1h_injection:false,rewrite_message_cache_control:false,openai_allow_claude_code_codex_plugin:false,enable_client_dateline_normalization:true,
    grok_default_text_model:'grok-4.5',grok_cross_client_model_map_enabled:false,grok_scheduling_threshold_percent:100,upstream_billing_auto_probe_enabled:false,upstream_billing_probe_interval_minutes:30,upstream_billing_probe_batch_size:20,upstream_billing_probe_concurrency:4,
    openai_fast_policy_settings:{version:1,priority_detection_by_request_enabled:true,priority_detection_by_response_enabled:true,rules:[]},enable_claude_oauth_system_prompt_injection:false,claude_oauth_system_prompt:'',claude_oauth_system_prompt_blocks:'' }
}
const renderer = createRenderer({ createElement:()=>({}),createText:()=>({}),createComment:()=>({}),setText(){},setElementText(){},parentNode:()=>null,nextSibling:()=>null,insert(){},remove(){},patchProp(){} })
const cache = new Map()
async function setupFor(path) {
  if(cache.has(path)) return cache.get(path)
  const url = new URL(path,import.meta.url), source = await readFile(url,'utf8')
  const {descriptor,errors} = parse(source,{filename:url.pathname}); assert.equal(errors.length,0)
  const compiled = compileScript(descriptor,{id:path}).content
  const modules=[],decl=[]
  for(const [statement,specifier] of compiled.matchAll(/^import[\s\S]*?from ['"]([^'"]+)['"];?\s*$/gm)) {
    let module
    if(specifier.endsWith('.vue') || specifier.endsWith('/core/apps')) module={default:{},openApp(){}}
    else { let target=specifier; if(specifier.startsWith('.')) { const u=new URL(specifier,url);if(!extname(u.pathname))u.pathname += specifier.endsWith('/api')?'/index.js':'.js';target=u.href } module=await import(target) }
    const clause=statement.slice(6,statement.lastIndexOf('from')).trim(),index=modules.push(module)-1
    decl.push(clause.startsWith('{')?`const ${clause.replace(/\bas\b/g,':')} = modules[${index}]`:`const ${clause} = modules[${index}].default`)
  }
  const body=compiled.replace(/^import[\s\S]*?from ['"]([^'"]+)['"];?\s*$/gm,'').replace('export default','return')
  const component=new Function('modules',decl.join('\n')+'\n'+body)(modules)
  cache.set(path,component.setup);return component.setup
}
function mount(setup, input={}) {
  let state;const guards={dirty:new Set(),busy:new Set()},events=[]
  const props=reactive(input)
  const child={props:Object.keys(input),emits:['update:open','update:modify','update:enabled','update:rows','close','success','updated'],setup(){state=setup(props,{expose(){},emit:(...args)=>events.push(args)});return ()=>h('div')}}
  const app=renderer.createApp({setup(){provideWindowContext({registerDirty(fn){guards.dirty.add(fn);return ()=>guards.dirty.delete(fn)},registerBusy(fn){guards.busy.add(fn);return ()=>guards.busy.delete(fn)}});return ()=>h(child)}})
  app.use(createPinia());app.mount({});mounts.push(app)
  return {state,props,events,dirty:()=>[...guards.dirty].some(fn=>fn()),busy:()=>[...guards.busy].some(fn=>fn()),guards,
    unmount(){app.unmount();mounts=mounts.filter(x=>x!==app)} }
}
const eventually = (fn) => new Promise((resolve,reject)=>{const until=Date.now()+5000;function tick(){if(fn())resolve();else if(Date.now()>until)reject(new Error('等待组件状态超时'));else setImmediate(tick)}tick()})

before(async()=>{
  server=createServer(async(req,res)=>{
    let text='';for await(const chunk of req)text+=chunk
    const url=new URL(req.url,'http://localhost'),path=url.pathname.replace('/api/v1',''),body=text?JSON.parse(text):null
    const record={path,method:req.method,body,query:Object.fromEntries(url.searchParams)};records.push(record)
    let data
    if(path==='/admin/settings') {
      if(req.method==='PATCH') {
        if(body.version!==settings.version){res.writeHead(409,{'Content-Type':'application/json'});res.end(JSON.stringify({code:409,reason:'SETTINGS_VERSION_CONFLICT',message:'合成冲突'}));return}
        const {version,turnstile_secret_key,...part}=body
        settings={...settings,...part,version:String(Number(version)+1)};if(turnstile_secret_key)settings.turnstile_secret_key_configured=true
      }
      data=clone(settings);if(omitSettings)delete data[omitSettings]
    } else if(path==='/admin/gateway-lite/config') {
      if(req.method==='PUT') { const {control_plane_token,admin_sync_key,...part}=body;config={...config,...part};if(control_plane_token)config.control_plane_token_configured=true;if(admin_sync_key)config.admin_sync_key_configured=true }
      data=clone(config);if(omitConfig)delete data[omitConfig]
    } else if(policies[path]) {
      if(req.method==='PUT' && failPolicy){res.writeHead(503,{'Content-Type':'application/json'});res.end(JSON.stringify({code:503,message:'合成写入失败'}));return}
      if(req.method==='PUT') policies[path]=clone(body)
      data=clone(policies[path])
    } else if(path==='/admin/accounts/bulk-update') {
      if(bulkWarning && !body.confirm_mixed_channel_risk){res.writeHead(409,{'Content-Type':'application/json'});res.end(JSON.stringify({error:'mixed_channel_warning',message:'合成混合渠道风险'}));return}
      data={success:1,failed:0}
    } else if(path.endsWith('/test')) {
      res.writeHead(200,{'Content-Type':'text/event-stream'})
      res.write('data: {"type":"test_start","model":"synthetic"}\n\n')
      const gate=pauses.get(`${req.method} ${path}`)
      if(gate){pauses.delete(`${req.method} ${path}`);gate.arrival.resolve(record);await gate.release.promise}
      res.end('data: {"type":"test_complete","success":true}\n\n');gate?.done.resolve();return
    } else if(path.endsWith('/models')) data=[{id:'synthetic-sonnet'}]
    else if(path==='/admin/groups/all'||path==='/admin/proxies/all')data=[]
    else if(path==='/user/totp/status')data={enabled:false,feature_enabled:true}
    else if(path==='/user/totp/verification-method')data={method:'password'}
    else if(path==='/user/totp/setup')data={secret:'synthetic-secret',qr_code_url:'',setup_token:'synthetic-setup'}
    else if(path==='/admin/compliance'){if(failCompliance){res.writeHead(503,{'Content-Type':'application/json'});res.end(JSON.stringify({code:503,message:'合成读取失败'}));return}data={required:true,version:'test',ack_phrase_zh:'合成确认'}}
    else if(path==='/admin/compliance/accept')data={required:false,version:'test'}
    else if(path==='/admin/usage/search-users')data=[{id:1,email:'synthetic@example.invalid'}]
    else if(path.endsWith('/quota'))data={account:path}
    else data={enabled:false,idempotent_enabled:true,mode:'idempotent_v2'}
    const key=`${req.method} ${path}`,gate=pauses.get(key)
    if(gate){pauses.delete(key);gate.arrival.resolve(record);await gate.release.promise}
    ok(res,data);gate?.done.resolve()
  });server.listen(0,'127.0.0.1');await once(server,'listening');window.location.origin=`http://127.0.0.1:${server.address().port}`
})
beforeEach(()=>{
  globalThis.fetch=nativeFetch;resetRequestSession('test');store.clear();setToken('synthetic-admin');records.length=0;notes.length=0
  settings=defaults();config={region:'default',gateway_code:'old',redis_prefix:'synthetic',control_plane_url:'',control_plane_token_configured:true,admin_sync_key_configured:true,legacy_snapshot_scheduler_authority:false,admin_login_path:'/synthetic',login_title:'合成入口',login_subtitle:'',control_plane_timeout_ms:1000,runtime_health_interval_seconds:15,runtime_active_window_seconds:300,config_sync_interval_seconds:30,cache_invalidation_interval_seconds:5,usage_queue_pending_alert_threshold:1000,usage_queue_dead_alert_threshold:1,allow_live:false}
  policies={ '/admin/settings/overload-cooldown':{enabled:false,cooldown_minutes:10},'/admin/settings/rate-limit-429-cooldown':{enabled:false,cooldown_seconds:5},'/admin/settings/fallback-queue':{enabled:false,max_waiting:100,wait_timeout_seconds:120},'/admin/settings/stream-timeout':{enabled:false,action:'none',temp_unsched_minutes:5,threshold_count:3,threshold_window_minutes:10},'/admin/settings/rectifier':{enabled:false,thinking_signature_enabled:false,thinking_budget_enabled:false,apikey_signature_enabled:false,apikey_signature_patterns:[]},'/admin/settings/beta-policy':{rules:[]} }
  bulkWarning=false;omitSettings='';omitConfig='';failPolicy=false;failCompliance=false
})
afterEach(()=>{for(const app of mounts)app.unmount();mounts=[];for(const gate of pauses.values())gate.release.resolve();pauses.clear()})
after(async()=>{globalThis.fetch=nativeFetch;server.closeAllConnections();await new Promise(r=>server.close(r))})

for(const [name,refName,field,save] of [['GeneralTab','s','site_name','save'],['FeaturesTab','fs','cyber_policy_user_allowlist','saveFs'],['SecurityTab','sec','turnstile_site_key','saveSec'],['GatewayTab','s','gateway_custom_error_message','saveErrMsg']]) {
  test(`${name} 真实基线/窗口保护/保存续改/卸载释放`,async()=>{
    const m=mount(await setupFor(`../components/settings/${name}.vue`)),c=m.state
    await eventually(()=>!c.loading.value && c[refName].value)
    assert.equal(m.dirty(),false);c[refName].value[field]='提交草稿';assert.equal(m.dirty(),true)
    const gate=pause('/admin/settings','PATCH'),saving=c[save]();await gate.seen;assert.equal(m.busy(),true)
    c[refName].value[field]='保存期间续改';gate.release();await saving
    assert.equal(c[refName].value[field],'保存期间续改');assert.equal(m.dirty(),true);assert.equal(m.busy(),false)
    await c[save]();assert.equal(m.dirty(),false)
    m.unmount();assert.equal(m.guards.dirty.size,0);assert.equal(m.guards.busy.size,0);assert.equal(c[refName].value,null)
  })
}
test('基础重新读取/缺字段/冲突保留草稿，未读完整配置不写入',async()=>{
  const m=mount(await setupFor('../components/settings/GeneralTab.vue')),c=m.state;await eventually(()=>!c.loading.value)
  c.s.value.site_name='保留草稿';await c.load();assert.equal(c.s.value.site_name,'保留草稿');assert.equal(m.dirty(),true)
  omitSettings='doc_url';await c.load();assert.match(c.err.value,/不完整/);assert.equal(c.s.value.site_name,'保留草稿')
  omitSettings='';settings.version='99';await c.save();assert.equal(m.dirty(),true);assert.match(c.conflict.value,/草稿/)
})
test('主站只提交当前区、保留另一卡和新令牌，Live不回放旧连接',async()=>{
  const m=mount(await setupFor('../components/settings/ControlPlaneTab.vue')),c=m.state;await eventually(()=>!c.loading.value)
  assert.equal(m.dirty(),false);c.f.value.gateway_code='连接草稿';c.f.value.login_title='提交标题'
  const gate=pause('/admin/gateway-lite/config','PUT'),saving=c.persist('login');await gate.seen;assert.equal(m.busy(),true)
  c.f.value.login_title='续改标题';c.f.value.control_plane_token='synthetic-new-secret';gate.release();await saving
  assert.equal(config.gateway_code,'old');assert.equal(c.f.value.gateway_code,'连接草稿');assert.equal(c.f.value.login_title,'续改标题');assert.equal(c.f.value.control_plane_token,'synthetic-new-secret')
  const f=mount(await setupFor('../components/settings/FeaturesTab.vue')).state;await eventually(()=>!f.loading.value)
  await c.persist('control-plane');await f.toggleLive(true);assert.equal(config.gateway_code,'连接草稿');assert.equal(config.allow_live,true)
  m.unmount();assert.equal(c.f.value,null)
})
test('主站慢读取不反盖保存，关闭后不提示；未知配置禁止全量写入',async()=>{
  const m=mount(await setupFor('../components/settings/ControlPlaneTab.vue')),c=m.state;await eventually(()=>!c.loading.value)
  globalThis.fetch=(url,options)=>nativeFetch(url,{...options,signal:undefined})
  const gate=pause('/admin/gateway-lite/config'),reading=c.load();await gate.seen;c.f.value.gateway_code='新写入';await c.persist('control-plane');gate.release();await reading
  assert.equal(c.f.value.gateway_code,'新写入')
  const late=pause('/admin/gateway-lite/config','PUT'),saving=c.persist('control-plane');await late.seen;m.unmount();notes.length=0;late.release();await saving;assert.equal(notes.length,0)
  globalThis.fetch=nativeFetch
  const n=mount(await setupFor('../components/settings/ControlPlaneTab.vue')).state;await eventually(()=>!n.loading.value);omitConfig='gateway_code'
  const count=records.filter(r=>r.method==='PUT').length;await n.persist('control-plane');assert.equal(records.filter(r=>r.method==='PUT').length,count)
})
test('策略六卡独立基线、失败保留、数组续改及模式输入续改',async()=>{
  const m=mount(await setupFor('../components/settings/PolicyTab.vue')),c=m.state;await eventually(()=>!c.loading.value);assert.equal(m.dirty(),false)
  c.oc.value.cooldown_minutes=12;c.rl.value.cooldown_seconds=9
  const gate=pause('/admin/settings/overload-cooldown','PUT'),saving=c.saveOc();await gate.seen;assert.equal(m.busy(),true);c.oc.value.cooldown_minutes=14;gate.release();await saving
  assert.equal(c.oc.value.cooldown_minutes,14);assert.equal(c.rl.value.cooldown_seconds,9);assert.equal(m.dirty(),true)
  failPolicy=true;await c.saveRl();assert.equal(c.rl.value.cooldown_seconds,9);assert.equal(m.dirty(),true);failPolicy=false
  c.rfPatterns.value='提交模式';const rf=pause('/admin/settings/rectifier','PUT'),writing=c.saveRf();await rf.seen;c.rfPatterns.value='续改模式';rf.release();await writing;assert.equal(c.rfPatterns.value,'续改模式')
  await c.saveOc();await c.saveRl();await c.saveRf();assert.equal(m.dirty(),false)
})
test('六设置会话切换清草稿，忽略取消的迟到保存不显示成功',async()=>{
  for(const [name,refName,save,path] of [['GeneralTab','s','save','/admin/settings'],['FeaturesTab','fs','saveFs','/admin/settings'],['SecurityTab','sec','saveSec','/admin/settings'],['GatewayTab','s','saveErrMsg','/admin/settings'],['ControlPlaneTab','f','persist','/admin/gateway-lite/config'],['PolicyTab','oc','saveOc','/admin/settings/overload-cooldown']]) {
    const m=mount(await setupFor(`../components/settings/${name}.vue`)),c=m.state;await eventually(()=>!c.loading.value)
    globalThis.fetch=(url,options)=>nativeFetch(url,{...options,signal:undefined})
    const gate=pause(path,path==='/admin/settings'?'PATCH':'PUT'),work=name==='ControlPlaneTab'?c.persist('control-plane'):c[save]();await gate.seen
    resetRequestSession('new-user');notes.length=0;gate.release();await work;assert.equal(c[refName].value,null);assert.equal(notes.length,0);m.unmount();globalThis.fetch=nativeFetch
  }
})
test('批量编辑真实裸409可确认，关开后旧响应不清新草稿',async()=>{
  const m=mount(await setupFor('../components/account/BulkEditModal.vue'),{open:false,rows:[{id:1,platform:'openai',type:'oauth'}]}),c=m.state
  c.open.value=true;await nextTick();c.toggle('status',true);bulkWarning=true;await c.submit();assert.match(c.mixedMsg.value,/混合渠道/);await c.confirmMixed();assert.equal(c.open.value,false)
  bulkWarning=false;c.open.value=true;await nextTick();c.toggle('status',true)
  globalThis.fetch=(url,options)=>nativeFetch(url,{...options,signal:undefined})
  const gate=pause('/admin/accounts/bulk-update','POST'),writing=c.submit();await gate.seen;c.open.value=false;c.open.value=true;await nextTick();c.toggle('priority',true);c.v.value.priority=8;notes.length=0
  gate.release();await writing;assert.equal(c.open.value,true);assert.equal(c.v.value.priority,8);assert.equal(notes.length,0)
})
test('配额A到B迟到不反盖，关闭清结果；过期实例不会发起刷新请求',async()=>{
  const m=mount(await setupFor('../components/account/QuotaModal.vue')),c=m.state
  globalThis.fetch=(url,options)=>nativeFetch(url,{...options,signal:undefined})
  const gate=pause('/admin/openai/accounts/1/quota'),old=c.query({id:1,name:'A',platform:'openai'});await gate.seen
  await c.query({id:2,name:'B',platform:'grok'});gate.release();await old;assert.equal(c.title.value,'配额 · B');assert.match(c.kv.value[0][1],/grok\/accounts\/2/)
  c.open.value=false;assert.deepEqual(c.kv.value,[])
  const controller=new AbortController();controller.abort();const count=records.length
  await assert.rejects(quotaPath({id:1,platform:'openai'},'refresh',undefined,{signal:controller.signal}),{name:'AbortError'});assert.equal(records.length,count)
})
test('实际测试弹窗关闭取消流，旧完成片段不出现在重开实例',async()=>{
  const m=mount(await setupFor('../components/account/TestModal.vue'),{open:false,account:{id:1,name:'synthetic',platform:'openai',type:'oauth'}}),c=m.state
  c.open.value=true;await eventually(()=>!c.loadingModels.value && c.modelId.value)
  const gate=pause('/admin/accounts/1/test','POST'),work=c.runTest();await gate.seen;c.close();c.open.value=true;await eventually(()=>!c.loadingModels.value && c.modelId.value);gate.release();await work
  assert.equal(c.status.value,'idle');assert.deepEqual(c.lines.value,[])
})
test('流解析保留CRLF和最后片段；会话重置取消读取及回调',async()=>{
  const events=[];await readAccountTestStream(1,{model_id:'synthetic'}, {},e=>events.push(e));assert.equal(events.at(-1).type,'test_complete')
  const gate=pause('/admin/accounts/1/test','POST'),old=readAccountTestStream(1,{}, {},e=>events.push(e));const rejected=assert.rejects(old,{name:'AbortError'});await gate.seen;const count=events.length;resetRequestSession('new-session');gate.release();await rejected;assert.equal(events.length,count)
})
test('Fast用户搜索清空/卸载取消防抖，不产生迟到结果',async()=>{
  const m=mount(await setupFor('../components/settings/FastPolicyCard.vue'),{model:clone(settings.openai_fast_policy_settings),saving:false}),c=m.state
  c.searchUsers('old');c.searchUsers('');await new Promise(r=>setTimeout(r,350));assert.equal(records.some(r=>r.path==='/admin/usage/search-users'),false)
  globalThis.fetch=(url,options)=>nativeFetch(url,{...options,signal:undefined})
  const gate=pause('/admin/usage/search-users');c.searchUsers('next');await gate.seen;m.unmount();gate.release();await gate.done;await nextTick();assert.deepEqual(c.userOptions.value,[])
})

test('批量覆写实际请求体开启/清空、资格拒绝及风险确认快照',async()=>{
  const m=mount(await setupFor('../components/account/BulkEditModal.vue'),{open:false,rows:[{id:1,platform:'openai',type:'apikey'},{id:2,platform:'kimi',type:'apikey'}]}),c=m.state
  c.open.value=true;await nextTick();assert.equal(c.headerEligible.value,true);c.toggle('header_override',true)
  c.v.value.hdr_on=true;c.v.value.hdr_rows=[{name:' User-Agent ',value:' synthetic-client '},{name:'X-Synthetic',value:''}]
  bulkWarning=true;await c.submit();const original=clone(records.findLast(r=>r.path==='/admin/accounts/bulk-update').body)
  assert.deepEqual(original.credentials,{header_override_enabled:true,header_overrides:{'user-agent':'synthetic-client','x-synthetic':''}})
  c.v.value.hdr_rows=[{name:'Authorization',value:'invalid'}];await c.confirmMixed()
  assert.deepEqual(records.findLast(r=>r.path==='/admin/accounts/bulk-update').body,{...original,confirm_mixed_channel_risk:true})
  c.open.value=true;await nextTick();bulkWarning=false;c.toggle('header_override',true);c.v.value.hdr_on=false;await c.submit()
  assert.deepEqual(records.findLast(r=>r.path==='/admin/accounts/bulk-update').body.credentials,{header_override_enabled:false,header_overrides:{}})
  c.open.value=true;await nextTick();c.toggle('header_override',true);c.v.value.hdr_on=true
  const count=records.filter(r=>r.path==='/admin/accounts/bulk-update').length;await c.submit();assert.equal(records.filter(r=>r.path==='/admin/accounts/bulk-update').length,count)
  m.props.rows=[{id:1,platform:'openai',type:'oauth'}];await nextTick();assert.equal(c.headerEligible.value,false);await c.submit()
  assert.equal(records.filter(r=>r.path==='/admin/accounts/bulk-update').length,count)
})
test('批量请求头边界与UTF-8字节限制、特殊名称正常序列化',()=>{
  for(const rows of [[],[{name:'authorization',value:'x'}],[{name:'X-A',value:'1'},{name:'x-a',value:'2'}],[{name:'bad name',value:'x'}],[{name:'',value:'x'}],[{name:'a'.repeat(201),value:'x'}],[{name:'x-a',value:'a\nb'}],[{name:'x-a',value:'中'.repeat(2731)}],Array.from({length:65},(_,i)=>({name:'x-'+i,value:''}))])assert.throws(()=>buildBulkHeaderPatch(true,rows))
  assert.equal(buildBulkHeaderPatch(true,[{name:'x-a',value:'a'.repeat(8192)}]).header_overrides['x-a'].length,8192)
  const special=buildBulkHeaderPatch(true,[{name:'__proto__',value:'synthetic'}]);assert.equal(JSON.parse(JSON.stringify(special)).header_overrides.__proto__,'synthetic')
})
test('请求头模板保留已填写值并补齐缺项',async()=>{
  const c=mount(await setupFor('../components/account/BulkHeaderFields.vue'),{modify:true,enabled:true,rows:[{name:'User-Agent',value:'synthetic-existing'}],platform:'openai'}).state
  c.applyTemplate();assert.equal(c.rows.value[0].value,'synthetic-existing');assert.equal(c.rows.value.filter(r=>r.name.toLowerCase()==='user-agent').length,1);assert.ok(c.rows.value.some(r=>r.name==='openai-beta'))
})
test('管理同步密钥留空保留、保存续改、旧快照显式关闭、只读标记不写入',async()=>{
  const m=mount(await setupFor('../components/settings/ControlPlaneTab.vue')),c=m.state;await eventually(()=>!c.loading.value)
  assert.equal(c.f.value.admin_sync_key,'');await c.persist('control-plane');let body=records.findLast(r=>r.method==='PUT').body
  assert.equal(Object.hasOwn(body,'admin_sync_key'),false);assert.equal(Object.hasOwn(body,'admin_sync_key_configured'),false)
  c.f.value.admin_sync_key='synthetic-submit-secret';c.f.value.legacy_snapshot_scheduler_authority=true
  const gate=pause('/admin/gateway-lite/config','PUT'),saving=c.persist('control-plane');await gate.seen;c.f.value.admin_sync_key='synthetic-next-secret';gate.release();await saving
  assert.equal(config.legacy_snapshot_scheduler_authority,true);assert.equal(c.f.value.admin_sync_key,'synthetic-next-secret');assert.equal(m.dirty(),true)
  await c.persist('login');assert.equal(c.f.value.admin_sync_key,'synthetic-next-secret');assert.equal(Object.hasOwn(records.findLast(r=>r.method==='PUT').body,'admin_sync_key'),false)
  c.f.value.legacy_snapshot_scheduler_authority=false;await c.persist('control-plane');body=records.findLast(r=>r.method==='PUT').body
  assert.equal(body.legacy_snapshot_scheduler_authority,false);assert.equal(c.f.value.admin_sync_key,'');assert.equal(config.legacy_snapshot_scheduler_authority,false);assert.equal(m.dirty(),false)
  c.f.value.admin_sync_key='synthetic-unsaved';omitConfig='admin_sync_key_configured';const count=records.filter(r=>r.method==='PUT').length;await c.persist('control-plane');assert.equal(records.filter(r=>r.method==='PUT').length,count);assert.equal(c.f.value.admin_sync_key,'synthetic-unsaved')
})
test('Beta兜底阻断提示保存/续改及清空使用全规则替换',async()=>{
  const m=mount(await setupFor('../components/settings/PolicyTab.vue')),c=m.state;await eventually(()=>!c.loading.value)
  c.addBetaRule();Object.assign(c.bp.value.rules[0],{beta_token:'synthetic-beta',model_whitelist:['synthetic-model'],fallback_action:'block',fallback_error_message:' 提交提示 '})
  const gate=pause('/admin/settings/beta-policy','PUT'),saving=c.saveBp();await gate.seen;c.bp.value.rules[0].fallback_error_message='续改提示';gate.release();await saving
  assert.equal(policies['/admin/settings/beta-policy'].rules[0].fallback_error_message,'提交提示');assert.equal(c.bp.value.rules[0].fallback_error_message,'续改提示');assert.equal(m.dirty(),true)
  c.bp.value.rules[0].fallback_error_message='';await c.saveBp();assert.equal(Object.hasOwn(policies['/admin/settings/beta-policy'].rules[0],'fallback_error_message'),false);assert.equal(m.dirty(),false)
})
test('采购探测非预设合法整数和边界规范化、保存期间续改保留',async()=>{
  const m=mount(await setupFor('../components/settings/GatewayTab.vue')),c=m.state;await eventually(()=>!c.loading.value)
  Object.assign(c.s.value,{upstream_billing_auto_probe_enabled:true,upstream_billing_probe_interval_minutes:7,upstream_billing_probe_batch_size:13,upstream_billing_probe_concurrency:3})
  const gate=pause('/admin/settings','PATCH'),saving=c.saveProbe();await gate.seen;c.s.value.upstream_billing_probe_interval_minutes=11;gate.release();await saving
  assert.equal(settings.upstream_billing_probe_interval_minutes,7);assert.equal(settings.upstream_billing_probe_batch_size,13);assert.equal(c.s.value.upstream_billing_probe_interval_minutes,11)
  Object.assign(c.s.value,{upstream_billing_probe_interval_minutes:9999,upstream_billing_probe_batch_size:0,upstream_billing_probe_concurrency:99});await c.saveProbe()
  assert.equal(settings.upstream_billing_probe_interval_minutes,1440);assert.equal(settings.upstream_billing_probe_batch_size,1);assert.equal(settings.upstream_billing_probe_concurrency,4);assert.equal(m.dirty(),false)
})

test('合规卡失败保留旧状态和草稿、保存续改、关窗迟到读不回填错误',async()=>{
  const m=mount(await setupFor('../components/settings/ComplianceCard.vue')),c=m.state;await eventually(()=>!c.loading.value)
  c.fillPhrase();assert.equal(m.dirty(),true);failCompliance=true;await c.load();assert.match(c.err.value,/合成读取失败/);assert.equal(c.status.value.version,'test');assert.equal(c.phrase.value,'合成确认');failCompliance=false
  const gate=pause('/admin/compliance/accept','POST'),saving=c.submit();await gate.seen;assert.equal(m.busy(),true);c.phrase.value='续改短语';gate.release();await saving;assert.equal(c.status.value.required,false);assert.equal(c.phrase.value,'续改短语');assert.equal(m.dirty(),true)
  globalThis.fetch=(url,options)=>nativeFetch(url,{...options,signal:undefined});const late=pause('/admin/compliance'),reading=c.load();await late.seen;m.unmount();notes.length=0;late.release();await reading;assert.equal(c.status.value,null);assert.equal(c.phrase.value,'');assert.equal(c.err.value,'');assert.equal(notes.length,0)
})
test('TOTP身份验证忙时保护、会话切换清密钥、关闭取消发送及冷却',async()=>{
  const m=mount(await setupFor('../components/settings/TotpModal.vue'),{mode:'setup'}),c=m.state;await eventually(()=>records.some(r=>r.path==='/user/totp/verification-method'));await nextTick()
  c.verifyPassword.value='synthetic-password';const gate=pause('/user/totp/setup','POST'),saving=c.doSetup();await gate.seen;assert.equal(m.busy(),true);await c.close();assert.equal(m.events.some(e=>e[0]==='close'),false);gate.release();await saving
  assert.equal(c.setupData.value.secret,'synthetic-secret');assert.equal(m.dirty(),true);resetRequestSession('replacement');assert.equal(c.setupData.value,null);assert.equal(c.verifyPassword.value,'');assert.equal(c.cooldown.value,0);m.unmount()
  const n=mount(await setupFor('../components/settings/TotpModal.vue'),{mode:'disable'}),d=n.state
  globalThis.fetch=(url,options)=>nativeFetch(url,{...options,signal:undefined});const late=pause('/user/totp/send-code','POST'),sending=d.sendCode();await late.seen;n.unmount();notes.length=0;late.release();await sending;assert.equal(d.cooldown.value,0);assert.equal(notes.length,0)
})
test('Grok自检迟到响应随所属实例取消，不回填或提示',async()=>{
  const m=mount(await setupFor('../components/settings/GrokSanityCard.vue')),c=m.state
  globalThis.fetch=(url,options)=>nativeFetch(url,{...options,signal:undefined});const gate=pause('/admin/grok/runtime-sanity'),work=c.run();await gate.seen;m.unmount();notes.length=0;gate.release();await work;assert.equal(c.report.value,null);assert.equal(notes.length,0)
})
test('测试弹窗上传关闭取消，忽略取消的旧FileReader不能覆盖新媒体',async()=>{
  const readers=[],previous=globalThis.FileReader
  globalThis.FileReader=class {constructor(){readers.push(this)}readAsDataURL(){}abort(){this.onabort?.()}}
  try {
    const m=mount(await setupFor('../components/account/TestModal.vue'),{open:false,account:{id:1,name:'synthetic',platform:'grok',type:'oauth'}}),c=m.state
    c.open.value=true;await eventually(()=>!c.loadingModels.value && c.modelId.value)
    const old=c.onImageFile({file:{}});assert.equal(readers.length,1);c.open.value=false;c.open.value=true;await eventually(()=>!c.loadingModels.value && c.modelId.value);const current=c.onImageFile({file:{}})
    readers[0].result='synthetic-old';readers[0].onload();await old;assert.equal(c.imageMsg.value,'')
    readers[1].result='synthetic-new';readers[1].onload();await current;assert.equal(c.imageMsg.value,'synthetic-new');m.unmount();assert.equal(c.imageMsg.value,'')
  } finally {if(previous===undefined)delete globalThis.FileReader;else globalThis.FileReader=previous}
})
