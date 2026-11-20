#!/usr/bin/env bash
# JustNav 交付前自检。
#
# 为什么有这个脚本：2026-11-12 清理死代码时，用「从 A 标记切到 B 函数」的方式删
# enableDrag，把夹在中间的整块卡片拖拽也一起删了 —— 编译照样过、单测照样过、
# 页面打开也不报错，只是拖拽静默失效。只有端到端跑 PointerEvent 才抓得到。
#
# ⚠️ 断言必须用【精确字面量】（grep -F），不能用子串/宽模式。
#    第一版写成 grep -c "bindCardDrag"，把 bindCardDragGONE 也算命中；
#    写成 grep -c "> 8)" 时，函数体被掏空但括号还在也算命中 —— 检查器自己骗自己。
#    改完一定要用「故意破坏的副本」验它能红。
#
# 用法：bash scripts/selfcheck.sh

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

FAIL=0
ok()  { printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad() { printf '  \033[31m✗\033[0m %s\n' "$1"; FAIL=1; }

WEB=web/index.html

# 精确断言：整行关键代码必须原样存在
need() {
  if grep -Fq -- "$1" "$WEB"; then ok "$2"; else bad "$2 —— 缺失：$1"; fi
}
forbid() {
  if grep -Fq -- "$1" "$WEB"; then bad "$2 —— 不该出现：$1"; else ok "$2"; fi
}

echo "== 1. 卡片拖拽（长按 → 抖动 → 拖 → 跨组）"
need '(function bindCardDrag() {'                     '卡片拖拽 IIFE 存在'
need 'cardDrag.mode = "jiggle";'                      '长按后进入抖动态'
need 'if (Math.abs(dx) + Math.abs(dy) > 8) dragReset();' '未长按就移动 → 放弃（防误触）'
need 'function ensureDropTargets() {'                 '空分组放置区'
need 'function dragReset(opts) {'                      '拖拽态复位（支持 keepMode）'
need 'function collectLayout() {'                     '布局采集（带 group）'
need 'commitItemOrder();'                             '松手后提交布局'
need '.card.picked{'                                  '卡片抖动样式'
need '@keyframes jnJiggle{'                           '抖动动画'

echo "== 2. 分组拖拽（同样要求长按）"
need '(function bindGroupDrag() {'                    '分组拖拽 IIFE 存在'
need 'st.mode = "jiggle";'                            '分组长按后进入抖动态'
need 'if (Math.abs(e.clientX - st.sx) + Math.abs(dy) > 8) reset();' '分组未长按就移动 → 放弃'
need '.nav-i.picked{'                                 '分组抖动样式'
need '@keyframes jnJiggleSoft{'                       '分组抖动动画'

echo "== 3. 误触防护"
need 'if (!suppressClick) return;'                    'click 抑制器存在'
need 'suppressClick = true;'                          '长按后置位抑制标记'
forbid 'function enableDrag'                          '无遗留的即时拖拽实现（会被误触）'

echo "== 3.5 新增功能（壁纸落盘 / 书签导入）"
need 'function parseBookmarks(html) {'                   '书签解析（Netscape 格式）'
need 'function prepareImport(raw) {'                     '导入去重'
need '/api/items/bulk'                                   '批量导入接口调用'
need '/api/wallpaper/upload'                             '壁纸上传走后端'
forbid 'fr.readAsDataURL(f)'                             '壁纸上传不再只存浏览器内存'
# 后端断言要看 Go 文件，不能拿前端文件 grep（第一版就写错了）
API=internal/api/api.go
grep -Fq -- 'handleWallpaperUpload' "$API" && ok "后端上传路由" || bad "后端缺少 upload 路由"
grep -Fq -- 'SaveCustom' internal/wallpaper/bing.go && ok "壁纸重编码落盘" || bad "缺少 SaveCustom"
grep -Fq -- 'handleItemsBulk' "$API" && ok "后端批量路由" || bad "后端缺少 bulk 路由"
grep -Fq -- 'BulkUpsert' internal/store/store.go && ok "批量写入（单次落盘）" || bad "缺少 BulkUpsert"
if grep -Fq -- '-webkit-touch-callout:none' "$WEB"; then
  ok "长按防系统菜单（真机关键）"
else
  bad "缺少 -webkit-touch-callout:none —— iOS 长按会弹系统菜单"
fi

echo "== 3.6 登录认证"
grep -Fq -- 'pbkdf2SHA256' internal/auth/auth.go && ok "密码哈希（PBKDF2）" || bad "缺少密码哈希实现"
grep -Fq -- 'IssueSession' internal/auth/auth.go && ok "会话签发" || bad "缺少会话签发"
grep -Fq -- 'func (s *Server) authGate' internal/api/api.go && ok "路由门禁" || bad "缺少 authGate —— 接口会裸奔"
grep -Fq -- '"/api/items":' internal/api/api.go && bad "/api/items 出现在白名单里 —— 千万别" || ok "业务接口不在白名单"
grep -Fq -- 'function showLogin()' "$WEB" && ok "登录页" || bad "缺少登录页"
grep -Fq -- '/api/auth/login' "$WEB" && ok "登录请求" || bad "缺少登录请求"
grep -Fq -- 'id="lgEye"' "$WEB" && ok "密码框右侧有小眼睛（明文/圆点切换）" || bad "密码框没有小眼睛按钮"
grep -Fq -- '.pw-eye{' "$WEB" && ok "小眼睛样式在位" || bad "小眼睛缺样式"
grep -Fq -- 'flex:0 0 100%;justify-content:flex-end' "$WEB" && ok "设置页账号按钮组独占一行（长用户名不再挡住改名/改密）" || bad "账号按钮组还会被长名字挤掉"
grep -Fq -- 'ic:groupIcon(s.g)' "$WEB" && ok "手机 tabbar 图标走通用规则（用户自建分组也认得出）" || bad "tabbar 又硬编码作者的分组图标了"
grep -Fq -- 'GROUP_RULES = [' "$WEB" && ok "分组图标关键词规则在位（28 条）" || bad "GROUP_RULES 没了，自建分组全变 folder"
grep -Fq -- 'if (!secs.length) return;' "$WEB" && bad "alignClock 又在空库早退（时钟会甩到左上角）" || ok "alignClock 空库不早退（新用户零分组时钟也在右上）"
grep -Fq -- 'top:82px;right:8px;width:240px;' "$WEB" && ok ".right-rail 有兜底定位（JS 算不出来也不跑偏）" || bad ".right-rail 没兜底定位了"
grep -Fq -- 'body.booting .app' "$WEB" && ok "启动态藏主界面（未登录不再闪一下主界面）" || bad "缺少 booting 启动态，未登录会先闪主界面"
grep -Fq -- '<body class="booting">' "$WEB" && ok "body 初始带 booting" || bad "body 没带 booting 初始态"
grep -Fq -- 'var DATA = [];' "$WEB" && ok "DATA 不带演示数据（开源不泄漏作者服务清单）" || bad "DATA 里又塞演示数据了"
grep -Fq -- 'function paintEmpty(' "$WEB" && ok "空态分三种（全新/空分组/搜索无果）" || bad "缺少 paintEmpty，新用户会看到「没有匹配的服务」"
grep -Fq -- 'id="btnLogout"' "$WEB" && ok "退出登录入口" || bad "缺少退出登录"

echo "== 3.7 多账号"
grep -Fq -- 'func (r *Registry) For' internal/store/registry.go && ok "按用户分库" || bad "缺少 Registry"
grep -Fq -- 'func (m *Manager) Add(' internal/auth/auth.go && ok "账号创建" || bad "缺少账号创建"
grep -Fq -- 'SessionTag' internal/auth/auth.go && ok "改密码使旧会话失效" || bad "缺少 session tag"
grep -Fq -- 'func (s *Server) storeFor' internal/api/api.go && ok "接口按当前用户取数据" || bad "缺少 storeFor —— 会串号"
grep -Fq -- 'handleUserDelete' internal/api/api.go && ok "删账号接口" || bad "缺少删账号接口"
grep -Fq -- 'id="users"' "$WEB" && ok "账号管理界面" || bad "缺少账号管理界面"

echo "== 3.8 已知易复发问题"
grep -Fq -- 'const DefaultGroup' internal/store/store.go && ok "空分组兜底（否则收录后看不见）" || bad "缺少 DefaultGroup 兜底"
grep -Fq -- 'order.forEach(function (g) {' "$WEB" && ok "分组 union（列表外的分组也渲染）" || bad "前端只按 groups 列表渲染 —— 会再次看不见条目"
grep -Fq -- 'hostHint' internal/api/api.go && ok "兜底候选地址用内网 hostHint" || bad "缺少 hostHint，反代下地址会拼错"
grep -Fq -- 'JUSTNAV_INTERNAL_HOST' README.md && ok "README 写了 JUSTNAV_INTERNAL_HOST" || bad "README 没写 InternalHost 配置"

echo "== 3.9 搜索 / 壁纸强度 / 拖拽退出"
grep -Fq -- 'id="q"' "$WEB" && ok "顶栏搜索框" || bad "缺少搜索框"
grep -Fq -- 'function buildRows(q)' "$WEB" && ok "搜索建议（条目+网址+引擎）" || bad "缺少搜索建议"
grep -Fq -- 'id="engmenu"' "$WEB" && ok "引擎切换菜单" || bad "缺少引擎菜单"
grep -Fq -- 'id="engines"' "$WEB" && ok "引擎管理弹窗" || bad "缺少引擎管理"
grep -Fq -- 'id="toneModes"' "$WEB" && bad "壁纸强度选择器又回来了（2026-11-13 起只留「清晰」一档）" || ok "壁纸强度单档（选择器已移除）"
grep -Fq -- 'id="dragDone"' "$WEB" && ok "拖拽退出按钮" || bad "缺少拖拽退出"
# 壁纸不能被启动时的空数据抹掉
if grep -Fq -- 'else if (state.wall === "daily") state.wall = "none";' "$WEB"; then
  bad "applyWall 里还有把 daily 抹成 none 的分支 —— 刷新会变纯色"
else
  ok "applyWall 不再销毁用户选择（无降级分支）"
fi
grep -Fq -- 'var flat' "$WEB" && bad "flat 变量回来了 —— 命令面板已删，会 ReferenceError" || ok "无孤儿 flat 引用"

echo "== 3.10 时间/天气"
grep -Fq -- 'id="clockCard"' "$WEB" && ok "右上角时钟卡片" || bad "缺少时钟卡片"
grep -Fq -- 'function loadWeather()' "$WEB" && ok "天气加载" || bad "缺少天气加载"
grep -Fq -- 'api.open-meteo.com' internal/weather/weather.go && ok "Open-Meteo 数据源" || bad "缺少天气数据源"
grep -Fq -- 'handleWeather' internal/api/api.go && ok "天气接口" || bad "缺少天气接口"
grep -Fq -- 'needCity' internal/api/api.go && ok "没配城市时优雅降级" || bad "缺少 needCity 降级"

echo "== 3.11 核心函数存在性（防区间误删）"
for fn in 'function applyWall(announce)' 'var elWallMeta' 'function paintWallpaper(info)' \
          'function loadWallpaper()' 'function applyTheme(id, announce)' 'function render()' \
          'function applyServerItems(d)' 'function loadWeather()' 'function buildNav()'; do
  if grep -Fq -- "$fn" "$WEB"; then ok "$fn"; else bad "核心函数被删了：$fn"; fi
done

grep -Fq -- 'document.getElementById("wallFile").addEventListener("change"' "$WEB" && ok "壁纸上传绑定" || bad "壁纸上传绑定被删了"
grep -Fq -- 'document.getElementById("btnWallNext").addEventListener(' "$WEB" && ok "换一张绑定" || bad "换一张绑定被删了"
grep -Fq -- 'querySelectorAll(".wall-modes button").forEach' "$WEB" && ok "壁纸模式切换绑定" || bad "壁纸模式切换绑定被删了"

grep -Fq -- 'function toggleThemePop(' "$WEB" && ok "设置弹层开关" || bad "设置弹层开关被删了（打不开设置）"
grep -Fq -- 'const themepop' "$WEB" || grep -Fq -- 'themepop=$("#themepop")' "$WEB" && ok "themepop 变量" || bad "themepop 变量被删了"

echo "== 3.12 卡片两档"
grep -Fq -- 'data-size="' "$WEB" && ok "卡片档位属性" || bad "缺少卡片档位"
grep -Fq -- '.card[data-size="normal"]{grid-column:span 2}' "$WEB" && ok "正常档跨 2 列" || bad "正常档没跨列"
grep -Fq -- 'function autoDesc(it)' "$WEB" && ok "描述自动取值" || bad "缺少自动描述"
# 2026-11-19 起全局统一 1×2，编辑里不再有档位选择
grep -Fq -- 'id="fSize"' "$WEB" && bad "档位选择又回来了（全局统一 1×2，不该再有这个下拉）" || ok "无档位选择（全局统一 1×2）"
grep -Fq -- 'showStat = (it.source === "docker")' "$WEB" && ok "状态灯只给 docker 条目" || bad "状态灯没做来源判断"
grep -Fq -- 'Description string' internal/store/store.go && ok "描述字段落库" || bad "缺少 description 字段"

echo "== 3.13 卡片不露地址 / 正常档居中 / 调参器偏移"
if grep -Fq -- 'card-meta" title="' "$WEB"; then
  bad "卡片还在渲染地址行 —— 导航页不该出现 IP/域名"
else
  ok "卡片不渲染地址行（地址只在编辑弹窗里看）"
fi
grep -Fq -- '绝不拿域名/IP 当自动描述' "$WEB" && ok "自动描述不兜底域名/IP" || bad "自动描述又拿域名兜底 —— 地址换个位置又露出来"
grep -Fq -- '#sections .card[data-size="normal"]{justify-content:center}' "$WEB" && ok "正常档内容垂直居中" || bad "正常档内容贴顶 —— 删了地址行会下半截空一片"
# 2026-11-20 frontend/ 原型目录已删（无任何代码引用 + 夹带真实自托管服务清单），
# 原「调参器文字偏移 / 横排 gap」两条断言随目录一起去掉。

echo "== 3.14 卡片定稿尺寸（方案 B 2026-11-12）"
grep -Fq -- 'grid-template-columns:repeat(auto-fill,240px)' "$WEB" && grep -Fq -- 'grid-auto-rows:80px' "$WEB" && ok "网格 240×80" || bad "网格不是定稿的 240×80"
grep -A2 -- '#sections .card .ico{' "$WEB" | grep -Fq -- 'width:40px;height:40px;flex:0 0 40px' && ok "图标 40，占卡高 50%（框/svg/favicon 三处一起）" || bad "图标尺寸没跟着改 —— 会出现框大图标小"
grep -Fq -- 'height:100%;padding:16px;border-radius:16px' "$WEB" && ok "卡片内边距 16（参考项目 18）" || bad "内边距不是 16，图标又会贴边显挤"
grep -Fq -- '#sections .card[data-size="compact"] .card-top{' "$WEB" && grep -Fq -- 'gap:16px' "$WEB" && ok "精简档横排 + 间距 16" || bad "精简档还是竖排或间距不对"

echo "== 3.15 拖拽到侧栏分组（2026-11-12）"
grep -Fq -- 'cardDrag.overGroup = overNav.dataset.g' "$WEB" && ok "侧栏分组是投放目标（原来只认 #sections）" || bad "侧栏 drop 逻辑被删了：拖到分类上松手不会改分组"
grep -Fq -- 'function commitMoveToGroup' "$WEB" && ok "改分组走独立提交（DATA 全量组 payload）" || bad "commitMoveToGroup 不见了"
grep -Fq -- 'if (targetGroup) { commitMoveToGroup' "$WEB" && ok "落点在侧栏 = 换分组；落点在卡片区 = 只调顺序" || bad "finish() 没按落点分叉"
grep -Fq -- 'body.drag-mode .nav-i:hover .n{visibility:visible}' "$WEB" && ok "拖拽态分类名不被 hover 规则隐藏" || bad "拖拽时分类文字会被 .nav-i:hover 藏掉"
grep -Fq -- '#sections .card.drag-live.dock' "$WEB" && ok "拖到侧栏时卡片缩小停靠" || bad "缺少 dock 缩放样式"
grep -Fq -- 'cardDrag.el.style.left = lx + "px"' "$WEB" && ok "幽灵卡按视口坐标定位（#sections 不再造包含块）" || bad "拖拽幽灵卡又改成包含块换算了吗？"
forbid '三层结构改造'                                  '已否决的三层改造块没有回流（界面须与线上一致）'
grep -Fq -- '#sections{' "$WEB" && bad "#sections 又有了独立样式规则：一旦加 backdrop-filter，幽灵卡会被困在里面裁掉" || ok "#sections 无 backdrop-filter（拖拽卡片不会被裁/压在侧栏下）"

echo "== 3.16 时间天气卡对齐 + 图标铺满（2026-11-12）"
grep -Fq -- 'function alignClock' "$WEB" && ok "时间天气卡尺寸由 alignClock() 按排版算，不写死像素" || bad "alignClock 不见了：时间卡尺寸会退回 CSS 里的兜底值"
grep -Fq -- 'rail.style.height = Math.round(Math.max(260, mr.bottom - sr.top - 8))' "$WEB" && ok "右侧栏撑满可视区（时钟卡 + 下方卡片槽位）" || bad "右栏高度算法变了/丢了"
grep -Fq -- 'var layers = Math.min(2, secs.length)' "$WEB" && bad "旧的「按两层卡片算时钟卡高度」又回来了（48 张卡会算出 806px，下方没空间放卡片）" || ok "不再按时钟卡吃满内容区高度"
grep -Fq -- 'mr.right - topA.getBoundingClientRect().right' "$WEB" && ok "右沿对齐右上工具栏右缘" || bad "右沿没跟工具栏对齐"
grep -Fq -- 'requestAnimationFrame(alignClock)' "$WEB" && ok "render 后重新对齐（筛选/增删条目也不跑偏）" || bad "render 没有触发重排"
grep -Fq -- '#sections .card .ico img{' "$WEB" && ok "favicon 有卡片内的独立规则（去框铺满）" || bad "图标规则丢了"
grep -Fq -- 'width:40px;height:40px;border-radius:10px;' "$WEB" && ok "favicon 尺寸 = 图标框 40（不再是 30 套 1px 描边）" || bad "favicon 又被压小了/描边回来了"
grep -Fq -- 'background:none;box-shadow:none;padding:0;' "$WEB" && ok "favicon 去掉自带白底 + 1px 描边的『框里框』" || bad "favicon 的白底/描边又回来了"
grep -Fq -- '[data-size="compact"] .ico img{width:30px' "$WEB" && bad "精简档又单独压图片尺寸：会和通用规则打架，露出底色框" || ok "精简档不再覆盖图标图片尺寸"
grep -Fq -- 'background:none;border:0;box-shadow:none;' "$WEB" && ok "图标格不画底板（无淡色底 / 描边 / 内高光）" || bad "图标格又加回底板了：图片圆角与框圆角不一致会漏出磨砂层"
grep -c -- '#sections .card .ico{' "$WEB" >/dev/null && ok "图标格有卡片内独立规则（覆盖 .ico[data-h] 的配色）" || bad "图标格规则缺失，会退回 .ico 的色调底"

echo "== 3.17 天气双源 + 壁纸强度单档（2026-11-13）"
grep -Fq -- 'func (c *Client) fetchWttr' internal/weather/weather.go && ok "备用源 wttr.in 在（主源被墙时不至于整块空白）" || bad "没有备用源：open-meteo 一断，天气就没了"
grep -Fq -- 'func wwoToWMO' internal/weather/weather.go && ok "WWO→WMO 代码翻译在（图标和中文文案还能对上）" || bad "缺少 WWO 代码翻译，备用源会给错图标"
grep -Fq -- 'if stale, ok := c.last[key]; ok && time.Since(stale.At) < staleTTL' internal/weather/weather.go && ok "上游全挂时挂上次成功数据（staleTTL 兜底）" || bad "没有 stale 兜底：上游抽风就整块消失"
grep -Fq -- 'if ctx.Err() != nil {' internal/weather/weather.go && ok "客户端断开不写失败缓存（否则关页面会让天气 1 分钟不显示）" || bad "ctx 取消被当成上游失败缓存了"
grep -Fq -- 'c.omDownUntil = time.Now().Add(omBreak)' internal/weather/weather.go && ok "主源失败后熔断 30 分钟（不再每次白等 4 秒）" || bad "没有熔断：缓存过期后每次都要等主源超时"
grep -Fq -- 'open-meteo' internal/weather/weather.go && ok "主源仍是 Open-Meteo（数据更全）" || bad "主源没了"
forbid 'toneModes'                                    '壁纸强度选择器已移除（只留「清晰」一档）'
grep -Fq -- 'SET.tone = "crisp";' "$WEB" && ok "壁纸强度档位固定 crisp" || bad "档位没固定成 crisp：可能又跟着旧设置走"
grep -Fq -- 'justify-content:space-between' "$WEB" && ok "时间卡内部时间/天气上下撑开" || bad "时间卡内部没有撑开"

echo "== 3.18 界面美化 A-E（2026-11-13）"
grep -Fq -- 'function isDockerImageRef' "$WEB" && ok "A: 镜像名识别（历史数据里的 ghcr.io/xxx 不再当描述）" || bad "A: 没有镜像名过滤，卡片下面又会冒 ghcr.io/..."
grep -Fq -- 'if (it.desc && !isDockerImageRef(it.desc, it)) return it.desc;' "$WEB" && ok "A: 描述只取人工填写内容" || bad "A: autoDesc 又兜底镜像名了"
grep -Fq -- 'var descLine = (size === "normal" && dText)' "$WEB" && ok "A: 没描述时不渲染空 .card-desc（名字居中）" || bad "A: 空描述仍渲染节点，卡片下半截空一片"
grep -Fq -- 'description: "",' "$WEB" && ok "A: 收录容器不再把镜像名写进库" || bad "A: adopt 又把 image 写进 description"
grep -Fq -- '#sections .card .ico.has-letter,' "$WEB" && grep -Fq -- 'linear-gradient(135deg,#4f6ef7,#364fc7)' "$WEB" && ok "B: 没 favicon 的条目是实心彩色圆角块 + 白字" || bad "B: 首字母又变回光秃秃一个蓝字"
grep -Fq -- '#sections .card .ico.has-img > svg,' "$WEB" && ok "B: 有图时隐藏占位 svg（不套第二层底框）" || bad "B: 图片背后还压着占位 svg"
grep -Fq -- 'var HD_ICONS = {' "$WEB" && ok "C: 内置高清图标库" || bad "C: 高清图标库不见了"
grep -Fq -- 'function matchHdIconKey' "$WEB" && ok "C: 按名字自动匹配高清图标" || bad "C: 缺少服务名→图标的匹配"
grep -Fq -- 'id="btnMatchIcon"' "$WEB" && ok "C: 编辑弹窗有「一键匹配高清图标」" || bad "C: 一键匹配按钮没了"
grep -Fq -- 'data-custom-icon=' "$WEB" && ok "C: 自定义图标随卡片下发" || bad "C: 自定义图标没渲染到卡片"
grep -Fq -- '.sect-h .rule{display:none}' "$WEB" && ok "D: 分组标题不再拖长横线" || bad "D: 长横线又回来了（把壁纸切碎）"
grep -Fq -- 'html[data-wall="none"] .sect-h h2' "$WEB" && ok "D: 纯色底自动切回深色标题" || bad "D: 纯色底会是白字白底"
grep -Fq -- 'func (s *Server) handleSysinfo' internal/api/sysinfo.go && ok "E: 宿主机状态接口" || bad "E: 缺少 sysinfo 接口"
grep -Fq -- 'syscall.Statfs' internal/api/sysinfo.go && ok "E: 存储占用走 Statfs（真实卷容量）" || bad "E: 没有 Statfs，拿不到磁盘占用"
grep -Fq -- 'id="nasStats"' "$WEB" && ok "E: 侧栏 NAS 状态条" || bad "E: 侧栏状态条没了"
grep -Fq -- 'function paintHostStats' "$WEB" && ok "E: 状态条有渲染逻辑" || bad "E: 状态条没有渲染函数"
grep -Fq -- '"host":      s.collectHostStats(),' internal/api/api.go && ok "E: /api/status 带上宿主机数据（复用轮询，不打额外接口）" || bad "E: status 没带宿主机数据"

echo "== 3.19 在线图标库 + docker socket 直挂（2026-11-13）"
grep -Fq -- 'func (c *Client) Search' internal/icons/icons.go && ok "图标库搜索（metadata 索引 + 别名）" || bad "缺少图标库搜索"
grep -Fq -- 'func (c *Client) Get' internal/icons/icons.go && ok "图标代理 + 磁盘缓存" || bad "缺少图标代理：前端会直连 CDN，换网络就空白"
grep -Fq -- 'homarr-labs/dashboard-icons' internal/icons/icons.go && ok "图标源是 dashboard-icons（3500+）" || bad "图标源丢了"
grep -Fq -- 'nameRe.MatchString(name)' internal/icons/icons.go && ok "图标名白名单校验（防路径穿越/SSRF）" || bad "图标名没校验"
grep -Fq -- 'func (s *Server) handleIconSearch' internal/api/iconlib.go && ok "搜索接口" || bad "缺少 /api/icons/search"
grep -Fq -- 'func (s *Server) handleIconGet' internal/api/iconlib.go && ok "取图标接口" || bad "缺少 /api/icons/get"
grep -Fq -- 'if (v.indexOf("lib:") === 0)' "$WEB" && ok "前端支持 lib:xxx（在线图标）" || bad "前端不认 lib: 前缀"
grep -Fq -- 'id="fIconSearch"' "$WEB" && ok "编辑弹窗有在线图标搜索框" || bad "没有在线搜索入口"
grep -Fq -- 'func dockerHost() string' internal/config/config.go && ok "未配置时自动探测 /var/run/docker.sock" || bad "仍需手写 JUSTNAV_DOCKER_HOST"
grep -Fq -- 'unix:///var/run/docker.sock' compose.yaml && ok "compose 默认挂 socket" || bad "compose 还在用旁车 tcp://"
grep -Eq '^[[:space:]]*docker-proxy:' compose.yaml && bad "compose 里还有旁车服务（2026-11-13 已改为直挂 socket）" || ok "compose 已去掉旁车"

echo "== 3.20 隐藏区（没有前端的容器不进首页，可恢复）"
grep -Fq -- 'Hidden bool `json:"hidden,omitempty"`' internal/store/store.go && ok "条目带 hidden 标记（落库，不是只在前端过滤）" || bad "缺少 hidden 字段，隐藏会丢"
grep -Fq -- 'if (it.hidden) { hid.push(it); return; }' "$WEB" && ok "隐藏条目不进导航页" || bad "隐藏条目仍会渲染"
grep -Fq -- 'function paintHidden()' "$WEB" && ok "隐藏区渲染函数" || bad "缺少隐藏区渲染"
grep -Fq -- 'id="hiddenList"' "$WEB" && ok "设置里有隐藏区列表" || bad "设置里没有隐藏区入口"
grep -Fq -- 'data-restore=' "$WEB" && ok "隐藏项可一键恢复" || bad "隐藏项无法恢复 —— 手滑就回不来了"
grep -Fq -- 'function adopt(i, hidden)' "$WEB" && ok "待收录支持「隐藏」动作" || bad "待收录只有收录，没有隐藏"
grep -Fq -- 'class="hidebtn"' "$WEB" && ok "待收录行有「隐藏」按钮" || bad "待收录行没有隐藏按钮"
grep -Fq -- 'hidden: false' "$WEB" && ok "恢复时显式写 hidden:false" || bad "恢复请求没清掉 hidden"
grep -Eq '^[[:space:]]*docker-proxy:' compose.yaml && bad "compose 里又出现旁车" || ok "compose 无旁车（只挂 socket）"

echo "== 3.21 分组图标（用户自建分组怎么给图标）"
grep -Fq -- 'var GROUP_RULES = [' "$WEB" && ok "分组名关键词表（27 类中英规则）" || bad "缺少关键词表"
grep -Fq -- 'var GROUP_ICON_KEYS = [' "$WEB" && ok "分组可选图标集" || bad "缺少图标集"
grep -Fq -- 'return "folder";' "$WEB" && ok "认不出的分组给中性文件夹图标" || bad "缺少中性兜底"
grep -Fq -- 'h * 31 + g.charCodeAt(i)' "$WEB" && bad "哈希随机挑图标又回来了（两个分组会撞同一个图标）" || ok "不再用名字哈希随机挑图标"
grep -Fq -- 'var ov = (SET.groupIcons || {})[g];' "$WEB" && ok "手动指定的图标优先于自动识别" || bad "手动指定没生效"
grep -Fq -- 'id="iconpop"' "$WEB" && ok "分组图标选择弹层" || bad "没有换图标入口"
grep -Fq -- 'data-act="icon"' "$WEB" && ok "侧栏分组悬浮有「换图标」按钮" || bad "分组上找不到换图标按钮"
grep -Fq -- 'function setGroupIcon' "$WEB" && ok "换图标会保存" || bad "换图标没保存逻辑"
grep -Fq -- 'GroupIcons map[string]string' internal/store/store.go && ok "分组图标落库（换设备也跟着走）" || bad "分组图标没落库"
grep -Fq -- 'if ic == "" {' internal/store/store.go && ok "「用回自动识别」会删掉覆盖项" || bad "清空图标后覆盖项残留"

echo "== 3.22 整理模式（长按进入后保持，点「完成」才退出）"
grep -Fq -- 'dragReset({ keepMode: true });' "$WEB" && ok "卡片拖完松手不退出整理模式" || bad "松手就退出 —— 每次都得重新长按"
grep -Fq -- 'reset({ keepMode: true });' "$WEB" && ok "分组拖完松手也不退出" || bad "分组松手就退出"
grep -Fq -- 'var keep = !!(opts && opts.keepMode);' "$WEB" && ok "dragReset 支持保留模式" || bad "dragReset 没有 keepMode"
grep -Fq -- 'if (document.body.classList.contains("drag-mode")) {' "$WEB" && ok "整理模式内可直接拖动（免长按）" || bad "整理模式里还要再长按一次"
grep -Fq -- 'function exitDragMode(' "$WEB" && ok "「完成」按钮是唯一显式出口" || bad "缺少统一退出函数"
grep -Fq -- 'e.key === "Escape" && document.body.classList.contains("drag-mode")' "$WEB" && ok "Esc 也能退出整理模式" || bad "Esc 不能退出"
grep -Fq -- '整理模式里点卡片只是选中/拖动' "$WEB" && ok "整理模式里点卡片不打开服务" || bad "整理模式点卡片会弹标签页"

echo "== 3.23 隐藏区改成二级弹窗（条数再多也不撑破设置面板）"
grep -Fq -- 'id="btnHiddenEntry"' "$WEB" && ok "设置里只有一行入口" || bad "隐藏区又在设置里平铺"
grep -Fq -- 'id="hidebox"' "$WEB" && ok "隐藏区独立弹窗" || bad "缺少二级弹窗"
grep -Fq -- 'max-height:46vh;overflow-y:auto' "$WEB" && ok "弹窗列表限高可滚动（几十条也不炸）" || bad "列表没限高，条目多会撑爆"
grep -Fq -- 'id="hiddenSearch"' "$WEB" && ok "隐藏区可搜索" || bad "缺少搜索框"
grep -Fq -- 'function openHidebox()' "$WEB" && ok "点入口打开二级页" || bad "入口打不开二级页"

echo "== 3.24 整理模式手感（悬停放大抖动 + 平滑过渡）"
grep -Fq -- 'body.drag-mode #sections .card:hover{' "$WEB" && ok "整理模式悬停有独立态" || bad "悬停没做样式"
grep -Fq -- 'transform:scale(1.05) translateY(-3px)' "$WEB" && ok "悬停放大 1.05 + 上浮" || bad "悬停没放大"
grep -Fq -- '@keyframes jnHoverJiggle{' "$WEB" && ok "悬停抖动动画" || bad "缺少抖动动画"
grep -Fq -- 'body.drag-mode #sections .card.drag-live,' "$WEB" && ok "被拖那张不叠抖动动画（跟手优先）" || bad "拖拽中的卡片被 hover 动画干扰"
grep -Fq -- 'transition:transform .3s cubic-bezier(.22,1,.36,1)' "$WEB" && ok "整理模式过渡走缓动曲线（不生硬）" || bad "过渡太生硬/缺失"
grep -Fq -- 'body.drag-mode .scroll{padding-bottom:104px}' "$WEB" && ok "底部留白：投放框不被「完成」按钮压住" || bad "投放框会被按钮挡住"
grep -Fq -- 'body.drag-mode #sections .grid.dropzone{' "$WEB" && ok "投放框显式改回 flex（提示文字居中）" || bad "投放框仍是 grid，提示文字会偏"

echo "== 3.25 布局避让 + 拖拽稳定性（2026-11-13）"
grep -Fq -- 'grids[gi].style.paddingRight' "$WEB" && ok "让位按分组施加（时钟卡下方的分组满宽，能多放卡片）" || bad "所有分组统一让位，时钟卡下方会白扔一列"
grep -Fq -- 'padding:16px 4px 6px 4px' "$WEB" && ok "内容区容器自身不再整体让位（避免和分组让位叠加缩两次）" || bad "容器和分组都让位，右边会缩两次"
grep -Fq -- 'setProperty("--clock-w"' "$WEB" && ok "让位宽度按实测时钟宽度算（侧栏开合/窗口变化都跟随）" || bad "让位宽度写死了"
grep -Fq -- 'Math.abs(dx) + Math.abs(dy) < 6' "$WEB" && ok "拖动 6px 阈值（不会因手抖反复重排整行）" || bad "1px 抖动就插占位符 —— 邻卡会疯狂换位"
grep -Fq -- 'backdrop-filter:none;-webkit-backdrop-filter:none;' "$WEB" && ok "整理模式卡片不用 backdrop-filter（消除重模糊频闪）" || bad "整理模式还带背景模糊，抖动会闪"
grep -Fq -- 'will-change:transform' "$WEB" && bad "will-change 又回来了（每张卡建层会闪）" || ok "没有 will-change 堆积"
grep -Fq -- 'background:var(--accent);border:0;color:#fff;' "$WEB" && ok "「完成」按钮无白边框" || bad "按钮还有白边"
grep -Fq -- 'body.is-dragging .card:hover{transform:none;animation:none!important}' "$WEB" && ok "拖拽中停用悬停抖动（跟手优先）" || bad "拖拽中还在抖"
grep -Fq -- 'position:fixed;right:20px;' "$WEB" && ok "「完成」按钮固定在右下角（不压卡片/投放框）" || bad "完成按钮还会盖住内容"
grep -Fq -- '.drag-done{right:auto;left:50%;transform:translateX(-50%) translateY(14px)}' "$WEB" && ok "手机端按钮回到底部居中" || bad "手机上按钮跑到右下角" 
grep -Fq -- 'cubic-bezier(.22,1,.36,1)' "$WEB" && ok "动效统一走 ease-out-quint 曲线" || bad "动效曲线还是硬的"

echo "== 3.26 拖拽占位符不闪（2026-11-19）"
grep -Fq -- 'cardDrag.snap = []' "$WEB" && ok "拖动开始时拍卡片快照（插入点不读实时布局，就不会插了又拆）" || bad "没有快照，占位符会跟着实时布局反复重排"
grep -Fq -- 'under.closest(".card-ph")' "$WEB" && ok "鼠标压在占位符上不动（切断「命中占位符 → 甩到末尾 → 再命中」死循环）" || bad "占位符自身命中会落到 grid 分支被甩到网格末尾"
grep -Fq -- 'bestCard.parentNode.insertBefore(cardDrag.ph, tgt2)' "$WEB" && ok "空隙里插到最近卡片旁边（不 appendChild 到末尾）" || bad "鼠标落在卡片间隙时占位符会被甩到末尾"
grep -Fq -- 'curGrid.insertBefore(nxt, cardDrag.ph)' "$WEB" && ok "逐格推挤：越过一张只和它换一格（不一次插到位）" || bad "又回到算目标位置一次插到位，DOM 会大范围重排"
grep -Fq -- 'function playFlip(' "$WEB" && ok "逐格交换带 FLIP 位移过渡（卡片滑过去而不是瞬间跳）" || bad "交换没有过渡，视觉上还是硬跳"
grep -Fq -- 'cardDrag.el.parentNode.appendChild(cardDrag.el)' "$WEB" && ok "拖拽卡挪到 DOM 末尾（否则占位符邻居判断被它挡住，拖不动）" || bad "拖拽卡夹在占位符和邻居之间，逐格推挤会失效"
grep -Fq -- 'leftCards[lci].style.transform = ""' "$WEB" && ok "松手清掉 FLIP 残留（不带着旧偏移错位闪）" || bad "松手后卡片带着旧 transform，会错位闪一下"
grep -Fq -- 'applyLayoutToData(items)' "$WEB" && ok "提交后把新顺序同步进 DATA（否则下一次 render 会弹回原位）" || bad "只改 DOM 不改 DATA，等一个状态轮询周期就弹回原位"
grep -Fq -- 'function applyLayoutToData' "$WEB" && ok "applyLayoutToData 实现存在（只重排视图内的条目，其余保持原序）" || bad "缺 applyLayoutToData，DATA 会和服务端脱节"
grep -Fq -- 'function paintStatuses' "$WEB" && ok "状态轮询只补状态点（paintStatuses），不重建整表" || bad "轮询还在 render()，每个周期整表重建一次，撞上拖拽就闪"
grep -Fq -- 'else paintStatuses();' "$WEB" && ok "首次加载才 render，之后一律走局部更新" || bad "loadStatus 仍然无条件 render()"
python3 - "$WEB" <<'PYCHK'
import sys
src = open(sys.argv[1], encoding='utf-8').read()
i = src.find('toast("布局已保存")')
j = src.find('function commitMoveToGroup', i)
if i < 0 or j < 0:
    print("BAD: 找不到 commitItemOrder 的成功分支")
    sys.exit(1)
if 'reloadData()' in src[i:j]:
    print("BAD: 提交成功后仍在 reloadData，整套列表会重建 → 闪")
    sys.exit(1)
print("OK")
PYCHK
[ "$?" = "0" ] && ok "提交成功后不重建列表（落盘不闪）" || bad "提交成功后又 reloadData，列表整体重建会闪"

echo "== 3.27 卡片右键菜单（2026-11-19）"
grep -Fq -- 'id="ctxMenu"' "$WEB" && ok "右键菜单 DOM 存在" || bad "没有右键菜单"
grep -Fq -- 'data-act="open-out"' "$WEB" && ok "菜单含「打开外网地址」" || bad "缺外网入口"
grep -Fq -- 'data-act="open-in"' "$WEB" && ok "菜单含「打开内网地址」" || bad "缺内网入口"
grep -Fq -- 'data-act="edit"' "$WEB" && ok "菜单含「编辑」" || bad "缺编辑入口"
grep -Fq -- 'data-act="del"' "$WEB" && ok "菜单含「删除」" || bad "缺删除入口"
grep -Fq -- 'bo.disabled = !found.it.oe' "$WEB" && ok "没配外网地址时置灰（不让人点了没反应）" || bad "没配地址也亮着"
grep -Fq -- 'e.target.closest("#sections .card")' "$WEB" && ok "右键只在卡片上拦截（不吞整页右键）" || bad "整页右键都被吃掉"
grep -Fq -- 'card-edit' "$WEB" && bad "卡片铅笔又回来了（编辑入口应统一走右键）" || ok "卡片铅笔已移除，编辑只走右键菜单"
grep -Fq -- 'cardDrag.holdTimer = setTimeout' "$WEB" && ok "长按 3 秒触发卡片操作（触屏编辑入口）" || bad "触屏还是没法编辑卡片"
grep -Fq -- 'id="cardSheet"' "$WEB" && ok "长按弹窗 DOM 存在（跟右键菜单两套独立 UI）" || bad "没有长按弹窗"
grep -Fq -- 'openCardSheet(el)' "$WEB" && ok "长按调的是弹窗，不是右键菜单" || bad "长按还是弹右键菜单"
grep -Fq -- 'id="csMask"' "$WEB" && ok "长按弹窗带遮罩（模态）" || bad "弹窗没有遮罩"
grep -Fq -- 'exitDragMode({ silent: true })' "$WEB" && ok "长按弹窗先完整退出整理模式（不留虚线圈）" || bad "长按后拖拽模式残留，页面上还有「拖到这里」"
grep -Fq -- 'if (!hasData && sec.parentNode) sec.parentNode.removeChild(sec);' "$WEB" && ok "退出时回收临时插入的空分组节点" || bad "退出后页面上还挂着空分组空壳"
grep -Fq -- '}, 2550);' "$WEB" && ok "长按阈值 = 450 抖动 + 2550 = 3000ms（3 秒才进编辑，防误触）" || bad "长按阈值不是 3 秒"
grep -Fq -- 'if (cardDrag.holdTimer) { clearTimeout(cardDrag.holdTimer); cardDrag.holdTimer = null; }' "$WEB" && ok "一开始拖拽就作废长按菜单（两条路不打架）" || bad "拖拽和长按菜单会同时触发"
python3 - "$WEB" <<'PYCHK'
import sys
src = open(sys.argv[1], encoding='utf-8').read()
i = src.find('id="ctxMenu"')
j = src.find('<script>', i) if i > 0 else -1
if i < 0 or j < 0 or i > j:
    print("BAD: ctxMenu 在 script 之后，绑定会拿不到元素")
    sys.exit(1)
print("OK")
PYCHK
[ "$?" = "0" ] && ok "菜单 DOM 在脚本之前（绑定能拿到元素）" || bad "菜单 DOM 在脚本之后，事件绑定会失败"

echo "== 3.28 壁纸持久化 + 时钟城市名（2026-11-19）"
grep -Fq -- 'if (found >= 0) state.wpi = found;' "$WEB" && ok "壁纸按日期找回索引（刷新不回退）" || bad "壁纸刷新后会变回第一张"
grep -Fq -- 'state.wallDate = w.d || ""' "$WEB" && ok "启动时从 localStorage 恢复壁纸日期" || bad "没记录选的是哪张壁纸"
python3 - "$WEB" <<'PYCHK'
import sys
src = open(sys.argv[1], encoding='utf-8').read()
i = src.find('function paintWallpaper')
if i < 0:
    print("BAD: 找不到 paintWallpaper")
    sys.exit(1)
seg = src[i:i + 3000]
# 关键：必须有「按日期找回索引」那段；单纯出现 state.wpi = 0 是越界保护，合法
if 'if (found >= 0) state.wpi = found;' not in seg:
    print("BAD: paintWallpaper 里没有按日期找回索引，刷新必回退")
    sys.exit(1)
print("OK")
PYCHK
[ "$?" = "0" ] && ok "paintWallpaper 按日期找回索引（这是回退的根因）" || bad "paintWallpaper 还是把 wpi 清零"
grep -Fq -- 'flex:0 0 auto;' "$WEB" && ok "时钟城市名不收缩（不会被挤成「宁……」）" || bad "城市名还会被压扁"

echo "== 3.29 天气缺测值 + 壁纸缩略图（2026-11-19）"
grep -Fq -- 'func cnTemp(' internal/weather/weather.go && ok "温度做物理范围校验（中国天气网用 999 表示缺测）" || bad "999 会被当真实温度显示"
grep -Fq -- 'id="wallThumbs"' "$WEB" && ok "壁纸缩略图面板 DOM 存在" || bad "没有缩略图面板"
grep -Fq -- 'function paintWallThumbs' "$WEB" && ok "缩略图渲染函数存在" || bad "缩略图没接"
grep -Fq -- 'it.dataset.i == null' "$WEB" && ok "点缩略图能切到那一张" || bad "缩略图点了没反应"

echo "== 3.30 壁纸面板 hover 死区 + 日期重复（2026-11-19）"
grep -Fq -- 'position:absolute;top:100%;left:50%;transform:translateX(-50%);' "$WEB" && ok "缩略图面板紧贴按钮（top:100%，不留 hover 死区）" || bad "面板和按钮之间有间隙，鼠标下移会消失"
grep -Eq '^\s*\.wall-thumbs\{' "$WEB" && ok "缩略图面板样式规则存在" || bad "面板样式丢了"
grep -Fq -- '.test(_t)' "$WEB" && ok "壁纸元信息按 8 位日期判断，不重复打印日期" || bad "日期会印两遍「20261117 · 2026-11-17」"

echo "== 3.31 分组下拉（2026-11-19）"
grep -Fq -- 'id="groupPop"' "$WEB" && ok "分组下拉面板存在（自绘，不用 datalist）" || bad "没有分组下拉"
grep -Fq -- 'datalist id="groupList"' "$WEB" && bad "还在用 datalist（Chromium 里点输入框不弹）" || ok "datalist 已移除"
grep -Fq -- 'function paintGroupPop' "$WEB" && ok "分组候选从 DATA 渲染" || bad "分组列表没接数据"
grep -Fq -- 'inp.value = it.dataset.g;' "$WEB" && ok "点选项能填进输入框" || bad "点选项没反应"

echo "== 3.32 弹层统一暗玻璃（2026-11-19）"
grep -Fq -- 'html:not([data-wall="none"]) .themepop,' "$WEB" && ok "弹层选择器组存在（设置/对话框/菜单/下拉一起）" || bad "弹层还是白底，跟暗色面板一明一暗"
python3 - "$WEB" <<'PYCHK'
import sys
src = open(sys.argv[1], encoding='utf-8').read()
i = src.find('所有浮层统一暗玻璃')
if i < 0:
    print("BAD: 找不到统一暗玻璃那段")
    sys.exit(1)
seg = src[i:i + 3000]
need = ['.themepop', '.sheet', '.dlg-box', '.sug', '.engmenu', '.iconpop', '.toast', '.ctx', '.cs', '.sel-pop', '.wall-thumbs']
miss = [n for n in need if n not in seg]
if miss:
    print("BAD: 这些弹层没纳入统一：" + ", ".join(miss))
    sys.exit(1)
print("OK")
PYCHK
[ "$?" = "0" ] && ok "11 类弹层全部纳入统一配色（含待收录抽屉）" || bad "有弹层漏在统一之外"
grep -Fq -- '.cs-cancel{' "$WEB" && ok "取消按钮也改了（不再用浅灰底）" || bad "取消按钮还是浅灰底"

grep -Fq -- '--accent:#5f83ff;' "$WEB" && ok "暗底弹层里的蓝色提亮（设置按钮/管理员/管理›）" || bad "蓝色在暗玻璃上还是看不清"
grep -Fq -- '--accent-ink:#9cb4ff;' "$WEB" && ok "蓝字用浅蓝 #9cb4ff（暗底可读）" || bad "蓝字还是深蓝 #2645c9"

grep -Fq -- '.tp-logout{color:#ff9a80}' "$WEB" && ok "退出登录橙色提亮（暗底上跳得出来）" || bad "退出登录还是暗橙 #c2410c"
grep -Fq -- '.ctx-item.danger{color:#ff9a80}' "$WEB" && ok "删除类危险色一并提亮" || bad "删除按钮还是暗红"

grep -Fq -- '首屏防闪' "$WEB" && ok "首屏内联脚本提前写回 data-wall（防先亮后暗）" || bad "刷新还会先闪一下亮色版本"
python3 - "$WEB" <<'PYCHK'
import sys
src = open(sys.argv[1], encoding='utf-8').read()
i = src.find('首屏防闪')
j = src.find('<style>', 0)
if i < 0:
    print("BAD: 找不到首屏防闪脚本")
    sys.exit(1)
if j > 0 and i > j:
    print("BAD: 脚本在 <style> 之后，CSS 已经先渲染了亮色")
    sys.exit(1)
print("OK")
PYCHK
[ "$?" = "0" ] && ok "内联脚本在样式表之前（浏览器首绘前就生效）" || bad "脚本来不及，还是会闪"

grep -Fq -- '侧栏收起状态提前到首帧' "$WEB" && ok "侧栏收放状态首帧就位（不再先展开后收起）" || bad "刷新时侧栏会先展开再收起"
grep -Fq -- '#wallPhoto{background-image:url' "$WEB" && ok "壁纸在解析阶段就开始下载（不等 applyWall）" || bad "壁纸要等 JS 跑完才拉，明显延迟"

grep -Fq -- 'function bindMaskClose' "$WEB" && ok "遮罩关闭要求按下+抬起都在遮罩上（全选文字划出去不会误关）" || bad "拖选文字划到遮罩上松开，弹窗会被关掉"
grep -Fq -- 'e.target === editEl) closeEdit()' "$WEB" && bad "编辑弹窗还留着旧的 click 关闭" || ok "编辑弹窗已改用 bindMaskClose"
python3 - "$WEB" <<'PYCHK'
import sys, re
src = open(sys.argv[1], encoding='utf-8').read()
# 旧写法：任何 "if (e.target === this) closeXxx()" 或 "e.target === xxxEl) xxx.classList.remove"
bad = re.findall(r'if \(e\.target === this\)', src) + re.findall(r'e\.target === \w+El\) \w+\.classList\.remove', src)
if bad:
    print(f"BAD: 还有 {len(bad)} 处旧的点遮罩写法，会误关弹窗")
    sys.exit(1)
n = len(re.findall(r'bindMaskClose\(', src)) - 1   # 减去定义本身
if n < 6:
    print(f"BAD: 只有 {n} 个弹窗用了 bindMaskClose，应该 >= 6")
    sys.exit(1)
print("OK")
PYCHK
[ "$?" = "0" ] && ok "全部弹窗统一用 bindMaskClose（逐项扫过旧写法）" || bad "还有弹窗用旧的点遮罩写法"

grep -Fq -- '留空时按站点地址抓 favicon' "$WEB" && ok "编辑弹窗的图标预览也走 favicon 抓取（不再只有首字母）" || bad "预览框跟提示文案矛盾：说留空自动抓，实际只显示首字母"
grep -Fq -- 'FAV_CACHE[siteUrl]' "$WEB" && ok "预览框复用卡片那套 favicon 缓存" || bad "预览框另起一套，会重复请求"

echo "== 3.33 favicon 抓取（2026-11-19）"
grep -Fq -- 'func (f *Fetcher) discoverIconLinks' internal/favicon/favicon.go && ok "解析首页 HTML 的 <link rel=icon>（SPA 的 hash 路径也能抓到）" || bad "只试固定路径，现代应用全 miss"
grep -Fq -- 'linkIconRe = regexp' internal/favicon/favicon.go && ok "图标链接正则存在" || bad "没有 HTML 解析"

grep -Fq -- 'id="btnRefetchIcon"' "$WEB" && ok "编辑弹窗有「重新抓取」按钮（和图标区放一起）" || bad "没有手动重抓入口"
grep -Fq -- 'func (f *Fetcher) Refresh' internal/favicon/favicon.go && ok "后端支持强制重抓（先清该站点缓存）" || bad "miss 过的站点永远抓不回来"
grep -Fq -- 'force=1' "$WEB" && ok "前端重抓带 force 参数" || bad "重抓没带 force，还是命中旧缓存"

python3 - "$WEB" <<'PYCHK'
import sys, re
src = open(sys.argv[1], encoding='utf-8').read()
m = re.search(r'\.toast-wrap\{[^}]*z-index:(\d+)', src)
d = re.search(r'\.dlg\{[^}]*z-index:(\d+)', src)
if not m or not d:
    print("BAD: 找不到 toast 或 dlg 的 z-index")
    sys.exit(1)
tz, dz = int(m.group(1)), int(d.group(1))
if tz <= dz:
    print(f"BAD: toast z-index {tz} <= 弹窗 {dz}，提示条会被遮罩压住")
    sys.exit(1)
print("OK")
PYCHK
[ "$?" = "0" ] && ok "toast 层级高于所有弹窗（提示条不会被遮罩盖住）" || bad "toast 还在弹窗下面，提示看不清"

grep -Fq -- 'id="fSize"' "$WEB" && bad "「卡片尺寸」下拉还在（原生 select 展开是白底，暗色界面上炸出一块白）" || ok "卡片尺寸下拉已移除（全局统一 1×2）"
grep -Fq -- 'var size = "normal";' "$WEB" && ok "渲染层统一 1×2（旧数据里的 compact 也收敛）" || bad "旧 compact 数据还会渲染成小方块"
# 注：编辑弹窗里是否还有原生 select，靠运行时验证（#edit select 计数 = 0），
# 静态切嵌套 HTML 容易跨到别的弹窗，误报率太高，这里不做。

# 2026-11-20 起登录卡片本身也走暗玻璃，输入框跟着变白字才正确；这条旧的「必须还原成深字」不再适用
grep -Fq -- '.login-box,  /* 登录卡片' "$WEB" && ok "登录页跟随配色（卡片暗 → 输入框白字，不再有白底白字）" || bad "登录卡片没纳入暗色组"

grep -Fq -- 'color-scheme:dark;' "$WEB" && ok "暗色弹层声明 color-scheme:dark（原生 select 弹出列表才会是暗色）" || bad "原生下拉仍是白底，白字看不见"
grep -Fq -- '#users select{' "$WEB" && ok "账号管理的裸 select 单独着色" || bad "账号管理下拉还是原生白底"

echo "== 3.34 docker 标记 + 新增条目排序（2026-11-20）"
grep -Fq -- 'id="fDocker"' "$WEB" && ok "编辑弹窗有「docker 来源」勾选框" || bad "勾选框丢了，条目无法标记来源"
grep -Fq -- 'document.getElementById("fDocker").checked' "$WEB" && ok "勾选框回填 + 保存都接了" || bad "勾选框是摆设，读了不写"
grep -Fq -- 'it.Order = maxOrder + 1' internal/store/store.go && ok "新增条目追加到分组末尾（不再挤到最前）" || bad "新条目 Order 又变回 0，会排到分组最前面"

echo "== 3.35 账号体系（2026-11-20）"
grep -Fq -- 'DefaultPassword = "admin"' internal/auth/auth.go && ok "开箱即用默认账号 admin/admin" || bad "没有默认密码常量"
grep -Fq -- 'minPassLen   = 5' internal/auth/auth.go && ok "密码最小长度 5（admin 才建得出来）" || bad "minPassLen 还是 6，默认密码创建会失败"
grep -Fq -- 'PUT /api/users/username' internal/api/api.go && ok "改用户名 API 存在" || bad "没有改用户名接口"
grep -Fq -- 'id="btnRename"' "$WEB" && ok "设置里有「改名」按钮" || bad "设置里改不了用户名"
grep -Fq -- 'func (s *Server) handleUserRename' internal/api/api.go && ok "改名走的是 Auth.Rename（不换 session，改完不用重登）" || bad "改名实现缺失"
python3 - internal/auth/auth.go <<'PYCHK'
import sys
src = open(sys.argv[1], encoding='utf-8').read()
i = src.find('// 已有账号 → 什么都不做')
if i < 0:
    print("BAD: 找不到「已有账号不动密码」那段")
    sys.exit(1)
seg = src[i:i + 400]
if 'SetPassword' in seg:
    print("BAD: 已有账号时还在重置密码，界面改密会被环境变量覆盖")
    sys.exit(1)
print("OK")
PYCHK
[ "$?" = "0" ] && ok "已有账号不再被环境变量重置密码（界面改密才算数）" || bad "环境变量仍在覆盖管理员密码"

grep -Fq -- '.login-box,  /* 登录卡片' "$WEB" && ok "登录卡片纳入暗玻璃组（退出登录后配色跟着走）" || bad "暗色模式下登录卡片还是白的，跟内部割裂"
grep -Fq -- '#users select{' "$WEB" && ok "账号管理下拉的暗色规则在（曾被我误删过）" || bad "账号管理下拉又变白底了"
python3 - "$WEB" <<'PYCHK'
import sys
src = open(sys.argv[1], encoding="utf-8").read()
i = src.find("<style>"); j = src.find("</style>")
css = src[i:j]
if css.count("{") != css.count("}"):
    print(f"BAD: CSS 大括号不平衡 {css.count('{')} vs {css.count('}')}")
    sys.exit(1)
print("OK")
PYCHK
[ "$?" = "0" ] && ok "CSS 大括号平衡（防跨段删除删坏）" || bad "CSS 括号不平衡"

grep -Fq -- 'var Version = "dev"' internal/api/api.go && ok "版本号源码兜底 dev（发版才注入，不再每次手改）" || bad "版本号兜底值被改回具体数字了"
grep -Fq -- 'id="sideVer"' "$WEB" && ok "侧栏底部有版本号元素" || bad "没有版本号显示位"
grep -Fq -- '.app.side-min .side-foot{display:none}' "$WEB" && ok "收起侧栏时整个 side-foot 隐藏（版本号一起藏）" || bad "收起态版本号还露着"

echo "== 3.36 滚动重叠 + 手机顶部 + 弹层穿透（2026-11-20）"
grep -Fq -- '让位全高（2026-11-20 方案 A）' "$WEB" && ok "卡片区让位全高（卡片滚到时钟卡位置不会重叠）" || bad "还是只让前几组，滚动会切卡"
grep -Fq -- '.searchwrap{flex:1 1 100%;max-width:none;order:3' "$WEB" && ok "手机端搜索框独占一行（标题不再压上去）" || bad "手机顶部还是挤成一行"
python3 - "$WEB" <<'PYCHK'
import sys, re
src = open(sys.argv[1], encoding="utf-8").read()
i = src.find('点面板外面 = 只关面板')
if i < 0:
    print("BAD: 找不到设置弹层的点击关闭逻辑")
    sys.exit(1)
seg = src[i:i + 700]
if 'stopPropagation' not in seg or '}, true)' not in seg:
    print("BAD: 设置弹层仍是冒泡阶段关闭，会穿透到底下的卡片")
    sys.exit(1)
print("OK")
PYCHK
[ "$?" = "0" ] && ok "设置弹层点外部不穿透（捕获阶段 + stopPropagation）" || bad "点面板外会触发卡片跳转"

grep -Fq -- '#sections .grid{grid-template-columns:repeat(2,minmax(0,1fr));grid-auto-rows:74px' "$WEB" && ok "手机端卡片两列（不再是单列拉满）" || bad "手机上还是一列铺满，浪费横向空间"
grep -Fq -- '#sections .card .card-top{gap:9px;padding-right:15px}' "$WEB" && ok "手机卡片给状态点留了 15px（长名字不顶到绿点）" || bad "长名字会撞到状态点"

echo "== 4. 仓库卫生"
forbid 'data:image/jpeg'                              '无内嵌位图'
forbid 'data:image/webp'                              '无内嵌位图'
# docs/screenshots 是 README 引用的演示截图（已压成 jpg，单张 <250KB）—— 属文档资产，放行。
# 其它位置出现图片仍然报警：.gitignore 只排了 /data/，壁纸缓存漏到别处会被这条抓住。
imgs=$(find . \( -name '*.jpg' -o -name '*.png' -o -name '*.webp' \) 2>/dev/null | grep -v '^./data' | grep -v '^./docs/screenshots' | head -5 || true)
[ -z "$imgs" ] && ok "仓库无图片文件" || bad "仓库里出现图片: $imgs"
if grep -rn -- '^ *|' --include='*.md' . 2>/dev/null | grep -q .; then
  bad "文档里出现了 Markdown 表格（排版铁律禁止）"
else
  ok "文档无 Markdown 表格"
fi

echo "== 5. 后端"
export PATH="$HOME/.toolchain/go/bin:$PATH"
export GOPATH="$HOME/.toolchain/gopath" GOCACHE="$HOME/.toolchain/gocache"
export GOPROXY=https://goproxy.cn,direct CGO_ENABLED=0

# ---------- 下方卡片（widget）: 日历 / 下载 ----------
need 'id="widgetSlot"' "时钟卡下方有 widget 槽位"
need 'id="wgList"' "设置里有「下方卡片」排布列表"
need '{ k: "calendar"' "卡片列表有日历"
need '{ k: "downloads"' "卡片列表有下载"
need 'class="wg-li-up"' "每张卡有上移箭头"
need 'class="wg-li-dn"' "每张卡有下移箭头"
need 'function paintWgList' "列表选中态同步函数"
need 'if (next) paintWgList()' "打开设置时同步排布（不然永远显示旧顺序）"
need 'body: JSON.stringify({ widgets: list })' "提交整个有序数组（单个 key 存不下多卡片）"
need 'function saveWidgets' "排布改动立即存盘"
need 'var lastWgKeys = []' "多卡渲染状态"
need 'd.className = "wg-holder"' "每张卡独立容器"
need 'slot.querySelector('"'"'[data-wg="'"'"' + k + '"'"'"]'"'"')' "按 key 找各自容器渲染"
need 'wgKeys().indexOf("downloads") < 0' "下载卡不在列表里时停轮询"
need '"↑ " + au.v + " " + au.u' "累计上传箭头后有空格（跟任务行对齐）"
need '"↓ " + ad.v + " " + ad.u' "累计下载箭头后有空格"
grep -Fq 'Widgets []string' internal/store/store.go && ok "Settings 有 Widgets 数组" || bad "缺 Widgets 数组"
grep -Fq 'func sanitizeWidgets' internal/store/store.go && ok "有白名单过滤" || bad "缺 sanitizeWidgets"
grep -Fq 'if len(d.Settings.Widgets) == 0 && d.Settings.Widget != ""' internal/store/store.go && ok "老数据迁移到数组" || bad "老 widget 设置会丢"

# ---------- 主滚动条：静止隐藏 ----------
need '.scroll.is-scrolling{scrollbar-color:var(--line-2) transparent}' "滚动时才显形（Firefox 分支）"
need '.scroll.is-scrolling::-webkit-scrollbar-thumb{background-color:var(--line-2)}' "滚动时才显形（WebKit 分支）"
need 'function bindScrollFade' "滚动事件绑定"
need '}, 700);' "停手 700ms 淡回去"
need 'scrollbar-width:thin;scrollbar-color:transparent transparent' "静止态完全透明"

# ---------- 下载器 ----------
[ -f internal/downloader/downloader.go ] && ok "downloader 包存在（qb/tr 走后端代理）" || bad "internal/downloader/ 不见了"
# Go 侧的断言不能走 need()（它只搜前端）
ST=internal/store/store.go
DLG=internal/downloader/downloader.go
grep -Fq 'Widgets []string' "$ST" && ok "Settings 有 Widgets 数组" || bad "Settings 缺 Widgets"
grep -Fq 'Downloaders []Downloader' "$ST" && ok "Settings 有 Downloaders 字段" || bad "Settings 缺 Downloaders"
grep -Fq 'func MaskDownloaders' "$ST" && ok "有密码脱敏函数" || bad "缺 MaskDownloaders"
grep -Fq 'func (s *Store) ClearWidget' "$ST" && ok "有单独的清空方法（SetSettings 空字段不改）" || bad "缺 ClearWidget"
grep -Fq 'Pass == "" && old[' "$ST" && ok "后端空密码沿用旧值（前端拿不到旧密码）" || bad "空密码会覆盖成空 —— 改个地址密码就丢了"
grep -Fq 'func friendlyErr' "$DLG" && ok "net 原始报错翻成人话（不然卡片被撑成好几行）" || bad "缺 friendlyErr"
grep -Fq 'authFailLimit = 2' "$DLG" && ok "认证失败熔断阈值 2（qb 默认错 5 次封 IP 一小时）" || bad "没有熔断，密码填错 75 秒就把 IP 封死"
grep -Fq 'func FetchForce' "$DLG" && ok "手动检测能绕过熔断" || bad "手动检测也被熔断挡住，改完配置试不了"
grep -Fq '"封禁"' "$DLG" && ok "「IP 被封禁」也计入熔断" || bad "被封禁还继续撞，封禁时间会一直续"
grep -Fq 'downloader.Test(c)' internal/api/api.go && ok "检测连接走 Test(→FetchForce)" || bad "检测连接没走绕行通道"
grep -Fq 'MaskDownloaders' internal/api/api.go && ok "api 层出货前脱敏" || bad "api.go 没调 MaskDownloaders —— 密码会跟着 settings 一起吐给前端"
need 'id="dlDlg"' "下载器配置弹窗"
need 'id="wgDlAdd"' "卡片上有「新增」按钮（不必非点齿轮）"
need 'id="wgDlSet"' "卡片上有齿轮（管理）"
need 'function renderDownloads' "下载卡片渲染函数"
need 'for (var ci = 0; ci < cfg.length; ci++)' "卡片遍历所有下载器，不只画第一台"
need 'class="wg-n"' "多台时显示「N 台」"
need 'data-edit=' "已配置列表能「改」（不用删了重加）"
need '留空 = 沿用原密码' "编辑时提示密码留空不改"

# ---------- CSS 大括号必须平衡 ----------
if [ -f "$WEB" ]; then
  op=$(grep -o '{' "$WEB" | wc -l); cl=$(grep -o '}' "$WEB" | wc -l)
  [ "$op" -eq "$cl" ] && ok "CSS 大括号平衡（$op/$cl）" || bad "CSS 大括号不平衡（{ $op / } $cl）—— 跨段删除又切多了"
fi

# ---- 日历改造（2026-11-20）：配色变量化 / 月份切换 / 农历 ----
grep -Fq -- '--danger:#d93a2b;' "$WEB" && ok "主题有 --danger 变量" || bad "缺 --danger 变量"
grep -Fq -- '--warn:#a86410;' "$WEB" && ok "主题有 --warn 变量" || bad "缺 --warn 变量"
# 2026-11-20 第二轮：日历卡片改成和左侧卡片同款「暗玻璃 + 白字」，
# 卡片内部色全部走 --wg-* 变量（壁纸模式白字发亮色，纯色模式覆盖成深字）
grep -Fq 'background:rgba(20,26,40,.26);' "$WEB" && ok "日历卡片与左侧卡片同款薄暗玻璃" || bad "日历卡片底色没对齐左侧"
grep -Fq -- '--wg-danger:#ff8095;' "$WEB" && ok "暗底假日用亮红" || bad "暗底假日色不对"
grep -Fq -- '--wg-warn:#ffc078;' "$WEB" && ok "暗底调休用亮橙" || bad "暗底调休色不对"
grep -Fq '.cal-d.off{color:var(--wg-danger)' "$WEB" && ok "假日配色走卡片变量" || bad "假日还是写死色值"
grep -Fq '.cal-d.work{color:var(--wg-warn)' "$WEB" && ok "调休配色走卡片变量" || bad "调休还是写死色值"
grep -Fq 'html[data-wall="none"] .wg{' "$WEB" && ok "纯色模式单独反色回浅底深字" || bad "纯色模式没覆盖"
grep -Fq -- '--wg-ink:#101420;' "$WEB" && ok "纯色模式文字变量已反转" || bad "纯色模式文字没反转"
# 底部假日明细：同名连续日期并区间，不再一天一行
grep -Fq 'g.n === x.n && !!g.work === !!x.work && x.d === g.to + 1' "$WEB" && ok "同名连续假日并成区间" || bad "假日还是逐日列出"
grep -Fq 'g.from + "-" + g.to + " 日"' "$WEB" && ok "区间显示为 1-7 日 形式" || bad "区间格式不对"
# 只查真正的渲染代码，注释里提到这句是解释为什么删，不算
if grep -Fq '<span><i>本月还有 ' "$WEB"; then bad "「本月还有 N 天」误导文案还在"; else ok "已删除「本月还有 N 天」误导文案"; fi
grep -Fq 'lu = info.o ? info.n : "班";' "$WEB" && ok "调休格子标「班」而非农历" || bad "调休格子还是农历"
grep -Fq 'var CAL = { off: 0, span: 24 }' "$WEB" && ok "日历有月份偏移状态" || bad "缺 CAL.off"
grep -Fq 'function bindCalNav(el)' "$WEB" && ok "月份切换已绑定" || bad "缺 bindCalNav"
grep -Fq 'id="calPrev"' "$WEB" && grep -Fq 'id="calNext"' "$WEB" && ok "左右箭头都在" || bad "缺月份箭头"
grep -Fq 'touchstart' "$WEB" && grep -Fq 'touchend' "$WEB" && ok "支持左右滑动切月" || bad "缺滑动手势"
grep -Fq 'CAL.off === 0 ? now.getDate() : -1' "$WEB" && ok "非本月不误标今天" || bad "今天高亮没做月份隔离"
grep -Fq 'CAL.off === 0 && k < 400' "$WEB" && ok "倒数只在本月算" || bad "倒数没跟月份挂钩"

# ---- 农历 ----
grep -Fq 'var LUNAR_INFO = [0x04bd8' "$WEB" && ok "农历年表内嵌（1900 起）" || bad "缺农历年表"
grep -Fq 'function solarToLunar' "$WEB" && ok "公历转农历函数在" || bad "缺 solarToLunar"
grep -Fq 'function lunarLabel' "$WEB" && ok "农历显示文案函数在" || bad "缺 lunarLabel"
grep -Fq 'if (isLeap && lm === leap + 1) isLeap = false;' "$WEB" && ok "闰月只扣减一次（2025 整段算对）" || bad "闰月处理回归成无条件重置"
grep -Fq 'window.__solarToLunar' "$WEB" && bad "临时调试钩子没删" || ok "无临时调试钩子"
grep -Fq 'class="cal-lu"' "$WEB" && ok "格子里有农历小字" || bad "缺 .cal-lu 渲染"
grep -Fq '.cal-lu{' "$WEB" && ok ".cal-lu 有样式" || bad "缺 .cal-lu 样式"

echo "── 下载器卡片与配置弹窗 ──"
# 下拉选项必须块级：button 默认 inline-block，两个选项会并排（2026-11-20 踩过）
grep -Fq 'display:block;width:100%;text-align:left;' "$WEB" && ok "下拉选项是块级（不会并排）" || bad "下拉选项没设 display:block，QB/TR 会并排"
grep -Fq 'html:not([data-wall="none"]) .sel-pop{' "$WEB" && ok "下拉面板暗色底" || bad "下拉面板暗色底缺失，会是白底白字"
# 浮层暗色是一整个选择器列表共享一套变量，中间插规则会把列表截断，
# 列表里所有浮层（.dlg-box / .toast / .ctx …）就只剩背景没有白字变量。
# 2026-11-20 踩过：插 .sel-pop 暗底插错位置，弹窗全变深底深字。
grep -Fq 'html:not([data-wall="none"]) .cs,
html:not([data-wall="none"]) .sel-pop,
html:not([data-wall="none"]) .wall-thumbs{' "$WEB" && ok "浮层变量选择器列表完整" || bad "浮层选择器列表被截断了"
grep -Fq 'html:not([data-wall="none"]) .dlg-hint{color:rgba(255,255,255,.68)}' "$WEB" && ok "弹窗说明文字是白字" || bad "弹窗说明文字会变黑"
grep -Fq 'html:not([data-wall="none"]) .dlg-box .fld > span{color:rgba(255,255,255,.68)}' "$WEB" && ok "弹窗字段标签是白字" || bad "弹窗字段标签会变黑"
grep -Fq 'background:rgba(26,32,46,.97);' "$WEB" && ok "下拉/弹窗暗底写死" || bad "缺暗底下拉"
# 保存后必须关弹窗
grep -Fq 'closeDlDlg();' "$WEB" && ok "保存后关弹窗" || bad "保存后没关弹窗"
grep -Fq 'var wasEdit = !!id;' "$WEB" && ok "编辑态标记在 reset 前存下" || bad "wasEdit 取值顺序错"
# 已保存条目：两行结构 + 图标按钮
grep -Fq 'class="ci-main"' "$WEB" && ok "已保存条目两行结构" || bad "缺 .ci-main"
grep -Fq 'class="ci-acts"' "$WEB" && ok "已保存条目右侧图标组" || bad "缺 .ci-acts"
grep -Fq '.ci-main{flex:1;min-width:0}' "$WEB" && ok "条目主区可收缩（地址不被挤没）" || bad "缺 min-width:0"
grep -Fq 'id="dlSaved"' "$WEB" && ok "已保存块有独立容器" || bad "缺 dlSaved 容器"
# 已保存列表在保存按钮之后，不再杵在表单顶部
awk '/id="dlCfg"/{c=NR} /id="dlSave"/{s=NR} END{exit !(c>s)}' "$WEB" && ok "已保存列表在表单下面" || bad "已保存列表还在表单上面"
# 卡片内容
grep -Fq 'class="dl-tk' "$WEB" && ok "卡片有任务进度条" || bad "缺任务进度条"
grep -Fq 'class="dl-tot"' "$WEB" && ok "卡片有累计流量（挪到速率行右端）" || bad "缺累计流量"
grep -Fq 'dl-foot' "$WEB" && bad "dl-foot 应已删除（累计不再单独占一行）" || ok "底部累计行已移除"
grep -Fq '剩 ' "$WEB" && bad "卡片仍在显示磁盘剩余（顶部宿主机信息已有）" || ok "卡片不再重复显示磁盘剩余"
grep -Fq 'class="dl-idle">空闲' "$WEB" && ok "速率为 0 显示空闲" || bad "速率 0 没改成空闲"
grep -Fq '.dl-top{display:flex;justify-content:flex-start' "$WEB" && ok "状态点+名字+计数同一行" || bad "名字行没重排"
grep -Fq 'function fmtBytes' "$WEB" && ok "容量格式化函数在" || bad "缺 fmtBytes"
grep -Fq '.dl-tk.seed .dl-bar' "$WEB" && ok "做种条样式在" || bad "缺做种条样式"

# Go 侧：新字段真的解析了
grep -Fq 'AllDL    int64  `json:"allDl,omitempty"`' internal/downloader/downloader.go && ok "Stats 有累计下载" || bad "缺 AllDL"
grep -Fq 'json:"alltime_dl"' internal/downloader/downloader.go && ok "qb 读 alltime_dl" || bad "qb 没读 alltime_dl"
grep -Fq 'json:"free_space_on_disk"' internal/downloader/downloader.go && ok "qb 读磁盘剩余" || bad "qb 没读磁盘剩余"
grep -Fq 'json:"cumulative-stats"' internal/downloader/downloader.go && ok "tr 读累计流量" || bad "tr 没读累计"
grep -Fq '"free-space"' internal/downloader/downloader.go && ok "tr 读磁盘剩余" || bad "tr 没读磁盘"
grep -Fq 'func pickTasks' internal/downloader/downloader.go && ok "任务挑选函数在" || bad "缺 pickTasks"
grep -Fq 'if seen[t.Name] {' internal/downloader/downloader.go && ok "同名任务去重" || bad "任务没去重"

go vet ./... >/dev/null 2>&1 && ok "go vet 干净" || bad "go vet 有报错"

if out=$(go test ./... 2>&1); then
  ok "go test 全过（$(echo "$out" | grep -c '^ok') 个包）"
else
  bad "go test 失败"; echo "$out" | grep -E '^(FAIL|---)' | head -5
fi

if go build -trimpath -ldflags="-s -w" -o /tmp/justnav-selfcheck . >/dev/null 2>&1; then
  ok "编译通过（$(ls -lh /tmp/justnav-selfcheck | awk '{print $5}')）"
  rm -f /tmp/justnav-selfcheck
else
  bad "编译失败"
fi

echo
[ "$FAIL" -eq 0 ] && echo "全部通过 ✅" || echo "有失败项 ❌"
exit "$FAIL"
