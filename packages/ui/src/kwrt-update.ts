/**
 * 路由器版「一键更新」卡片（关于页）。
 *
 * 仅在路由器版（后端找得到 cfpilot-ctl）时显示，桌面版 supported=false 时整张卡片保持隐藏。
 * 升级由后端调用 cfpilot-ctl 在后台完成；升级中服务会重启，页面短暂断开属于正常现象，
 * 这里轮询状态，恢复连接并看到 ok 后自动刷新页面。
 */
export interface KwrtUpdateDeps {
  log: (message: string, level?: 'info' | 'ok' | 'warn' | 'err') => void
}

function el<T extends HTMLElement = HTMLElement>(id: string): T {
  return document.getElementById(id) as T
}

export function bindKwrtUpdate(deps: KwrtUpdateDeps): void {
  const api = window.cfpilot
  const card = document.getElementById('kwrtUpdateCard')
  if (!card || !api || typeof api.kwrtUpdateCheck !== 'function') return

  const badge = el('kwrtBadge')
  const ver = el('kwrtVer')
  const btnCheck = el<HTMLButtonElement>('btnKwrtCheck')
  const btnApply = el<HTMLButtonElement>('btnKwrtApply')
  const logBox = el('kwrtLog')
  let busy = false

  const setBadge = (text: string, cls: string): void => {
    badge.textContent = text
    badge.className = 'badge ' + cls
  }

  async function check(silent: boolean): Promise<void> {
    if (busy) return
    btnCheck.disabled = true
    try {
      const r = await api.kwrtUpdateCheck()
      if (!r.supported) return
      card!.hidden = false
      // 路由器版只用这张卡片更新：把上游「桌面版」的检查更新卡片藏起来，避免混淆
      const desk = document.getElementById('updateBadge')?.closest('.card') as HTMLElement | null
      if (desk) desk.hidden = true
      ver.textContent = `当前版本：${r.current || '—'}　最新版本：${r.latest || '—'}`
      if (r.error) {
        setBadge('检查失败', 'err')
        if (!silent) deps.log('检查更新失败：' + r.error, 'warn')
        btnApply.disabled = true
      } else if (r.update) {
        setBadge('有新版本', 'warn')
        btnApply.disabled = false
      } else {
        setBadge('已是最新', 'ok')
        btnApply.disabled = true
      }
    } catch (e) {
      if (!silent) deps.log('检查更新失败：' + String(e), 'warn')
    } finally {
      btnCheck.disabled = false
    }
  }

  async function poll(): Promise<void> {
    // 升级重启期间接口会短暂不可用：失败就继续等，最多约 3 分钟
    for (let i = 0; i < 90; i++) {
      await new Promise((r) => setTimeout(r, 2000))
      try {
        const s = await api.kwrtUpdateStatus()
        if (s.log) {
          logBox.hidden = false
          logBox.textContent = s.log
        }
        if (s.state === 'ok') {
          setBadge('更新完成', 'ok')
          deps.log('一键更新完成，页面即将刷新', 'ok')
          setTimeout(() => location.reload(), 1500)
          return
        }
        if (s.state === 'fail') {
          setBadge('更新失败', 'err')
          deps.log('一键更新失败，已保留旧版本，详见下方日志', 'err')
          return
        }
      } catch {
        // 服务正在重启，继续等
      }
    }
    setBadge('等待超时', 'warn')
    busy = false
  }

  btnCheck.addEventListener('click', () => void check(false))
  btnApply.addEventListener('click', async () => {
    if (busy || !confirm('确定现在更新？更新过程中服务会重启几秒钟。')) return
    busy = true
    btnApply.disabled = true
    btnCheck.disabled = true
    setBadge('更新中…', 'info')
    try {
      const r = await api.kwrtUpdateApply()
      if (!r.ok) {
        busy = false
        btnCheck.disabled = false
        setBadge('启动失败', 'err')
        deps.log(r.error || '启动更新失败', 'err')
        return
      }
      deps.log('已开始更新，请稍候…', 'info')
      void poll().finally(() => { busy = false; btnCheck.disabled = false })
    } catch (e) {
      busy = false
      btnCheck.disabled = false
      deps.log('启动更新失败：' + String(e), 'err')
    }
  })

  void check(true)
}
