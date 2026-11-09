// Run with: bun test scripts/admin_ui.test.js
import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';

const html=readFileSync(new URL('../ui.html',import.meta.url),'utf8');
const bytesSource=html.slice(html.indexOf('const adminBytes='),html.indexOf('function adminCurrent('));
const adminBytes=new Function(bytesSource+';return adminBytes')();
const renderSource=html.slice(html.indexOf('function adminRenderUsers(d){'),html.indexOf("$('adminRefresh').onclick="));
const escapeHTML=s=>String(s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));

function render(data){
  const elements=new Map();
  const $=id=>{if(!elements.has(id))elements.set(id,{style:{},hidden:false,innerHTML:'',textContent:''});return elements.get(id)};
  const t=key=>({'admin.offline':'offline','admin.online':'online: {0}'}[key]||key);
  new Function('$','t','adminBytes','esc',renderSource+';adminRenderUsers(arguments[4])')($,t,adminBytes,escapeHTML,data);
  return $;
}

describe('admin traffic statistics',()=>{
  test('zero traffic and offline users remain visible',()=>{
    const $=render({stats_enabled:true,stats:true,users:[{user:'Admin',owner:true}]});
    expect($('adminUsers').innerHTML).toContain('offline');
    expect($('adminUsers').innerHTML).toContain('↓ 0 B · ↑ 0 B');
    expect($('adminTotals').hidden).toBe(false);
    expect($('adminTotals').innerHTML).toContain('↓ 0 B');
    expect($('adminDisableStats').style.display).toBe('');
    expect($('adminEnableStats').style.display).toBe('none');
  });
  test('nonzero traffic and connections are summed across users',()=>{
    const $=render({stats_enabled:true,stats:true,users:[{user:'a',tx:1000,rx:2000,online:1},{user:'b',tx:2000,rx:4000,online:2}]});
    expect($('adminTotals').innerHTML).toContain('↓ 3.0 KB');
    expect($('adminTotals').innerHTML).toContain('↑ 6.0 KB');
    expect($('adminTotals').innerHTML).toContain('<b>3</b>');
    expect($('adminUsers').innerHTML).toContain('online: 2');
  });
  test('API failure is not presented as zero traffic or offline',()=>{
    const $=render({stats_enabled:true,stats:false,stats_error:'unavailable',users:[{user:'Admin',owner:true}]});
    expect($('adminUsers').innerHTML).not.toContain('0 B');
    expect($('adminUsers').innerHTML).not.toContain('offline');
    expect($('adminUsers').innerHTML).toContain('admin.statsnodata');
    expect($('adminStatsState').textContent).toBe('admin.statsfailed');
    expect($('adminDisableStats').style.display).toBe('');
  });
  test('disabled collection hides counters and offers enabling',()=>{
    const $=render({stats_enabled:false,stats:false,users:[{user:'Admin',owner:true}]});
    expect($('adminTotals').hidden).toBe(true);
    expect($('adminUsers').innerHTML).not.toContain('0 B');
    expect($('adminStatsWarning').textContent).toBe('admin.statsoffhint');
    expect($('adminDisableStats').style.display).toBe('none');
    expect($('adminEnableStats').style.display).toBe('');
  });
  test('usernames are escaped in both labels and actions',()=>{
    const $=render({stats_enabled:true,stats:true,users:[{user:'<test>"'}]});
    expect($('adminUsers').innerHTML).not.toContain('<test>');
    expect($('adminUsers').innerHTML).toContain('&lt;test&gt;&quot;');
  });
});
