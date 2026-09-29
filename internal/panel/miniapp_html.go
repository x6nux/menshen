package panel

// miniAppHTML 是 Mini App 的单页界面。所有数据经 /miniapp/api/* 读写，
// 鉴权靠 Telegram WebApp 的 initData（服务端验签）。样式只用 Telegram
// 主题变量与少量兜底色，深浅色都能看。
//
// 页面是两段式导航：机器人/群组/上游/模型/名单都是「先列表、点进去改
// 配置」（SUB 记当前展开的条目），避免把所有配置平铺成一长页让人来回
// 上下滑。返回用页内的「‹ 返回」和 Telegram 的返回按钮双保险。
const miniAppHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1,user-scalable=no">
<title>门神配置</title>
<script src="https://telegram.org/js/telegram-web-app.js" async></script>
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
.row.item{cursor:pointer}
.row.item:active{background:rgba(128,128,128,.1)}
.chev{color:var(--hint);font-size:16px;flex:0 0 auto}
input,select,textarea{background:var(--bg);color:var(--fg);border:1px solid rgba(128,128,128,.3);
border-radius:8px;padding:8px 10px;font-size:14px;width:100%}
textarea{min-height:80px;font-family:ui-monospace,Menlo,monospace}
button.b{border:0;border-radius:8px;padding:8px 12px;background:var(--accent);
color:var(--accent-fg);font-size:14px}
button.g{background:transparent;color:var(--accent);border:1px solid var(--accent)}
button.d{background:transparent;color:var(--danger);border:1px solid var(--danger)}
button:disabled{opacity:.5}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:8px}
.mono{font-family:ui-monospace,Menlo,monospace;font-size:12px;word-break:break-all}
.hint{color:var(--hint);font-size:12px;line-height:1.5}
.badge{font-size:11px;border-radius:6px;padding:2px 6px;background:rgba(128,128,128,.15);flex:0 0 auto}
.badge.ok{background:rgba(60,180,100,.2)}
.badge.no{background:rgba(224,82,82,.2)}
.chips{display:flex;gap:6px;overflow-x:auto;padding:2px 0}
.chips button{flex:0 0 auto;border:0;border-radius:14px;padding:5px 10px;font-size:12px;
background:rgba(128,128,128,.15);color:var(--fg)}
.chips button.on{background:var(--accent);color:var(--accent-fg)}
label.sw{position:relative;display:inline-flex;align-items:center;gap:6px;font-size:13px;
color:var(--hint);cursor:pointer}
label.sw input{position:absolute;opacity:0;width:0;height:0}
label.sw .tr{width:40px;height:24px;border-radius:12px;background:rgba(128,128,128,.4);
position:relative;transition:.2s;flex:0 0 auto}
label.sw .tr:after{content:'';position:absolute;left:2px;top:2px;width:20px;height:20px;
border-radius:50%;background:var(--card);transition:.2s;box-shadow:0 1px 2px rgba(0,0,0,.2)}
label.sw input:checked~.tr{background:var(--accent)}
label.sw input:checked~.tr:after{left:18px}
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
var tg = null, booted = false;
// telegram-web-app.js 在部分网络下加载不出来（telegram.org 被屏蔽）。
// 同步加载会把后面的脚本一起卡住，页面永远停在「加载中」——所以脚本
// 异步加载，这里轮询等待最多 3 秒，然后无论如何都把界面跑起来。
function boot(){
  if (booted) return; booted = true;
  tg = (window.Telegram && window.Telegram.WebApp) || null;
  if (tg) { try { tg.ready(); tg.expand();
    tg.BackButton.onClick(function(){ if(SUB) back(); }); } catch(e){} }
  if (!tg) {
    document.getElementById('view').innerHTML =
      '<div class="card">请通过 Telegram 里的菜单按钮「配置」打开本页。<br>' +
      '<span class="hint">如果你已经在 Telegram 里打开，说明官方脚本没加载出来' +
      '（telegram.org 在部分网络下不可达），换网络或挂代理后重试。</span></div>';
    return;
  }
  load();
}
var _tries = 0;
var _iv = setInterval(function(){
  if (window.Telegram || ++_tries > 30) { clearInterval(_iv); boot(); }
}, 100);
var botID = new URLSearchParams(location.search).get('bot') || '0';
var S = null, TAB = 'overview';
// SUB 是详情导航：形如 bot:43 / chat:43:-100999 / up:2 / md:a%2Fgpt-5-mini，
// 空串表示停在列表页。切 tab 清空。
var SUB = '';
// LSUB 是「名单」页的内部分段；UPADD/MDADD 控制两个新增表单的展开。
var LSUB = 'own', UPADD = false, MDADD = false;
// LOGF/LOGQ/LOGPAGE 是记录页的筛选、搜索与页码；APF/APPAGE 同理给申诉。
// 记录默认筛「已删除」：被判广告删掉的才是要盯的，其余靠切换筛选看。
var LOGF = 'deleted', LOGQ = '', LOGPAGE = 1;
var APF = '', APPAGE = 1;
var ADDCHAT = false;
var TABS = [['overview','概览'],['bots','机器人'],['chats','群组'],['upstreams','上游'],
  ['models','模型'],['settings','设置'],['lists','名单'],['logs','记录'],['appeals','申诉']];

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
function load(){ return api('state').then(function(d){
  // 详情缓存跨刷新保留：操作（复查/解除/标记）后回到详情不闪加载页。
  var ld = S && S.LD; S=d; if(ld) S.LD=ld; render(); })
  .catch(function(e){ document.getElementById('view').innerHTML =
    '<div class="card">❌ '+esc(e.message)+'</div>'; }); }

function go(tab,key){ TAB=tab; SUB=key||''; render(); }
function back(){ SUB=''; render(); }

function render(){
  document.getElementById('who').textContent = (S.me.main?'主管理员':'次级管理员')+' · uid '+S.me.uid;
  var nav = document.getElementById('tabs'); nav.innerHTML='';
  TABS.forEach(function(t){
    if(!S.me.main && (t[0]=='upstreams'||t[0]=='models')) return;
    var b=document.createElement('button'); b.textContent=t[1];
    if(TAB==t[0]) b.className='on';
    b.onclick=function(){ go(t[0]); };
    nav.appendChild(b);
  });
  // Telegram 返回按钮与页内「‹ 返回」等价；不在详情页就收起来。
  try{ if(tg && tg.BackButton){ if(SUB) tg.BackButton.show(); else tg.BackButton.hide(); } }catch(e){}
  var v=document.getElementById('view');
  v.innerHTML = ({overview:viewOverview, bots:viewBots, chats:viewChats,
    upstreams:viewUpstreams, models:viewModels, settings:viewSettings,
    lists:viewLists, logs:viewLogs, appeals:viewAppeals}[TAB]||viewOverview)();
}
function toggleBtn(on, cb){ return '<button class="b" data-on="'+!!on+'" onclick="'+cb+'">'
  +(on?'已开启':'已关闭')+'</button>'; }
