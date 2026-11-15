/**
 * 固定默认优选域名：永远进入自定义（手动）池。
 *
 * 钉住：
 *   1. 手动池没勾选 / 池为空 / 全部来源都不勾 —— 它们照样在；
 *   2. 国家筛选（按机房）不会把它们筛掉（域名没有机房码）；
 *   3. maxNodes 不占额度、也不会把它们截掉；
 *   4. 与用户手填的同名域名只保留一份；
 *   5. 在订阅里用的是自己的备注「固定-<首段>」，且 9 个都在。
 */
import { describe, it, expect } from 'vitest'
import { collectNodes } from '../src/sub-engine'
import { FIXED_PREFERRED_DOMAINS } from '../src/data/pools'
import { DEFAULT_CONFIG, mergeConfig } from '../src/config'

const cfg = (over: Record<string, unknown> = {}) => mergeConfig(DEFAULT_CONFIG, over as never)
const fixedOf = (r: Awaited<ReturnType<typeof collectNodes>>) => r.nodes.filter((n) => n.fixed)

describe('固定默认优选域名', () => {
  it('内置 9 个，且正是用户指定的那 9 个', () => {
    expect([...FIXED_PREFERRED_DOMAINS]).toEqual([
      'sb.mao99.eu.org', 'sb6.mao99.eu.org', 'sb4.mao99.eu.org',
      'vvv.me88.cloudns.org', 'vvv4.me88.cloudns.org', 'vvv6.me88.cloudns.org',
      'o0o.maoii.eu.org', 'o0o4.maoii.eu.org', 'o0o6.maoii.eu.org',
    ])
  })

  it('手动池没勾选也照样进入', async () => {
    const r = await collectNodes(cfg({ poolOfficial: true, poolDomain: false, poolManual: false, officialCount: 3 }), '')
    expect(fixedOf(r).map((n) => n.ip)).toEqual([...FIXED_PREFERRED_DOMAINS])
    expect(r.groups.some((g) => g.key === 'manual')).toBe(true)
  })

  it('手动池勾选但为空：给提示，固定域名仍在', async () => {
    const r = await collectNodes(cfg({ poolOfficial: false, poolDomain: false, poolManual: true }), '')
    expect(fixedOf(r)).toHaveLength(9)
    expect(r.stats.errors.join('')).toContain('手动优选 IP 为空')
  })

  it('国家筛选不会筛掉它们，但普通节点照常被筛', async () => {
    const r = await collectNodes(cfg({ poolOfficial: true, poolDomain: false, poolManual: true, officialCount: 20, poolCountries: 'JP' }), '1.1.1.1#SJC')
    expect(fixedOf(r)).toHaveLength(9)
    expect(r.nodes.filter((n) => !n.fixed).every((n) => n.country === 'JP')).toBe(true)
  })

  it('maxNodes 只限制普通节点', async () => {
    const r = await collectNodes(cfg({ poolOfficial: true, poolDomain: false, poolManual: false, officialCount: 30, maxNodes: 5 }), '')
    expect(r.nodes.filter((n) => !n.fixed)).toHaveLength(5)
    expect(fixedOf(r)).toHaveLength(9)
  })

  it('用户手填同名域名只保留一份（固定的那份）', async () => {
    const r = await collectNodes(cfg({ poolOfficial: false, poolDomain: false, poolManual: true, add: 'sb.mao99.eu.org:443#手填' }), '')
    const hits = r.nodes.filter((n) => n.ip === 'sb.mao99.eu.org')
    expect(hits).toHaveLength(1)
    expect(hits[0]!.fixed).toBe(true)
  })

  it('备注是「固定-首段」', async () => {
    const r = await collectNodes(cfg({ poolOfficial: false, poolDomain: false, poolManual: false }), '')
    const remarks = fixedOf(r).map((n) => n.remark)
    expect(remarks).toContain('固定-sb')
    expect(remarks).toContain('固定-o0o6')
  })
})
