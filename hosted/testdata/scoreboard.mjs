import assert from 'node:assert/strict';import fs from 'node:fs';
const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE);
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox']});
try{
 const context=await browser.newContext({ignoreHTTPSErrors:true,reducedMotion:'reduce',viewport:{width:1365,height:1050}});
 const app=process.argv[2];await context.addCookies([{name:'__Host-labcentral',value:'cookie',url:app,secure:true,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
 await page.goto(app+'/admin/');await page.getByRole('tab',{name:'Scoreboard',exact:true}).click();await page.waitForURL('**/admin/#scoreboard');await page.reload();assert.equal(await page.getByRole('tab',{name:'Scoreboard',exact:true}).getAttribute('aria-selected'),'true');await page.getByRole('heading',{name:'Service scoreboard'}).waitFor();await page.getByRole('heading',{name:'Live activity'}).waitFor();
 await page.getByLabel('Schedule interval').selectOption('120');await page.getByRole('button',{name:'Start scheduled checks'}).click();await page.getByRole('button',{name:'Pause checks'}).waitFor();
 await page.getByRole('row',{name:'Team 1 service checks'}).getByText('Details',{exact:false}).click();
 await page.getByRole('row',{name:'Team 1 service checks'}).getByText('guest execution unavailable',{exact:true}).waitFor();
 await page.getByRole('button',{name:'Pause checks'}).click();await page.getByText('Scheduled checks paused. Check now runs once on ready teams.',{exact:true}).waitFor();
 const output=process.env.LABCENTRAL_BROWSER_EVIDENCE;if(output){fs.mkdirSync(output,{recursive:true});await page.screenshot({path:output+'/scoreboard-desktop.png',fullPage:true})}
 await page.setViewportSize({width:390,height:844});assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));if(output)await page.screenshot({path:output+'/scoreboard-mobile.png',fullPage:true});
 await page.goto(app+'/admin/templates/');await page.getByText('Instructor only · abcdef1',{exact:true}).click();await page.getByText('private instructor sentinel',{exact:true}).waitFor();
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));if(output)await page.screenshot({path:output+'/instructor-mobile.png',fullPage:true});
 await page.setViewportSize({width:1365,height:1050});if(output)await page.screenshot({path:output+'/instructor-desktop.png',fullPage:true});assert.deepEqual(errors,[]);
 console.log('Scoreboard schedule/pause, failure visibility, live activity, pinned private notes and desktop/mobile layouts passed with guest execution unavailable fixture.');
}finally{await browser.close()}
