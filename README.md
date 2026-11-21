# JustNav 🔖

> 一个原生、纯粹的导航页。不塞信息流，不做容器管理面板，不当监控大屏。

[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Version](https://img.shields.io/github/v/release/fenglibo51/justnav?color=green)](https://github.com/fenglibo51/justnav/releases)
[![Image](https://img.shields.io/badge/ghcr.io-justnav-2496ED?logo=docker&logoColor=white)](https://github.com/fenglibo51/justnav/pkgs/container/justnav)

![桌面端](docs/screenshots/desktop.jpg)

---

## 📌 为什么有这个项目

想要一个**原生、纯粹的导航页**。

不用框架、不用 npm、不用构建工具链，一个 Go 二进制加一个 HTML 文件就能跑起来，放到哪儿都不依赖环境。打开就该是「点一下就进某个服务」，而不是先看一眼今日待办、再看一眼 RSS、最后才想起来自己是来打开某个页面的。

导航页这件事本身很小，所以这个项目的取舍很明确：

```
做        打开服务、找服务、管理服务入口
不做      RSS / 股票 / 容器启停 / 终端 / 权限体系 / AI 助手
```

只有一处例外：右侧栏允许挂**信息型卡片**（时钟、日历、天气、下载进度）。理由是这些属于「看一眼就走」的信息，不是需要交互的功能模块 —— 它们不会把你留在页面上，也不会抢走「打开服务」这件事的注意力。

---

## 🖼️ 界面

### 桌面端：左侧找服务，中间看服务，右侧看状态

左边是分组导航 + 全局搜索，中间按分组铺开服务卡片，右边是时钟卡和它下面的信息卡片。

![桌面端](docs/screenshots/desktop.jpg)

材质跟着壁纸走，分两种：

```
有壁纸    暗玻璃 + 白字，文字自带投影 —— 换任何壁纸都不会出现「浅色壁纸上看不清字」
纯色底    浅色玻璃 + 深色字；时钟这类纯文字信息直接浮在背景上，不加底框
```

两种模式下**排布完全一致，只换颜色**。不会出现「关掉壁纸整个界面变了个样」。

### 右侧栏：卡片可以叠成一摞

![右侧栏卡片](docs/screenshots/widgets.jpg)

时钟卡固定在顶部，下面是**卡片槽位**，目前支持三种卡片，可以同时开、可以调顺序：

```
📅 日历     公历 + 农历 + 法定节假日（含调休）+ 节日倒计时，月份可前后翻
🌤️ 天气     5 天预报 + 当天详情，左滑看下一张
⬇️ 下载     接 qBittorrent / Transmission，看上下行速率和正在跑的任务
```

设置里勾选控制显示，`↑` `↓` 调整顺序。**放不下就整体滚动，卡片不会被压扁。**

### 天气卡片：两张卡叠着，可以翻

![天气详情](docs/screenshots/weather-detail.jpg)

天气卡是两张叠在一起的一摞：

```
第一张   5 天预报    今天 / 明天 / 后天 / 周一…，每天 星期 + 图标 + 低温 + 高温
第二张   当天详情    体感 / 湿度 / 风 / 紫外线 / 日出 / 日落 / 能见度 / 气压
```

点任意一张卡、或者底部那两条短横，都能翻过去。**翻到哪一页会记住**，刷新页面不会回到第一张。

这样设计的目的是：右侧栏再挂多少卡片也不会无限往下堆，一摞里放几张都行。

### 设置面板：不跳页，就地改

![设置面板](docs/screenshots/settings.jpg)

所有设置在右侧弹层里改，改完立即生效，不刷新页面。壁纸强度、搜索引擎、卡片显示与顺序、天气城市、账号管理都在这一层。

### 手机端：底部 tab，卡片两列

![手机端](docs/screenshots/mobile.jpg)

手机端自动切成底部 tab 布局，卡片两列。长按卡片 3 秒进入整理模式，可以拖拽排序、改分组。

---

## ✨ 功能

**导航**

- 🔀 **内外网双地址** —— 每个条目配内网 + 外网地址，全局一键切换
- 🔍 **全局搜索** —— 常驻顶栏：匹配本地条目 / 直接打开网址 / 用百度或谷歌搜；引擎可增删改，`⌘K` 或 `/` 聚焦
- 🗂️ **分组管理** —— 增删改分组、拖拽排序、分组图标可手动指定；没指定就按组名关键词自动匹配（影音/下载/网络/开发/容器/记账/AI… 共 28 条规则）
- 🕹️ **拖拽整理** —— 桌面按住卡片拖动排序，手机长按 3 秒进入整理模式

**外观**

- 🖼️ **每日 4K 壁纸** —— Bing 每日 UHD，本地缓存，30 天 / 300MB 自动清理（可关闭）
- 🎨 **壁纸强度三档** —— 清晰（默认）/ 平衡 / 毛玻璃
- 🌗 **深浅双材质** —— 有壁纸走暗玻璃 + 白字，纯色底走浅玻璃 + 深字，排布一致只换色
- 📱 **双端自适应** —— 桌面侧边栏 + 手机底部 tab

**右侧栏卡片**

- 🕐 **时钟 + 当天天气** —— 大时钟 + 当前温度 / 天气 / 今日高低温，城市可搜索切换，1 小时刷新一次
- 🌤️ **5 天预报** —— 每天 星期 + 图标 + 低温 + 高温，点击翻到详情
- 📋 **当天详情** —— 体感 / 湿度 / 风 / 紫外线 / 日出 / 日落 / 能见度 / 气压
- 📚 **卡片堆叠** —— 多张卡叠成一摞左右翻，翻到哪张会记住
- 📅 **日历** —— 公历 + 农历 + 法定节假日（含调休）+ 节日倒计时
- ⬇️ **下载器** —— 后端代理 qBittorrent / Transmission，密码不出后端

**部署与运维**

- 🏷️ **Docker 自动发现** —— 独立采集器旁车扫容器，只输出白名单字段；带 `nav.*` 标签的精确收录，没标签有端口的也能兜底，都要人工确认才上首页
- 📡 **在线状态探测** —— 定期探测每个服务，卡片角标显示在线 / 未响应
- 📦 **单二进制部署** —— 无外部数据库，`FROM scratch`（约 9MB），多架构镜像
- 🔐 **多账号** —— 每个账号一套独立的条目、分组、壁纸和设置

---

## 🚀 快速开始

### Docker（推荐）

```bash
docker run -d \
  --name justnav \
  -p 8522:8522 \
  -v ./data:/data \
  --restart unless-stopped \
  ghcr.io/fenglibo51/justnav:latest
```

打开 `http://你的IP:8522`，默认账号 **`admin` / `admin`**，登录后立刻去「设置 → 改密码」。

> ⚠️ 默认密码是硬编码的 `admin`，不是随机生成 —— 部署在能被别人访问到的网络上就必须改。
> 想第一次启动就用自定义密码，加 `-e JUSTNAV_PASSWORD=你的密码`（**只在还没有任何账号时生效**，之后不会覆盖你在界面上改的密码）。

### Docker Compose

```yaml
services:
  justnav:
    image: ghcr.io/fenglibo51/justnav:latest
    container_name: justnav
    restart: unless-stopped
    ports:
      - "8522:8522"
    environment:
      TZ: Asia/Shanghai
      JUSTNAV_SITE_NAME: JustNav
      # 改成你自己 NAS / 主机的内网地址
      JUSTNAV_INTERNAL_HOST: "192.168.1.10"
    volumes:
      - ./data:/data:rw
```

完整带注释的版本见 [`.env.example`](.env.example)。

### 源码

```bash
# 需 Go 1.24+
go run .

# 或编译
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o justnav .
./justnav
```

---

## ⚙️ 配置

全部走环境变量，都有默认值，不写死任何地址。

```
变量                          默认              说明
────────────────────────────────────────────────────────────────────────────
JUSTNAV_PORT                  8522             监听端口
JUSTNAV_DATA_DIR              ./data           数据目录（条目 + 壁纸缓存）
JUSTNAV_SITE_NAME             JustNav          站点标题
JUSTNAV_WALLPAPER             true             是否启用每日壁纸
JUSTNAV_WALLPAPER_KEEP_DAYS   30               壁纸保留天数
JUSTNAV_WALLPAPER_MAX_BYTES   314572800        壁纸缓存总容量上限（300MB）
JUSTNAV_PROBE                 true             是否探测服务在线状态
JUSTNAV_PROBE_INTERVAL_SEC    60               探测间隔
JUSTNAV_PROBE_TIMEOUT_SEC     4                单次探测超时
JUSTNAV_PROBE_THRESHOLD       2                连续失败几次判离线
JUSTNAV_PROBE_INSECURE       true             放行自签名证书
JUSTNAV_DISCOVERY_FILE        （空=未启用）     容器快照路径，见「Docker 自动发现」
JUSTNAV_DISCOVERY_STALE_SEC   60               快照超过多少秒没更新判为过期
JUSTNAV_DOCKER_SCAN_ALL       true             扫所有有端口映射的容器（不要求打标签）
JUSTNAV_AUTH                  true             是否启用登录认证
JUSTNAV_USER                  admin            账号
JUSTNAV_PASSWORD              （空=admin）      初始密码，只在首次创建管理员时生效
JUSTNAV_COOKIE_SECURE         false            走 HTTPS 反代时设 true
JUSTNAV_INTERNAL_HOST         （空）            内网 IP/主机名，如 192.168.1.10
                                                「待收录」拼候选地址用它，走反代时必填
```

---

## 🔌 接口

全部返回 JSON，除 `/api/health` 外都需要登录（`JUSTNAV_AUTH=false` 时可关掉）。

```
认证
  GET    /api/auth/status          当前登录状态
  POST   /api/auth/login           登录
  POST   /api/auth/logout          登出

条目
  GET    /api/items                条目 + 分组 + 设置（前端一次拿全）
  POST   /api/items                新增 / 更新单条
  POST   /api/items/bulk           批量导入
  PUT    /api/items/order          调整条目顺序
  DELETE /api/items?id=            删除单条

分组
  POST   /api/groups               新增分组
  PUT    /api/groups               重命名分组
  PUT    /api/groups/order         调整分组顺序
  DELETE /api/groups               删除分组

设置
  POST   /api/settings             更新设置（壁纸强度 / 搜索引擎 / 卡片排布 / 天气城市…）

状态
  GET    /api/health               健康检查 + 版本号
  GET    /api/status               各条目在线探测结果
  GET    /api/sysinfo              宿主机 CPU / 内存 / 存储

图标
  GET    /api/favicon              抓站点 favicon
  GET    /api/icons/search         搜索图标库
  GET    /api/icons/get            取图标

壁纸
  GET    /api/wallpaper            壁纸元信息 + 缓存列表
  GET    /api/wallpaper/img?date=  取图
  POST   /api/wallpaper/refresh    立即抓一次
  POST   /api/wallpaper/upload     上传自定义壁纸

下载器
  GET    /api/downloaders          下载器状态（密码不出后端）
  POST   /api/downloaders/test     测试连接

天气
  GET    /api/weather              当前实况（温度 / 体感 / 湿度 / 今日高低）
  GET    /api/weather/forecast     5 天预报 + 当天详情（右侧栏天气卡片用）
  GET    /api/weather/search?q=    按城市名查经纬度

Docker 发现
  GET    /api/inbox                待收录候选

账号（仅管理员）
  GET    /api/users                账号列表
  POST   /api/users                新建账号
  PUT    /api/users/password       改密码
  PUT    /api/users/username       改用户名
  DELETE /api/users?id=            删除账号
```

天气数据走三级兜底，第一个成功的就用：

```
The Weather Channel（苹果天气同源）→ 中国天气网 → open-meteo → wttr.in
```

TWC 排第一是因为它一次请求就带全实况和真实当日高低；中国天气网经常把当天最高温给成 `999`（缺测）。

---

## 🔐 登录

首次启动且没设 `JUSTNAV_PASSWORD` 时，创建的管理员是 **`admin` / `admin`**，
启动日志里会有一行醒目警告：

```
[justnav] ⚠️  首次启动，已创建管理员：admin  密码：admin  —— 登录后请立刻改掉
```

`JUSTNAV_PASSWORD` 的语义是**「首次初始化密码」**：只在账号表为空时用一次，
之后重启不会覆盖你在界面上改过的密码。

机制：

```
存储      data/users.json —— PBKDF2-HMAC-SHA256 哈希 + 会话签名密钥
会话      无状态签名 cookie；勾「记住我」30 天，不勾则关浏览器失效
限速      同一 IP 连续失败 5 次锁 5 分钟
改密码    重新生成签名密钥 → 所有旧会话立即失效
浏览器端  只存账号名，不存密码
```

### 多账号

不同账号看到**完全不同的导航页**（条目、分组、壁纸、主题、卡片、内外网设置都各自独立），
共用 Bing 每日壁纸缓存与状态探测调度。

```
data/users/<uid>/nav.json        每个账号自己的条目 + 设置
data/users/<uid>/wallpaper.jpg   每个账号自己的自定义壁纸
data/wallpaper/                  Bing 每日缓存（共享，只下一份）
```

角色：

```
管理员    第一个账号；能新建/删除账号、重置别人的密码
普通用户  只能改自己的密码、管自己的导航
```

账号隔离是**数据目录级**的，不是一个文件里靠 userID 过滤 —— 漏一处过滤就会串号，分目录不会。

⚠️ 这是「多个自用者分账号」，不是权限系统：普通用户仍然能改自己那套的全部内容，
没有只读/共享分组这类概念。

---

## 🏷️ Docker 自动发现（可选）

想让导航页自动发现 NAS 上的容器，多起一个采集器就行。

### 开启 / 关闭

**默认就开。** `docker compose up -d` 会同时起主程序和采集器。

不想要：把 compose 里 `justnav-discovery` 那段注释掉，或者把主程序里的
`JUSTNAV_DISCOVERY_FILE` 留空。

### 为什么是两个容器

把 Docker 的 socket 挂进主程序，等于把 NAS 的最高权限交给它。单独拆个采集器出来，主程序就拿不到。

代价是多 8MB 内存，实测 CPU 占用 0%。

### 打标签（可选）

不打标签也能用——有端口映射的容器都会进「待收录」，勾一下就行。
想控制名字和图标就打：

```
nav.enable=true
nav.name=Jellyfin
nav.url=http://192.168.1.10:8096
nav.icon=film
nav.group=影音媒体
```

详细的安全分析在 [`docs/docker-socket-security.md`](docs/docker-socket-security.md)。

---

## 🛠️ 构建镜像

镜像由 GitHub Actions 自动构建，推到 `ghcr.io/fenglibo51/justnav`：

```
触发方式    打 tag（v*）/ push 到 master / 手动 workflow_dispatch
架构        linux/amd64 + linux/arm64
标签        1.1.0 / 1.1 / latest（镜像标签不带 v 前缀，v1.1.0 是 git tag）
```

本地构建：

```bash
docker build -t justnav:local --build-arg VERSION=dev .
```

版本号通过 `-ldflags -X justnav/internal/api.Version=$VERSION` 注入。
源码里的兜底值是 `dev` —— 开发期不手改数字，**只有正式发版打 tag 时才注入真实版本号**。

---

## 🧪 自检

仓库里带一套 `scripts/selfcheck.sh`，400+ 条断言，跑一遍能挡住绝大多数「改一处崩另一处」：

```bash
bash scripts/selfcheck.sh
```

覆盖：

```
go vet / go test / 编译
前端关键结构（登录框、卡片网格、时钟对齐、右侧栏定位…）
CSS 大括号平衡（防跨段删除删坏）
暗色主题共享变量选择器的完整性
功能性回归（每个修过的 bug 都留一条断言，防止再犯）
```

---

## ⚠️ 版权提醒

每日壁纸来自 Bing，图源为 Getty / 摄影师投稿。本项目**只在运行时下载并缓存在本地数据目录**，
仓库里不携带任何壁纸图片，`.gitignore` 已排除 `/data/`。自行部署时同理 —— 别把缓存提交上去。

README 里的截图使用示例数据（Google / GitHub / YouTube / Netflix 这类通用服务），
天气为北京，下载器参数为虚构，不含任何真实内网信息。

## 📄 许可

[MIT](LICENSE)