function backRow(){ return '<div class="row" style="border:0;padding:0 0 8px">'+
  '<button class="b g" onclick="back()">‹ 返回</button></div>'; }

/* ---- 全局默认值：占位里画「30(全局)」 ---- */
function gdef(key){
  if(S.global && S.global[key]!=null && S.global[key]!=='') return S.global[key];
  if(S.global_defaults && S.global_defaults[key]!=null && S.global_defaults[key]!=='')
    return S.global_defaults[key];
  return '';
}
// 模型列表键是 JSON 数组，展示成逗号分隔。
function glist(key){ var v=gdef(key); try{ var a=JSON.parse(v);
  if(a&&a.length) return a.join(', '); }catch(e){} return ''; }
function gph(key){ var v=gdef(key); return v!=='' ? String(v)+'(全局)' : '全局未配置'; }

function botById(id){ for(var i=0;i<S.bots.length;i++)
  if(S.bots[i].bot_id==id) return S.bots[i]; return null; }
function botOpts(){ return S.bots.map(function(b){
  return '<option value="'+b.bot_id+'">'+esc(b.label)+'</option>'; }).join(''); }

/* ---- 概览 ---- */
function viewOverview(){
  var st=S.stats;
  var h='<div class="card"><h3>近 24 小时</h3>'+
    '<div class="row"><span class="k">送检</span><b>'+st.checked+'</b></div>'+
    '<div class="row"><span class="k">命中</span><b>'+st.hits+'</b></div>'+
    '<div class="row"><span class="k">折算开销</span><b>'+esc(st.cost_text)+'</b></div>'+
    '<div class="row"><span class="k">生效群</span><b>'+st.chats+'</b></div></div>';
  h+='<div class="card"><h3>机器人</h3>'+S.bots.map(function(b){
    return '<div class="row item" onclick="go(\'bots\',\'bot:'+b.bot_id+'\')"><span>'+esc(b.label)+
      (b.is_main?' <span class="badge">主 bot</span>':'')+
      '</span><span class="badge '+(b.live?'ok':'no')+'">'+(b.live?'运行中':'未运行')+'</span></div>';
  }).join('')+'</div>';
  return h;
}

/* ---- 机器人：列表 → 点进去改配置 ---- */
function viewBots(){
  if(SUB.indexOf('bot:')==0){ var b=botById(SUB.slice(4)); if(b) return viewBotDetail(b); SUB=''; }
  var h='<div class="card"><h3>机器人</h3>';
  if(!S.bots.length) h+='<div class="hint">没有可管理的机器人</div>';
  h+=S.bots.map(function(b){
    return '<div class="row item" onclick="go(\'bots\',\'bot:'+b.bot_id+'\')">'+
      '<span>'+esc(b.label)+(b.is_main?' <span class="badge">主 bot</span>':'')+'</span>'+
      '<span class="badge '+(b.live?'ok':'no')+'">'+
      (b.enabled?(b.live?'运行中':'未运行'):'已停用')+'</span>'+
      '<span class="chev">›</span></div>';
  }).join('');
  return h+'</div>';
}
/* 归属行：主 bot 的归属由配置文件决定，只读；工作 bot 主管理员可改派
   （候选来自后端下发的 owner_opts），次级管理员只读。 */
function ownerRow(b){
  if(b.is_main || !S.me.main)
    return '<div class="row"><span class="k">归属</span><span class="mono">uid '+b.owner_id+'</span></div>';
  var sel='own_'+b.bot_id;
  return '<div class="row"><span class="k">归属</span><select id="'+sel+'" style="width:150px">'+
    (S.owner_opts||[]).map(function(o){
      return '<option value="'+o.user_id+'"'+(o.user_id==b.owner_id?' selected':'')+'>'+
        esc(o.label)+' · '+o.user_id+'</option>'; }).join('')+
    '</select><button class="b" style="margin-left:6px" '+
    'onclick="if(confirm(\'把该 bot 改派给所选管理员？\'))act(\'bot\',{bot_id:'+b.bot_id+
    ',action:\'owner\',owner_id:document.getElementById(\''+sel+'\').value},\'已改派\')">改派</button></div>';
}
function viewBotDetail(b){
  var bs=(S.bot_settings[String(b.bot_id)])||{};
  var specs=S.specs.filter(function(sp){ return sp.group=='antiad'||sp.group=='both'; });
  var gso=glist('antiad_so_models'), gllm=glist('antiad_llm_models');
  var h=backRow();
  h+='<div class="card"><h3>'+esc(b.label)+' '+
    (b.is_main?'<span class="badge">主 bot</span>':'')+' <span class="badge '+(b.live?'ok':'no')+'">'+
    (b.live?'运行中':'未运行')+'</span></h3>'+
    '<div class="row"><span class="k">启用</span>'+toggleBtn(b.enabled,
      "act('bot',{bot_id:"+b.bot_id+",action:'"+(b.enabled?'disable':'enable')+"'},'已切换')")+'</div>'+
    ownerRow(b);
  if(S.me.main){
    // 模型由主管理员配置（与 TG 面板一致）；次管看不到这两个输入框。
    h+='<div class="row"><span class="k">判定模型（逗号分隔，按重试顺序）</span></div>'+
      '<input value="'+esc((b.so_models||[]).join(', '))+'" placeholder="'+
      esc(gso?gso+'(全局)':'全局未配置')+'" '+
      'onchange="act(\'bot\',{bot_id:'+b.bot_id+',action:\'models\',which:\'so\',value:this.value},\'已保存\')">'+
      '<div class="row"><span class="k">复判模型（同上）</span></div>'+
      '<input value="'+esc((b.llm_models||[]).join(', '))+'" placeholder="'+
      esc(gllm?gllm+'(全局)':'全局未配置')+'" '+
      'onchange="act(\'bot\',{bot_id:'+b.bot_id+',action:\'models\',which:\'llm\',value:this.value},\'已保存\')">';
  }
  h+='<div class="hint" style="margin-top:8px">单 bot 参数（覆盖全局；清空 = 恢复全局）</div>';
  specs.forEach(function(sp){
    var val = (sp.key in bs) ? bs[sp.key] : '';
    h+='<div class="row"><span class="k">'+esc(sp.label)+'</span>'+
      '<input style="width:110px" value="'+esc(val)+'" placeholder="'+esc(gph(sp.key))+'" '+
      'onchange="act(\'set\',{scope:\'bot\',bot_id:'+b.bot_id+',key:\''+sp.key+'\',value:this.value})"></div>';
  });
  if(!b.is_main){
    h+='<div class="row" style="border:0;padding-top:10px"><button class="d" style="width:100%" '+
      'onclick="if(confirm(\'移除该 bot？它的群配置与阈值会一并删除，webhook 会被撤销；'+
      '判定流水保留。\'))act(\'bot\',{bot_id:'+b.bot_id+',action:\'remove\'},\'已移除\')">移除该 bot</button></div>';
  }
  return h+'</div>';
}

