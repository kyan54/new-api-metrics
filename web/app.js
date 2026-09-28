'use strict';
const $=id=>document.getElementById(id), num=n=>Number(n).toLocaleString('zh-CN');
let dimension='channel', ready=false, busy=false, lastQuery=null;
async function api(path,body){
 const opts=body?{method:'POST',headers:{'Content-Type':'application/json','X-Metrics-Request':'1'},body:JSON.stringify(body)}:{};
 const res=await fetch(path,opts); const data=await res.json();
 if(res.status===401&&path!='/api/login'){showLogin();const err=Error('会话已过期，请重新登录');err.status=401;throw err;}
 if(!res.ok)throw Error(data.error||'请求失败');return data;
}
function showLogin(){$('dashboard').hidden=true;$('login').hidden=false;ready=false;}
function fill(id,list,all){const select=$(id);select.replaceChildren(new Option(all,''));for(const x of list)select.add(new Option(typeof x==='string'?x:x.name+' · #'+x.id,typeof x==='string'?x:x.id));}
function query(){const q=new URLSearchParams({dimension});for(const id of ['month','channel','user','key','model'])if($(id).value)q.set(id,$(id).value);return q;}
async function init(){
 try{const o=await api('/api/options');fill('channel',o.channels,'全部渠道');fill('user',o.users,'全部用户');fill('key',o.keys,'全部 Key');fill('model',o.models,'全部模型');$('month').value=o.current_month;$('login').hidden=true;$('dashboard').hidden=false;ready=true;await report();}
 catch(e){$('login-error').textContent=e.status===401?'':e.message;}
}
$('login-form').addEventListener('submit',async e=>{e.preventDefault();const b=e.submitter;b.disabled=true;$('login-error').textContent='';try{await api('/api/login',{username:$('username').value,password:$('password').value});$('password').value='';await init();}catch(err){$('login-error').textContent=err.message;}finally{b.disabled=false;}});
$('logout').onclick=async()=>{try{await api('/api/logout',{});showLogin();}catch(e){$('error').textContent=e.message;}};
$('filters').onsubmit=e=>{e.preventDefault();report();};
$('tabs').onclick=e=>{const b=e.target.closest('button[data-dim]');if(!b||busy)return;dimension=b.dataset.dim;report();};
$('export').onclick=()=>{if(ready&&!busy&&lastQuery)location.href='/api/export?'+lastQuery;};
function svg(tag,attrs){const e=document.createElementNS('http://www.w3.org/2000/svg',tag);for(const [k,v]of Object.entries(attrs))e.setAttribute(k,v);return e;}
function chart(days){
 const s=svg('svg',{viewBox:'0 0 1100 180',role:'img','aria-label':'每日总 Token'}),max=Math.max(1,...days.map(d=>d.total_tokens)),step=1060/days.length;
 s.append(svg('line',{x1:20,y1:145,x2:1080,y2:145,stroke:'#e2e8e5'}));
 days.forEach((d,i)=>{const h=d.total_tokens/max*120;const b=svg('rect',{x:20+i*step+3,y:145-h,width:Math.max(4,step-7),height:Math.max(1,h),rx:3,fill:'#237c68',tabindex:0,'aria-label':d.date+'：'+num(d.total_tokens)+' Token'});const t=svg('title',{});t.textContent=d.date+' | '+num(d.total_tokens)+' Token | '+num(d.requests)+' 次';b.append(t);s.append(b);if(i%3===0||i===days.length-1){const text=svg('text',{x:20+i*step+step/2,y:170,'text-anchor':'middle',fill:'#71817b','font-size':12});text.textContent=d.date.slice(8);s.append(text);}});
 $('chart').replaceChildren(s);
}
async function report(){
 if(busy||!ready)return;busy=true;$('export').disabled=true;$('error').textContent='';$('rows').setAttribute('aria-busy','true');
 try{
 const requested=query().toString();const r=await api('/api/report?'+requested);lastQuery=requested;for(const id of ['total','input','output'])$(id).textContent=num(r.summary[id+'_tokens']);$('requests').textContent=num(r.summary.requests);$('cost').textContent=(r.summary.quota/r.quota_per_unit).toFixed(4);$('currency').textContent=r.currency;
 $('period').textContent=r.month+' · '+r.timezone+' · 当前筛选范围';chart(r.days);
 const body=$('rows');body.replaceChildren();for(const row of r.rows){const tr=document.createElement('tr');const name=document.createElement('td');const strong=document.createElement('strong');strong.textContent=row.name;const small=document.createElement('small');small.textContent='#'+row.id+(row.user?' · '+row.user+' #'+row.user_id:'');name.append(strong,small);tr.append(name);for(const v of [num(row.requests),num(row.input_tokens),num(row.output_tokens),num(row.total_tokens),(row.quota/r.quota_per_unit).toFixed(4)]){const td=document.createElement('td');td.textContent=v;tr.append(td);}body.append(tr);}
 $('name-head').textContent={channel:'渠道',key:'API Key',user:'用户'}[r.dimension];$('empty').hidden=r.rows.length>0;$('row-count').textContent=r.rows.length+' 个统计对象';$('quality').textContent=r.summary.zero_usage_requests?'有 '+r.summary.zero_usage_requests+' 条消费记录的输入和输出均为零；可能为零用量或未记录 usage，请核对原始日志。':'记录中的零 Token 消费请求：0';
 $('generated').textContent='查询时间：'+new Date(r.generated_at).toLocaleString('zh-CN')+' · 额度换算系数：'+num(r.quota_per_unit);
 for(const b of $('tabs').querySelectorAll('button')){b.classList.toggle('active',b.dataset.dim===r.dimension);b.setAttribute('aria-pressed',b.dataset.dim===r.dimension);}
 }catch(e){lastQuery=null;$('error').textContent=e.message; /* Preserve last successful report visibly, but disable export until next success. */ $('period').textContent='查询未完成；下方为上次成功的结果';}
 finally{busy=false;$('export').disabled=!lastQuery;$('rows').removeAttribute('aria-busy');}
}
init();
