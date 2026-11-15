# CFPilot 路由器版：自动同步上游 + GitHub 构建 ARMv7

## 原理
上游（xmwengxing/CFPilot）一直在更新；你的全部功能（1:N 多数据面、固定优选域名、路由器限流、Kwrt 入口、LuCI/安装脚本）
固化在 `kwrt-1n.patch` 里。GitHub 每 6 小时自动：
上游最新代码 → 叠加补丁 → 类型检查 + 内核测试 + 脱敏检查 + Go 测试 → 编译 ARMv7 → 发布 Release。

- 上游没更新：自动跳过。
- 成功：发布两个 Release：`kwrt-latest`（固定地址，路由器升级用）和 `kwrt-v版本-提交号`（留档可回退）。
- 补丁与上游冲突 / 测试不过：**不发布**，旧版本继续可用，自动开一个 Issue；把 Issue 链接发给 Claude 重新对齐补丁即可。

## 一次性设置（只做一次）
1. 把本包里的 `.github` 文件夹和 `kwrt-overlay` 文件夹，上传到你的仓库 bovbo/CFPilot 根目录
   （网页：Add file → Upload files，把两个文件夹一起拖进去 → Commit）。
2. 仓库 Settings → Actions → General：启用 Actions；Workflow permissions 选 **Read and write permissions** → Save。
3. 仓库 Actions 页 → 左侧「Kwrt ARMv7 自动同步构建」→ Run workflow（首次手动跑一次，约 10~15 分钟）。
4. 完成后 Releases 里出现 `kwrt-latest`，里面有 cfpilot-kwrt-armv7 和 install-cfpilot-kwrt.sh。

## 路由器使用
- 首次安装：下载 install-cfpilot-kwrt.sh 传到路由器 /tmp，执行 `sh /tmp/install-cfpilot-kwrt.sh`（安装脚本已内置升级地址）。
- 以后升级：`cfpilot-ctl upgrade`（自动下载 + SHA256 校验 + 健康检查 + 失败回滚）。
- 每周自动升级：`cfpilot-ctl autoupdate on`。
- 路由器连不上 GitHub：编辑 /etc/cfpilot/upgrade.conf 填 GH_PROXY（加速前缀，以 / 结尾）。

## 注意
- GitHub 对 60 天没有仓库活动的定时任务会自动停用：收到停用邮件或发现不再构建时，Actions 页点一下 Enable 即可。
- 想立刻构建某个上游版本：Run workflow 时填 upstream_ref（分支/标签/提交），勾选 force。
- 你以后要新增功能：告诉 Claude，会更新 kwrt-1n.patch（覆盖上传 kwrt-overlay 里的同名文件）。
