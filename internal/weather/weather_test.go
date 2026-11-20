package weather

import "testing"

func TestCodeText(t *testing.T) {
	cases := map[int]string{
		0: "晴", 2: "多云", 3: "阴", 45: "雾",
		61: "小雨", 65: "大雨", 71: "小雪", 80: "阵雨", 95: "雷阵雨",
	}
	for code, want := range cases {
		if got := CodeText(code); got != want {
			t.Errorf("code %d → %q, want %q", code, got, want)
		}
	}
	if CodeText(999) != "未知" {
		t.Error("未知代码应返回「未知」")
	}
}

func TestEmptyCityRejected(t *testing.T) {
	c := New()
	if _, err := c.Geocode(nil, ""); err == nil {
		t.Error("空城市名应报错")
	}
}
