package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Forecast 是右侧栏「天气卡片」要用的数据：未来几天 + 今天的详情。
type Forecast struct {
	City string     `json:"city"`
	Days []DayBrief `json:"days"`
	Now  DayDetail  `json:"now"`
	At   time.Time  `json:"at"`
}

// DayBrief 是竖排列表里的一天，只有扫一眼需要的字段。
type DayBrief struct {
	Day    string  `json:"day"`    // 今天 / 明天 / 周日
	Icon   int     `json:"icon"`   // WMO，前端 wxIcon() 直接用
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Precip int     `json:"precip"` // 降水概率 %
	Text   string  `json:"text"`   // 晴 / 大部晴朗
}

// DayDetail 是点开某一天看到的详情。
// 字段数是按"两列四行刚好填满 224px 卡高"配的 —— 只有三行时下面会空一大块。
type DayDetail struct {
	Feels    float64 `json:"feels,omitempty"`
	Humidity int     `json:"humidity,omitempty"`
	Wind     string  `json:"wind,omitempty"`
	UV       string  `json:"uv,omitempty"`
	Sunrise  string  `json:"sunrise,omitempty"`
	Sunset   string  `json:"sunset,omitempty"`
	// Visibility 能见度（km），Pressure 海平面气压（hPa）
	Visibility float64 `json:"visibility,omitempty"`
	Pressure   float64 `json:"pressure,omitempty"`
	Narrative  string  `json:"narrative,omitempty"`
}

const twcDailyURL = "https://api.weather.com/v3/wx/forecast/daily/5day"

