'use strict';
'require view';
'require fs';
'require poll';
'require ui';

/* CFPilot 路由器版 LuCI 控制页：状态 / 启停 / 开机自启 / 管理地址 / 日志 */

var INIT = '/etc/init.d/cfpilot';
var URL_FILE = '/tmp/cfpilot.url';

function sh(cmd, args) {
	return fs.exec_direct(cmd, args || [], 'text').catch(function () { return ''; });
}

function readStatus() {
	return Promise.all([
		sh('/bin/pidof', ['cfpilot']),
		fs.stat('/etc/rc.d/S99cfpilot').then(function () { return true; }).catch(function () { return false; }),
		fs.read_direct(URL_FILE).catch(function () { return ''; }),
		sh('/sbin/logread', ['-e', 'cfpilot']),
		sh('/usr/bin/cfpilot', ['--version']),
		fs.read_direct('/tmp/cfpilot-mem.log').catch(function () { return ''; }),
		fs.read_direct('/proc/meminfo').catch(function () { return ''; })
	]).then(function (r) {
		var pid = String(r[0] || '').trim().split(/\s+/)[0] || '';
		return Promise.all([
			pid ? fs.read_direct('/proc/' + pid + '/status').catch(function () { return ''; }) : Promise.resolve('')
		]).then(function (m) {
			var rss = /VmRSS:\s+(\d+)/.exec(m[0]);
			return {
				pid: pid,
				running: pid !== '',
				enabled: r[1],
				url: String(r[2] || '').trim(),
				log: String(r[3] || '').trim().split('\n').slice(-40).join('\n'),
				version: String(r[4] || '').trim(),
				rssMB: rss ? (parseInt(rss[1], 10) / 1024).toFixed(1) : '',
				mem: String(r[5] || '').trim().split('\n').slice(-12).join('\n'),
				meminfo: String(r[6] || '')
			};
		});
	});
}

/* 把文件里的地址换成「你当前访问路由器用的主机名」，从 LuCI 同一个地址就能打开 */
function adminURL(st) {
	if (!st.url) return '';
	try {
		var u = new URL(st.url);
		u.hostname = window.location.hostname;
		return u.toString();
	} catch (e) {
		return st.url;
	}
}

function memSummary(info) {
	var t = /MemTotal:\s+(\d+)/.exec(info || '');
	var a = /MemAvailable:\s+(\d+)/.exec(info || '');
	if (!t) return '系统内存：未知';
	var total = Math.round(parseInt(t[1], 10) / 1024);
	var avail = a ? Math.round(parseInt(a[1], 10) / 1024) : null;
	return '系统内存：共 ' + total + ' MB' + (avail !== null ? '，可用约 ' + avail + ' MB' : '');
}

function badge(text, ok) {
	return E('span', {
		'style': 'display:inline-block;padding:2px 10px;border-radius:10px;color:#fff;background:' + (ok ? '#2e9d4f' : '#c0392b')
	}, text);
}

