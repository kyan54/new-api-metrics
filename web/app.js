'use strict';
const $=id=>document.getElementById(id), num=n=>Number(n).toLocaleString('zh-CN');
let dimension='channel', ready=false, busy=false, lastQuery=null, periods=[], channels=[];
async function api(path,body){
 const opts=body?{method:'POST',headers:{'Content-Type':'application/json','X-Metrics-Request':'1'},body:JSON.stringify(body)}:{};
 const res=await fetch(path,opts); const data=await res.json();
 if(res.status===401&&path!='/api/login'){showLogin();const err=Error('会话已过期，请重新登录');err.status=401;throw err;}
 if(!res.ok)throw Error(data.error||'请求失败');return data;
}
function showLogin(){$('dashboard').hidden=true;$('login').hidden=false;ready=false;}
function fill(id,list,all){const select=$(id);select.replaceChildren(new Option(all,''));for(const x of list)select.add(new Option(typeof x==='string'?x:x.name+' · #'+x.id,typeof x==='string'?x:x.id));}
function query(){const q=new URLSearchParams({dimension,mode:$('mode').value});const dates=$('mode').value==='range'?['start','end']:['month'];for(const id of [...dates,'channel','user','key','model'])if($(id).value)q.set(id,$(id).value);return q;}
function channelName(id){return channels.find(c=>c.id===id)?.name||'渠道 #'+id;}
function naturalDates(month){if(!/^\d{4}-\d{2}$/.test(month))return '';const [y,m]=month.split('-').map(Number);return month+'-01 至 '+month+'-'+new Date(y,m,0).getDate();}
function rangePreview(){
 if($('mode').value==='range'){$('range-preview').textContent='统一日期范围：'+($('start').value||'请选择开始日期')+' 至 '+($('end').value||'请选择结束日期')+'（含结束当天）';return;}
 const month=$('month').value,id=Number($('channel').value);
 if(id){const saved=periods.filter(p=>p.channel_id===id),p=saved.find(p=>p.month===month);$('range-preview').textContent=p?month+' 归属月 · '+p.start+' 至 '+p.end+'（含结束当天）':saved.length?'该渠道 '+month+' 尚未配置订阅周期，请先补充或选择日期范围。':'自然月：'+naturalDates(month);}
 else {$('range-preview').textContent='统计月份 '+month+'：各渠道按已保存的订阅周期统计；未配置订阅的渠道按自然月。';}
}
function timeMode(){
 const range=$('mode').value==='range';
 for(const id of ['start','end']){$(id+'-field').hidden=!range;$(id).required=range;$(id).disabled=!range;}
 $('month-field').hidden=range;$('month').required=!range;$('month').disabled=range;rangePreview();
}
function renderPeriods(){
 $('period-rows').replaceChildren();
 for(const p of periods){const tr=document.createElement('tr');for(const v of [channelName(p.channel_id)+' #'+p.channel_id,p.month,p.start,p.end]){const td=document.createElement('td');td.textContent=v;tr.append(td);}
 const td=document.createElement('td');for(const action of ['编辑','删除']){const b=document.createElement('button');b.type='button';b.className='quiet';b.textContent=action;b.onclick=async()=>{
 if(action==='编辑'){$('period-channel').value=p.channel_id;$('period-start').value=p.start;$('period-end').value=p.end;periodMonth();return;}
 if(!confirm('删除 '+channelName(p.channel_id)+' '+p.month+' 的周期？删除最后一个周期后，该渠道恢复自然月统计。'))return;
 try{periods=await api('/api/periods',{channel_id:p.channel_id,month:p.month,delete:true});renderPeriods();rangePreview();await report();}catch(e){$('settings-error').textContent=e.message;}
 };td.append(b);}tr.append(td);$('period-rows').append(tr);}
}
function periodMonth(){$('period-month').textContent=$('period-start').value?'归属月份：'+$('period-start').value.slice(0,7)+'；相同渠道和归属月份再次保存将更新原周期。':'';}
$('mode').onchange=timeMode;for(const id of ['month','channel','start','end'])$(id).addEventListener('change',rangePreview);
$('period-start').onchange=periodMonth;
$('period-form').onsubmit=async e=>{e.preventDefault();e.submitter.disabled=true;$('settings-error').textContent='';try{periods=await api('/api/periods',{channel_id:Number($('period-channel').value),start:$('period-start').value,end:$('period-end').value});renderPeriods();rangePreview();$('settings-error').textContent='已保存';await report();}catch(err){$('settings-error').textContent=err.message;}finally{e.submitter.disabled=false;}};

