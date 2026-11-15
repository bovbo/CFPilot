/**
 * 1:N 多数据面 —— 注册表 / 按数据面推送 / 订阅聚合 回归测试。
 *
 * 钉住的约束：
 *   1. 没有注册任何数据面时，订阅与旧版行为一致（单数据面不受影响）；
 *   2. 每个数据面的节点用**自己的**域名 / UUID / 路径渲染，不串；
 *   3. 往一个数据面推送 IP，不影响其它数据面的池；
 *   4. append 去重、remove 按 ip:port 删除、replace 整体替换；
 *   5. 未注册的数据面推送被拒绝；
 *   6. 删除数据面时它的独立池一并清掉。
 */
import { describe, it, expect, beforeEach } from 'vitest'
import { handleApi, type ApiContext } from '../src/api'
import { routeControlPlane } from '../src/routes/control'
import { invalidateConfig } from '../src/routes/shared'
import { DEFAULT_CONFIG, mergeConfig, dpPoolKey, loadDpRegistry, saveDpRegistry } from '../src/config'
import { KvStore } from '../src/store'
import type { Env } from '../src/types'

const UUID = '11111111-2222-3333-4444-555555555555'
const TOKEN = 'scan-token'
const UA = '33333333-aaaa-bbbb-cccc-000000000001'
const UB = '44444444-aaaa-bbbb-cccc-000000000002'

class FakeKV {
  readonly map = new Map<string, string>()
  async get(k: string): Promise<string | null> {
    return this.map.get(k) ?? null
  }
  async put(k: string, v: string): Promise<void> {
    this.map.set(k, v)
  }
  async delete(k: string): Promise<void> {
    this.map.delete(k)
  }
  async list(o: { prefix?: string } = {}) {
    const p = o.prefix ?? ''
    return { keys: [...this.map.keys()].filter((k) => k.startsWith(p)).map((name) => ({ name })), list_complete: true }
  }
}

const DPS = [
  { id: 'acc-a', name: 'A号', domain: 'a.example.com', uuid: UA, wsPath: '/ws-a', trojanPassword: 'pa', trojanPath: '/t-a', xhttpPath: '/x-a', enabled: true },
  { id: 'acc-b', name: 'B号', domain: 'b.example.net', uuid: UB, wsPath: '/ws-b', trojanPassword: 'pb', trojanPath: '/t-b', xhttpPath: '/x-b', enabled: true },
]

let kv: FakeKV
let env: Env

beforeEach(() => {
  invalidateConfig()
  kv = new FakeKV()
  kv.map.set('SCANNER_TOKEN', TOKEN)
  kv.map.set('CONFIG', JSON.stringify(mergeConfig(DEFAULT_CONFIG, { uuid: UUID, subDomain: 'main.example.org', poolManual: true, poolOfficial: false, poolDomain: false })))
  env = { UUID, KV: kv } as unknown as Env
})

function apiCtx(): ApiContext {
  return {
    store: new KvStore(kv as never),
    env,
    config: mergeConfig(DEFAULT_CONFIG, { uuid: UUID }),
    auth: { panelPath: '/panel', sessionOk: true, generatedPassword: false } as never,
    onConfigChanged: () => invalidateConfig(),
  }
}

async function call(path: string, method: string, body?: unknown) {
  const res = await handleApi(
    new Request('https://panel.example.com/api/v1' + path, {
      method,
      headers: { Authorization: `Bearer ${TOKEN}`, 'Content-Type': 'application/json' },
      body: body === undefined ? undefined : JSON.stringify(body),
    }),
    new URL('https://panel.example.com/api/v1' + path),
    apiCtx()
  )
  expect(res).not.toBeNull()
  return { status: res!.status, json: (await res!.json()) as Record<string, unknown> }
}

async function sub(target: string): Promise<string> {
  invalidateConfig()
  const res = await routeControlPlane(new Request(`https://panel.example.com/sub/${UUID}?target=${target}`), env)
  expect(res!.status).toBe(200)
  return await res!.text()
}

