package weather

import "testing"

// cnTemp 是「中国天气网温度字段 → float」的唯一入口，它的误判会直接反映到界面上。
// 2026-11-20 实测：北京 tempn 是 "0"，而旧实现里 `v == 0` 被判成缺测，
// 结果当天最低温被退回成当前温度，界面显示「-0.3° / -0.3°」。
func TestCnTemp(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
		why  string
	}{
		{"0", 0, true, "0°C 是合法温度，不能被当成缺测"},
		{"0.0", 0, true, "同上，带小数点的 0"},
		{"-0.6", -0.6, true, "零下小数值"},
		{"12.5", 12.5, true, "普通温度"},
		{"-15", -15, true, "零下整数"},
		{"999", 0, false, "中国天气网的缺测标记"},
		{"-999", 0, false, "缺测标记的负数形式"},
		{"", 0, false, "字段为空"},
		{"abc", 0, false, "不是数字"},
		{"60", 0, false, "超出物理范围（上界）"},
		{"-60", 0, false, "超出物理范围（下界）"},
	}
	for _, c := range cases {
		got, ok := cnTemp(c.in)
		if ok != c.ok {
			t.Errorf("cnTemp(%q) ok = %v, want %v（%s）", c.in, ok, c.ok, c.why)
			continue
		}
		if ok && got != c.want {
			t.Errorf("cnTemp(%q) = %v, want %v（%s）", c.in, got, c.want, c.why)
		}
	}
}