async function init(){
 try{const o=await api('/api/options');channels=o.channels;fill('channel',o.channels,'全部渠道');fill('period-channel',o.channels,'请选择渠道');periods=await api('/api/periods');renderPeriods();fill('user',o.users,'全部用户');fill('key',o.keys,'全部 Key');fill('model',o.models,'全部模型');$('month').value=o.current_month;$('start').value=o.current_month+'-01';$('end').value=naturalDates(o.current_month).split(' 至 ')[1];timeMode();$('login').hidden=true;$('dashboard').hidden=false;ready=true;await report();}
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
 days.forEach((d,i)=>{const h=d.total_tokens/max*120;const b=svg('rect',{x:20+i*step+3,y:145-h,width:Math.max(4,step-7),height:Math.max(1,h),rx:3,fill:'#237c68',tabindex:0,'aria-label':d.date+'：'+num(d.total_tokens)+' Token'});const t=svg('title',{});t.textContent=d.date+' | '+num(d.total_tokens)+' Token | '+num(d.requests)+' 次';b.append(t);s.append(b);if(i%Math.max(3,Math.ceil(days.length/10))===0||i===days.length-1){const text=svg('text',{x:20+i*step+step/2,y:170,'text-anchor':'middle',fill:'#71817b','font-size':12});text.textContent=d.date.slice(5);s.append(text);}});
 $('chart').replaceChildren(s);
}
async function report(){
 if(busy||!ready)return;busy=true;$('export').disabled=true;$('error').textContent='';$('rows').setAttribute('aria-busy','true');
 try{
 const requested=query().toString();const r=await api('/api/report?'+requested);lastQuery=requested;for(const id of ['total','input','output'])$(id).textContent=num(r.summary[id+'_tokens']);$('requests').textContent=num(r.summary.requests);$('cost').textContent=(r.summary.quota/r.quota_per_unit).toFixed(4);$('currency').textContent=r.currency;
 $('period').textContent=(r.mode==='range'?'自定义日期范围':r.month+' 归属月')+' · '+r.start+' 至 '+r.end+'（含当天） · '+r.timezone;
 const notes=r.periods.map(p=>channelName(p.channel_id)+'：'+p.start+' 至 '+p.end);
 if(r.mode==='month'&&r.periods.length&& !$('channel').value)notes.unshift('按渠道独立周期汇总；上方日期是各周期覆盖范围。未配置订阅的渠道按自然月。');
 if(r.missing_period_channels.length)notes.push('未计入（该月份缺少周期）：'+r.missing_period_channels.map(channelName).join('、'));
 $('range-preview').textContent=notes.length?notes.join(' ｜ '):r.start+' 至 '+r.end+'（含结束当天）';chart(r.days);
 const body=$('rows');body.replaceChildren();for(const row of r.rows){const tr=document.createElement('tr');const name=document.createElement('td');const strong=document.createElement('strong');strong.textContent=row.name;const small=document.createElement('small');small.textContent='#'+row.id+(row.user?' · '+row.user+' #'+row.user_id:'');name.append(strong);if(r.dimension!=='model')name.append(small);if(r.dimension==='channel'){const p=r.periods.find(p=>p.channel_id===row.id);const dates=document.createElement('small');dates.textContent=p?p.start+' 至 '+p.end:r.mode==='range'?r.start+' 至 '+r.end:naturalDates(r.month);name.append(dates);}tr.append(name);for(const v of [num(row.requests),num(row.input_tokens),num(row.output_tokens),num(row.total_tokens),(row.quota/r.quota_per_unit).toFixed(4)]){const td=document.createElement('td');td.textContent=v;tr.append(td);}body.append(tr);}
 $('name-head').textContent={channel:'渠道',key:'API Key',user:'用户',model:'模型'}[r.dimension];$('empty').hidden=r.rows.length>0;$('row-count').textContent=r.rows.length+' 个统计对象';$('quality').textContent=r.summary.zero_usage_requests?'有 '+r.summary.zero_usage_requests+' 条消费记录的输入和输出均为零；可能为零用量或未记录 usage，请核对原始日志。':'记录中的零 Token 消费请求：0';
 $('generated').textContent='查询时间：'+new Date(r.generated_at).toLocaleString('zh-CN')+' · 额度换算系数：'+num(r.quota_per_unit);
 for(const b of $('tabs').querySelectorAll('button')){b.classList.toggle('active',b.dataset.dim===r.dimension);b.setAttribute('aria-pressed',b.dataset.dim===r.dimension);}
 }catch(e){lastQuery=null;$('error').textContent=e.message; /* Preserve last successful report visibly, but disable export until next success. */ $('period').textContent='查询未完成；下方为上次成功的结果';}
 finally{busy=false;$('export').disabled=!lastQuery;$('rows').removeAttribute('aria-busy');}
}
init();
