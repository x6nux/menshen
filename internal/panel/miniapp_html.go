package panel

// miniAppHTML 是 Mini App 的单页界面。所有数据经 /miniapp/api/* 读写，
// 鉴权靠 Telegram WebApp 的 initData（服务端验签）。样式只用 Telegram
// 主题变量与少量兜底色，深浅色都能看。
const miniAppHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no">
<title>门神配置</title>
<script src="https://telegram.org/js/telegram-web-app.js"></script>
<style>
:root{--bg:var(--tg-theme-bg-color,#f5f5f5);--fg:var(--tg-theme-text-color,#1a1a1a);
--hint:var(--tg-theme-hint-color,#888);--card:var(--tg-theme-secondary-bg-color,#fff);
--accent:var(--tg-theme-button-color,#2481cc);--accent-fg:var(--tg-theme-button-text-color,#fff);
--danger:#e05252}
*{box-sizing:border-box}
body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
background:var(--bg);color:var(--fg);font-size:15px;padding-bottom:40px}
header{position:sticky;top:0;z-index:5;background:var(--bg);padding:12px 16px 8px;
font-weight:600;font-size:17px;display:flex;align-items:center;gap:8px}
header small{font-weight:400;color:var(--hint);font-size:12px}
nav{display:flex;gap:6px;overflow-x:auto;padding:0 12px 10px;position:sticky;top:44px;
background:var(--bg);z-index:5}
nav button{flex:0 0 auto;border:0;border-radius:16px;padding:6px 12px;background:var(--card);
color:var(--fg);font-size:13px}
nav button.on{background:var(--accent);color:var(--accent-fg)}
main{padding:0 12px}
.card{background:var(--card);border-radius:12px;padding:14px;margin-bottom:12px}
.card h3{margin:0 0 10px;font-size:15px}
.row{display:flex;align-items:center;justify-content:space-between;gap:8px;
padding:6px 0;border-bottom:1px solid rgba(128,128,128,.12)}
.row:last-child{border-bottom:0}
.row .k{color:var(--hint);font-size:13px}
input,select,textarea{background:var(--bg);color:var(--fg);border:1px solid rgba(128,128,128,.3);
border-radius:8px;padding:8px 10px;font-size:14px;width:100%}
textarea{min-height:80px;font-family:ui-monospace,Menlo,monospace}
button.b{border:0;border-radius:8px;padding:8px 12px;background:var(--accent);
color:var(--accent-fg);font-size:14px}
button.g{background:transparent;color:var(--accent);border:1px solid var(--accent)}
button.d{background:transparent;color:var(--danger);border:1px solid var(--danger)}
button:disabled{opacity:.5}
label.sw{display:inline-flex;align-items:center;gap:6px;font-size:13px;color:var(--hint)}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:8px}
.mono{font-family:ui-monospace,Menlo,monospace;font-size:12px;word-break:break-all}
.hint{color:var(--hint);font-size:12px;line-height:1.5}
.badge{font-size:11px;border-radius:6px;padding:2px 6px;background:rgba(128,128,128,.15)}
.badge.ok{background:rgba(60,180,100,.2)}
.badge.no{background:rgba(224,82,82,.2)}
#toast{position:fixed;left:50%;bottom:24px;transform:translateX(-50%);background:#000c;
color:#fff;padding:8px 14px;border-radius:8px;font-size:13px;opacity:0;transition:.2s;z-index:9}
#toast.on{opacity:1}
</style>
</head>
<body>
<header>🛡 门神配置 <small id="who"></small></header>
<nav id="tabs"></nav>
<main id="view">加载中…</main>
<div id="toast"></div>
<script>
var tg = (window.Telegram && window.Telegram.WebApp) || null;
if (tg) { try { tg.ready(); tg.expand(); } catch(e){} }
var botID = new URLSearchParams(location.search).get('bot') || '0';
var S = null, TAB = 'overview';
var TABS = [['overview','概览'],['bots','机器人'],['chats','群组'],['upstreams','上游'],
  ['models','模型'],['settings','设置'],['lists','名单'],['logs','记录']];

function toast(m){ var t=document.getElementById('toast'); t.textContent=m; t.classList.add('on');
  setTimeout(function(){t.classList.remove('on');},2200); }
function esc(s){ return String(s==null?'':s).replace(/[&<>"]/g,function(c){
  return {'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]; }); }

function api(op, body){
  return fetch('/miniapp/api/'+op,{method:'POST',
    headers:{'Content-Type':'application/json',
      'X-Tg-Init-Data': tg ? tg.initData : '',
      'X-Bot-Id': botID},
    body: JSON.stringify(body||{})})
  .then(function(r){ return r.json().then(function(d){
    if(!r.ok) throw new Error(d.error || ('HTTP '+r.status)); return d; }); });
}
function act(op, body, okMsg){
  return api(op, body).then(function(){ toast(okMsg||'已保存'); return load(); })
    .catch(function(e){ toast('❌ '+e.message); });
}
function load(){ return api('state').then(function(d){ S=d; render(); })
  .catch(function(e){ document.getElementById('view').innerHTML =
    '<div class="card">❌ '+esc(e.message)+'</div>'; }); }

function render(){
  document.getElementById('who').textContent = (S.me.main?'主管理员':'次级管理员')+' · uid '+S.me.uid;
  var nav = document.getElementById('tabs'); nav.innerHTML='';
  TABS.forEach(function(t){
    if(!S.me.main && (t[0]=='upstreams'||t[0]=='models'||t[0]=='lists')) return;
    var b=document.createElement('button'); b.textContent=t[1];
    if(TAB==t[0]) b.className='on';
    b.onclick=function(){ TAB=t[0]; render(); };
    nav.appendChild(b);
  });
  var v=document.getElementById('view');
  v.innerHTML = ({overview:viewOverview, bots:viewBots, chats:viewChats,
    upstreams:viewUpstreams, models:viewModels, settings:viewSettings,
    lists:viewLists, logs:viewLogs}[TAB]||viewOverview)();
}
function toggleBtn(on, cb){ return '<button class="b" data-on="'+!!on+'" onclick="'+cb+'">'
  +(on?'已开启':'已关闭')+'</button>'; }

/* ---- 概览 ---- */
function viewOverview(){
  var st=S.stats;
  var h='<div class="card"><h3>近 24 小时</h3>'+
    '<div class="row"><span class="k">送检</span><b>'+st.checked+'</b></div>'+
    '<div class="row"><span class="k">命中</span><b>'+st.hits+'</b></div>'+
    '<div class="row"><span class="k">折算开销</span><b>'+esc(st.cost_text)+'</b></div>'+
    '<div class="row"><span class="k">生效群</span><b>'+st.chats+'</b></div></div>';
  h+='<div class="card"><h3>机器人</h3>'+S.bots.map(function(b){
    return '<div class="row"><span>'+esc(b.label)+(b.is_main?' <span class="badge">主 bot</span>':'')+
      '</span><span class="badge '+(b.live?'ok':'no')+'">'+(b.live?'运行中':'未运行')+'</span></div>';
  }).join('')+'</div>';
  return h;
}

/* ---- 机器人 ---- */
function viewBots(){
  return S.bots.map(function(b){
    var bs=(S.bot_settings[String(b.bot_id)])||{};
    var specs=S.specs.filter(function(sp){ return sp.group=='antiad'||sp.group=='both'; });
    var h='<div class="card"><h3>'+esc(b.label)+' '+
      (b.is_main?'<span class="badge">主 bot</span>':'')+' <span class="badge '+(b.live?'ok':'no')+'">'+
      (b.live?'运行中':'未运行')+'</span></h3>'+
      '<div class="row"><span class="k">启用</span>'+toggleBtn(b.enabled,
        "act('bot',{bot_id:"+b.bot_id+",action:'"+(b.enabled?'disable':'enable')+"'},'已切换')")+'</div>'+
      '<div class="row"><span class="k">判定模型（逗号分隔，按重试顺序）</span></div>'+
      '<input value="'+esc((b.so_models||[]).join(', '))+'" placeholder="a/gpt-5-mini, b/gpt-5-mini" '+
      'onchange="act(\'bot\',{bot_id:'+b.bot_id+',action:\'models\',which:\'so\',value:this.value},\'已保存\')">'+
      '<div class="row"><span class="k">复判模型（同上；留空跟随全局）</span></div>'+
      '<input value="'+esc((b.llm_models||[]).join(', '))+'" placeholder="留空 = 全局默认" '+
      'onchange="act(\'bot\',{bot_id:'+b.bot_id+',action:\'models\',which:\'llm\',value:this.value},\'已保存\')">'+
      '<div class="hint" style="margin-top:8px">单 bot 参数（覆盖全局）</div>';
    specs.forEach(function(sp){
      var val = (sp.key in bs) ? bs[sp.key] : '';
      h+='<div class="row"><span class="k">'+esc(sp.label)+'</span>'+
        '<input style="width:90px" value="'+esc(val)+'" placeholder="跟随全局" '+
        'onchange="act(\'set\',{scope:\'bot\',bot_id:'+b.bot_id+',key:\''+sp.key+'\',value:this.value})"></div>';
    });
    h+='<div class="hint">'+esc('留空 = 删除覆盖、跟随全局')+'</div></div>';
    return h;
  }).join('') || '<div class="card">没有可管理的机器人</div>';
}

/* ---- 群组 ---- */
function viewChats(){
  var h='';
  S.bots.forEach(function(b){
    var cs=S.chats.filter(function(c){ return c.bot_id==b.bot_id; });
    h+='<div class="card"><h3>'+esc(b.label)+' 的生效群</h3>';
    if(!cs.length) h+='<div class="hint">（还没有群）</div>';
    cs.forEach(function(c){
      var mode=c.dryrun?'🧪 演练':'⚔️ 正式';
      var punish=c.punish==0?'禁言':(c.punish==1?'封禁':'跟随');
      h+='<div class="row"><span>'+esc(c.title||c.chat_id)+'<div class="mono">'+c.chat_id+'</div></span>'+
        '<span><span class="badge">'+mode+'</span></span></div>'+
        '<div class="row"><label class="sw"><input type="checkbox" '+(c.enabled?'checked':'')+
          ' onchange="act(\'chat\',{bot_id:'+b.bot_id+',chat_id:'+c.chat_id+',action:\'update\',enabled:this.checked})"> 启用</label>'+
        '<label class="sw"><input type="checkbox" '+(c.dryrun?'checked':'')+
          ' onchange="act(\'chat\',{bot_id:'+b.bot_id+',chat_id:'+c.chat_id+',action:\'update\',dryrun:this.checked})"> 演练</label>'+
        '<label class="sw"><input type="checkbox" '+(c.group_alert?'checked':'')+
          ' onchange="act(\'chat\',{bot_id:'+b.bot_id+',chat_id:'+c.chat_id+',action:\'update\',group_alert:this.checked})"> 群内展示</label>'+
        '<span class="badge">处罚:'+punish+'</span></div>';
    });
    h+='<div class="grid" style="margin-top:8px">'+
      '<input id="addchat'+b.bot_id+'" placeholder="chat_id，如 -1001234567890">'+
      '<button class="b" onclick="act(\'chat\',{bot_id:'+b.bot_id+
      ',chat_id:document.getElementById(\'addchat'+b.bot_id+'\').value,action:\'add\'},\'已添加（默认演练）\')">添加群</button></div></div>';
  });
  return h||'<div class="card">没有机器人</div>';
}

/* ---- 上游（主管理员） ---- */
function viewUpstreams(){
  var h='<div class="card"><h3>新增上游</h3>'+
    '<input id="up_name" placeholder="名称（模型名前缀，不含 / 和 :）">'+
    '<input id="up_url" placeholder="base_url，如 https://api.example.com" style="margin-top:6px">'+
    '<input id="up_key" placeholder="api_key" style="margin-top:6px">'+
    '<div class="row"><label class="sw"><input type="checkbox" id="up_chat" checked> chat</label>'+
    '<label class="sw"><input type="checkbox" id="up_so" checked> systemone</label></div>'+
    '<button class="b" onclick="act(\'upstream\',{action:\'add\',name:document.getElementById(\'up_name\').value,'+
    'base_url:document.getElementById(\'up_url\').value,api_key:document.getElementById(\'up_key\').value,'+
    'supports_chat:document.getElementById(\'up_chat\').checked,supports_systemone:document.getElementById(\'up_so\').checked},\'已添加\')">添加</button></div>';
  S.upstreams.forEach(function(u){
    h+='<div class="card"><h3>'+esc(u.name)+' <span class="badge '+(u.status?'ok':'no')+'">'+
      (u.status?'启用':'停用')+'</span></h3>'+
      '<div class="row"><span class="k">base_url</span></div><input value="'+esc(u.base_url)+'" '+
      'onchange="act(\'upstream\',{action:\'update\',id:'+u.id+',base_url:this.value})">'+
      '<div class="row"><span class="k">api_key（当前 '+esc(u.api_key)+'）</span></div>'+
      '<input placeholder="留空 = 不改" onchange="act(\'upstream\',{action:\'update\',id:'+u.id+',api_key:this.value})">'+
      '<div class="row"><label class="sw"><input type="checkbox" '+(u.supports_chat?'checked':'')+
        ' onchange="act(\'upstream\',{action:\'update\',id:'+u.id+',supports_chat:this.checked})"> chat</label>'+
      '<label class="sw"><input type="checkbox" '+(u.supports_systemone?'checked':'')+
        ' onchange="act(\'upstream\',{action:\'update\',id:'+u.id+',supports_systemone:this.checked})"> systemone</label>'+
      '<label class="sw"><input type="checkbox" '+(u.status?'checked':'')+
        ' onchange="act(\'upstream\',{action:\'update\',id:'+u.id+',status:this.checked})"> 启用</label></div>'+
      '<div class="grid"><button class="b g" onclick="act(\'upstream\',{action:\'update\',id:'+u.id+
      ',name:prompt(\'新名称\',\''+esc(u.name)+'\')},\'已改名\')">改名</button>'+
      '<button class="d" onclick="if(confirm(\'删除该上游？\'))act(\'upstream\',{action:\'remove\',id:'+u.id+'},\'已删除\')">删除</button></div></div>';
  });
  return h;
}

/* ---- 模型（主管理员） ---- */
function viewModels(){
  var opts=S.upstreams.filter(function(u){return u.status;}).map(function(u){
    return '<option value="'+esc(u.name)+'">'+esc(u.name)+'</option>'; }).join('');
  var h='<div class="card"><h3>新增模型</h3>'+
    '<div class="grid"><select id="md_up">'+opts+'</select>'+
    '<input id="md_id" placeholder="模型 ID，如 gpt-5-mini"></div>'+
    '<div class="grid" style="margin-top:6px">'+
    '<input id="md_pp" placeholder="输入价 $/M">'+
    '<input id="md_cp" placeholder="补全价 $/M"></div>'+
    '<div class="grid" style="margin-top:6px">'+
    '<input id="md_crp" placeholder="缓存读取价">'+
    '<input id="md_cwp" placeholder="缓存创建价"></div>'+
    '<button class="b" style="margin-top:8px" onclick="act(\'model\',{action:\'add\',upstream:document.getElementById(\'md_up\').value,'+
    'model_id:document.getElementById(\'md_id\').value,prompt_price:document.getElementById(\'md_pp\').value,'+
    'completion_price:document.getElementById(\'md_cp\').value,cache_read_price:document.getElementById(\'md_crp\').value,'+
    'cache_write_price:document.getElementById(\'md_cwp\').value},\'已添加\')">添加</button></div>';
  S.models.forEach(function(m){
    h+='<div class="card"><h3>'+esc(m.name)+' <span class="badge '+(m.enabled?'ok':'no')+'">'+
      (m.enabled?'启用':'停用')+'</span></h3>'+
      '<div class="hint">上游 '+esc(m.upstream||'（旧格式）')+' ｜ 模型 ID '+esc(m.model_id)+'</div>'+
      '<div class="grid" style="margin-top:6px">'+
      '<input value="'+m.prompt_price+'" onchange="act(\'model\',{action:\'update\',name:\''+esc(m.name)+'\',prompt_price:this.value})">'+
      '<input value="'+m.completion_price+'" onchange="act(\'model\',{action:\'update\',name:\''+esc(m.name)+'\',completion_price:this.value})">'+
      '<input value="'+m.cache_read_price+'" onchange="act(\'model\',{action:\'update\',name:\''+esc(m.name)+'\',cache_read_price:this.value})">'+
      '<input value="'+m.cache_write_price+'" onchange="act(\'model\',{action:\'update\',name:\''+esc(m.name)+'\',cache_write_price:this.value})">'+
      '</div><div class="grid" style="margin-top:6px">'+
      '<button class="b g" onclick="act(\'model\',{action:\'update\',name:\''+esc(m.name)+'\',enabled:'+(!m.enabled)+'})">'+
      (m.enabled?'停用':'启用')+'</button>'+
      '<button class="d" onclick="if(confirm(\'删除该模型？\'))act(\'model\',{action:\'remove\',name:\''+esc(m.name)+'\'},\'已删除\')">删除</button>'+
      '</div></div>';
  });
  return h;
}

/* ---- 全局设置（主管理员） ---- */
function viewSettings(){
  var g=S.global||{};
  var h='<div class="card"><h3>总开关</h3>'+
    '<div class="row"><span class="k">反广告总开关</span>'+
    '<button class="b" onclick="act(\'set\',{scope:\'global\',key:\'antiad_enabled\',value:'+
      (g.antiad_enabled=='1'?'0':'1')+",'已切换')+'">'+(g.antiad_enabled=='1'?'已开启':'已关闭')+'</button></div>'+
    '<div class="row"><span class="k">告警抄送主管理员</span>'+
    '<button class="b" onclick="act(\'set\',{scope:\'global\',key:\'alert_copy_main\',value:'+
      (g.alert_copy_main=='1'?'0':'1')+",'已切换')+'">'+(g.alert_copy_main=='1'?'已开启':'已关闭')+'</button></div>'+
    '<div class="row"><span class="k">联合封禁</span>'+
    '<button class="b" onclick="act(\'set\',{scope:\'global\',key:\'gban_enabled\',value:'+
      (g.gban_enabled=='1'?'0':'1')+",'已切换')+'">'+(g.gban_enabled=='1'?'已开启':'已关闭')+'</button></div></div>';
  var specs=S.specs.filter(function(sp){ return sp.group==''||sp.group=='both'; });
  h+='<div class="card"><h3>全局参数</h3>'+specs.map(function(sp){
    return '<div class="row"><span class="k">'+esc(sp.label)+'<div class="hint">'+esc(sp.hint)+'</div></span>'+
      '<input style="width:110px" value="'+esc(g[sp.key]||'')+'" '+
      'onchange="act(\'set\',{scope:\'global\',key:\''+sp.key+'\',value:this.value})"></div>';
  }).join('')+'</div>';
  h+='<div class="card"><h3>形态摘要</h3>'+
    '<textarea id="dg">'+esc(S.digest||'')+'</textarea>'+
    '<div class="grid" style="margin-top:8px">'+
    '<button class="b" onclick="act(\'digest\',{action:\'save\',value:document.getElementById(\'dg\').value},\'已保存\')">保存</button>'+
    '<button class="b g" onclick="act(\'digest\',{action:\'run\'},\'已触发重新总结\')">立即重新总结</button>'+
    '</div></div>';
  return h;
}

/* ---- 名单（主管理员） ---- */
function viewLists(){
  var h='<div class="card"><h3>次级管理员</h3>'+
    (S.admins||[]).map(function(a){
      return '<div class="row"><span class="mono">'+a.user_id+' '+esc(a.note)+'</span>'+
      '<button class="d" onclick="act(\'admin\',{action:\'remove\',user_id:'+a.user_id+',},\'已移除\')">移除</button></div>';
    }).join('')+
    '<div class="grid" style="margin-top:8px"><input id="ad_uid" placeholder="user_id">'+
    '<input id="ad_note" placeholder="备注"></div>'+
    '<button class="b" style="margin-top:8px" onclick="act(\'admin\',{action:\'add\',user_id:document.getElementById(\'ad_uid\').value,'+
    'note:document.getElementById(\'ad_note\').value},\'已添加\')">添加</button></div>';
  h+='<div class="card"><h3>联合封禁</h3>'+
    (S.gban||[]).map(function(g){
      return '<div class="row"><span class="mono">'+g.user_id+' '+esc(g.reason)+'</span>'+
      '<button class="d" onclick="if(confirm(\'解除封禁？\'))act(\'gban\',{action:\'remove\',user_id:'+g.user_id+'},\'已解除\')">解除</button></div>';
    }).join('')+
    '<div class="grid" style="margin-top:8px"><input id="gb_uid" placeholder="user_id">'+
    '<input id="gb_reason" placeholder="原因"></div>'+
    '<button class="b" style="margin-top:8px" onclick="act(\'gban\',{action:\'add\',user_id:document.getElementById(\'gb_uid\').value,'+
    'reason:document.getElementById(\'gb_reason\').value},\'已加入\')">加入名单</button></div>';
  h+='<div class="card"><h3>白名单</h3>'+
    (S.whitelist||[]).map(function(w){
      var scope = w.bot_id==0?'全平台':(w.chat_id==0?'bot 所有群':'群 '+w.chat_id);
      return '<div class="row"><span class="mono">'+w.user_id+' · '+scope+' · '+esc(w.source)+'</span>'+
      '<button class="d" onclick="act(\'whitelist\',{action:\'remove\',bot_id:'+w.bot_id+',chat_id:'+w.chat_id+
      ',user_id:'+w.user_id+'},\'已移除\')">移除</button></div>';
    }).join('')+
    '<div class="grid" style="margin-top:8px"><select id="wl_bot">'+
    S.bots.map(function(b){return '<option value="'+b.bot_id+'">'+esc(b.label)+'</option>';}).join('')+
    '</select><input id="wl_uid" placeholder="user_id"></div>'+
    '<div class="grid" style="margin-top:6px"><input id="wl_chat" placeholder="chat_id（0 = 该 bot 所有群）">'+
    '<input id="wl_hours" placeholder="小时（空 = 永久）"></div>'+
    '<button class="b" style="margin-top:8px" onclick="act(\'whitelist\',{action:\'add\',bot_id:document.getElementById(\'wl_bot\').value,'+
    'chat_id:document.getElementById(\'wl_chat\').value||0,user_id:document.getElementById(\'wl_uid\').value,'+
    'hours:document.getElementById(\'wl_hours\').value||0},\'已加入\')">加入白名单</button></div>';
  return h;
}

/* ---- 记录 ---- */
function viewLogs(){
  return '<div class="card"><h3>最近判定记录</h3><div id="logbox">加载中…</div>'+
    '<div class="grid" style="margin-top:8px">'+
    '<button class="b g" onclick="pageLogs(-1)">上一页</button>'+
    '<button class="b g" onclick="pageLogs(1)">下一页</button></div></div>'+
    '<div class="card"><h3>申诉单</h3><div id="apbox">加载中…</div></div>';
}
var LOGPAGE=1;
function pageLogs(d){ LOGPAGE=Math.max(1,LOGPAGE+d); loadLogs(); }
function loadLogs(){
  api('logs',{page:LOGPAGE}).then(function(d){
    var el=document.getElementById('logbox'); if(!el) return;
    el.innerHTML = d.logs.map(function(l){
      return '<div class="row"><span class="mono">#'+l.id+' · '+esc(l.verdict)+' '+Math.round(l.confidence*100)+
        '% · '+esc(l.action)+'</span>'+(l.view_url?'<a href="'+esc(l.view_url)+'" target="_blank">查看</a>':'')+'</div>';
    }).join('') || '<div class="hint">（没有记录）</div>';
  }).catch(function(e){ var el=document.getElementById('logbox'); if(el) el.textContent='❌ '+e.message; });
  api('appeals',{page:1}).then(function(d){
    var el=document.getElementById('apbox'); if(!el) return;
    el.innerHTML = d.appeals.map(function(a){
      return '<div class="row"><span class="mono">#'+a.id+' · uid '+a.user_id+' · '+esc(a.status)+
        ' · '+esc(a.ai_result)+'</span>'+(a.detail_url?'<a href="'+esc(a.detail_url)+'" target="_blank">详情</a>':'')+'</div>';
    }).join('') || '<div class="hint">（没有申诉）</div>';
  }).catch(function(){});
}
var _render = render;
render = function(){ _render(); if(TAB=='logs') loadLogs(); };

load();
</script>
</body>
</html>`