describe('注册表', () => {
  it('保存 / 读取 / 清理非法项', async () => {
    const store = new KvStore(kv as never)
    const r = await saveDpRegistry(store, [...DPS, { id: 'Bad Id', domain: 'x.com' }, { id: 'no-domain' }])
    expect(r.saved.map((d) => d.id)).toEqual(['acc-a', 'acc-b'])
    expect(r.dropped).toBe(2)
    expect((await loadDpRegistry(store)).length).toBe(2)
  })

  it('删除数据面时清掉它的独立池', async () => {
    const store = new KvStore(kv as never)
    await saveDpRegistry(store, DPS)
    kv.map.set(dpPoolKey('acc-b'), '1.1.1.1:443')
    await saveDpRegistry(store, [DPS[0]])
    expect(kv.map.has(dpPoolKey('acc-b'))).toBe(false)
  })

  it('PUT /dataplanes 需要扫描令牌', async () => {
    const res = await handleApi(
      new Request('https://panel.example.com/api/v1/dataplanes', { method: 'PUT', body: '{"items":[]}' }),
      new URL('https://panel.example.com/api/v1/dataplanes'),
      apiCtx()
    )
    expect(res!.status).toBe(401)
  })
})

describe('按数据面推送', () => {
  beforeEach(async () => {
    await call('/dataplanes', 'PUT', { items: DPS })
  })

  it('只写目标数据面的池，不碰其它数据面和全局池', async () => {
    const r = await call('/preferred-ips', 'POST', { ips: ['1.1.1.1:443#A1', '1.0.0.1:443'], mode: 'append', dp: 'acc-a' })
    expect(r.status).toBe(200)
    expect(r.json.dp).toBe('acc-a')
    expect(r.json.added).toBe(2)
    expect(kv.map.get(dpPoolKey('acc-a'))).toContain('1.1.1.1:443')
    expect(kv.map.has(dpPoolKey('acc-b'))).toBe(false)
    expect(kv.map.has('LOCAL_PREFERRED_IPS')).toBe(false)
  })

  it('append 去重；remove 按 ip:port 删除；replace 整体替换', async () => {
    await call('/preferred-ips', 'POST', { ips: ['1.1.1.1:443', '1.0.0.1:443'], mode: 'append', dp: 'acc-a' })
    const again = await call('/preferred-ips', 'POST', { ips: ['1.1.1.1:443', '8.8.8.8:443'], mode: 'append', dp: 'acc-a' })
    expect(again.json.added).toBe(1)
    const rm = await call('/preferred-ips', 'POST', { ips: ['1.0.0.1:443'], mode: 'remove', dp: 'acc-a' })
    expect(rm.json.removed).toBe(1)
    expect(kv.map.get(dpPoolKey('acc-a'))!.split('\n').sort()).toEqual(['1.1.1.1:443', '8.8.8.8:443'])
    const rp = await call('/preferred-ips', 'POST', { ips: ['9.9.9.9:443'], mode: 'replace', dp: 'acc-a' })
    expect(rp.json.count).toBe(1)
    expect(kv.map.get(dpPoolKey('acc-a'))).toBe('9.9.9.9:443')
  })

  it('未注册 / 非法的数据面被拒绝', async () => {
    expect((await call('/preferred-ips', 'POST', { ips: ['1.1.1.1:443'], dp: 'nope' })).status).toBe(404)
    expect((await call('/preferred-ips', 'POST', { ips: ['1.1.1.1:443'], dp: '../x' })).status).toBe(400)
  })
})