/* ---- 群组：列表 → 点进去改配置 ---- */
function chatById(bid,cid){ for(var i=0;i<S.chats.length;i++){
  var c=S.chats[i]; if(c.bot_id==bid&&c.chat_id==cid) return c; } return null; }
function viewChats(){
  if(SUB.indexOf('chat:')==0){
    var pp=SUB.slice(5).split(':');
    var c=chatById(pp[0],pp[1]);
    if(c) return viewChatDetail(c);
    SUB='';
  }
  var h='<div class="card"><div class="row" style="border:0;padding:0 0 8px">'+
    '<h3 style="margin:0">生效群</h3>'+
    '<button class="b" onclick="ADDCHAT=!ADDCHAT;render()">＋ 添加群</button></div>';
  if(ADDCHAT){
    h+='<select id="ca_bot">'+botOpts()+'</select>'+
      '<input id="ca_id" placeholder="chat_id，如 -1001234567890" style="margin-top:6px">'+
      '<button class="b" style="margin-top:6px" onclick="act(\'chat\',{bot_id:document.getElementById(\'ca_bot\').value,'+
      'chat_id:document.getElementById(\'ca_id\').value,action:\'add\'},\'已添加（默认演练）\')">添加</button>'+
      '<div style="height:8px"></div>';
  }
  if(!S.chats.length) h+='<div class="hint">（还没有群）</div>';
  h+=S.chats.map(function(c){
    var b=botById(c.bot_id);
    return '<div class="row item" onclick="go(\'chats\',\'chat:'+c.bot_id+':'+c.chat_id+'\')">'+
      '<span>'+esc(c.title||c.chat_id)+'<div class="mono">'+c.chat_id+(b?' · '+esc(b.label):'')+'</div></span>'+
      '<span class="badge '+(c.enabled?'ok':'no')+'">'+
      (c.dryrun?'演练':(c.enabled?'判定中':'停用'))+'</span>'+
      '<span class="chev">›</span></div>';
  }).join('');
  return h;
}
function viewChatDetail(c){
  var b=botById(c.bot_id);
  var pre="act('chat',{bot_id:"+c.bot_id+",chat_id:"+c.chat_id+",action:'update',";
  var h=backRow();
  h+='<div class="card"><h3>'+esc(c.title||c.chat_id)+' <span class="badge">'+c.chat_id+'</span></h3>'+
    '<div class="hint" style="margin-bottom:8px">所属：'+esc(b?b.label:'未知 bot')+'</div>'+
    '<div class="row"><label class="sw"><input type="checkbox" '+(c.enabled?'checked':'')+
      ' onchange="'+pre+'enabled:this.checked})"><span class="tr"></span> 启用判定</label></div>'+
    '<div class="row"><label class="sw"><input type="checkbox" '+(c.dryrun?'checked':'')+
      ' onchange="'+pre+'dryrun:this.checked})"><span class="tr"></span> 演练（只记不罚）</label></div>'+
    '<div class="row"><label class="sw"><input type="checkbox" '+(c.group_alert?'checked':'')+
      ' onchange="'+pre+'group_alert:this.checked})"><span class="tr"></span> 群内展示判定结果</label></div>'+
    '<div class="row"><span class="k">处罚方式</span>'+
      '<select style="width:170px" onchange="'+pre+'punish:this.value})">'+
      '<option value="-1"'+(c.punish==-1?' selected':'')+'>跟随 bot 设置</option>'+
      '<option value="0"'+(c.punish==0?' selected':'')+'>'+esc(muteOptLabel(c.bot_id))+'</option>'+
      '<option value="1"'+(c.punish==1?' selected':'')+'>封禁出群（永久）</option></select></div>'+
    '<div class="row"><span class="k">实际执行</span><span>'+esc(punishLabel(c.bot_id,c.punish))+'</span></div>'+
    (punishLabel(c.bot_id,c.punish).indexOf('禁言')===0?
      '<div class="hint">要改成永久禁言：把本 bot 的「禁言时长（小时）」设为 0；要踢出群就选「封禁出群」。</div>':'')+
    '<button class="d" style="margin-top:10px" '+
      'onclick="if(confirm(\'移除该群？判定与处置立即停止，配置一并删除。\'))act(\'chat\',{bot_id:'+c.bot_id+
      ',chat_id:'+c.chat_id+',action:\'remove\'},\'已移除\')">移除该群</button>'+
    '</div>';
  return h;
}

/* ---- 上游（主管理员）：列表 → 详情 ---- */
function upById(id){ for(var i=0;i<S.upstreams.length;i++)
  if(S.upstreams[i].id==id) return S.upstreams[i]; return null; }
