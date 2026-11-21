# JustNav Logo V3 ·「入口 / Portal」

这是一套**与之前 J 形路径完全不同**的品牌构思：三个深色圆角方块代表聚合在导航页里的服务卡片；右上角不再做第四张卡片，而是变成一支跳出网格的珊瑚橙箭头，象征“从首页前往你的服务”。

设计语言是**温暖的极简主义**：不使用渐变、立体高光或复杂阴影。

## 视觉元素

- 背景奶油白：`#F7F4EF`
- 服务卡片深炭色：`#25333A`
- 导航箭头珊瑚橙：`#ED6D54`
- 深色背景字标橙：`#FF937A`
- 品牌字标全部小写：`justnav`，与此前大小写混排的设计形成区别。

## 文件清单

| 文件 | 用途 |
| --- | --- |
| `logo-horizontal.svg` | 浅色背景主 Logo |
| `logo-horizontal-dark.svg` | 深色背景主 Logo |
| `logo-mark.svg` | 正方形项目头像、Docker Hub、GitHub |
| `logo-1024.png` | 高清项目图标（1024×1024） |
| `favicon.svg` | 浏览器标签页矢量图标 |
| `favicon.ico` | 含 16、32、48px 多尺寸兼容图标 |
| `favicon-16.png`、`favicon-32.png`、`favicon-48.png` | 固定尺寸 PNG |
| `apple-touch-icon.png` | iOS 主屏图标（180×180） |
| `logo-horizontal-preview.png`、`logo-horizontal-dark-preview.png` | 横版预览 PNG |

## 网页接入

将 favicon 文件复制到网站静态资源目录，然后在页面 `<head>` 中引用：

```html
<link rel="icon" href="/assets/favicon.svg" type="image/svg+xml">
<link rel="icon" href="/assets/favicon.ico" sizes="any">
<link rel="apple-touch-icon" href="/assets/apple-touch-icon.png">
```

JustNav 使用 Go embed 静态前端时，需确认 `web/assets/` 在 embed 文件列表中，并且 `/assets/` 路由能返回对应文件。

## 重新导出

编辑 SVG 后，安装 Node.js 和 `sharp` (`npm install sharp`)，运行：

```bash
node render_assets.cjs
```

所有图形由几何构成，不包含第三方品牌图形；尚未进行正式商标近似检索。
