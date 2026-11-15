# 生成 install-cfpilot-kwrt.sh：python3 installer/build.py <输出文件> [升级地址]
import json, os, sys
d = os.path.dirname(os.path.abspath(__file__)) + os.sep
t = open(d + 'ctl.sh.in', encoding='utf-8').read()
acl = json.load(open(d + 'acl.json', encoding='utf-8'))
acl['luci-app-cfpilot']['read']['file']['/tmp/cfpilot-mem.log'] = ['read']
acl['luci-app-cfpilot']['read']['file']['/proc/meminfo'] = ['read']
acl['luci-app-cfpilot']['read']['file']['/usr/bin/cfpilot-ctl'] = ['exec']
acl['luci-app-cfpilot']['write']['file']['/usr/bin/cfpilot-ctl'] = ['exec']
up = sys.argv[2] if len(sys.argv) > 2 else ''
subs = {
    '@@INIT@@': open(d + 'init.sh', encoding='utf-8').read().rstrip('\n'),
    '@@WATCHDOG@@': open(d + 'watchdog.sh', encoding='utf-8').read().rstrip('\n'),
    '@@LUCIJS@@': open(d + 'luci-main.js', encoding='utf-8').read().rstrip('\n'),
    '@@MENU@@': open(d + 'menu.json', encoding='utf-8').read().rstrip('\n'),
    '@@ACL@@': json.dumps(acl, indent='\t', ensure_ascii=False),
}
for k, v in subs.items():
    assert t.count(k) == 1, k
    t = t.replace(k, v)
t = t.replace('@@UPGRADE_URL@@', up)
open(sys.argv[1], 'w', encoding='utf-8', newline='\n').write(t)