function viewUpstreams(){
  if(SUB.indexOf('up:')==0){ var u=upById(SUB.slice(3)); if(u) return viewUpDetail(u); SUB=''; }
  var h='<div class="card"><h3>上游</h3>';
  if(!S.upstreams.length) h+='<div class="hint">（还没有上游）</div>';
  h+=S.upstreams.map(function(u){
    return '<div class="row item" onclick="go(\'upstreams\',\'up:'+u.id+'\')">'+
      '<span>'+esc(u.name)+'</span>'+
      '<span class="badge '+(u.status?'ok':'no')+'">'+(u.status?'启用':'停用')+'</span>'+
      '<span class="chev">›</span></div>';
  }).join('')+'</div>';
  h+='<div class="card"><h3>新增上游</h3>'+(UPADD?
    '<input id="up_name" placeholder="名称（模型名前缀，不含 / 和 :）">'+
    '<input id="up_url" placeholder="base_url，如 https://api.example.com" style="margin-top:6px">'+
    '<input id="up_key" placeholder="api_key" style="margin-top:6px">'+
    '<div class="row"><label class="sw"><input type="checkbox" id="up_chat" checked> chat</label>'+
    '<label class="sw"><input type="checkbox" id="up_so" checked> systemone</label></div>'+
    '<div class="grid" style="margin-top:8px">'+
    '<button class="b" onclick="act(\'upstream\',{action:\'add\',name:document.getElementById(\'up_name\').value,'+
    'base_url:document.getElementById(\'up_url\').value,api_key:document.getElementById(\'up_key\').value,'+
    'supports_chat:document.getElementById(\'up_chat\').checked,supports_systemone:document.getElementById(\'up_so\').checked},'+
    '\'已添加\')">添加</button>'+
    '<button class="b g" onclick="UPADD=false;render()">收起</button></div>'
    :'<button class="b g" onclick="UPADD=true;render()">展开表单</button>')+'</div>';
  return h;
}
function viewUpDetail(u){
  var h=backRow();
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
    '<div class="grid" style="margin-top:8px"><button class="b g" onclick="act(\'upstream\',{action:\'update\',id:'+u.id+
    ',name:prompt(\'新名称\',\''+esc(u.name)+'\')},\'已改名\')">改名</button>'+
    '<button class="d" onclick="if(confirm(\'删除该上游？\'))act(\'upstream\',{action:\'remove\',id:'+u.id+'},\'已删除\')">删除</button></div></div>';
  return h;
}

/* ---- 模型（主管理员）：列表 → 详情 ---- */
function modelByName(n){ for(var i=0;i<S.models.length;i++)
  if(S.models[i].name==n) return S.models[i]; return null; }
function viewModels(){
  if(SUB.indexOf('md:')==0){ var m=modelByName(decodeURIComponent(SUB.slice(3)));
    if(m) return viewModelDetail(m); SUB=''; }
  var h='<div class="card"><h3>模型</h3>';
  if(!S.models.length) h+='<div class="hint">（还没有模型）</div>';
  h+=S.models.map(function(m){
    return '<div class="row item" onclick="go(\'models\',\'md:'+encodeURIComponent(m.name)+'\')">'+
      '<span class="mono">'+esc(m.name)+'</span>'+
      '<span class="badge '+(m.enabled?'ok':'no')+'">'+(m.enabled?'启用':'停用')+'</span>'+
      '<span class="chev">›</span></div>';
  }).join('')+'</div>';
  h+='<div class="card"><h3>新增模型</h3>'+(MDADD?
    '<div class="grid"><select id="md_up">'+
    S.upstreams.filter(function(u){return u.status;}).map(function(u){
      return '<option value="'+esc(u.name)+'">'+esc(u.name)+'</option>'; }).join('')+
    '</select><input id="md_id" placeholder="模型 ID，如 gpt-5-mini"></div>'+
    '<div class="grid" style="margin-top:6px">'+
    '<input id="md_pp" placeholder="输入价 $/M">'+
    '<input id="md_cp" placeholder="补全价 $/M"></div>'+
    '<div class="grid" style="margin-top:6px">'+
    '<input id="md_crp" placeholder="缓存读取价">'+
    '<input id="md_cwp" placeholder="缓存创建价"></div>'+
    '<div class="grid" style="margin-top:8px">'+
    '<button class="b" onclick="act(\'model\',{action:\'add\',upstream:document.getElementById(\'md_up\').value,'+
    'model_id:document.getElementById(\'md_id\').value,prompt_price:document.getElementById(\'md_pp\').value,'+
    'completion_price:document.getElementById(\'md_cp\').value,cache_read_price:document.getElementById(\'md_crp\').value,'+
    'cache_write_price:document.getElementById(\'md_cwp\').value},\'已添加\')">添加</button>'+
    '<button class="b g" onclick="MDADD=false;render()">收起</button></div>'
    :'<button class="b g" onclick="MDADD=true;render()">展开表单</button>')+'</div>';
  return h;
}
function viewModelDetail(m){
  var h=backRow();
  h+='<div class="card"><h3 class="mono">'+esc(m.name)+' <span class="badge '+(m.enabled?'ok':'no')+'">'+
    (m.enabled?'启用':'停用')+'</span></h3>'+
    '<div class="hint">上游 '+esc(m.upstream||'（旧格式）')+' ｜ 模型 ID '+esc(m.model_id)+'</div>'+
    '<div class="grid" style="margin-top:6px">'+
    '<input value="'+m.prompt_price+'" placeholder="输入价 $/M" onchange="act(\'model\',{action:\'update\',name:\''+esc(m.name)+'\',prompt_price:this.value})">'+
    '<input value="'+m.completion_price+'" placeholder="补全价 $/M" onchange="act(\'model\',{action:\'update\',name:\''+esc(m.name)+'\',completion_price:this.value})">'+
    '<input value="'+m.cache_read_price+'" placeholder="缓存读取价" onchange="act(\'model\',{action:\'update\',name:\''+esc(m.name)+'\',cache_read_price:this.value})">'+
    '<input value="'+m.cache_write_price+'" placeholder="缓存创建价" onchange="act(\'model\',{action:\'update\',name:\''+esc(m.name)+'\',cache_write_price:this.value})">'+
    '</div><div class="grid" style="margin-top:6px">'+
    '<button class="b g" onclick="act(\'model\',{action:\'update\',name:\''+esc(m.name)+'\',enabled:'+(!m.enabled)+'})">'+
    (m.enabled?'停用':'启用')+'</button>'+
    '<button class="d" onclick="if(confirm(\'删除该模型？\'))act(\'model\',{action:\'remove\',name:\''+esc(m.name)+'\'},\'已删除\')">删除</button>'+
    '</div></div>';
  return h;
}