describe('订阅聚合', () => {
  it('没有数据面：沿用全局域名 / UUID（旧行为）', async () => {
    kv.map.set('LOCAL_PREFERRED_IPS', '7.7.7.7:443#G')
    const body = Buffer.from(await sub('base64'), 'base64').toString()
    expect(body).toContain(UUID)
    expect(body).toContain('main.example.org')
    expect(body).not.toContain(UA)
  })

  it('每个数据面的节点用自己的域名 / UUID / 路径，互不串味', async () => {
    await call('/dataplanes', 'PUT', { items: DPS })
    await call('/preferred-ips', 'POST', { ips: ['1.1.1.1:443#PA'], mode: 'replace', dp: 'acc-a' })
    await call('/preferred-ips', 'POST', { ips: ['2.2.2.2:443#PB'], mode: 'replace', dp: 'acc-b' })
    const links = Buffer.from(await sub('base64'), 'base64').toString().split('\n').filter((l: string) => l.startsWith('vless://'))
    const la = links.filter((l: string) => l.includes('@1.1.1.1:'))
    const lb = links.filter((l: string) => l.includes('@2.2.2.2:'))
    expect(la.length).toBeGreaterThan(0)
    expect(lb.length).toBeGreaterThan(0)
    for (const l of la) {
      expect(l).toContain(UA)
      expect(l).toContain('sni=a.example.com')
      expect(decodeURIComponent(l)).toContain('/ws-a')
      expect(l).not.toContain(UB)
      expect(l).not.toContain('b.example.net')
    }
    for (const l of lb) {
      expect(l).toContain(UB)
      expect(l).toContain('sni=b.example.net')
      expect(l).not.toContain(UA)
    }
  })

  it('Clash：生成数据面均衡与每数据面 url-test，且不引用不存在的组', async () => {
    await call('/dataplanes', 'PUT', { items: DPS })
    await call('/preferred-ips', 'POST', { ips: ['1.1.1.1:443#PA'], mode: 'replace', dp: 'acc-a' })
    await call('/preferred-ips', 'POST', { ips: ['2.2.2.2:443#PB'], mode: 'replace', dp: 'acc-b' })
    const y = await sub('clash')
    expect(y).toContain('type: load-balance')
    expect(y).toContain('数据面·A号')
    expect(y).toContain('数据面·B号')
    expect(y).toContain('servername: a.example.com')
    expect(y).toContain('servername: b.example.net')
    // 每个 name 只定义一次
    const names = [...y.matchAll(/^\s+- name: "(.+)"$/gm)].map((m) => m[1])
    expect(new Set(names).size).toBe(names.length)
  })

  it('数据面池为空时借用全局池（刚加入就有节点），但仍用自己的域名', async () => {
    kv.map.set('LOCAL_PREFERRED_IPS', '7.7.7.7:443#G')
    await call('/dataplanes', 'PUT', { items: [DPS[0]] })
    const links = Buffer.from(await sub('base64'), 'base64').toString().split('\n').filter((l: string) => l.startsWith('vless://'))
    expect(links.length).toBeGreaterThan(0)
    expect(links.every((l: string) => l.includes('sni=a.example.com') && l.includes(UA))).toBe(true)
  })

  it('停用的数据面不进订阅', async () => {
    await call('/dataplanes', 'PUT', { items: [{ ...DPS[0], enabled: false }, DPS[1]] })
    await call('/preferred-ips', 'POST', { ips: ['2.2.2.2:443'], mode: 'replace', dp: 'acc-b' })
    const body = Buffer.from(await sub('base64'), 'base64').toString()
    expect(body).not.toContain('a.example.com')
    expect(body).toContain('b.example.net')
  })

  it('sing-box：含 urltest，节点 server_name 各用各的', async () => {
    await call('/dataplanes', 'PUT', { items: DPS })
    await call('/preferred-ips', 'POST', { ips: ['1.1.1.1:443'], mode: 'replace', dp: 'acc-a' })
    await call('/preferred-ips', 'POST', { ips: ['2.2.2.2:443'], mode: 'replace', dp: 'acc-b' })
    const cfg = JSON.parse(await sub('singbox')) as { outbounds: Array<Record<string, unknown>> }
    expect(cfg.outbounds.some((o) => o.type === 'urltest')).toBe(true)
    const vless = cfg.outbounds.filter((o) => o.type === 'vless') as Array<{ server: string; uuid: string; tls: { server_name: string } }>
    expect(vless.find((o) => o.server === '1.1.1.1')!.uuid).toBe(UA)
    expect(vless.find((o) => o.server === '2.2.2.2')!.tls.server_name).toBe('b.example.net')
  })
})
