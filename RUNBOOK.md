# RUNBOOK — 运维手册

## 启动

```bash
# 方式一：源码直接跑
export PATH="$HOME/.toolchain/go/bin:$PATH"
cd /projects/public/justnav
JUSTNAV_PORT=8522 JUSTNAV_DATA_DIR=./data go run .

# 方式二：编译后跑
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o justnav .
JUSTNAV_PORT=8522 JUSTNAV_DATA_DIR=./data ./justnav

# 方式三：Docker
cd /volume1/docker/justnav && docker compose up -d --build
```

⚠️ 改了 `web/` 下的前端**必须重新编译** —— 前端是 `go:embed` 编进二进制的，不重编等于没改。
这是本项目最容易踩的坑。

## 停止

```bash
docker compose down          # 容器方式
kill <pid>                   # 裸跑方式（SIGTERM 会走优雅关闭，8 秒超时）
```

## 交付前自检（必跑）

```bash
bash scripts/selfcheck.sh      # 全绿才交付，红着别发
```

它断言的是**「静默失效」类问题** —— 编译能过、单测能过、页面打开也不报错，
但功能已经没了：

```
前端关键实现是否原样存在（长按门控 / 抖动 / click 抑制 / 跨组 / 空组放置区）
无遗留的即时拖拽实现（那会被误触）
仓库卫生（内嵌位图 / 图片文件 / Markdown 表格）
go vet + go test + 编译
```

⚠️ 断言用的是 `grep -F` 精确字面量。第一版写成子串匹配，`bindCardDragGONE`
这种改名也算命中 —— **检查器自己骗自己**。改动断言后，务必用「故意破坏的副本」
跑一遍确认它会变红（`exit 1`），否则等于没检查。

## 🚀 当前部署实例（NAS 920+）

```
位置        /volume1/docker/justnav/          compose.yaml + data/
镜像        justnav:local（8.1MB，scratch）
容器        justnav                    0.0.0.0:8522->8522（挂了 /var/run/docker.sock）
访问        http://<你的NAS>:8522
内存        justnav ≈16MiB（socket 直挂，无旁车）
```

**每次改完前端必须重新构建镜像**（前端是 go:embed 编进去的）：

```bash
cd /volume1/work/projects/public/justnav && docker build -t justnav:local .
cd /volume1/docker/justnav && docker compose up -d
```

### Docker 发现为什么需要第二个容器

Docker API 在宿主机的 `/var/run/docker.sock` 上，要读容器列表只有两条路：

```
路 A  把 socket 直接挂进 justnav（当前采用）
      零额外容器；但 `:ro` 只约束文件系统挂载、不约束 Docker API ——
      等于把 NAS 的 root 交给这个容器
路 B  socket-proxy 旁车（可选，想收紧权限时自行启用）
      多 1 个容器（约 19MB 内存）；只能读容器列表，写操作全部 403
```

路 B 的安全边界**实测过**：

```
GET  /version         → 200  能读
POST /containers/json → 403  Forbidden（写被拒）
```