/* ---- 全局设置（主管理员） ---- */
function viewSettings(){
  var g=S.global||{};
  var h='<div class="card"><h3>总开关</h3>'+
    '<div class="row"><span class="k">反广告总开关</span>'+
    '<button class="b" onclick="act(\'set\',{scope:\'global\',key:\'antiad_enabled\',value:\''+
      (g.antiad_enabled=='1'?'0':'1')+'\'},\'已切换\')">'+(g.antiad_enabled=='1'?'已开启':'已关闭')+'</button></div>'+
    '<div class="row"><span class="k">告警抄送主管理员</span>'+
    '<button class="b" onclick="act(\'set\',{scope:\'global\',key:\'alert_copy_main\',value:\''+
      (g.alert_copy_main=='1'?'0':'1')+'\'},\'已切换\')">'+(g.alert_copy_main=='1'?'已开启':'已关闭')+'</button></div>'+
    '<div class="row"><span class="k">联合封禁</span>'+
    '<button class="b" onclick="act(\'set\',{scope:\'global\',key:\'gban_enabled\',value:\''+
      (g.gban_enabled=='1'?'0':'1')+'\'},\'已切换\')">'+(g.gban_enabled=='1'?'已开启':'已关闭')+'</button></div></div>';
  var specs=S.specs.filter(function(sp){ return sp.group==''||sp.group=='both'; });
  h+='<div class="card"><h3>全局参数</h3>'+specs.map(function(sp){
    return '<div class="row"><span class="k">'+esc(sp.label)+'<div class="hint">'+esc(sp.hint)+'</div></span>'+
      '<input style="width:110px" value="'+esc(g[sp.key]||'')+'" '+
      'onchange="act(\'set\',{scope:\'global\',key:\''+sp.key+'\',value:this.value})"></div>';
  }).join('')+'</div>';
  h+='<div class="card"><h3>展示时区</h3>'+
    '<div class="row"><span class="k">IANA 时区名，所有时间按它显示</span></div>'+
    '<input value="'+esc(g.tz_name||'')+'" placeholder="Asia/Shanghai" '+
    'onchange="act(\'set\',{scope:\'global\',key:\'tz_name\',value:this.value},\'已保存\')"></div>';
  h+='<div class="card"><h3>群内提示附加链接</h3>'+
    '<div class="row"><span class="k">原样附在群内告警与进群限制通知的末尾</span></div>'+
    '<input value="'+esc(g.antiad_group_footer||'')+'" '+
    'placeholder="② 电报使用指南 (https://t.me/TGwikiAppBot)" '+
    'onchange="act(\'set\',{scope:\'global\',key:\'antiad_group_footer\',value:this.value},\'已保存\')"></div>';
  h+='<div class="card"><h3>形态摘要</h3>'+
    '<textarea id="dg">'+esc(S.digest||'')+'</textarea>'+
    '<div class="grid" style="margin-top:8px">'+
    '<button class="b" onclick="act(\'digest\',{action:\'save\',value:document.getElementById(\'dg\').value},\'已保存\')">保存</button>'+
    '<button class="b g" onclick="act(\'digest\',{action:\'run\'},\'已触发重新总结\')">立即重新总结</button>'+
    '</div></div>';
  return h;
}