// FetchForecast 拉 5 天预报。没有经纬度直接报错，让上层退回「只显示时钟」。
//
// TWC 返回的数组有两套下标，别搞混：
//   · dayOfWeek / calendarDayTemperatureMax / calendarDayTemperatureMin / narrative
//     是【每天一项】，从今天开始
//   · daypart[0] 里的 iconCode / precipChance / wxPhraseLong 是【白天夜间交替】，
//     index 0 常常是 null（今天白天已经过去），白天的项落在 2*i 上
// 所以取第 i 天的图标要用 iconCode[2*i]，而第 0 天直接借 current 的图标。
func (c *Client) FetchForecast(ctx context.Context, city string, lat, lon float64, nowIcon int) (Forecast, error) {
	if lat == 0 && lon == 0 {
		return Forecast{}, fmt.Errorf("没有经纬度")
	}

	q := url.Values{}
	q.Set("geocode", strconv.FormatFloat(lat, 'f', 3, 64)+","+strconv.FormatFloat(lon, 'f', 3, 64))
	q.Set("units", "m")
	q.Set("language", "zh-CN")
	q.Set("format", "json")
	q.Set("apiKey", twcAPIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, twcDailyURL+"?"+q.Encode(), nil)
	if err != nil {
		return Forecast{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "+
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := c.twc.Do(req)
	if err != nil {
		return Forecast{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Forecast{}, fmt.Errorf("TWC 预报返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return Forecast{}, err
	}

	var d struct {
		DayOfWeek []string  `json:"dayOfWeek"`
		Max       []float64 `json:"calendarDayTemperatureMax"`
		Min       []float64 `json:"calendarDayTemperatureMin"`
		Narrative []string  `json:"narrative"`
		Sunrise   []string  `json:"sunriseTimeLocal"`
		Sunset    []string  `json:"sunsetTimeLocal"`
		Daypart   []struct {
			IconCode     []*int     `json:"iconCode"`
			PrecipChance []*int     `json:"precipChance"`
			Phrase       []string   `json:"wxPhraseLong"`
			Temp         []*float64 `json:"temperature"`
			Humidity     []*int     `json:"relativeHumidity"`
			UV           []string   `json:"uvDescription"`
			WindDir      []string   `json:"windDirectionCardinal"`
			WindSpeed    []*int     `json:"windSpeed"`
		} `json:"daypart"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return Forecast{}, fmt.Errorf("TWC 预报解析失败: %w", err)
	}
	if len(d.Max) == 0 || len(d.Min) == 0 {
		return Forecast{}, fmt.Errorf("TWC 预报没有温度数据")
	}

	var dp struct {
		IconCode     []*int
		PrecipChance []*int
		Phrase       []string
		Temp         []*float64
		Humidity     []*int
		UV           []string
		WindDir      []string
		WindSpeed    []*int
	}
	if len(d.Daypart) > 0 {
		dp.IconCode = d.Daypart[0].IconCode
		dp.PrecipChance = d.Daypart[0].PrecipChance
		dp.Phrase = d.Daypart[0].Phrase
		dp.Temp = d.Daypart[0].Temp
		dp.Humidity = d.Daypart[0].Humidity
		dp.UV = d.Daypart[0].UV
		dp.WindDir = d.Daypart[0].WindDir
		dp.WindSpeed = d.Daypart[0].WindSpeed
	}

	pickInt := func(a []*int, i int) (int, bool) {
		if i >= 0 && i < len(a) && a[i] != nil {
			return *a[i], true
		}
		return 0, false
	}
	pickStr := func(a []string, i int) string {
		if i >= 0 && i < len(a) && a[i] != "" {
			return a[i]
		}
		return ""
	}

	fc := Forecast{City: city, At: time.Now()}
	n := len(d.Max)
	if n > 6 {
		n = 6
	}
	for i := 0; i < n; i++ {
		b := DayBrief{High: d.Max[i]}
		if i < len(d.Min) {
			b.Low = d.Min[i]
		}
		// 周一那一项是「星期几」，第 0 天换成更好懂的说法
		switch i {
		case 0:
			b.Day = "今天"
		case 1:
			b.Day = "明天"
		case 2:
			b.Day = "后天"
		default:
			b.Day = pickStr(d.DayOfWeek, i)
		}
		// 白天图标在 2*i 上；第 0 天用当前实况的图标（今天白天已经过去了）
		if i == 0 {
			b.Icon = nowIcon
		} else if v, ok := pickInt(dp.IconCode, 2*i); ok {
			b.Icon = twcWMO(v)
		} else if v, ok := pickInt(dp.IconCode, 2*i-1); ok {
			b.Icon = twcWMO(v)
		}
		if v, ok := pickInt(dp.PrecipChance, 2*i); ok {
			b.Precip = v
		} else if v, ok := pickInt(dp.PrecipChance, 2*i-1); ok {
			b.Precip = v
		}
		if i == 0 {
			b.Text = pickStr(d.Narrative, i)
			if b.Text == "" {
				b.Text = pickStr(dp.Phrase, 1)
			}
		} else {
			b.Text = pickStr(dp.Phrase, 2*i)
			if b.Text == "" {
				b.Text = pickStr(d.Narrative, i)
			}
		}
		// 描述太长塞不进 240 宽，砍到第一个句号
		b.Text = firstSentence(b.Text)
		fc.Days = append(fc.Days, b)
	}

	// 今天详情：daypart 的白天项（index 2 是明天白天，index 1 是今天夜间）
	// 这里优先用白天项，拿不到就退到任意可用项
	for _, idx := range []int{2, 1, 3} {
		if v, ok := pickInt(dp.Humidity, idx); ok {
			fc.Now.Humidity = v
			break
		}
	}
	for _, idx := range []int{1, 2} {
		if s := pickStr(dp.UV, idx); s != "" {
			fc.Now.UV = s
			break
		}
	}
	for _, idx := range []int{1, 2} {
		if d2 := pickStr(dp.WindDir, idx); d2 != "" {
			sp, _ := pickInt(dp.WindSpeed, idx)
			if sp > 0 {
				fc.Now.Wind = d2 + " " + strconv.Itoa(sp) + "km/h"
			} else {
				fc.Now.Wind = d2
			}
			break
		}
	}
	if i := len(d.Sunrise); i > 0 {
		fc.Now.Sunrise = clockOf(d.Sunrise[0])
	}
	if i := len(d.Sunset); i > 0 {
		fc.Now.Sunset = clockOf(d.Sunset[0])
	}
	if len(d.Narrative) > 0 {
		fc.Now.Narrative = firstSentence(d.Narrative[0])
	}
	return fc, nil
}

// firstSentence 截到第一个句号/换行：右侧栏一行放不下整段叙述。
func firstSentence(s string) string {
	if s == "" {
		return ""
	}
	rs := []rune(s)
	for i, r := range rs {
		if r == '。' || r == '\n' || r == '；' {
			return string(rs[:i])
		}
		if i > 22 {
			return string(rs[:22]) + "…"
		}
	}
	return s
}

// clockOf 从 "2026-11-20T06:23:08+0800" 里取 "06:23"。
func clockOf(s string) string {
	if len(s) < 16 {
		return ""
	}
	return s[11:16]
}