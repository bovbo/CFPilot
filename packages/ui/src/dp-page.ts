/**
 * 「🛰️ 数据面」页：1:N 多数据面管理。
 *
 * 独立成模块的原因：
 * 1. 这一页只依赖 window.cfpilot.dp* 这组接口，和其它页没有共享状态；
 * 2. 与上游 renderer.ts 的耦合只有三处（导入、切页时懒加载、启动时绑定），
 *    上游改 renderer.ts 时不容易冲突。
 *
 * 全部写入都走本机后端（/api/dp*），后端负责：
 *   - 凭据（Token / Account ID / KV）只留在本机，不进控制面；
 *   - 每个数据面各自验证、各自重试、各自记录，一个失败不影响其它。
 */
import { escapeHtml } from './task-rows'

export interface DpPageDeps {
  log: (message: string, level?: 'info' | 'ok' | 'warn' | 'err') => void
}

function el<T extends HTMLElement = HTMLElement>(id: string): T {
  return document.getElementById(id) as T
}

function fmtTime(ms: number): string {
  if (!ms) return '—'
  const d = new Date(ms)
  const p = (n: number): string => String(n).padStart(2, '0')
  return `${d.getMonth() + 1}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

const REASON: Record<string, string> = {
  scan: '优选完成', schedule: '定时任务', add: '新增后', manual: '手动',
}
const MODE: Record<string, string> = { append: '追加', replace: '替换', remove: '删除' }
const STATUS: Record<string, string> = { ok: '成功', failed: '失败', skipped: '跳过' }

function statusBadge(status: string): string {
  const cls = status === 'ok' ? 'ok' : status === 'failed' ? 'err' : 'info'
  return `<span class="badge ${cls}">${escapeHtml(STATUS[status] || status || '—')}</span>`
}

/** 绑定整页交互。返回「切到本页时懒加载」的函数。 */
export function bindDpPage(deps: DpPageDeps): () => void {
  let data: DpListResult | null = null
  let loaded = false
  let pollTimer: number | null = null

  const log = deps.log

  function planeById(id: string): DpPlaneView | undefined {
    return data?.planes.find((p) => p.id === id)
  }

  function profileLabel(id: string): string {
    const p = data?.profiles.find((x) => x.id === id)
    if (!p) return '（账号已不存在）'
    return p.name + (p.accountName ? '（' + p.accountName + '）' : '')
  }

  /* ---------------- 加载与渲染 ---------------- */

  async function load(withPools: boolean): Promise<void> {
    try {
      const r = await window.cfpilot.dpList(withPools)
      if (!r || !r.ok) {
        log('数据面清单加载失败：' + ((r && r.error) || '未知错误'), 'err')
        return
      }
      data = r
      loaded = true
      renderAll()
      if (r.poolError) log('读取各数据面池条数失败：' + r.poolError, 'warn')
    } catch (e) {
      log('数据面清单加载失败：' + (e instanceof Error ? e.message : String(e)), 'err')
    }
  }

  function renderAll(): void {
    if (!data) return
    renderSettings()
    renderTable()
    renderLogPick()
    renderFixed()
    el('dpBadge').textContent = data.planes.length + ' 个数据面'
    el('dpPushState').textContent = data.pushing ? '正在推送…' : ''
  }

  function renderSettings(): void {
    if (!data) return
    const s = data.settings
    const sel = el<HTMLSelectElement>('dpCtlProfile')
    const keep = s.controlProfileId || ''
    sel.innerHTML = '<option value="">当前活跃账号</option>' + data.profiles.map((p) =>
      `<option value="${escapeHtml(p.id)}">${escapeHtml(p.name + (p.accountName ? '（' + p.accountName + '）' : ''))}</option>`).join('')
    sel.value = keep
    el<HTMLSelectElement>('dpMode').value = s.mode === 'replace' ? 'replace' : 'append'
    el<HTMLInputElement>('dpTop').value = String(s.top || 15)
    el<HTMLInputElement>('dpPushAfterAdd').checked = !!s.pushAfterAdd
    el<HTMLInputElement>('dpPushAfterScan').checked = !!s.pushAfterScan
  }

  function lastRecord(p: DpPlaneView): DpPushRecordView | null {
    return p.log && p.log.length ? p.log[p.log.length - 1]! : null
  }

  function renderTable(): void {
    if (!data) return
    const body = el('dpBody')
    const table = el('dpTable')
    const empty = el('dpEmpty')
    if (data.planes.length === 0) {
      table.hidden = true
      empty.hidden = false
      body.innerHTML = ''
      return
    }
    table.hidden = false
    empty.hidden = true
    body.innerHTML = data.planes.map((p) => {
      const last = lastRecord(p)
      const pool = data!.pools && p.id in data!.pools ? String(data!.pools[p.id]) : '—'
      const lastHtml = last
        ? statusBadge(last.status) + ' <span class="tip" style="margin:0">' +
          escapeHtml(fmtTime(last.at) + ' · ' + (MODE[last.mode] || last.mode) +
            (last.message ? ' · ' + last.message : '')) + '</span>'
        : '<span class="tip" style="margin:0">还没推送过</span>'
      const state = (p.enabled ? '<span class="badge ok">启用</span>' : '<span class="badge info">停用</span>') +
        (p.autoPush ? '' : ' <span class="badge info">不自动推送</span>')
      return '<tr data-id="' + escapeHtml(p.id) + '">' +
        '<td>' + escapeHtml(p.name || p.id) + '</td>' +
        '<td>' + escapeHtml(profileLabel(p.profileId)) + '</td>' +
        '<td>' + escapeHtml(p.domain || '—') + '</td>' +
        '<td>' + state + '</td>' +
        '<td>' + escapeHtml(pool) + '</td>' +
        '<td>' + lastHtml + '</td>' +
        '<td>' + escapeHtml(p.kernelVersion || '—') + '</td>' +
        '<td style="white-space:nowrap">' +
        '<button class="btn btn-ghost btn-sm" data-act="deploy" type="button">部署 / 升级</button> ' +
        '<button class="btn btn-ghost btn-sm" data-act="push" type="button">追加推送</button> ' +
        '<button class="btn btn-ghost btn-sm" data-act="replace" type="button">替换推送</button> ' +
        '<button class="btn btn-ghost btn-sm" data-act="sync" type="button">读取部署</button> ' +
        '<button class="btn btn-ghost btn-sm" data-act="edit" type="button">编辑</button> ' +
        '<button class="btn btn-ghost btn-sm" data-act="del" type="button">删除</button>' +
        '</td></tr>'
    }).join('')
  }

  function renderLogPick(): void {
    if (!data) return
    const sel = el<HTMLSelectElement>('dpLogPick')
    const cur = sel.value
    sel.innerHTML = '<option value="">请选择…</option>' + data.planes.map((p) =>
      `<option value="${escapeHtml(p.id)}">${escapeHtml(p.name || p.id)}</option>`).join('')
    if (cur && planeById(cur)) sel.value = cur
    renderLog()
  }

  function renderLog(): void {
    const id = el<HTMLSelectElement>('dpLogPick').value
    const p = id ? planeById(id) : undefined
    const table = el('dpLogTable')
    const empty = el('dpLogEmpty')
    if (!p || !p.log || p.log.length === 0) {
      table.hidden = true
      empty.hidden = false
      empty.textContent = p ? '这个数据面还没有推送记录。' : '选择一个数据面查看它自己的推送记录。'
      return
    }
    table.hidden = false
    empty.hidden = true
    el('dpLogBody').innerHTML = p.log.slice().reverse().map((r) =>
      '<tr><td>' + escapeHtml(fmtTime(r.at)) + '</td>' +
      '<td>' + escapeHtml(REASON[r.reason] || r.reason) + '</td>' +
      '<td>' + escapeHtml(MODE[r.mode] || r.mode) + '</td>' +
      '<td>' + statusBadge(r.status) + '</td>' +
      '<td>' + r.checked + ' / ' + r.valid + '</td>' +
      '<td>' + r.added + '</td><td>' + r.pool + '</td><td>' + r.attempts + '</td>' +
      '<td>' + escapeHtml(r.message || '') + '</td></tr>').join('')
  }

  function renderFixed(): void {
    if (!data) return
    const list = data.fixedDomains || []
    const table = el('dpFixedTable')
    const empty = el('dpFixedEmpty')
    // 9 个固定域名一直列出来（没测速前显示“尚未测速”），不再藏到第一次优选之后
    table.hidden = list.length === 0
    empty.hidden = list.length > 0
    el('dpFixedBody').innerHTML = list.map((r) =>
      '<tr><td>' + escapeHtml(r.domain) + '</td>' +
      '<td>' + (r.at ? (r.ok ? '<span class="badge ok">可用</span>' : '<span class="badge info">未通过</span>') : '—') + '</td>' +
      '<td>' + (r.latencyMs ? r.latencyMs + ' ms' : '—') + '</td>' +
      '<td>' + (r.speedMBps ? r.speedMBps.toFixed(2) + ' MB/s' : '—') + '</td>' +
      '<td>' + escapeHtml(r.colo || '—') + '</td>' +
      '<td>' + escapeHtml(r.note || '') + '</td>' +
      '<td>' + escapeHtml(fmtTime(r.at)) + '</td></tr>').join('')
  }

  /* ---------------- 表单 ---------------- */

  function fillProfileOptions(selected: string): void {
    const sel = el<HTMLSelectElement>('dpFormProfile')
    const list = data?.profiles || []
    sel.innerHTML = '<option value="">请选择账号…</option>' + list.map((p) => {
      const warn = !p.hasToken ? '（缺 Token）' : !p.hasKv ? '（未选 KV）' : ''
      return `<option value="${escapeHtml(p.id)}">${escapeHtml(p.name + (p.accountName ? '（' + p.accountName + '）' : '') + warn)}</option>`
    }).join('')
    sel.value = selected
  }

  function openForm(p?: DpPlaneView): void {
    el('dpFormCard').hidden = false
    el('dpFormTitle').textContent = p ? '编辑数据面：' + (p.name || p.id) : '新增数据面'
    el<HTMLInputElement>('dpFormId').value = p ? p.id : ''
    el<HTMLInputElement>('dpFormName').value = p ? p.name : ''
    fillProfileOptions(p ? p.profileId : '')
    el<HTMLInputElement>('dpFormDomain').value = p ? p.domain : ''
    el<HTMLInputElement>('dpFormUUID').value = p ? p.uuid : ''
    el<HTMLInputElement>('dpFormWs').value = p ? p.wsPath : ''
    el<HTMLInputElement>('dpFormTrojanPath').value = p ? p.trojanPath : ''
    el<HTMLInputElement>('dpFormXhttp').value = p ? p.xhttpPath : ''
    el<HTMLInputElement>('dpFormTrojanPw').value = p ? p.trojanPassword : ''
    el<HTMLInputElement>('dpFormFromDeploy').checked = !p
    el<HTMLInputElement>('dpFormEnabled').checked = p ? p.enabled : true
    el<HTMLInputElement>('dpFormAutoPush').checked = p ? p.autoPush : true
    el('dpFormCard').scrollIntoView({ behavior: 'smooth', block: 'nearest' })
  }

  function closeForm(): void {
    el('dpFormCard').hidden = true
  }

  async function saveForm(): Promise<void> {
    const v = (id: string): string => el<HTMLInputElement>(id).value.trim()
    const profileId = el<HTMLSelectElement>('dpFormProfile').value
    if (!profileId) {
      log('请先选择该数据面所属的账号（账号需先在「凭据」页添加）', 'warn')
      return
    }
    const req: DpSaveRequest = {
      id: v('dpFormId'),
      name: v('dpFormName'),
      profileId,
      domain: v('dpFormDomain'),
      uuid: v('dpFormUUID'),
      wsPath: v('dpFormWs'),
      trojanPath: v('dpFormTrojanPath'),
      xhttpPath: v('dpFormXhttp'),
      trojanPassword: v('dpFormTrojanPw'),
      enabled: el<HTMLInputElement>('dpFormEnabled').checked,
      autoPush: el<HTMLInputElement>('dpFormAutoPush').checked,
      fromDeploy: el<HTMLInputElement>('dpFormFromDeploy').checked,
    }
    const btn = el<HTMLButtonElement>('btnDpFormSave')
    btn.disabled = true
    try {
      const r = await window.cfpilot.dpSave(req)
      if (!r || !r.ok) {
        log('保存数据面失败：' + ((r && r.error) || '未知错误'), 'err')
        return
      }
      log('已保存数据面：' + ((r.plane && (r.plane.name || r.plane.id)) || ''), 'ok')
      if (r.warning) log(r.warning, 'warn')
      if (r.pushStarted) {
        log('已开始把当前优选结果推送到新数据面（结果见「推送记录」）', 'info')
        startPolling()
      }
      closeForm()
      await load(true)
    } finally {
      btn.disabled = false
    }
  }

  /* ---------------- 推送 / 同步 / 删除 ---------------- */

  function startPolling(): void {
    if (pollTimer !== null) window.clearInterval(pollTimer)
    let n = 0
    pollTimer = window.setInterval(() => {
      n++
      void load(true).then(() => {
        if ((data && !data.pushing) || n >= 40) {
          if (pollTimer !== null) window.clearInterval(pollTimer)
          pollTimer = null
        }
      })
    }, 3000)
  }

  async function push(ids: string[], mode: string): Promise<void> {
    if (mode === 'replace') {
      const names = ids.length ? ids.map((i) => (planeById(i)?.name || i)).join('、') : '全部启用的数据面'
      if (!window.confirm('「替换」会用本次优选结果覆盖这些数据面的池：\n' + names + '\n\n确定继续吗？')) return
    }
    const r = await window.cfpilot.dpPush(ids, mode)
    if (!r || !r.ok) {
      log('推送未开始：' + ((r && r.error) || '未知错误'), 'warn')
      return
    }
    log('已开始推送（' + (MODE[r.mode || ''] || r.mode) + '，' + r.count + ' 条）；各数据面的结果见「推送记录」', 'info')
    startPolling()
  }

  async function deploy(ids: string[]): Promise<void> {
    const names = ids.length ? ids.map((i) => (planeById(i)?.name || i)).join('、') : '全部启用的数据面'
    const plane = el<HTMLSelectElement>('dpDeployPlane').value
    const force = el<HTMLInputElement>('dpDeployForce').checked
    if (!window.confirm('部署 / 升级：' + names + '\n\n只更新数据面，用各自账号的凭据，逐个串行执行（每个之间间隔几秒）；失败不会重试。\n确定开始吗？')) return
    const r = await window.cfpilot.dpDeploy(ids, { force, dataPlaneMode: plane })
    if (!r || !r.ok) {
      log('部署未开始：' + ((r && r.error) || '未知错误'), 'warn')
      return
    }
    log('已开始部署 ' + r.count + ' 个数据面，进度见运行日志', 'info')
    if (r.skippedPrecheck && r.skippedPrecheck.length) {
      log('这些数据面缺条件，已跳过：\n' + r.skippedPrecheck.join('\n'), 'warn')
    }
  }

  async function removeIps(): Promise<void> {
    const id = el<HTMLSelectElement>('dpLogPick').value
    if (!id) {
      log('请先在上面选择一个数据面', 'warn')
      return
    }
    const ips = el<HTMLTextAreaElement>('dpRemoveIps').value
      .split(/\r?\n/).map((x) => x.trim()).filter(Boolean)
    if (ips.length === 0) {
      log('请输入要删除的 IP（每行一个）', 'warn')
      return
    }
    const name = planeById(id)?.name || id
    if (!window.confirm('从「' + name + '」的池里删除 ' + ips.length + ' 条 IP？\n只影响这个数据面。')) return
    const r = await window.cfpilot.dpRemoveIps(id, ips)
    if (!r || !r.ok) {
      log('删除失败：' + ((r && r.error) || '未知错误'), 'err')
    } else {
      log('已从「' + name + '」删除 ' + ips.length + ' 条 IP', 'ok')
      el<HTMLTextAreaElement>('dpRemoveIps').value = ''
    }
    await load(true)
  }

  async function onTableClick(ev: Event): Promise<void> {
    const btn = (ev.target as HTMLElement).closest('button[data-act]') as HTMLElement | null
    if (!btn) return
    const tr = btn.closest('tr') as HTMLElement | null
    const id = tr?.dataset.id || ''
    const p = planeById(id)
    if (!p) return
    const act = btn.dataset.act
    if (act === 'push') {
      await push([id], 'append')
    } else if (act === 'replace') {
      await push([id], 'replace')
    } else if (act === 'deploy') {
      await deploy([id])
    } else if (act === 'edit') {
      openForm(p)
    } else if (act === 'sync') {
      const r = await window.cfpilot.dpSyncDeploy(id)
      if (!r || !r.ok) {
        log('读取部署失败：' + ((r && r.error) || '未知错误'), 'err')
      } else {
        log('已从该账号的部署更新「' + (p.name || id) + '」的域名 / UUID / 路径', 'ok')
        if (r.warning) log(r.warning, 'warn')
      }
      await load(true)
    } else if (act === 'del') {
      if (!window.confirm('删除数据面「' + (p.name || id) + '」？\n它在控制面上的订阅条目和独立优选池会一并清掉；Cloudflare 上的 Worker 不会被删除。')) return
      const r = await window.cfpilot.dpDelete(id)
      if (!r || !r.ok) {
        log('删除失败：' + ((r && r.error) || '未知错误'), 'err')
      } else {
        log('已删除数据面「' + (p.name || id) + '」', 'ok')
        if (r.warning) log(r.warning, 'warn')
      }
      await load(true)
    }
  }

  async function saveSettings(): Promise<void> {
    if (!data) return
    const top = parseInt(el<HTMLInputElement>('dpTop').value, 10)
    const next: DpSettingsView = {
      controlProfileId: el<HTMLSelectElement>('dpCtlProfile').value,
      pushAfterAdd: el<HTMLInputElement>('dpPushAfterAdd').checked,
      pushAfterScan: el<HTMLInputElement>('dpPushAfterScan').checked,
      mode: el<HTMLSelectElement>('dpMode').value === 'replace' ? 'replace' : 'append',
      top: Number.isFinite(top) && top > 0 ? Math.min(top, 200) : 15,
    }
    if (next.mode === 'replace' && next.pushAfterScan &&
      !window.confirm('自动推送设为「替换」后，每次优选完成都会覆盖各数据面的池。\n建议保持「追加」。仍要保存吗？')) return
    const r = await window.cfpilot.dpSettings(next)
    const hint = el('dpSettingsHint')
    if (!r || !r.ok) {
      hint.textContent = '保存失败：' + ((r && r.error) || '未知错误')
      log('保存数据面设置失败：' + ((r && r.error) || '未知错误'), 'err')
      return
    }
    hint.textContent = '已保存'
    log('已保存多数据面推送设置', 'ok')
    await load(false)
  }

  /* ---------------- 事件绑定 ---------------- */

  const on = (id: string, fn: () => void | Promise<void>): void => {
    el(id)?.addEventListener('click', () => { void fn() })
  }
  on('btnDpAdd', () => openForm())
  on('btnDpFormCancel', closeForm)
  on('btnDpFormSave', saveForm)
  on('btnDpRefresh', () => load(true))
  on('btnDpPushAll', () => push([], ''))
  on('btnDpDeployAll', () => deploy([]))
  on('btnDpSettingsSave', saveSettings)
  on('btnDpRemoveIps', removeIps)
  el('dpBody')?.addEventListener('click', (e) => { void onTableClick(e) })
  el('dpLogPick')?.addEventListener('change', renderLog)
  // 定时 / 优选完成后的自动推送结束时，后端会发 dp-push 事件：已打开过本页就静默刷新
  try {
    window.cfpilot.onDpPush(() => { if (loaded) void load(true) })
    window.cfpilot.onDpDeploy((p) => {
      for (const x of p.results || []) {
        log('数据面「' + x.name + '」：' + (x.ok ? (x.skipped ? '已是最新' : '部署完成') : '失败') +
          (x.message ? ' —— ' + x.message : ''), x.ok ? 'ok' : 'err')
      }
      if (loaded) void load(true)
    })
  } catch {
    /* 适配层较旧时没有这个事件，页面仍可手动刷新 */
  }

  /** 切到本页：首次加载带池条数；之后切回来不重拉，避免冲掉正在编辑的内容。 */
  return () => {
    if (!loaded) void load(true)
  }
}