/* ---- 名单：主管理员四段切换；次级管理员看到专属组 + 全局组 ---- */
function viewLists(){
  if(!S.me.main) return viewGbanOwn()+viewGban();
  var tabs=[['own','专属封禁组'],['gban','全局封禁'],['admins','次级管理员'],['white','白名单']];
  var h='<div class="card" style="padding:10px 12px"><div style="display:flex;gap:6px">'+
    tabs.map(function(t){ return '<button style="flex:1" class="'+(LSUB==t[0]?'b':'b g')+
      '" onclick="LSUB=\''+t[0]+'\';render()">'+t[1]+'</button>'; }).join('')+'</div></div>';
  if(LSUB=='gban') return h+viewGban();
  if(LSUB=='white') return h+viewWhite();
  if(LSUB=='own') return h+viewGbanOwn();
  return h+viewAdmins();
}
/* 专属联合封禁组：每个管理员名下一个，可开关、圈定生效群、管名单。 */
function viewGbanOwn(){
  var o=S.gban_own||{enabled:true,chats:[],bans:[]};
  var inGroup={}; (o.chats||[]).forEach(function(c){ inGroup[c]=true; });
  var ownChats=S.chats.filter(function(c){
    var b=botById(c.bot_id); return b && b.owner_id==S.me.uid; });
  var h='<div class="card"><h3>专属联合封禁组</h3>'+
    '<div class="row"><span class="k">开启</span>'+toggleBtn(o.enabled,
      "act('gbanown',{action:'enable',on:"+(o.enabled?'false':'true')+"},'已切换')")+'</div>'+
    '<div class="hint">名下 bot 判定的最高档命中自动进这个组，只在你圈定的群里执行；'+
    'bot 是否参与全局组在机器人详情页里选。</div>';
  h+='<div class="hint" style="margin-top:10px">生效群（点开关圈定）</div>';
  if(!ownChats.length) h+='<div class="hint">（名下 bot 还没有群）</div>';
  ownChats.forEach(function(c){
    h+='<div class="row"><label class="sw"><input type="checkbox" '+(inGroup[c.chat_id]?'checked':'')+
      ' onchange="act(\'gbanown\',{action:\'chat\',chat_id:'+c.chat_id+',on:this.checked},\'已保存\')"><span class="tr"></span> '+
      esc(c.title||c.chat_id)+'</label><span class="mono">'+c.chat_id+'</span></div>';
  });
  h+='<div class="hint" style="margin-top:10px">封禁名单</div>';
  (o.bans||[]).forEach(function(g){
    h+='<div class="row"><span class="mono">'+g.user_id+' · '+esc(g.reason)+'</span>'+
      '<button class="d" onclick="act(\'gbanown\',{action:\'remove\',user_id:'+g.user_id+'},\'已移除\')">移除</button></div>';
  });
  if(!(o.bans||[]).length) h+='<div class="hint">（名单为空）</div>';
  h+='<div class="grid" style="margin-top:8px"><input id="ow_uid" placeholder="user_id">'+
    '<input id="ow_reason" placeholder="原因"></div>'+
    '<button class="b" style="margin-top:8px" onclick="act(\'gbanown\',{action:\'add\',user_id:document.getElementById(\'ow_uid\').value,'+
    'reason:document.getElementById(\'ow_reason\').value},\'已加入\')">加入专属组</button></div>';
  return h;
}
function viewAdmins(){
  return '<div class="card"><h3>次级管理员</h3>'+
    (S.admins||[]).map(function(a){
      return '<div class="row"><span class="mono">'+a.user_id+' '+esc(a.note)+'</span>'+
      '<button class="d" onclick="act(\'admin\',{action:\'remove\',user_id:'+a.user_id+',},\'已移除\')">移除</button></div>';
    }).join('')+
    '<div class="grid" style="margin-top:8px"><input id="ad_uid" placeholder="user_id">'+
    '<input id="ad_note" placeholder="备注"></div>'+
    '<button class="b" style="margin-top:8px" onclick="act(\'admin\',{action:\'add\',user_id:document.getElementById(\'ad_uid\').value,'+
    'note:document.getElementById(\'ad_note\').value},\'已添加\')">添加</button></div>';
}
function viewGban(){
  return '<div class="card"><h3>全局联合封禁组</h3>'+
    '<div class="hint" style="margin-bottom:8px">所有管理员共同维护；只有加入全局组的 bot 会执行'+
    '（机器人详情页里选），命中自动入组的规则见专属组说明。</div>'+
    (S.gban||[]).map(function(g){
      return '<div class="row"><span class="mono">'+g.user_id+' '+esc(g.reason)+'</span>'+
      '<button class="d" onclick="if(confirm(\'解除封禁？\'))act(\'gban\',{action:\'remove\',user_id:'+g.user_id+'},\'已解除\')">解除</button></div>';
    }).join('')+
    '<div class="grid" style="margin-top:8px"><input id="gb_uid" placeholder="user_id">'+
    '<input id="gb_reason" placeholder="原因"></div>'+
    '<button class="b" style="margin-top:8px" onclick="act(\'gban\',{action:\'add\',user_id:document.getElementById(\'gb_uid\').value,'+
    'reason:document.getElementById(\'gb_reason\').value},\'已加入\')">加入名单</button></div>';
}
function viewWhite(){
  var h='<div class="card"><h3>白名单</h3>'+
    (S.whitelist||[]).map(function(w){
      var scope = w.bot_id==0?'全平台':(w.chat_id==0?'bot 所有群':'群 '+w.chat_id);
      return '<div class="row"><span class="mono">'+w.user_id+' · '+scope+' · '+esc(w.source)+'</span>'+
      '<button class="d" onclick="act(\'whitelist\',{action:\'remove\',bot_id:'+w.bot_id+',chat_id:'+w.chat_id+
      ',user_id:'+w.user_id+'},\'已移除\')">移除</button></div>';
    }).join('')+
    '<div class="grid" style="margin-top:8px"><select id="wl_bot">'+botOpts()+'</select>'+
    '<input id="wl_uid" placeholder="user_id"></div>'+
    '<div class="grid" style="margin-top:6px"><input id="wl_chat" placeholder="chat_id（0 = 该 bot 所有群）">'+
    '<input id="wl_hours" placeholder="小时（空 = 永久）"></div>'+
    '<button class="b" style="margin-top:8px" onclick="act(\'whitelist\',{action:\'add\',bot_id:document.getElementById(\'wl_bot\').value,'+
    'chat_id:document.getElementById(\'wl_chat\').value||0,user_id:document.getElementById(\'wl_uid\').value,'+
    'hours:document.getElementById(\'wl_hours\').value||0},\'已加入\')">加入白名单</button></div>';
  // 默认豁免可视化：这些人不在白名单表里，但判定链路（adExempt）本来就放行。
  // 列在这里只为让管理员能核对「谁不用判」，不要往这里加配置入口。
  h+='<div class="card"><h3>默认豁免（内置，无需配置）</h3>'+
    '<div class="hint">下面这些人的消息不送检、不处置。它们不在上面的白名单表里，是判定前的内置放行：</div>'+
    '<div class="row"><span>主管理员（你）</span><span class="mono">uid '+S.me.uid+'</span></div>'+
    S.bots.filter(function(b){return b.owner_id;}).map(function(b){
      return '<div class="row"><span>「'+esc(b.label)+'」归属人</span><span class="mono">uid '+b.owner_id+'</span></div>';
    }).join('')+
    '<div class="row"><span>各群的群主与管理员</span><span class="badge ok">判定时实时查询</span></div>'+
    '<div class="row"><span>匿名管理员 / 关联频道转发</span><span class="badge ok">默认放行</span></div>'+
    '<div class="row"><span>有管理员权限的 bot</span><span class="badge ok">默认不判</span></div>'+
    '<div class="row"><span>普通 bot（工具 bot）</span><span class="badge">默认照判</span></div>'+
    '<div class="hint" style="margin-top:6px">群主/管理员向 Telegram 实时查询，结果缓存 10 分钟。'+
    '工具 bot 默认与普通成员一样送检；要全豁免可在机器人详情页关闭「判定普通成员 bot」。</div></div>';
  return h;
}

/* ---- 记录与申诉：共用的小渲染器 ---- */
function fmtTS(at){ if(!at) return '—';
  var d=new Date(at*1000);
  function p(n){ return (n<10?'0':'')+n; }
  return (d.getMonth()+1)+'-'+p(d.getDate())+' '+p(d.getHours())+':'+p(d.getMinutes()); }
function verdictLabel(v){ return {ad:'广告',none:'正常',clean:'正常',error:'失败',
  skipped:'未送检'}[v]||v; }
function actionLabel(a){ return {none:'无处置',muted:'禁言',banned:'封禁出群',
  deleted:'删除',deleted_muted:'删除+禁言',deleted_banned:'删除+封禁',undone:'已撤销'}[a]||
  (a.indexOf('dryrun:')==0?'演练:'+a.slice(7):a); }
function apStatus(s){ return {statement:'待写理由',ai:'AI 复核中',web:'等网页验证',
  noweb:'待人工处理',code:'已发解禁码',redeemed:'已兑换',lifted:'已解除',
  rejected:'已驳回',expired:'已过期'}[s]||s; }
function apAI(r){ return {uphold:'维持原判',overturn:'撤销原判',error:'复核出错',
  skipped:'跳过复核'}[r]||'未复核'; }
function chatTitleOf(cid){ for(var i=0;i<S.chats.length;i++)
  if(S.chats[i].chat_id==cid) return S.chats[i].title; return ''; }
/* settingOf 取某个 bot 的实际生效值：bot 覆盖优先，否则全局默认。 */
function settingOf(botID,key){
  var o=(S.bot_settings[String(botID)]||{});
  if(o[key]!=null&&o[key]!=='') return o[key];
  var d=S.global_defaults||{};
  return d[key]!=null?d[key]:'';
}
/* punishLabel 把「跟随/禁言/封禁」渲染成实际会执行的动作。
   只写「禁言」而不写时长时，很容易以为永久封禁已经生效。 */
