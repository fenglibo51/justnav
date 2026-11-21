package icons

import "testing"

// SVG 是唯一会被浏览器当「文档」渲染的图标格式，也是最容易被塞脚本的载体。
// 这个白名单校验是唯一一道闸：筛漏了 = /api/icons/get 变成存储型 XSS 入口。
func TestSVGUnsafeRejectsExecutableContent(t *testing.T) {
	bad := []struct {
		name string
		src  string
	}{
		{"script 标签", `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`},
		{"onload 属性", `<svg onload="alert(1)"></svg>`},
		{"onload 与等号之间有空格", `<svg xmlns="http://www.w3.org/2000/svg" onload = "alert(1)"></svg>`},
		{"onload 与等号之间有 Tab", "<svg onload	=\"alert(1)\"></svg>"},
		{"onload 与等号之间换行", "<svg onload\n=\"alert(1)\"></svg>"},
		{"属性名和值之间多个空格", `<svg onload   =   "alert(1)"></svg>`},
		{"onerror 属性", `<svg><image href="x" onerror="alert(1)"/></svg>`},
		{"大小写混淆", `<svg><image href="x" OnErRoR="alert(1)"/></svg>`},
		{"javascript 伪协议", `<svg><a xlink:href="javascript:alert(1)"><text>x</text></a></svg>`},
		{"foreignObject", `<svg><foreignObject><body onload="alert(1)"/></foreignObject></svg>`},
		{"iframe", `<svg><iframe src="//evil.example"></iframe></svg>`},
		{"XML 实体展开", `<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg>&x;</svg>`},
		{"onbegin 动画事件", `<svg><animate onbegin="alert(1)" attributeName="x"/></svg>`},
	}
	for _, tc := range bad {
		if !svgUnsafe([]byte(tc.src)) {
			t.Errorf("%s 应被判定为危险，却放行了:\n%s", tc.name, tc.src)
		}
	}

	good := []string{
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/></svg>`,
		`<svg width="24" height="24"><circle cx="12" cy="12" r="10" fill="#fff"/></svg>`,
		// 真实的 dashboard-icons 风格：带 DOCTYPE 声明但没有内部子集
		`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd"><svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`,
		// 带 HTML 实体
		`<svg xmlns="http://www.w3.org/2000/svg"><title>a&nbsp;b</title><path d="M0 0"/></svg>`,
		// 带命名空间的 xlink:href 指向正常图片不吃拦截
		`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="#a"/></svg>`,
	}
	for _, src := range good {
		if svgUnsafe([]byte(src)) {
			t.Errorf("误杀了正常图标:\n%s", src)
		}
	}
	// 注意：任何 on* 开头的属性都会被拦 —— 包括 onclicked 这种其实不存在的
	// 名字。这是刻意的保守：真实图标不会有自定义 on* 属性，误杀的代价是
	// 回退到 png，漏放的代价是同源脚本执行。
}

// 超大文件直接拒，别让一张图把内存吃穿
func TestSVGUnsafeRejectsOversize(t *testing.T) {
	if !svgUnsafe(make([]byte, 70<<10)) {
		t.Error("超过 64KB 应判为危险")
	}
	// 正常大小的合法 SVG 不拦
	if svgUnsafe([]byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`)) {
		t.Error("正常 SVG 被拦了")
	}
	// 解析不了的字节流按危险处理（GET 那边另有 len==0 的过滤）
	if !svgUnsafe([]byte{0x00, 0x01, 0x02, 0xff}) {
		t.Error("非 XML 的二进制应判为危险")
	}
}