"""

## 健康检查

```bash
curl -s http://127.0.0.1:8522/api/health
# {"ok":true,"version":"0.1.0","wallpaper":true,"wallpaperAt":true,"docker":false,...}
```

`wallpaperAt:false` 表示还没抓到壁纸 —— 看日志里的「壁纸抓取失败」。

## 排障

```
症状                                可能原因 / 处理
────────────────────────────────────────────────────────────────────────────────
页面显示「离线演示」                    /api/health 不通。查服务是否在跑、端口是否对上
卡片是演示数据不是自己的                 同上，前端在离线兜底模式
壁纸不显示但接口 200                   （1）壁纸功能关闭 JUSTNAV_WALLPAPER=false
                                    （2）还没抓到：curl -X POST /api/wallpaper/refresh
                                    （3）浏览器缓存了旧图，Ctrl+F5
日志出现「壁纸抓取失败（沿用现有）」      Bing 接口不通或被限流。导航本身不受影响，
                                    修好网络后下一次 6 小时轮询会自动补上
inbox 返回 enabled:false             没配 JUSTNAV_DOCKER_HOST。见下节
改了前端但页面没变                     没重新编译。go:embed 是编译期嵌入
数据目录权限报错                       容器内 uid 与挂载目录属主不一致，chown 对齐
```

## 数据与备份

```
<data>/nav.json                  导航条目 + 设置（唯一的业务数据，最重要）
<data>/wallpaper/<yyyymmdd>.jpg  壁纸缓存（可随时删，会自动重抓）
<data>/wallpaper/meta.json       当前壁纸元信息
```

**备份只需要 `nav.json`。** 壁纸是缓存，删了自动重下。

清理壁纸缓存（不想等定时清理时）：

```bash
rm -f <data>/wallpaper/2*.jpg     # 保留 meta.json 即可，下次启动会重抓当天的
```

## Docker 自动发现

**默认就开着**，compose 里直挂宿主机 socket，`JUSTNAV_DOCKER_HOST` 指向 `/var/run/docker.sock`。
想收紧权限可以自行加只读旁车（见上面「路 B」），再把变量改成 `tcp://docker-proxy:2375`。

发现两种来源：

```
有 nav.enable=true 标签   → 名称/图标/分组/外网地址都按标签来（精确）
无标签但有端口映射        → 进「待收录」当候选，界面勾选后收录（兜底）
```

兜底扫描由 `JUSTNAV_DOCKER_SCAN_ALL` 控制（默认 true）。
关掉就只认打了标签的容器。

**安全红线**：`:ro` 只约束文件系统挂载，不约束 Docker API，
socket 一通等于宿主机 root。当前就是直挂 socket（2026-11-13 定），
默认不跑旁车 —— 所以这个容器**只在局域网里跑，不要暴露到公网**。
真要不放心，可以按上面「路 B」自己加只读旁车。

容器要主动排除就加 `nav.hide=true`。

## 已知运维注意

- 壁纸缓存有版权（Bing 图源为 Getty / 摄影师投稿）。**只存本地，绝不提交进 git 仓库。**
  `.gitignore` 已包含 `/data/`，改过之后要验证：`git status --short --ignored` 命中行必须是 `!!`。
- 服务不预设反向代理。局域网直连 `http://NAS_IP:8522` 即可；只有需要外网访问、
  或 iOS 装 PWA 依赖 Service Worker（必须 HTTPS）时才加。

## 📱 真机自测清单（1 分钟，我做不到的部分）

长按在 iOS / 安卓上很容易被系统抢走（弹出的「拷贝/共享」菜单、文本选择、滚动）。
代码层面已经加了 `-webkit-touch-callout:none` + `user-select:none` + `touch-action:manipulation`，
并用 `hasTouch` + `pointerType:"touch"` 的指针事件验过逻辑，**但真机手感只能你按**。

手机上打开后按顺序做这五步，每步只看一个现象：

```
① 轻点卡片           → 新标签打开服务（不该出现任何抖动）
② 按住卡片 1 秒后松手  → 卡片放大并抖动；松手后不打开服务、顺序不变
③ 长按后拖动到别的分组 → 卡片跟手移动，虚线占位跟着走，松手后落到新分组
④ 长按期间看有没有     → 弹出「拷贝/共享」菜单 / 文字被选中 / 页面跟着滚动
                        （三者任一出现就是没防住，回来告诉我具体机型）
⑤ 侧栏分组上重复 ②     → 同样只在长按后能拖，轻点只是切换分组
```

已知风险点：
- iOS 的「长按 500ms 弹出菜单」比我们的 450ms 晚一点，理论上我们抢先；但 Safari 版本差异大
- 微信内置浏览器（X5 内核）长按可能直接走「识别图中内容」，这条最可能出问题
- 安卓部分机型长按会触发选择手柄，靠 `user-select:none` 挡，需要实测