function punishLabel(botID,punish){
  var hours=settingOf(botID,'antiad_mute_hours')||'24';
  if(punish==1) return '封禁出群（永久）';
  var mute=(hours==='0')?'永久禁言':('禁言 '+hours+' 小时');
  if(punish==0) return mute;
  return settingOf(botID,'antiad_ban')=='1' ? '封禁出群（永久）' : mute;
}
function muteOptLabel(botID){
  var hours=settingOf(botID,'antiad_mute_hours')||'24';
  return (hours==='0')?'永久禁言':('禁言 '+hours+' 小时');
}
function logact(a,id){
  api('logact',{id:id,action:a}).then(function(){
    toast('已执行');
    // 详情缓存作废：操作后旧卡片会继续显示旧状态并重复给已执行的按钮。
    if(S.LD) delete S.LD[id];
    return load();
  }).catch(function(e){ toast('❌ '+e.message); });
}
function apact(a,id){ act('appealact',{id:id,action:a},'已执行'); }

/* ---- 记录：筛选 + 搜索 + 列表 → 详情可操作 ---- */
function viewLogs(){
  if(SUB.indexOf('log:')==0){
    var id=+SUB.slice(4);
    // 详情总是走单独的接口：流水正文为空时后端会回查全量留底，
    // 列表接口没有这一步，直接用缓存会漏掉原文。
    if(S.LD && S.LD[id]) return viewLogDetail(S.LD[id]);
    api('log',{id:id}).then(function(d){
      S.LD=S.LD||{}; S.LD[id]=d; render();
    }).catch(function(e){ SUB=''; render(); toast('❌ '+e.message); });
    return backRow()+'<div class="card">加载中…</div>';
  }
  var chips=[['deleted','已删除'],['all','全部'],['ad','命中'],['clean','正常'],
    ['skipped','跳过']];
  return '<div class="card"><div class="row" style="border:0;padding:0 0 8px">'+
    '<input id="log_q" placeholder="搜原文 / 理由 / uid / 群号" value="'+esc(LOGQ)+'" '+
    'onkeydown="if(event.key==\'Enter\'){LOGQ=this.value;LOGPAGE=1;loadLogs();render();}">'+
    '<button class="b" onclick="LOGQ=document.getElementById(\'log_q\').value;'+
    'LOGPAGE=1;loadLogs();render()">搜</button></div>'+
    '<div class="chips">'+chips.map(function(f){
      return '<button class="'+(LOGF==f[0]?'on':'')+'" onclick="LOGF=\''+f[0]+
        '\';LOGPAGE=1;loadLogs();render()">'+f[1]+'</button>';
    }).join('')+'</div>'+
    '<div id="logbox" style="margin-top:8px">加载中…</div>'+
    '<div class="grid" style="margin-top:8px">'+
    '<button class="b g" onclick="pageLogs(-1)">上一页</button>'+
    '<button class="b g" onclick="pageLogs(1)">下一页</button></div></div>';
}
function pageLogs(d){ LOGPAGE=Math.max(1,LOGPAGE+d); loadLogs(); }
function loadLogs(){
  api('logs',{page:LOGPAGE,verdict:LOGF=='all'?'':LOGF,q:LOGQ}).then(function(d){
    S.LOGS=d.logs;
    var el=document.getElementById('logbox'); if(!el) return;
    el.innerHTML = d.logs.map(function(l){
      return '<div class="row item" onclick="go(\'logs\',\'log:'+l.id+'\')">'+
        '<span><span class="mono">#'+l.id+' · '+fmtTS(l.created_at)+'</span> '+
        '<span class="badge '+(l.verdict=='ad'?'no':'ok')+'">'+verdictLabel(l.verdict)+'</span>'+
        '<span class="badge">'+esc(actionLabel(l.action))+'</span>'+
        '<div class="hint">uid '+l.user_id+' · 群 '+l.chat_id+' · '+
        Math.round(l.confidence*100)+'% · '+esc(l.cost_text)+'</div>'+
        (l.text?'<div class="hint">'+esc(l.text.slice(0,50))+'</div>':'')+
        '</span><span class="chev">›</span></div>';
    }).join('') || '<div class="hint">（没有记录）</div>';
  }).catch(function(e){ var el=document.getElementById('logbox'); if(el) el.textContent='❌ '+e.message; });
}
function viewLogDetail(l){
  var h=backRow();
  h+='<div class="card"><h3>记录 #'+l.id+' <span class="badge '+
    (l.verdict=='ad'?'no':'ok')+'">'+verdictLabel(l.verdict)+'</span></h3>'+
    '<div class="row"><span class="k">时间</span><span class="mono">'+fmtTS(l.created_at)+'</span></div>'+
    '<div class="row"><span class="k">群 / 用户</span><span class="mono">'+l.chat_id+
    (chatTitleOf(l.chat_id)?' · '+esc(chatTitleOf(l.chat_id)):'')+' / uid '+l.user_id+'</span></div>'+
    '<div class="row"><span class="k">判定</span><span>'+verdictLabel(l.verdict)+' · '+
    Math.round(l.confidence*100)+'%'+(l.decider?' · '+esc(l.decider):'')+
    (l.kind?' · '+esc(l.kind):'')+'</span></div>'+
    '<div class="row"><span class="k">处置</span><span>'+esc(actionLabel(l.action))+'</span></div>'+
    '<div class="row"><span class="k">开销</span><span>'+esc(l.cost_text)+'</span></div>'+
    (l.reason?'<div class="row"><span class="k">理由</span><span>'+esc(l.reason)+'</span></div>':'')+
    (l.text?'<div class="hint" style="margin-top:8px">原文</div><div class="mono" style="white-space:pre-wrap">'+esc(l.text)+'</div>':'')+
    (l.view_url?'<button class="b g" style="margin-top:10px;width:100%" onclick="window.open(\''+
      l.view_url+'\',\'_blank\')">📄 打开原文查看页</button>':'')+
    '</div>';
  h+='<div class="card"><h3>操作</h3>'+
    '<div class="grid">'+
    '<button class="b g" onclick="logact(\'review\','+l.id+')">🔎 AI 复查</button>'+
    '<button class="b g" onclick="logact(\'unmute\','+l.id+')">🔓 解除限制</button>'+
    '<button class="b g" onclick="logact(\'white\','+l.id+')">🤍 加白名单 24h</button>'+
    '<button class="b" onclick="if(confirm(\'不经 AI 直接按最高档处置？\'))logact(\'ban\','+l.id+')">🖐 人工标记广告</button>'+
    '</div>'+
    (S.me.main?'<div class="grid" style="margin-top:8px">'+
      '<button class="d" onclick="if(confirm(\'加入联合封禁名单并全平台执行？\'))logact(\'gban\','+l.id+')">🚫 联合封禁</button>'+
      '<button class="d" onclick="logact(\'ungban\','+l.id+')">解除联合封禁</button></div>':'')+
    '<div class="hint" style="margin-top:8px">复查结果发到群里（受群内静默开关约束）；'+
    '人工标记与群内 /ban 命令同效。</div></div>';
  return h;
}

