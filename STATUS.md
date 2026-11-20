# STATUS — 当前状态

```
更新时间     2026-11-20
版本         1.0.1（开源发布版）
技术栈       Go 1.24+（单二进制 + go:embed + 单 JSON 存储）
二进制       8.5MB（8,962,208 字节，strip 后，CGO_ENABLED=0）
代码量       7,573 行 Go（26 个文件）+ 336KB 单文件前端（7,030 行）
自检         386 条断言全绿
许可证       MIT
```

## 部署拓扑

```
环境        容器                  端口    数据目录                          用途
──────────────────────────────────────────────────────────────────────────────────
正式        justnav               8522    /volume1/docker/justnav/data      日常使用
预览        justnav-preview       8524    /volume1/docker/justnav-preview/  改东西先在这
```

**开发流程**：改代码 → 构建 → 推 8524 验证 → 确认后才同步 8522（代码 + 数据）。
两个环境数据目录独立，同步靠拷 `data/`，不共享。

- 8522 用**镜像内二进制**（`justnav:local`），更新需 `docker cp` 进容器或重新 `docker build`
- 8524 用 **bin 挂载**（`:ro`），更新需 `docker stop` → `docker cp` → `docker start`

## ✅ 1.0.1 这一轮

```
[x] 开源：MIT 许可 + LICENSE 文件
[x] CI：.github/workflows/docker.yml —— v* tag / main / 手动触发 → ghcr.io 多架构镜像
[x] 版本号 const → var（-ldflags -X 只能改 var，否则 CI 注入不了 tag 号）
[x] Dockerfile 改多架构（BUILDPLATFORM + TARGETOS/TARGETARCH，全平台构建不靠 QEMU 模拟）
[x] 右侧栏从单卡片改多卡片，Widgets []string 有序数组 + ↑↓ 自定义排布
[x] 老数据自动迁移（Widget 单值 → Widgets 数组，迁完清空旧字段）
[x] 下载卡三层重排（状态/名字/做种数并一行，累计流量并入速率行，速率 0 显示「空闲」）
[x] 主滚动条静止隐藏（只改颜色不改宽度，无布局抖动）
[x] 修复弹窗文字变黑（浮层共享变量选择器列表被插规则截断）
[x] 去敏：真实内网 IP → 示例值；真实域名 → example.com
[x] 删除 frontend/ 原型（无代码引用 + 夹带真实自托管服务清单）
[x] README 重写：界面章节（4 张演示截图）+ 镜像部署 + 完整接口表 + 环境变量表
```

## ✅ 0.3.x 这一轮（历史）

```
[x] 天气换国内源（中国天气网，0.6s；open-meteo/wttr 降为备用）
[x] 天气最高温 999 缺测值过滤（CN 用 999 表示缺测）
[x] 时钟卡：字号加大、城市名不收缩、min-height 防天气行溢出
[x] 卡片配色改暗玻璃白字（默认 .26 / 悬停 .52 + blur）
[x] 侧栏 + 顶栏 + 待收录抽屉同款暗玻璃
[x] 全部 11 类弹层统一暗玻璃（含 color-scheme:dark 修原生下拉）
[x] 全部 8 个弹窗统一 bindMaskClose（拖选文字划出去不误关）
[x] 编辑入口：右键小菜单 + 长按 3 秒居中弹窗（触屏），移除 hover 铅笔
[x] 拖拽改逐格推挤 + FLIP 过渡（DOM 移动 35 次 → 4 次）
[x] 卡片统一 1×2，取消 1×1
[x] favicon 抓取加 HTML <link rel=icon> 解析 + 编辑框「重新抓取」按钮
[x] 壁纸：按日期恢复选中张 + 悬停缩略图面板
[x] 首屏防闪：内联脚本在 CSS 前写回 data-wall / side-min + 壁纸预下载
[x] 分组选择从 datalist 换成自绘下拉
[x] 新增条目 Order = 全局最大+1（追加到分组末尾）
```

## 📊 自检

```bash
bash scripts/selfcheck.sh     # 386 条断言，交付前必跑
```

覆盖：拖拽（快照/逐格/FLIP/不误关）、弹层配色、favicon、壁纸持久化、
首屏防闪、右键菜单与长按弹窗、多卡片排布、滚动条三态、后端 go vet/test/编译、仓库卫生。

## ⚠️ 已知问题

```
1  壁纸只有当天那张带 Bing 标题，历史张只有日期（后端 meta 只存当前）
2  PWA manifest / Service Worker 未做（iOS 装主屏需 HTTPS + manifest）
3  抓取 miss 过的站点不会自动重试，要手动点「重新抓取」
4  右侧栏多卡片高度溢出时整体滚动，不做高度自适应分配（压扁日历会变形，暂可接受）
5  自更新功能未做（8524 的 bin 挂载是 :ro，程序写不了自身二进制）
```

## ⏭️ 下一步

```
P1  壁纸历史标题补全（后端每天抓的时候把 title 一起存进缓存列表）
P1  PWA（iOS 装主屏要 HTTPS）
P2  备忘 / 待办卡片（widgetAllowed 已预留扩展位）
P2  天气详情卡片（后端 fc 有 5 天预报，字段映射待摸清）
P2  缩略图预生成（壁纸原图 3.6MB 偏大，手机端加载慢）
```

## 📋 文档索引

```
README.md                项目定位、界面截图、快速开始、接口、环境变量、标签约定
ARCHITECTURE.md          设计决策
CHANGELOG.md             版本变更（0.1.0 → 1.0.1）
TODO.md                  需求池
RUNBOOK.md               部署、排障
LICENSE                  MIT
docs/screenshots/        README 用的演示截图（演示数据，无真实内网信息）
```
