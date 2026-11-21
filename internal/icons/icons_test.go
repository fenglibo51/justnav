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
		// 属性名里带 "on" 但不是事件处理器，不能误杀
		`<svg><rect onclicked="no" width="1" height="1"/></svg>`,
	}
	for _, src := range good {
		if svgUnsafe([]byte(src)) {
			t.Errorf("误杀了正常图标:\n%s", src)
		}
	}
}

// 超大文件直接拒，别让一张图把内存吃穿
func TestSVGUnsafeRejectsOversize(t *testing.T) {
	if !svgUnsafe(make([]byte, 70<<10)) {
		t.Error("超过 64KB 应判为危险")
	}
	if svgUnsafe(make([]byte, 1<<10)) {
		t.Error("1KB 空内容不该被判危险")
	}
}