/* ---- 申诉：独立页签，处理流程在详情页里闭环 ---- */
function viewAppeals(){
  if(SUB.indexOf('ap:')==0){
    var id=+SUB.slice(3);
    var a=(S.APS||[]).filter(function(x){return x.id==id;})[0];
    if(a) return viewAppealDetail(a);
    SUB='';
  }
  var chips=[['open','未结'],['','全部']];
  return '<div class="card"><div class="chips">'+chips.map(function(f){
      return '<button class="'+(APF==f[0]?'on':'')+'" onclick="APF=\''+f[0]+
        '\';APPAGE=1;loadAppeals();render()">'+f[1]+'</button>';
    }).join('')+'</div>'+
    '<div id="apbox" style="margin-top:8px">加载中…</div>'+
    '<div class="grid" style="margin-top:8px">'+
    '<button class="b g" onclick="pageAppeals(-1)">上一页</button>'+
    '<button class="b g" onclick="pageAppeals(1)">下一页</button></div></div>';
}
function pageAppeals(d){ APPAGE=Math.max(1,APPAGE+d); loadAppeals(); }
function loadAppeals(){
  // 筛选交给服务端：只筛已取回的这一页，更新更早的未结单永远不会出现。
  api('appeals',{page:APPAGE,filter:APF}).then(function(d){
    S.APS=d.appeals;
    // 详情页也依赖 S.APS：拿到新数据后原地重绘，否则操作后一直显示旧状态。
    if(SUB.indexOf('ap:')==0){
      var cur=(S.APS||[]).filter(function(x){return x.id==+SUB.slice(3);})[0];
      var v=document.getElementById('view');
      if(cur&&v) v.innerHTML=viewAppealDetail(cur);
      return;
    }
    var el=document.getElementById('apbox'); if(!el) return;
    el.innerHTML = d.appeals.map(function(a){
      return '<div class="row item" onclick="go(\'appeals\',\'ap:'+a.id+'\')">'+
        '<span><span class="mono">#'+a.id+' · uid '+a.user_id+'</span> '+
        '<span class="badge '+(a.status=='lifted'||a.status=='redeemed'?'ok':
          (a.status=='rejected'?'no':''))+'">'+apStatus(a.status)+'</span>'+
        '<div class="hint">'+apAI(a.ai_result)+
        (a.ai_reason?' · '+esc(a.ai_reason.slice(0,40)):'')+'</div></span>'+
        '<span class="chev">›</span></div>';
    }).join('') || '<div class="hint">（没有申诉）</div>';
  }).catch(function(e){ var el=document.getElementById('apbox'); if(el) el.textContent='❌ '+e.message; });
}
function viewAppealDetail(a){
  var open=['statement','ai','web','noweb','code'].indexOf(a.status)>=0;
  var bb=botById(a.bot_id);
  var h=backRow();
  h+='<div class="card"><h3>申诉 #'+a.id+' <span class="badge">'+apStatus(a.status)+'</span></h3>'+
    '<div class="row"><span class="k">申诉人 / bot</span><span class="mono">uid '+a.user_id+
    ' / '+(bb?esc(bb.label):a.bot_id)+'</span></div>'+
    '<div class="row"><span class="k">提交 / 更新</span><span class="mono">'+
    fmtTS(a.created_at)+' / '+fmtTS(a.updated_at)+'</span></div>'+
    (a.statement?'<div class="hint" style="margin-top:8px">申诉理由</div><div>'+esc(a.statement)+'</div>':'')+
    '<div class="row" style="margin-top:8px"><span class="k">AI 复核</span><span>'+apAI(a.ai_result)+
    (a.ai_conf?' · '+Math.round(a.ai_conf*100)+'%':'')+
    (a.ai_model?' · '+esc(a.ai_model):'')+'</span></div>'+
    (a.ai_reason?'<div class="hint">'+esc(a.ai_reason)+'</div>':'')+
    '<div class="row"><span class="k">网页验证</span><span>'+a.web_attempts+' 次尝试</span></div>'+
    (a.has_code?'<div class="row"><span class="k">解禁码</span><span>已签发'+
      (a.code_expires?' · '+fmtTS(a.code_expires)+' 到期':'')+'</span></div>':'')+
    (a.detail_url?'<div style="margin-top:8px"><a href="'+esc(a.detail_url)+
      '" target="_blank">📄 申诉详情页</a></div>':'')+
    '</div>';
  if(open){
    h+='<div class="card"><h3>处理</h3>'+
      '<div class="grid">'+
      '<button class="b" onclick="if(confirm(\'确认通过并解除该用户全部限制？\'))apact(\'approve\','+a.id+')">✅ 人工解除</button>'+
      '<button class="d" onclick="if(confirm(\'确认驳回？限制保持原样。\'))apact(\'reject\','+a.id+')">❌ 驳回</button>'+
      '</div>'+
      ((a.status=='web'||a.status=='noweb'||a.status=='ai')?
        '<div class="grid" style="margin-top:8px">'+
        (a.status!='ai'?'<button class="b g" onclick="apact(\'issue_code\','+a.id+')">🎟 直接签发解禁码</button>':'')+
        '<button class="b g" onclick="apact(\'rerun\','+a.id+')">🔁 重跑 AI 复核</button></div>':'')+
      '<div class="hint" style="margin-top:8px">人工解除与 AI 撤销同效：解除禁言与冷判定限制；'+
      '联合封禁只有主管理员能在此一并解除。</div></div>';
  }
  return h;
}
var _render = render;
render = function(){ _render();
  if(TAB=='logs') loadLogs();
  if(TAB=='appeals') loadAppeals(); };
// 首屏加载由 boot() 驱动：等 telegram-web-app.js（拿 initData）就绪再请求。
</script>
</body>
</html>`
