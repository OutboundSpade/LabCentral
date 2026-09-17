#!/usr/bin/env python3
"""Build/install independent lifecycle, access, builder and browser modules against an already-built core/UI."""
import base64,concurrent.futures,hashlib,io,json,os,pathlib,re,shutil,signal,socket,subprocess,tarfile,tempfile,time,urllib.request
import yaml
ROOT=pathlib.Path(__file__).resolve().parents[2]
SDK=pathlib.Path(os.environ.get('LABCENTRAL_EXTENSION_SDK_ROOT',ROOT)).resolve()
CORE=pathlib.Path(os.environ.get('LABCENTRAL_EXTENSION_TEST_BINARY',ROOT/'bin/extension-foundations/labd')).resolve()
def digest(p): return hashlib.sha256(p.read_bytes()).hexdigest()
def port():
    with socket.socket() as s:s.bind(('127.0.0.1',0));return s.getsockname()[1]
before=digest(CORE)
with tempfile.TemporaryDirectory(prefix='labcentral-build-proof-') as temporary:
    root=pathlib.Path(temporary);package=root/'package';package.mkdir(mode=0o700)
    manifests=[]; extensions=[]; providers=[];stream_port=port();asset_port=port()
    for category,module,pluginid in [('builder','external-builder','org.example.builder'),('provider','external-lifecycle-provider','org.example.lifecycle')]:
        executable=package/category
        subprocess.run(['go','build','-o',str(executable),'.'],cwd=SDK/'examples'/module,check=True);executable.chmod(0o700)
        manifest=dict(apiVersion='labcentral.io/plugin-manifest/v2',id=pluginid,version='0.1.0',categories=[category],executable=category,sha256=digest(executable),protocolMajor=1,minimumProtocolMinor=0,maximumProtocolMinor=0 if category=='builder' else 1,operatingSystems=['linux'],architectures=['amd64'],requestedPermissions=[])
        manifest[category+'Kinds']=['fixture']
        entry=dict(manifest=category+'.json',instanceId='fixture.'+category,mode='local',args=['--state',str(root/(category+'-journal'))],grantedPermissions=[])
        if category=='builder':
            entry.update(kind='fixture',bindings=[dict(id='image',projectIds=['project.local'],configuration={},limits=dict(timeoutSeconds=30,cpuCount=1,memoryBytes=16777216,inputBytes=8388608,outputBytes=8388608,egress=[]))]);extensions.append(entry)
        else:
            config_file=package/'provider-config.json';config_file.write_text('{"delayImport":true}');config_file.chmod(0o600)
            entry['targets']=[dict(id='target.fixture',kind='fixture',configFile=str(config_file),policy=dict(trustedLaptop=True))];providers.append(entry)
        manifests.append((category+'.json',manifest))
    executable=package/'access';subprocess.run(['go','build','-o',str(executable),'.'],cwd=SDK/'examples/external-access',check=True);executable.chmod(0o700)
    methods=['org.example.echo','org.example.network']
    manifests.append(('access.json',dict(apiVersion='labcentral.io/plugin-manifest/v2',id='org.example.access',version='0.1.0',categories=['access'],executable='access',sha256=digest(executable),protocolMajor=1,minimumProtocolMinor=2,maximumProtocolMinor=2,operatingSystems=['linux'],architectures=['amd64'],requestedPermissions=[],accessKinds=['fixture'],accessMethods=methods,endpointSchemas=['fixture.endpoint'])))
    extensions.append(dict(manifest='access.json',instanceId='fixture.access',mode='local',kind='fixture',args=['--state',str(root/'access-journal'),'--listen','127.0.0.1:'+str(stream_port)],grantedPermissions=[],methods=methods,targetIds=['target.fixture'],streamEndpoint='http://127.0.0.1:'+str(stream_port)))
    subprocess.run(['node','examples/external-browser/package.mjs',str(package/'browser')],cwd=SDK,check=True,stdout=subprocess.DEVNULL)
    extensions.append(dict(manifest='browser/plugin.json',instanceId='fixture.browser',grantedPermissions=['context.read','topology.read','access.create','access.stream']))
    installation=dict(apiVersion='labcentral.io/installation/v2',pluginRoot=str(package),providers=providers,extensions=extensions)
    for name,value in manifests+[('installation.json',installation)]:
        p=package/name;p.write_text(json.dumps(value));p.chmod(0o600)
    base='http://127.0.0.1:'+str(port());operations=port();logs=open(root/'core.log','w+')
    args=[str(CORE),'--auth-mode=none','--docker-plugin=','--listen',base.removeprefix('http://'),'--observability-listen','127.0.0.1:'+str(operations),'--data-dir',str(root/'data'),'--installation',str(package/'installation.json'),'--extension-asset-origin','http://127.0.0.2:'+str(asset_port),'--extension-asset-listen','127.0.0.2:'+str(asset_port)]
    child=None
    def api(path,body=None,key=None,version=None):
        headers={'Content-Type':'application/json'}
        if key:headers['Idempotency-Key']=key
        if version is not None:headers['If-Match']='"'+str(version)+'"'
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
        template=api('/lab-templates',dict(projectId='project.local',name='build-fixture',description='Independent build proof'),'fixture-template')
        lab=(ROOT/'examples/classroom-debian/lab.yaml').read_text();lab=re.sub(r'imageRef: .*', 'imageRef: build://base', lab)
        files={'lab.yaml':lab.encode(),'machine.txt':os.urandom(256)}
        manifest=dict(apiVersion='labcentral.bundle/v1alpha1',kind='LabBundle',entrypoint='lab.yaml',files=[dict(path=p,size=len(b),sha256=hashlib.sha256(b).hexdigest(),mediaType='text/plain') for p,b in files.items()],builds=dict(apiVersion='labcentral.build/v1',recipes=[dict(id='base',builder='fixture.builder/image',schema='fixture.image.v1',architecture='amd64',configurationJSON='{"delay":true}',context=['machine.txt'],baseImageDigests=[])]))
        files['bundle.yaml']=yaml.safe_dump(manifest).encode();archive=io.BytesIO()
        with tarfile.open(fileobj=archive,mode='w:gz',format=tarfile.USTAR_FORMAT) as tar:
            for p,b in files.items():
                info=tarfile.TarInfo(p);info.size=len(b);tar.addfile(info,io.BytesIO(b))
        revision=api('/lab-templates/'+template['id']+'/bundle-revisions?projectId=project.local',dict(archive=base64.b64encode(archive.getvalue()).decode()),'fixture-revision')
        assert revision['rendererVersion']=='labcentral.renderer/v1alpha1+builds/v1',revision
        def build(key):return api('/builds',dict(projectId='project.local',revisionId=revision['id'],recipeId='base'),key)
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:works=list(pool.map(build,['fixture-build-'+str(i) for i in range(4)]))
        assert len({w['id'] for w in works})==1,works
        work=works[0];journal=root/'builder-journal'/(work['invocationId']+'.output.json')
        for _ in range(100):
            if journal.exists():break
            time.sleep(.05)
        assert journal.exists()
        os.killpg(child.pid,signal.SIGKILL);child.wait();start()
        finished=wait(work['id']);assert finished['state']=='succeeded',finished
        assert finished['invocationId']==work['invocationId']
        import_work=api('/image-imports',dict(projectId='project.local',buildId=work['id'],targetId='target.fixture'),'fixture-import')
        import_journal=root/'provider-journal'/(import_work['invocationId']+'.import.json')
        for _ in range(100):
            if import_journal.exists():break
            time.sleep(.05)
        assert import_journal.exists()
        os.killpg(child.pid,signal.SIGKILL);child.wait();start()
        imported=wait(import_work['id']);assert imported['state']=='succeeded' and imported['invocationId']==import_work['invocationId'],imported

        binding=api('/image-bindings/'+imported['id']+'?projectId=project.local');assert not binding['approved']
        prep=api('/deployment-preparations',dict(projectId='project.local',preparation=dict(revisionId=revision['id'],targetId='target.fixture',inputs=[],acknowledged=True,lifetime=dict(ttlSeconds=3600,persistent=False))),'fixture-prepare')
        time.sleep(2);pending=api('/extension-operations/'+prep['id']+'?projectId=project.local');assert pending['state'] in ('queued','running'),pending
        api('/image-bindings/'+imported['id']+'/policy',dict(projectId='project.local',approved=True,retired=False))
        prepared=wait(prep['id']);assert prepared['state']=='succeeded',prepared
        deployment=prepared['result']['deployment'];instance=deployment.get('instanceId') or deployment.get('InstanceID');assert instance,deployment
        for _ in range(100):
            detail=api('/lab-instances/'+instance)
            if detail.get('phase')=='ready':break
            time.sleep(.2)
        assert detail.get('phase')=='ready',detail
        topology=api('/lab-instances/'+instance+'/topology');network=next(r for r in topology['resources'] if r['kind']=='Network');machine=next(r for r in topology['resources'] if r['kind']=='VM')
        assert any(a['method']=='org.example.network' for a in network['actions']),topology
        assert any(a['method']=='org.example.echo' for a in machine['actions']),topology
        subprocess.run(['node','web/tests/extension-stream.mjs',base,instance],cwd=ROOT,check=True)
        def network_access(key):
            launch=api('/access-sessions',dict(instanceId=instance,resourceId=network['id'],method='org.example.network',durationSeconds=120),key)
            config=api('/access-sessions/'+launch['session']['id']+'/download',dict(token=launch['oneTimeToken']))
            try:api('/access-sessions/'+launch['session']['id']+'/download',dict(token=launch['oneTimeToken']));raise AssertionError('download replay accepted')
            except RuntimeError:pass
            host,p=config['Address'].split(':');conn=socket.create_connection((host,int(p)),timeout=2);conn.sendall(config['Token'].encode()+b'\nproof');assert conn.recv(5)==b'proof';return launch,conn
        network_launch,connection=network_access('fixture-network-revoke')
        api('/access-sessions/'+network_launch['session']['id']+'/actions/revoke',{},'fixture-revoke-'+network_launch['session']['id'],api('/access-sessions/'+network_launch['session']['id'])['resourceVersion'])
        connection.settimeout(3)
        try:closed=connection.recv(1)==b''
        except (ConnectionResetError,BrokenPipeError):closed=True
        assert closed;connection.close()
        network_launch,connection=network_access('fixture-network-outage')
        # Freeze the core, leaving the independent traffic enforcement process alive.
        os.kill(child.pid,signal.SIGSTOP);started=time.monotonic();connection.settimeout(32)
        try:closed=connection.recv(1)==b''
        except ConnectionResetError:closed=True
        finally:os.kill(child.pid,signal.SIGCONT)
        assert closed and time.monotonic()-started<31;connection.close()
        api('/access-sessions/'+network_launch['session']['id']+'/actions/revoke',{},'fixture-revoke-'+network_launch['session']['id'],api('/access-sessions/'+network_launch['session']['id'])['resourceVersion'])
        for _ in range(120):
            try:api('/admin/provider-targets/target.fixture/actions/refresh',{});break
            except RuntimeError:time.sleep(.2)
        api('/lab-instances/'+instance+'/actions/destroy',{},'fixture-destroy')
        for _ in range(100):
            detail=api('/lab-instances/'+instance)
            if detail.get('phase')=='destroyed':break
            time.sleep(.2)
        assert detail.get('phase')=='destroyed',detail
        print('PASS: independent builder/provider installation, concurrent deduplication, response-loss recovery, import, explicit approval, preparation, apply/read/destroy, UI stream, network replay/revocation and bounded core-outage closure')
        assert digest(CORE)==before
    finally:
        stop();logs.flush();logs.seek(0);(ROOT/'bin/extension-foundations/build-proof.log').write_text(logs.read());logs.close()
