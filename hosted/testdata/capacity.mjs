import assert from 'node:assert/strict';
import fs from 'node:fs';
const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE);
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox']});
try {
 const app=process.argv[2];
 const context=await browser.newContext({ignoreHTTPSErrors:true,viewport:{width:1365,height:950}});
 await context.addCookies([{name:'__Host-labcentral',value:'cookie',url:app,secure:true,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage();
 await page.goto(app+'/admin/');
 await page.getByText('Five teams, six students per team.',{exact:false}).waitFor();
 const output=process.env.LABCENTRAL_BROWSER_EVIDENCE;
 if(output){fs.mkdirSync(output,{recursive:true});await page.screenshot({path:output+'/capacity-desktop.png',fullPage:true})}
 await page.setViewportSize({width:390,height:844});
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));
 if(output)await page.screenshot({path:output+'/capacity-mobile.png',fullPage:true});
 await page.getByRole('button',{name:'Create meeting',exact:true}).click();
 await page.getByRole('button',{name:'Prepare labs',exact:true}).waitFor();
 assert.equal(await page.getByRole('cell',{name:'0 / 6',exact:true}).count(),5);
 const data=await page.evaluate(async()=>await(await fetch('/api/admin')).json());
 assert.equal(data.teams.length,5);
 assert(data.teams.every(team=>team.capacity===6));
 console.log('Admin form creates five six-seat teams through the real API; desktop and mobile layouts passed.');
} finally {await browser.close()}
