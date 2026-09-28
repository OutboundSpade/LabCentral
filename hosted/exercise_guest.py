# Fixed, host-owned program. Pinned template contents arrive only as data.
import base64, hashlib, json, os, pathlib, pwd, subprocess, sys
p = json.loads(base64.b64decode(sys.argv[1]))
def run(*args):
    return subprocess.run(args, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=25).stdout.decode()
def write(path, text, mode=0o644):
    path = pathlib.Path(path)
    if path.is_symlink() or any(x.is_symlink() for x in path.parents):
        raise RuntimeError('Exercise path is a symlink')
    path.write_text(text); path.chmod(mode)
def account():
    try: return pwd.getpwnam('svc_reports')
    except KeyError: return None
def keyfile():
    a=account()
    return pathlib.Path(a.pw_dir)/'.ssh/authorized_keys' if a else None
def has_key():
    f=keyfile()
    return bool(f and f.exists() and any(p['key'].split()[1] in line.split() for line in f.read_text().splitlines()))
def remove_key():
    f=keyfile()
    if f and f.exists():
        write(f, '\n'.join(line for line in f.read_text().splitlines() if p['key'].split()[1] not in line.split())+'\n', 0o600)
def http(port):
    r=subprocess.run(['curl','--noproxy','*','--silent','--fail','--max-time','5','-H','Host: web','http://127.0.0.1:'+str(port)+'/'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    return r.returncode==0
index='/var/www/branch-services/public/index.php'
if p['action']=='reset':
    remove_key()
    # This synthetic support account has no application services.
    a=account()
    if a:
        subprocess.run(['pkill','-KILL','-u',str(a.pw_uid)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    else:
        run('useradd','--create-home','--shell','/bin/bash','svc_reports')
    run('usermod','--shell','/bin/bash','--password',p['password'],'--append','--groups','sudo','svc_reports')
    run('chage','--expiredate','-1','svc_reports')
    pathlib.Path('/tmp/branch-review.txt').unlink(missing_ok=True)
    write('/etc/apache2/ports.conf','Listen 80\n')
    write('/etc/apache2/sites-available/000-default.conf',p['site'])
    write(index,p['index'])
    run('chown','www-data:www-data',index)
    run('apache2ctl','configtest')
    run('systemctl','unmask','apache2')
    run('systemctl','enable','apache2')
    run('systemctl','restart','apache2')
    if not http(80) or has_key(): raise RuntimeError('Reset verification failed')
elif p['action']=='stop-web':
    run('systemctl','stop','apache2')
    if http(80): raise RuntimeError('Web server still responds')
elif p['action']=='change-port':
    write('/etc/apache2/ports.conf','Listen 8080\n')
    write('/etc/apache2/sites-available/000-default.conf',p['site'].replace('<VirtualHost *:80>','<VirtualHost *:8080>'))
    run('apache2ctl','configtest');run('systemctl','restart','apache2')
    if http(80) or not http(8080): raise RuntimeError('Port change verification failed')
elif p['action']=='ssh-intrusion':
    a=account(); f=keyfile(); folder=f.parent
    if folder.is_symlink(): raise RuntimeError('SSH folder is a symlink')
    folder.mkdir(mode=0o700,exist_ok=True);folder.chmod(0o700);os.chown(folder,a.pw_uid,a.pw_gid)
    previous=f.read_text() if f.exists() else ''
    write(f,previous.rstrip()+'\n'+p['key']+'\n',0o600);os.chown(f,a.pw_uid,a.pw_gid)
    write(index,'<div role="alert">Branch support notice: service review in progress.</div>\n'+p['index'])
    run('chown','www-data:www-data',index)
elif p['action']=='check':
    print(json.dumps({'web':http(80),'portRestored':not http(8080),'keyRemoved':not has_key(),'homepageRestored':pathlib.Path(index).read_text()==p['index'],'artifactRemoved':not pathlib.Path('/tmp/branch-review.txt').exists()}))
else:
    raise RuntimeError('Unsupported exercise')
