import assert from 'node:assert/strict';
import fs from 'node:fs';
const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE);
const [app]=process.argv.slice(2);
const evidence=process.env.LABCENTRAL_BROWSER_EVIDENCE;
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox']});
try {
 for(const width of [1366,390]) {
  const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width,height:950}});
  const page=await context.newPage(); const errors=[];
  page.on('pageerror',e=>errors.push(e.message));
  await context.addCookies([{name:'__Host-labcentral',value:'cookie',url:app,secure:true,httpOnly:true,sameSite:'Strict'}]);
  await page.goto(app+'/lab/');
  await page.getByRole('heading',{name:'Live Red vs. Blue scoreboard',exact:true}).waitFor();
  await page.getByText('Web service',{exact:true}).waitFor();
  await page.getByText('120 / 200',{exact:true}).waitFor();
  await page.getByRole('heading',{name:'Your services',exact:true}).waitFor();
  assert.equal(await page.locator('section[aria-label="scorer"]').count(),0);
  assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),true);
  if(evidence){fs.mkdirSync(evidence,{recursive:true});await page.screenshot({path:`${evidence}/live-scoring-${width}.png`,fullPage:true});}
  assert.deepEqual(errors,[]);
  await context.close();
 }
 const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1366,height:950}});
 await context.addCookies([{name:'__Host-labcentral',value:'cookie',url:app,secure:true,httpOnly:true,sameSite:'Strict'}]);
 const page=await context.newPage();
 await page.goto(app+'/lab/');
 await page.getByText('Web service',{exact:true}).waitFor();
 await page.evaluate(()=>fetch('/_fixture/stale'));
 await page.reload();
 await page.getByText(/^Stale results/).waitFor();
 await page.getByText('120 / 200',{exact:true}).waitFor();
 assert.ok(await page.getByText('Last: Up',{exact:true}).isVisible());
 await page.evaluate(()=>fetch('/_fixture/coach'));
 await page.goto(app+'/admin/#scoreboard');
 await page.getByRole('heading',{name:'Live Red vs. Blue scoreboard',exact:true}).waitFor();
 await page.getByRole('heading',{name:'Team 1 services',exact:true}).waitFor();
 await page.evaluate(()=>fetch('/_fixture/ordinary'));
 let scoreRequests=0;
 page.on('request',r=>{if(new URL(r.url()).pathname==='/api/live-scoreboard')scoreRequests++;});
 await page.goto(app+'/lab/');
 await page.getByRole('heading',{name:'Team machines',exact:true}).waitFor();
 assert.equal(await page.getByRole('heading',{name:'Live Red vs. Blue scoreboard',exact:true}).count(),0);
 assert.equal(scoreRequests,0,'Ordinary meeting polled the live scorer');
 await context.close();
 console.log('Live scoreboard desktop/mobile, actual SSH-tunneled guest JSON, student service view, coach scoreboard tab, stale last-good scores and ordinary-meeting isolation passed. Local fixtures only.');
} finally {await browser.close()}
