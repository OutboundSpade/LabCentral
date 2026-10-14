import assert from 'node:assert/strict';
import fs from 'node:fs';
const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE);
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox']});
try{
 const app=process.argv[2];const context=await browser.newContext({ignoreHTTPSErrors:true,reducedMotion:"reduce",viewport:{width:1365,height:1150}});
 await context.addCookies([{name:'__Host-labcentral',value:'cookie',url:app,secure:true,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage();await page.goto(app+'/admin/');await page.getByRole('tab',{name:'Exercises',exact:true}).click();await page.waitForURL('**/admin/#exercises');await page.getByRole('heading',{name:'Exercises',exact:true}).waitFor();
 const tab=page.getByRole('tab',{name:'Exercises',exact:true});await tab.focus();await page.keyboard.press('ArrowLeft');assert.equal(await page.getByRole('tab',{name:'Teams',exact:true}).getAttribute('aria-selected'),'true');await page.keyboard.press('ArrowRight');await page.getByRole('heading',{name:'Exercises',exact:true}).waitFor();
 await page.getByLabel('Team 1',{exact:true}).check();await page.getByRole('tab',{name:'Teams',exact:true}).click();await tab.click();assert(await page.getByLabel('Team 1',{exact:true}).isChecked(),'Tab changes lost the selected team');await page.getByLabel('Team 2',{exact:true}).check();await page.getByLabel('Exercise',{exact:true}).selectOption('stop-web');
 await page.getByRole('button',{name:'Run on 2 teams',exact:true}).click();await page.getByRole('button',{name:'Run selected exercise',exact:true}).click();
 await page.getByText('Baseline and symptoms verified',{exact:true}).waitFor({timeout:20000});await page.getByText('Web SSH connection unavailable; retry or reset',{exact:true}).waitFor({timeout:20000});
 await page.getByText('Instructor walkthrough',{exact:true}).click();
 const output=process.env.LABCENTRAL_BROWSER_EVIDENCE;if(output){fs.mkdirSync(output,{recursive:true});await page.screenshot({path:output+'/exercises-desktop.png',fullPage:true})}
 await page.setViewportSize({width:390,height:844});await page.locator('#exercises-title').scrollIntoViewIfNeeded();
 assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));if(output)await page.screenshot({path:output+'/exercises-mobile.png',fullPage:true});
 await page.getByRole('button',{name:'Reset exercise baseline',exact:true}).click();await page.getByRole('dialog').waitFor();await page.waitForTimeout(300);if(output)await page.screenshot({path:output+'/exercises-reset-mobile.png'});await page.getByRole('button',{name:'Cancel',exact:true}).click();
 console.log('Admin breaks: real API authorization, team selection, confirmation, success/failure states, walkthrough and mobile layout passed. Guest actions use a fixture.');
}finally{await browser.close()}
