const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE || 'playwright');
import assert from 'node:assert/strict';
import fs from 'node:fs';
const evidence=process.env.LABCENTRAL_BROWSER_EVIDENCE;
assert(evidence,'Set evidence directory outside source');fs.mkdirSync(evidence,{recursive:true});
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox']});
try {
 for(const width of [1366,390]) {
  const context=await browser.newContext({viewport:{width,height:900}}),page=await context.newPage();
  const errors=[];page.on('pageerror',e=>errors.push(e.message));
  let enabled=true,confirmation='first',mutations=0,stale=true,admin=true;
  let labs=[{id:'practice-one',user:'Student One',lab:'Linux investigation',status:'ready'},{id:'practice-two',user:'Student Two',lab:'Service practice',status:'preparing'}];
  const settings=()=>({enabled,confirmation,activeLabs:labs});
  const template={id:'pinned',name:'investigation',commit:'a'.repeat(40),commitTitle:'Investigation',description:'Investigate the service',handover:'Read the investigation guide.',imagesBuilt:true,machines:[],stages:[],stageCount:0,practiceStageIDs:[],scenarios:[]};
  const privateTemplate={...template,built:true,imagesReady:1,imagesTotal:1,instructor:{notes:'Private meeting run sheet',checks:[],breaks:[],stages:[]}};
  await context.route('**/api/**',async route=>{
   const req=route.request(),path=new URL(req.url()).pathname;
   if(path==='/api/me')return route.fulfill({json:{practiceEnabled:enabled,user:{id:'admin',name:'Administrator',admin},csrf:'csrf',meeting:null,membership:{teamID:''},teams:[],lab:{machines:[]}}});
   if(path==='/api/admin')return route.fulfill({json:{meeting:null,teams:[],jobs:[],templates:[privateTemplate],breaks:[],breakRuns:[],checkRuns:[]}});
   if(path==='/api/admin/practice') {
    if(req.method()==='GET')return route.fulfill({json:settings()});
    assert.equal(req.headers()['x-csrf-token'],'csrf');const body=req.postDataJSON();mutations++;
    if(stale) { stale=false;labs.push({id:'practice-three',user:'Student Three',lab:'New cloud reservation',status:'queued'});confirmation='second';return route.fulfill({status:409,json:{error:'Practice allocations or settings changed. Review the current list and confirm again.'}}); }
    assert.equal(body.confirmation,confirmation);enabled=body.enabled;confirmation='revision-'+mutations;
    if(!enabled)labs=labs.map(lab=>({...lab,status:'ending'}));
    return route.fulfill({json:settings()});
   }
   if(path==='/api/templates')return route.fulfill({json:{templates:[template]}});
   if(path==='/api/templates/pinned')return route.fulfill({json:template});
   if(path==='/api/learning')return route.fulfill({status:403,json:{error:'Practice disabled'}});
   throw new Error('Unexpected API route '+path);
  });
  await page.goto('http://127.0.0.1:5173/admin/');
  await page.getByRole('button',{name:'Disable practice…',exact:true}).click();
  const dialog=page.getByRole('dialog',{name:'Are you sure you want to disable practice?'});
  await dialog.waitFor();await dialog.getByText('Student Two · preparing',{exact:true}).waitFor();
  assert.equal(mutations,0,'opening confirmation mutated state');
  await dialog.getByRole('button',{name:'Cancel',exact:true}).click();assert.equal(mutations,0);
  await page.getByRole('button',{name:'Disable practice…',exact:true}).click();
  await dialog.getByRole('button',{name:'Disable practice and delete labs',exact:true}).click();
  await dialog.getByText('Practice allocations or settings changed.',{exact:false}).waitFor();
  assert(await dialog.getByRole('button',{name:'Disable practice and delete labs',exact:true}).isDisabled());
  await dialog.getByRole('button',{name:'Review current list',exact:true}).click();
  await dialog.getByText('Student Three · queued',{exact:true}).waitFor();
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
  await page.screenshot({path:`${evidence}/practice-confirm-${width}.png`});
  await dialog.getByRole('button',{name:'Disable practice and delete labs',exact:true}).click();
  await page.getByText('Disabled for everyone.',{exact:false}).waitFor();
  assert.equal(await page.getByRole('navigation',{name:'Main navigation'}).getByRole('link',{name:'Practice',exact:true}).count(),0);
  for(const route of ['/labs/','/learn/?lab=old-attempt']){
   await page.goto('http://127.0.0.1:5173'+route);await page.getByRole('heading',{name:'Practice labs are disabled',exact:true}).waitFor();
   assert.equal(await page.getByRole('button',{name:'Start practice',exact:true}).count(),0);
  }
  admin=false;await page.reload();await page.getByRole('heading',{name:'Practice labs are disabled',exact:true}).waitFor();await page.getByRole('link',{name:'Open meeting',exact:true}).click();await page.getByRole('heading',{name:'No meeting right now',exact:true}).waitFor();
  admin=true;await page.goto('http://127.0.0.1:5173/admin/templates/');await page.getByRole('link',{name:'Explore Investigation revision aaaaaaa',exact:true}).click();
  await page.waitForURL('**/admin/templates/?template=pinned');await page.getByText('Instructor notes',{exact:true}).click();await page.getByText('Private meeting run sheet',{exact:true}).waitFor();await page.waitForFunction(()=>[...document.querySelectorAll('details')].some(d=>d.open && d.textContent.includes('Private meeting run sheet')));
  assert.equal(await page.getByRole('heading',{name:'Practice',exact:true}).count(),0,'admin management exposed disabled practice');
  assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));await page.screenshot({path:`${evidence}/disabled-admin-explore-${width}.png`});
  await page.goto('http://127.0.0.1:5173/admin/');await page.getByRole('button',{name:'Enable practice…',exact:true}).click();
  await page.getByRole('dialog',{name:'Enable practice labs?',exact:true}).getByRole('button',{name:'Enable practice',exact:true}).click();await page.getByText('Enabled for everyone.',{exact:false}).waitFor();
  assert(labs.every(l=>l.status==='ending'),'reenable cancelled cleanup');
  assert.deepEqual(errors,[]);await context.close();console.log('PASS practice confirmation, stale preview, cancel, disabled routes and re-enable at '+width);
 }
} finally {await browser.close();}