return view.extend({
	load: function () { return readStatus(); },

	act: function (action) {
		var self = this;
		ui.showModal(null, [E('p', { 'class': 'spinning' }, _('正在执行：') + action)]);
		return fs.exec(INIT, [action]).then(function () {
			return new Promise(function (res) { setTimeout(res, 2500); });
		}).then(function () {
			ui.hideModal();
			return self.refresh();
		}).catch(function (e) {
			ui.hideModal();
			ui.addNotification(null, E('p', _('操作失败：') + e.message), 'danger');
		});
	},

	draw: function (st) {
		var self = this;
		var link = adminURL(st);
		var btn = function (label, cls, action) {
			return E('button', {
				'class': 'cbi-button ' + cls,
				'style': 'margin-right:8px',
				'click': ui.createHandlerFn(self, function () { return self.act(action); })
			}, label);
		};

		return E('div', { 'id': 'cfpilot-root' }, [
			E('h2', {}, 'CFPilot'),
			E('div', { 'class': 'cbi-map-descr' },
				'Cloudflare Workers 部署与优选 IP 管理（路由器版）。完整的管理界面是独立的 Web 页面，点下方「打开管理界面」即可。'),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, '运行状态'),
				E('table', { 'class': 'table' }, [
					E('tr', { 'class': 'tr' }, [E('td', { 'class': 'td left', 'width': '25%' }, '服务'),
						E('td', { 'class': 'td left' }, badge(st.running ? '运行中' : '未运行', st.running))]),
					E('tr', { 'class': 'tr' }, [E('td', { 'class': 'td left' }, '进程 / 内存'),
						E('td', { 'class': 'td left' }, st.running ? ('PID ' + st.pid + (st.rssMB ? '，占用约 ' + st.rssMB + ' MB' : '')) : '-')]),
					E('tr', { 'class': 'tr' }, [E('td', { 'class': 'td left' }, '开机自启'),
						E('td', { 'class': 'td left' }, badge(st.enabled ? '已启用' : '未启用', st.enabled))]),
					E('tr', { 'class': 'tr' }, [E('td', { 'class': 'td left' }, '程序版本'),
						E('td', { 'class': 'td left' }, st.version || '-')])
				]),
				E('div', { 'style': 'margin-top:12px' }, [
					btn('启动', 'cbi-button-apply', 'start'),
					btn('停止', 'cbi-button-reset', 'stop'),
					btn('重启', 'cbi-button-action', 'restart'),
					btn(st.enabled ? '关闭开机自启' : '开启开机自启', 'cbi-button-neutral', st.enabled ? 'disable' : 'enable')
				])
			]),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, '管理界面（独立 Web）'),
				link
					? E('div', {}, [
						E('p', {}, [E('a', { 'class': 'cbi-button cbi-button-save', 'href': link, 'target': '_blank', 'rel': 'noopener' }, '打开管理界面（新窗口）')]),
						E('p', { 'style': 'word-break:break-all;opacity:.75' }, link),
						E('p', { 'style': 'opacity:.75' }, '链接里的令牌每次重启服务都会变；同一浏览器打开过一次后会保持登录。该端口只应在局域网内使用，不要映射到公网。')
					])
					: E('p', {}, st.running ? '管理地址还没生成，请几秒后刷新。' : '服务未运行，先点上面的「启动」。')
			]),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, '资源监控（看门狗每 5 分钟采样）'),
				E('p', { 'style': 'opacity:.75' }, memSummary(st.meminfo) + '。内存或连接表异常时，看门狗只重启 CFPilot，不动路由器其它服务。'),
				E('pre', { 'style': 'max-height:200px;overflow:auto;white-space:pre-wrap;font-size:12px' }, st.mem || '（还没有采样记录，刚启动请稍等几分钟）')
			]),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, '更新（来自 GitHub Releases）'),
				E('p', { 'id': 'cfpilot-upd-info', 'style': 'opacity:.85' }, '点「检查更新」查看是否有新版本；一键更新会下载、校验、自检后替换并自动重启，失败自动回退，数据不受影响。'),
				E('div', {}, [
					E('button', { 'class': 'cbi-button cbi-button-action', 'style': 'margin-right:8px',
						'click': ui.createHandlerFn(self, function () { return self.checkUpdate(); }) }, '检查更新'),
					E('button', { 'id': 'cfpilot-upd-btn', 'class': 'cbi-button cbi-button-apply',
						'click': ui.createHandlerFn(self, function () { return self.applyUpdate(); }) }, '一键更新')
				]),
				E('pre', { 'id': 'cfpilot-upd-log', 'style': 'max-height:200px;overflow:auto;white-space:pre-wrap;font-size:12px;margin-top:8px;display:none' }, ''),
				E('p', { 'style': 'opacity:.6;font-size:12px' }, 'SSH 命令：cfpilot-ctl check / cfpilot-ctl upgrade / cfpilot-ctl rollback / cfpilot-ctl autoupdate on')
			]),

			E('div', { 'class': 'cbi-section' }, [
				E('h3', {}, '最近日志'),
				E('pre', { 'style': 'max-height:340px;overflow:auto;white-space:pre-wrap;font-size:12px' }, st.log || '（暂无日志）')
			])
		]);
	},

	setUpd: function (text, log) {
		var i = document.getElementById('cfpilot-upd-info');
		if (i && text !== null) i.textContent = text;
		var l = document.getElementById('cfpilot-upd-log');
		if (l && log) { l.style.display = ''; l.textContent = log; }
	},

	checkUpdate: function () {
		var self = this;
		self.setUpd('正在检查更新…');
		return fs.exec('/usr/bin/cfpilot-ctl', ['check']).then(function (r) {
			var out = String(r.stdout || '').trim().split('\n').pop();
			try {
				var j = JSON.parse(out);
				if (j.error) self.setUpd('检查失败：' + j.error);
				else if (j.update) self.setUpd('发现新版本：' + j.latest + '（当前 ' + j.current + '）');
				else self.setUpd('已是最新版本：' + j.current);
			} catch (e) { self.setUpd('检查失败：' + out); }
		}).catch(function (e) { self.setUpd('检查失败：' + e.message); });
	},

	applyUpdate: function () {
		var self = this;
		if (!confirm('确定现在更新？过程中 CFPilot 会重启几秒钟。')) return Promise.resolve();
		self.setUpd('正在启动更新…');
		return fs.exec('/usr/bin/cfpilot-ctl', ['upgrade-bg']).then(function () {
			var n = 0;
			var tick = function () {
				return fs.exec('/usr/bin/cfpilot-ctl', ['upgrade-status']).then(function (r) {
					var t = String(r.stdout || '').split('\n');
					var state = (t[0] || '').split(' ')[0];
					self.setUpd(state === 'ok' ? '更新完成' : state === 'fail' ? '更新失败（已保留旧版本）' : '更新中…', t.slice(1).join('\n'));
					if (state === 'ok') return self.refresh();
					if (state === 'fail' || ++n > 90) return null;
					return new Promise(function (res) { setTimeout(res, 2000); }).then(tick);
				}).catch(function () {
					if (++n > 90) return null;
					return new Promise(function (res) { setTimeout(res, 2000); }).then(tick);
				});
			};
			return tick();
		}).catch(function (e) { self.setUpd('启动更新失败：' + e.message); });
	},

	refresh: function () {
		var self = this;
		return readStatus().then(function (st) {
			var root = document.getElementById('cfpilot-root');
			if (root) root.parentNode.replaceChild(self.draw(st), root);
		});
	},

	render: function (st) {
		var self = this;
		poll.add(function () { return self.refresh(); }, 10);
		return this.draw(st);
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
