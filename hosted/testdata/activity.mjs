import assert from 'node:assert/strict';import fs from 'node:fs';
const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE);
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox']});
try{
 const app=process.argv[2],context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1365,height:1000}});
 await context.addCookies([{name:'__Host-labcentral',value:'cookie',url:app,secure:true,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage(),errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(app+'/admin/');const activity=page.locator('section').filter({has:page.getByRole('heading',{name:'Live activity',exact:true})});
 const active=activity.getByRole('button',{name:'Active (0)',exact:true}),attention=activity.getByRole('button',{name:/^Needs attention/}),recent=activity.getByRole('button',{name:'Recent',exact:true});
 await active.waitFor();assert.equal(await active.getAttribute('aria-pressed'),'true');assert((await active.getAttribute('class')).includes('btn-primary'));assert(!(await attention.getAttribute('class')).includes('btn-primary'));assert(!(await attention.getAttribute('class')).includes('btn-error'));
 await recent.click();await activity.getByText('This meeting ended or expired. Run checks or exercises in the current meeting.',{exact:true}).waitFor();assert.equal(await activity.getByRole('button',{name:'Retry recovery',exact:true}).count(),2);
 const output=process.env.LABCENTRAL_BROWSER_EVIDENCE;
 for(const width of [1365,390]){
  await page.setViewportSize({width,height:1000});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
  if(output){fs.mkdirSync(output,{recursive:true});await activity.screenshot({path:output+`/activity-recent-${width}.png`})}
 }
 await attention.click();await activity.getByText('Desktop cleanup',{exact:true}).waitFor();const current=activity.locator('li').filter({has:page.getByText('Team 1',{exact:true})});assert.equal(await attention.getAttribute('aria-pressed'),'true');assert((await attention.getAttribute('class')).includes('btn-primary'));assert.equal(await activity.getByText('Team 2',{exact:true}).count(),0);
 // A race can invalidate a previously offered retry. Rejection stays beside
 // the controls and preserves the action, rather than disappearing above scroll.
 await context.route('**/api/admin/jobs/retry',async route=>{await new Promise(r=>setTimeout(r,250));await route.fulfill({status:409,json:{error:'This check was superseded while you were viewing it.'}})});
 await current.getByRole('button',{name:'Retry recovery',exact:true}).click();await activity.getByRole('button',{name:'Queuing recovery…',exact:true}).waitFor();assert(await activity.getByRole('button',{name:'Queuing recovery…',exact:true}).isDisabled());await activity.getByRole('alert').getByText('This check was superseded while you were viewing it.',{exact:true}).waitFor();
 await context.unroute('**/api/admin/jobs/retry');
 await current.getByRole('button',{name:'Retry recovery',exact:true}).click();await activity.getByRole('status').getByText('Recovery queued for Team 1. Follow its progress under Active.',{exact:true}).waitFor();await activity.getByRole('button',{name:'Active (1)',exact:true}).waitFor();assert.equal(await activity.getByRole('button',{name:'Active (1)',exact:true}).getAttribute('aria-pressed'),'true');
 const data=await(await context.request.get(app+'/api/admin')).json();assert.equal(data.jobs.filter(j=>j.status==='queued'&&j.kind==='check').length,1);assert.equal(data.jobs.filter(j=>j.status==='failed'&&j.needsAttention).length,1);
 assert.deepEqual(errors,[]);await context.close();console.log('Activity selection, obsolete failures, inline rejection, pending state, real queue acceptance and responsive layouts passed. Guest execution was not performed.');
}finally{await browser.close()}
