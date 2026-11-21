// Package svgscan 判断一段 SVG 里有没有会在浏览器里执行的内容。
//
// 为什么需要单独一个包：JustNav 有两条路径会把第三方 SVG 以**同源**方式
// 发给浏览器 ——
//
//	/api/icons/get   图标库代理（CDN 下载后缓存）
//	/api/favicon     站点图标抓取（从目标网站抓 <link rel="icon">）
//
// 两条路径的威胁完全一样：谁把那个 URL 直接打开，SVG 就在本站源下当文档
// 渲染，里面的脚本能读 cookie、调写接口。检查逻辑必须一致，所以抽出来。
//
// 调用方还要在 HTTP 层再套一层 sandbox CSP（见 api 包的 handleIconGet /
// handleFavicon）—— 这里是黑名单，不是消毒器，不能当唯一边界。
package svgscan

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

// badElements 是 SVG 里能挂脚本或加载外部内容的元素。
// 图标只需要图形元素，这几个出现就直接拒。
var badElements = map[string]bool{
	"script":           true,
	"foreignobject":    true, // 能塞整段 HTML
	"iframe":           true,
	"embed":            true,
	"object":           true,
	"handler":          true, // SVG Tiny 的事件元素
	"set":              true,
	"animate":          true, // 配合 onbegin 用
	"animatemotion":    true,
	"animatetransform": true,
}

// MaxSize 是允许检查的 SVG 上限，超过一律当危险。
// 图标不该这么大；放这么大的文件进来只会拖慢解析。
const MaxSize = 64 << 10

// Unsafe 判断 SVG 字节流里有没有可执行内容。
//
// 用 encoding/xml 真解析一遍，按 token 看元素名和属性名 —— 这点很重要：
// 字符串黑名单会被 `onload = "..."` / `onload\t=` / `onload\n=` 绕过，
// 因为 XML 规范允许属性名和等号之间有空白，解析器照样认它是 onload 属性。
//
// 判定规则：
//   - 元素：命中 badElements 直接拒
//   - 属性：任何 on* 开头（事件处理器）拒；href/src/from/to/values/begin
//     的值以 javascript: / data: / vbscript: 开头拒；style 含 url( 或
//     expression 拒
//   - XML 指令：<!DOCTYPE svg ...> 这类纯声明放行（正经图标常带），但
//     内部子集 [ <!ENTITY ...> ] 一律拒 —— XXE 和实体爆破的入口
//   - 解析不了的字节流按危险处理（调用方另有长度/空判断）
func Unsafe(b []byte) bool {
	if len(b) > MaxSize {
		return true
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	// 常见 HTML 实体（&nbsp; 等）放行，别让正常图标因为实体解析失败被拒
	dec.Entity = xml.HTMLEntity

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return true // 解析不了的一律拒，宁可回退
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if badElements[strings.ToLower(t.Name.Local)] {
				return true
			}
			for _, a := range t.Attr {
				name := strings.ToLower(a.Name.Local)
				val := strings.ToLower(strings.TrimSpace(a.Value))
				if strings.HasPrefix(name, "on") {
					return true
				}
				switch name {
				case "href", "src", "from", "to", "values", "begin":
					if strings.HasPrefix(val, "javascript:") ||
						strings.HasPrefix(val, "data:") ||
						strings.HasPrefix(val, "vbscript:") {
						return true
					}
				case "style":
					if strings.Contains(val, "url(") || strings.Contains(val, "expression") {
						return true
					}
				}
			}
		case xml.Directive:
			d := strings.ToLower(string(t))
			if strings.Contains(d, "entity") || strings.Contains(d, "[") {
				return true
			}
		}
	}
}

// CSP 是给 SVG 响应用的沙箱策略：即使黑名单漏过，脚本也执行不了。
// style-src 放开内联是为了让图标自己的样式生效。
const CSP = "sandbox; default-src 'none'; script-src 'none'; object-src 'none'; style-src 'unsafe-inline'"
