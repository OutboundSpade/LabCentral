#!/usr/bin/env python3
"""Build/install a separate source module against an already-built core/UI."""
import hashlib,json,os,pathlib,shutil,signal,socket,subprocess,tempfile,time,urllib.request
ROOT=pathlib.Path(__file__).resolve().parents[2]
SDK=pathlib.Path(os.environ.get('LABCENTRAL_EXTENSION_SDK_ROOT',ROOT)).resolve()
CORE=pathlib.Path(os.environ.get('LABCENTRAL_EXTENSION_TEST_BINARY',ROOT/'bin/extension-foundations/labd')).resolve()
def digest(p): return hashlib.sha256(p.read_bytes()).hexdigest()
def port():
    with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]
before=digest(CORE)
with tempfile.TemporaryDirectory(prefix='labcentral-source-proof-') as temporary:
    root=pathlib.Path(temporary);package=root/'package';package.mkdir(mode=0o700)
    executable=package/'source'
    subprocess.run(['go','build','-o',str(executable),'.'],cwd=SDK/'examples/external-source',check=True)
    executable.chmod(0o700)
    manifest=dict(apiVersion='labcentral.io/plugin-manifest/v2',id='org.example.source',version='0.1.0',categories=['source'],sourceKinds=['fixture'],executable='source',sha256=digest(executable),protocolMajor=1,minimumProtocolMinor=0,maximumProtocolMinor=0,operatingSystems=['linux'],architectures=['amd64'],requestedPermissions=[])
    installation=dict(apiVersion='labcentral.io/installation/v2',pluginRoot=str(package),extensions=[dict(manifest='plugin.json',instanceId='fixture.source',mode='local',kind='fixture',args=['--state',str(root/'journal')],grantedPermissions=[],bindings=[dict(id='source',projectIds=['project.local'],configuration={})])])
    for name,value in [('plugin.json',manifest),('installation.json',installation)]:
        p=package/name;p.write_text(json.dumps(value));p.chmod(0o600)
    base='http://127.0.0.1:'+str(port());operations=port();logs=open(root/'core.log','w+')
    args=[str(CORE),'--auth-mode=none','--docker-plugin=','--listen',base.removeprefix('http://'),'--observability-listen','127.0.0.1:'+str(operations),'--data-dir',str(root/'data'),'--installation',str(package/'installation.json')]
    child=None
    def api(path,body=None,key=None):
        headers={'Content-Type':'application/json'}
        if key:headers['Idempotency-Key']=key
        req=urllib.request.Request(base+'/api/v1'+path,data=None if body is None else json.dumps(body).encode(),headers=headers)
        try:
            with urllib.request.urlopen(req,timeout=10) as response:return json.load(response)
        except urllib.error.HTTPError as e:raise RuntimeError(str(e)+': '+e.read().decode()) from e
    def start():
        global child
        child=subprocess.Popen(args,stdout=logs,stderr=logs,start_new_session=True)
        for _ in range(100):
            if child.poll() is not None:logs.seek(0);raise RuntimeError(logs.read())
            try:api('/server-info');return
            except Exception:time.sleep(.1)
        raise RuntimeError('startup timeout')
    def stop():
        if child and child.poll() is None:
            os.killpg(child.pid,signal.SIGTERM)
            try:child.wait(timeout=10)
            except subprocess.TimeoutExpired:os.killpg(child.pid,signal.SIGKILL);child.wait()
    def wait(id):
        for _ in range(300):
            result=api('/extension-operations/'+id+'?projectId=project.local')
            if result['state'] in ('succeeded','failed','canceled'):return result
            time.sleep(.2)
        logs.flush();logs.seek(0);raise RuntimeError('work timeout: '+logs.read())
    try:
        start()
        template=api('/lab-templates',dict(projectId='project.local',name='source-fixture',description='Independent source proof'),'fixture-template')
        def resolve(reference,key):return api('/source-resolutions',dict(projectId='project.local',templateId=template['id'],binding='fixture.source/source',reference=reference),key)
        first=resolve('main-v1','fixture-source-v1');finished=wait(first['id']);assert finished['state']=='succeeded',finished
        replay=resolve('main-v1','fixture-source-v1');assert replay['id']==first['id']
        second=wait(resolve('main-v2','fixture-source-v2')['id']);assert second['state']=='succeeded',second
        assert first['invocationId']!=second['invocationId']
        assert finished['result']['source']['resolvedIdentity']!=second['result']['source']['resolvedIdentity']
        malformed=wait(resolve('malformed','fixture-source-invalid')['id']);assert malformed['state']=='failed',malformed
        delayed=resolve('delayed','fixture-source-delayed')
        journal=root/'journal'/(delayed['invocationId']+'.json')
        for _ in range(100):
            if journal.exists():break
            time.sleep(.05)
        assert journal.exists()
        os.killpg(child.pid,signal.SIGKILL);child.wait()
        start()
        recovered=wait(delayed['id']);assert recovered['state']=='succeeded',recovered
        assert recovered['invocationId']==delayed['invocationId']
        assert len(list((root/'journal').glob('*.json')))==4
        assert api('/extension-operations/'+first['id']+'?projectId=project.local')['result']==finished['result']
        assert digest(CORE)==before
        print('PASS: source-only independent install, immutable identities, idempotent publication, malformed bundle rejection and recovery after response loss')
    finally:
        stop();logs.flush();logs.seek(0);(ROOT/'bin/extension-foundations/source-proof.log').write_text(logs.read());logs.close()
