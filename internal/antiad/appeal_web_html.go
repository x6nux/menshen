package antiad

// appealPageHTML 是验证页模板。占位符用 strings.ReplaceAll 填：
// {{NONCE}} / {{SITEKEY}} / {{CDATA}} / {{DAYS}}。
//
// 用占位符而不是 html/template：页面里有一段内联脚本，模板引擎会把
// 里面的 {{ }} 与 JS 当模板语法处理，维护成本高于收益。
const appealPageHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>申诉验证</title>
<style>
body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:0;padding:24px;
 max-width:560px;margin:0 auto;line-height:1.6;color:#1a1a1a;background:#fafafa}
h1{font-size:20px;margin:8px 0 16px}
.card{background:#fff;border:1px solid #e5e5e5;border-radius:10px;padding:16px;margin-bottom:16px}
.warn{color:#8a5a00;background:#fff8e6;border:1px solid #f0d9a8;border-radius:8px;
 padding:10px 12px;font-size:13px;margin-bottom:16px}
#out{margin-top:16px;font-size:15px;white-space:pre-wrap}
#code{font-family:ui-monospace,Menlo,monospace;font-size:20px;letter-spacing:1px;
 display:inline-block;padding:8px 12px;background:#eef7ee;border:1px solid #b7ddb7;border-radius:8px}
</style>
</head>
<body>
<h1>申诉验证</h1>
<div class="warn">为防止滥用，本页会记录你的 IP 与浏览器特征，仅用于反垃圾审核，保留 {{DAYS}} 天。</div>
<div class="card">
  <p>完成下方的人机验证后，会给你一个<b>解禁码</b>，把它交给群管理员即可解除限制。</p>
  <div class="cf-turnstile" data-sitekey="{{SITEKEY}}" data-action="appeal"
       data-cdata="{{CDATA}}" data-callback="onToken"></div>
  <p style="font-size:13px;color:#666">验证组件加载不出来时，请用系统浏览器打开本页。</p>
  <div id="out"></div>
</div>
<script nonce="{{NONCE}}" src="https://challenges.cloudflare.com/turnstile/v0/api.js" async defer></script>
<script nonce="{{NONCE}}">
function onToken(token){
  collect().then(function(signals){
    fetch(location.href,{method:'POST',headers:{'Content-Type':'application/json'},
      body:JSON.stringify({token:token,signals:signals})})
    .then(function(r){return r.json()})
    .then(function(d){
      var out=document.getElementById('out');
      if(d.ok){out.innerHTML='解禁码：<span id="code">'+d.code+'</span><br>'+d.msg;}
      else{out.textContent=d.msg;}
    })
    .catch(function(){document.getElementById('out').textContent='提交失败，请刷新重试。';});
  });
}
function sha256hex(str){
  var enc=new TextEncoder().encode(str);
  if(!window.crypto||!crypto.subtle)return Promise.resolve('');
  return crypto.subtle.digest('SHA-256',enc).then(function(buf){
    return Array.prototype.map.call(new Uint8Array(buf),function(b){
      return ('00'+b.toString(16)).slice(-2);}).join('');
  }).catch(function(){return '';});
}
function collect(){
  var s={ua:navigator.userAgent,platform:navigator.platform,
    languages:navigator.languages||[],timezone:'',
    webdriver:navigator.webdriver===true,automation:[],doc_props:[],
    screen:{w:screen.width,h:screen.height,depth:screen.colorDepth,
      dpr:window.devicePixelRatio||1},
    hardware_concurrency:navigator.hardwareConcurrency||0,
    device_memory:navigator.deviceMemory||0,
    max_touch_points:navigator.maxTouchPoints||0,
    canvas_hash:'',webgl_vendor:'',webgl_renderer:'',audio_hash:0,fonts:[],
    plugins:navigator.plugins?navigator.plugins.length:0,
    notification_permission:(typeof Notification!=='undefined')?Notification.permission:'',
    permission_query:window.__perm||''};
  try{s.timezone=Intl.DateTimeFormat().resolvedOptions().timeZone||'';}catch(e){}
  ['callPhantom','_phantom','__nightmare','domAutomation','domAutomationController',
   '_selenium','__webdriver_evaluate','__selenium_unwrapped','__fxdriver_unwrapped'
  ].forEach(function(k){if(k in window)s.automation.push(k);});
  for(var k in document){
    if(k.indexOf('$cdc_')===0||k.indexOf('$wdc_')===0)s.doc_props.push(k);
  }
  try{
    var c=document.createElement('canvas');c.width=200;c.height=40;
    var x=c.getContext('2d');x.textBaseline='top';x.font='14px Arial';
    x.fillText('menshen-fp',2,2);
    s.canvas_hash=c.toDataURL();
  }catch(e){}
  try{
    var g=document.createElement('canvas').getContext('webgl');
    var dbg=g.getExtension('WEBGL_debug_renderer_info');
    s.webgl_vendor=dbg?g.getParameter(dbg.UNMASKED_VENDOR_WEBGL):'';
    s.webgl_renderer=dbg?g.getParameter(dbg.UNMASKED_RENDERER_WEBGL):'';
  }catch(e){}
  try{
    var base='monospace';
    var list=['Arial','Courier New','Georgia','Times New Roman','Verdana','Tahoma',
      'Trebuchet MS','Impact','Comic Sans MS','Segoe UI','Calibri','Cambria','Consolas',
      'Helvetica','Roboto','Ubuntu','Noto Sans','PingFang SC','Microsoft YaHei','SimSun',
      'SimHei','WenQuanYi Micro Hei','Apple Color Emoji','Menlo','Monaco','Courier',
      'Futura','Gill Sans','Optima','Palatino'];
    var cv=document.createElement('canvas');var ctx=cv.getContext('2d');
    var probe='mmmmmmmmmmlli';
    ctx.font='72px '+base;var bw=ctx.measureText(probe).width;
    list.forEach(function(f){
      ctx.font='72px "'+f+'",'+base;
      if(ctx.measureText(probe).width!==bw)s.fonts.push(f);
    });
  }catch(e){}
  try{
    if(navigator.permissions&&navigator.permissions.query){
      navigator.permissions.query({name:'notifications'}).then(function(p){
        window.__perm=p.state;}).catch(function(){});
    }
  }catch(e){}
  return new Promise(function(resolve){
    try{
      var AC=window.OfflineAudioContext||window.webkitOfflineAudioContext;
      if(!AC){resolve(s);return;}
      var ctx=new AC(1,44100,44100);
      var osc=ctx.createOscillator();osc.type='triangle';osc.frequency.value=1000;
      var comp=ctx.createDynamicsCompressor();
      osc.connect(comp);comp.connect(ctx.destination);osc.start(0);
      ctx.startRendering();
      ctx.oncomplete=function(ev){
        var b=ev.renderedBuffer.getChannelData(0);var sum=0;
        for(var i=0;i<b.length;i++)sum+=Math.abs(b[i]);
        s.audio_hash=Math.round(sum*1000000)/1000000;
        resolve(s);
      };
    }catch(e){resolve(s);}
  }).then(function(sig){
    return sha256hex(sig.canvas_hash).then(function(h){
      sig.canvas_hash=h||'raw';return sig;});
  });
}
</script>
</body>
</html>`

// expiredPageHTML 是链接失效页。
const expiredPageHTML = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>链接已失效</title>
<style>body{font-family:-apple-system,sans-serif;padding:40px;text-align:center;color:#333}</style>
</head><body>
<h1>链接已失效</h1>
<p>请回到 bot 的私聊里重新发起申诉。</p>
</body></html>`
