// Package store 是导航条目的单文件存储。
//
// 为什么用 JSON 文件而不是数据库：
//   - 条目量级几十到几百条，JSON 完全够
//   - 可读、可 git diff、备份就是拷文件、导出就是它本身
//   - 没有 CGO、没有外部依赖，符合单二进制目标
//
// 写入必须原子：先写同目录临时文件 → fsync → rename。
// 直接覆盖原文件在掉电/被 kill 时会留下半截 JSON。
package store

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const schemaVersion = 1

// Item 是一个导航条目。Internal/External 两个地址是核心能力：
// 同一个服务在内网和外网各有一个入口，由全局开关决定用哪个。
type Item struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Internal string `json:"internal"`
	External string `json:"external,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Tag      string `json:"tag,omitempty"`
	// Description 卡片描述：空则由前端自动取（docker 用镜像名，网站用域名）
	Description string `json:"description,omitempty"`
	// Size 卡片档位：compact(1×1) / normal(1×2)，空按 normal
	Size string `json:"size,omitempty"`
	// 排序权重，越小越靠前；同权重按 Name 排
	Order int `json:"order"`
	// 来源：manual = 人工添加，docker = 从容器标签收录
	Source string `json:"source,omitempty"`
	// Hidden 收进「隐藏区」：不显示在导航页，但数据还在，设置里可以恢复。
	// 用途：Docker 里那些没有前端的容器（数据库、worker、一次性任务）不该占首页位置，
	// 又不想每次都从「待收录」列表里重新看一眼。
	Hidden bool `json:"hidden,omitempty"`
}

// Engine 是一个搜索引擎。URL 里用 %s 代表关键词占位。
type Engine struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Settings struct {
	// NetMode: "in" 用内网地址，"out" 用外网地址
	NetMode string `json:"netMode"`
	Theme   string `json:"theme"`
	Wall    string `json:"wall"`
	// WallTone: crisp（壁纸全清晰，卡片实底）/ balanced / glass（整屏毛玻璃）
	WallTone string `json:"wallTone"`
	// 搜索：当前引擎 + 引擎列表
	EngineID string   `json:"engineId"`
	Engines  []Engine `json:"engines"`
	// 天气小组件：城市名 + 经纬度（空表示不显示）
	Weather WeatherSetting `json:"weather"`
	// GroupIcons 用户手动指定的分组图标：{分组名: 图标key}
	// 没指定的分组由前端按名字猜，猜不到用中性图标（不用哈希随机，免得两个组撞图标）
	GroupIcons map[string]string `json:"groupIcons,omitempty"`
	// Widgets 时钟卡下方要显示哪些卡片，数组顺序 = 从上往下的排布顺序。
	// 空数组 = 都不显示。白名单见 widgetAllowed。
	Widgets []string `json:"widgets,omitempty"`
	// Deprecated: 老的单值字段，只在 Load 里把旧数据迁进 Widgets，不再写入。
	Widget string `json:"widget,omitempty"`
	// Downloaders 下载器列表（qb / tr）。密码只存这儿，出货给前端时一律抹掉。
	Downloaders []Downloader `json:"downloaders,omitempty"`
}

// Downloader 一台下载器的连接配置。
//   Type: qb = qBittorrent（.41:8085）、tr = Transmission（.42:9091）
//   Pass: 明文存 data.json 里（本机文件，0600）。GET 接口永远不回它。
type Downloader struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Type string `json:"type"`
	URL  string `json:"url"`
	User string `json:"user,omitempty"`
	Pass string `json:"pass,omitempty"`
}

// ClearWidget 关掉时钟卡下方的 widget 槽位（设置里选「不显示」时走这里）。
// 单独开个方法是因为 SetSettings 的语义是「空字段不改」，清空得另走一条路。
func (s *Store) ClearWidget() error {
	s.mu.Lock()
	s.data.Settings.Widgets = nil
	s.data.Settings.Widget = ""
	s.mu.Unlock()
	return s.Save()
}

// widgetAllowed 白名单：不认的 key 直接丢掉，免得前端塞个乱七八糟的字符串
// 进来之后渲染出个空槽位（以后加天气/备忘录卡片时往这里补）。
var widgetAllowed = map[string]bool{
	"calendar":  true,
	"downloads": true,
	"weather":   true, // 5 天预报 + 今天详情（数据来自 TWC，见 internal/weather/forecast.go）
}

// sanitizeWidgets 过滤白名单 + 去重，保持传入顺序。
func sanitizeWidgets(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, k := range in {
		if !widgetAllowed[k] || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// MaskDownloaders 出货给前端前把密码抹成空串（长度信息也不给，直接空）。
func MaskDownloaders(in []Downloader) []Downloader {
	if in == nil {
		return nil
	}
	out := make([]Downloader, len(in))
	for i, d := range in {
		d.Pass = ""
		out[i] = d
	}
	return out
}

type WeatherSetting struct {
	City string  `json:"city"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
}

type Data struct {
	Version  int      `json:"version"`
	Groups   []string `json:"groups"`
	Items    []Item   `json:"items"`
	Settings Settings `json:"settings"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	data Data
}

func New(dir string) *Store {
	return &Store{path: filepath.Join(dir, "nav.json")}
}

func (s *Store) Path() string { return s.path }

// Load 读取数据文件。文件不存在时初始化为一个可用的空库（不报错）。
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.data = defaultData()
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 %s: %w", s.path, err)
	}
	var d Data
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("解析 %s: %w", s.path, err)
	}
	if d.Version == 0 {
		d.Version = schemaVersion
	}
	// 旧数据迁移：只有一个 Widget 单值，搬进有序数组（2026-11-20 支持多卡片）
	if len(d.Settings.Widgets) == 0 && d.Settings.Widget != "" && d.Settings.Widget != "none" {
		d.Settings.Widgets = sanitizeWidgets([]string{d.Settings.Widget})
	}
	d.Settings.Widget = ""   // 迁完就清掉，之后只认 Widgets

	if d.Settings.NetMode == "" {
		d.Settings.NetMode = "in"
	}
	if d.Groups == nil {
		d.Groups = defaultData().Groups
	}
	// 老数据没有这些字段：补默认值，别让前端拿到空
	if d.Settings.WallTone == "" {
		d.Settings.WallTone = "crisp"
	}
	if len(d.Settings.Engines) == 0 {
		d.Settings.Engines = DefaultEngines()
	}
	if d.Settings.EngineID == "" {
		d.Settings.EngineID = d.Settings.Engines[0].ID
	}
	s.data = d
	return nil
}

// Save 原子写入。
func (s *Store) Save() error {
	s.mu.RLock()
	payload, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	return atomicWrite(s.path, payload)
}

// Snapshot 返回深拷贝，避免调用方改到内部状态。
func (s *Store) Snapshot() Data {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d := s.data
	d.Groups = append([]string(nil), s.data.Groups...)
	d.Items = append([]Item(nil), s.data.Items...)
	d.Settings = s.data.Settings.clone()
	return d
}

// clone 复制 Settings 里的引用类型（slice / map）。
//
// 光靠 `out := st` 只是浅拷贝：Engines、Widgets、Downloaders 三个 slice 和
// GroupIcons 这个 map 复制走的只是 header，底层数据仍跟内部状态共用一份。
// 调用方拿着 Snapshot() 在锁外遍历 GroupIcons 的同时，SetSettings 在锁内
// 写同一个 map —— Go 运行时会直接 fatal error: concurrent map read and map
// write，整个进程挂掉，不是脏数据那么简单。
func (st Settings) clone() Settings {
	out := st
	if st.Engines != nil {
		out.Engines = append([]Engine(nil), st.Engines...)
	}
	if st.Widgets != nil {
		out.Widgets = append([]string(nil), st.Widgets...)
	}
	if st.Downloaders != nil {
		out.Downloaders = append([]Downloader(nil), st.Downloaders...)
	}
	if st.GroupIcons != nil {
		out.GroupIcons = make(map[string]string, len(st.GroupIcons))
		for k, v := range st.GroupIcons {
			out.GroupIcons[k] = v
		}
	}
	return out
}

// List 返回按 order、name 排序后的条目副本。
func (s *Store) List() []Item {
	d := s.Snapshot()
	sort.SliceStable(d.Items, func(i, j int) bool {
		if d.Items[i].Order != d.Items[j].Order {
			return d.Items[i].Order < d.Items[j].Order
		}
		return d.Items[i].Name < d.Items[j].Name
	})
	return d.Items
}

// Upsert 新增或按 ID 更新一条。
// DefaultGroup 是没有分组的条目落到的地方。
//
// ⚠️ 必须同时把「未分组」写进 Groups 列表，否则前端按 groups 渲染时
// 条目会存在但永远不显示 —— 2026-11 实测踩过：从「待收录」收进来的容器
// 没有 nav.group 标签，group 是空串，收录后界面上什么都没出现。
const DefaultGroup = "未分组"

// sanitizeURL 只放行 http/https，以及站内相对路径。
//
// 为什么必须在后端做：导航地址可以由用户手填、也可以由 Docker 的 nav.url 标签提供。
// 前端虽然也会挡，但前端校验永远可以被绕过（直接打 API），所以后端必须自己判一道。
// 典型攻击是 javascript: 开头 —— 点一下卡片就在当前页面执行脚本。
//
// 返回空串表示「这个地址不能用」，调用方决定是丢弃整条还是只清空这个字段。
func sanitizeURL(u string) string {
	s := strings.TrimSpace(u)
	if s == "" {
		return ""
	}
	// 先去掉所有控制字符和空白再判协议，防 "java\nscript:" 和 " javascript:" 这类绕过
	probe := strings.Map(func(r rune) rune {
		if r <= 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	low := strings.ToLower(probe)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return s
	}
	// 站内相对路径 / 锚点 / 查询串也允许。
	// ★ 但 "//host/x" 要挡住 —— 浏览器会把它当成协议相对地址解析成 https://host/x，
	//   等于绕过了「只允许站内路径」这条规则。站内路径必须是单个斜杠开头。
	if strings.HasPrefix(s, "//") {
		return ""
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "#") || strings.HasPrefix(s, "?") {
		return s
	}
	return ""
}

// sanitizeItem 清掉不能用的地址。
//
// 注意这里【不强制】Internal 非空 —— 单条编辑允许先建条目后补地址，
// 原来就是这个行为。要求「必须有可用地址」是 BulkUpsert（批量导入）的规则，
// 由调用方自己判。
func sanitizeItem(it *Item) {
	it.Internal = sanitizeURL(it.Internal)
	it.External = sanitizeURL(it.External)
}

func (s *Store) Upsert(it Item) error {
	sanitizeItem(&it)
	if it.Group == "" {
		it.Group = DefaultGroup
	}
	if it.ID == "" {
		if it.Name == "" {
			return errors.New("条目缺少 name 与 id")
		}
		it.ID = slugify(it.Name)
	}
	s.mu.Lock()
	found := false
	for i := range s.data.Items {
		if s.data.Items[i].ID == it.ID {
			if it.Source == "" {
				it.Source = s.data.Items[i].Source
			}
			if it.Order == 0 {
				it.Order = s.data.Items[i].Order
			}
			s.data.Items[i] = it
			found = true
			break
		}
	}
	if !found {
		// 2026-11-20 新增条目排到末尾（波哥要求：新卡片应出现在对应分组最后，而不是最前）。
		// 原来新条目 Order 默认 0，而 List() 按 Order 升序，导致它一路挤到分组最前面。
		// 这里取全局最大 Order + 1；调用方显式给了 Order（导入等）就尊重原值。
		if it.Order == 0 {
			maxOrder := -1
			for _, x := range s.data.Items {
				if x.Order > maxOrder {
					maxOrder = x.Order
				}
			}
			it.Order = maxOrder + 1
		}
		s.data.Items = append(s.data.Items, it)
	}
	// 分组必须出现在 Groups 里，前端才渲染得到
	if !contains(s.data.Groups, it.Group) {
		s.data.Groups = append(s.data.Groups, it.Group)
	}
	s.mu.Unlock()
	return s.Save()
}

// Delete 按 ID 删除。
func (s *Store) Delete(id string) (bool, error) {
	s.mu.Lock()
	idx := -1
	for i := range s.data.Items {
		if s.data.Items[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return false, nil
	}
	s.data.Items = append(s.data.Items[:idx], s.data.Items[idx+1:]...)
	s.mu.Unlock()
	return true, s.Save()
}

// SetSettings 局部更新设置（空字段不改）。
func (s *Store) SetSettings(patch Settings) error {
	s.mu.Lock()
	if patch.NetMode != "" {
		s.data.Settings.NetMode = patch.NetMode
	}
	if patch.Theme != "" {
		s.data.Settings.Theme = patch.Theme
	}
	if patch.Wall != "" {
		s.data.Settings.Wall = patch.Wall
	}
	if patch.WallTone != "" {
		s.data.Settings.WallTone = patch.WallTone
	}
	if patch.EngineID != "" {
		s.data.Settings.EngineID = patch.EngineID
	}
	// 分组图标：按 key 合并，值为空串表示恢复自动（删掉这条覆盖）
	if patch.GroupIcons != nil {
		if s.data.Settings.GroupIcons == nil {
			s.data.Settings.GroupIcons = map[string]string{}
		}
		for g, ic := range patch.GroupIcons {
			if ic == "" {
				delete(s.data.Settings.GroupIcons, g)
			} else {
				s.data.Settings.GroupIcons[g] = ic
			}
		}
	}
	// 引擎列表整体替换（前端编辑完一次性提交），但不允许空列表
	if patch.Engines != nil && len(patch.Engines) > 0 {
		s.data.Settings.Engines = patch.Engines
	}
	// 城市名非空就整体替换（清空则关闭天气）
	if patch.Weather.City != "" || patch.Weather.Lat != 0 {
		s.data.Settings.Weather = patch.Weather
	}
	// widget 列表：整体替换。传 []（非 nil 空数组）= 全部关掉。
	if patch.Widgets != nil {
		s.data.Settings.Widgets = sanitizeWidgets(patch.Widgets)
	}
	// 下载器：整体替换。前端提交时旧密码会回传空串，这里按 ID 继承旧密码，
	// 免得用户改个名字就得把密码重打一遍。
	if patch.Downloaders != nil {
		old := map[string]string{}
		for _, d := range s.data.Settings.Downloaders {
			old[d.ID] = d.Pass
		}
		for i := range patch.Downloaders {
			if patch.Downloaders[i].Pass == "" && old[patch.Downloaders[i].ID] != "" {
				patch.Downloaders[i].Pass = old[patch.Downloaders[i].ID]
			}
		}
		s.data.Settings.Downloaders = patch.Downloaders
	}
	s.mu.Unlock()
	return s.Save()
}

// ---------- 分组管理 ----------
//
// 分组是「导航结构」而不是「条目属性」：它可以增、改名、调整顺序、删除。
// 删除时如果组里还有条目，必须显式指定迁移目标，绝不静默丢数据。

const MaxGroupName = 24

func cleanGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("分组名不能为空")
	}
	if utf8.RuneCountInString(name) > MaxGroupName {
		return "", fmt.Errorf("分组名不能超过 %d 个字", MaxGroupName)
	}
	return name, nil
}

func (s *Store) AddGroup(name string) error {
	clean, err := cleanGroupName(name)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if contains(s.data.Groups, clean) {
		s.mu.Unlock()
		return fmt.Errorf("分组「%s」已存在", clean)
	}
	s.data.Groups = append(s.data.Groups, clean)
	s.mu.Unlock()
	return s.Save()
}

// RenameGroup 改名会同步更新该组下所有条目，保证不出现「孤儿分组」。
func (s *Store) RenameGroup(from, to string) error {
	cleanFrom, err := cleanGroupName(from)
	if err != nil {
		return err
	}
	cleanTo, err := cleanGroupName(to)
	if err != nil {
		return err
	}
	if cleanFrom == cleanTo {
		return nil
	}

	s.mu.Lock()
	idx := -1
	for i, g := range s.data.Groups {
		if g == cleanFrom {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("分组「%s」不存在", cleanFrom)
	}
	if contains(s.data.Groups, cleanTo) {
		s.mu.Unlock()
		return fmt.Errorf("分组「%s」已存在", cleanTo)
	}
	s.data.Groups[idx] = cleanTo
	for i := range s.data.Items {
		if s.data.Items[i].Group == cleanFrom {
			s.data.Items[i].Group = cleanTo
		}
	}
	s.mu.Unlock()
	return s.Save()
}

// DeleteGroup 删除空分组；组里还有条目时必须给 moveTo，把条目迁过去。
// 返回被迁移的条目数。
func (s *Store) DeleteGroup(name, moveTo string) (int, error) {
	clean, err := cleanGroupName(name)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()

	idx := -1
	for i, g := range s.data.Groups {
		if g == clean {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return 0, fmt.Errorf("分组「%s」不存在", clean)
	}

	var count int
	for i := range s.data.Items {
		if s.data.Items[i].Group == clean {
			count++
		}
	}
	if count > 0 {
		target, err := cleanGroupName(moveTo)
		if err != nil {
			s.mu.Unlock()
			return 0, fmt.Errorf("组里还有 %d 个条目，需要先指定迁移到哪个分组", count)
		}
		if target == clean {
			s.mu.Unlock()
			return 0, errors.New("迁移目标不能是被删除的分组本身")
		}
		if !contains(s.data.Groups, target) {
			s.data.Groups = append(s.data.Groups, target)
		}
		for i := range s.data.Items {
			if s.data.Items[i].Group == clean {
				s.data.Items[i].Group = target
			}
		}
	}

	s.data.Groups = append(s.data.Groups[:idx], s.data.Groups[idx+1:]...)
	s.mu.Unlock()
	return count, s.Save()
}

// SetGroupOrder 按给定顺序重排，未提及的分组按原相对顺序追加到末尾。
func (s *Store) SetGroupOrder(order []string) error {
	s.mu.Lock()
	seen := map[string]bool{}
	out := make([]string, 0, len(s.data.Groups))
	for _, g := range order {
		if contains(s.data.Groups, g) && !seen[g] {
			out = append(out, g)
			seen[g] = true
		}
	}
	for _, g := range s.data.Groups {
		if !seen[g] {
			out = append(out, g)
		}
	}
	s.data.Groups = out
	s.mu.Unlock()
	return s.Save()
}

// Placement 是一次布局提交：条目 id + 它应该属于的分组。
// 跨组拖拽必须同时带 group，只改 order 的话条目会留在原组里。
type Placement struct {
	ID    string `json:"id"`
	Group string `json:"group,omitempty"`
}

// SetLayout 按给定顺序重排，并（若给了 group）把条目移到目标分组。
// 未提及的条目保持原相对顺序追加在末尾，绝不丢。
func (s *Store) SetLayout(ps []Placement) error {
	s.mu.Lock()
	byID := make(map[string]int, len(s.data.Items))
	for i := range s.data.Items {
		byID[s.data.Items[i].ID] = i
	}

	ordered := make([]Item, 0, len(s.data.Items))
	used := make(map[string]bool, len(ps))

	for _, p := range ps {
		idx, ok := byID[p.ID]
		if !ok || used[p.ID] {
			continue // 不认识的 id、重复的 id 直接跳过
		}
		it := s.data.Items[idx]
		if p.Group != "" {
			it.Group = p.Group
			if !contains(s.data.Groups, p.Group) {
				s.data.Groups = append(s.data.Groups, p.Group)
			}
		}
		it.Order = len(ordered)
		ordered = append(ordered, it)
		used[p.ID] = true
	}

	// 没被提及的条目按原顺序接在后面
	for _, it := range s.data.Items {
		if used[it.ID] {
			continue
		}
		it.Order = len(ordered)
		ordered = append(ordered, it)
	}

	s.data.Items = ordered
	s.mu.Unlock()
	return s.Save()
}

// SetItemOrder 兼容旧调用：只调顺序，不动分组。
func (s *Store) SetItemOrder(ids []string) error {
	ps := make([]Placement, 0, len(ids))
	for _, id := range ids {
		ps = append(ps, Placement{ID: id})
	}
	return s.SetLayout(ps)
}

// BulkUpsert 批量导入：一次加锁、一次落盘。
// 逐条 Upsert 会导致 N 次写盘（书签导入动辄几百条），这里是给导入用的。
// 返回新增/更新条数。
func (s *Store) BulkUpsert(items []Item) (int, int, error) {
	s.mu.Lock()
	added, updated := 0, 0

	for _, it := range items {
		sanitizeItem(&it)
		if it.Name == "" || it.Internal == "" {
			continue // 缺关键字段或地址不合法（被清成空），直接跳过，不写半条
		}
		if it.Group == "" {
			it.Group = DefaultGroup
		}
		if it.ID == "" {
			it.ID = slugify(it.Name)
		}
		// ID 撞了但地址不同 → 加后缀，别把两个不同服务合成一个
		if idx := s.findByID(it.ID); idx >= 0 && s.data.Items[idx].Internal != it.Internal {
			base := it.ID
			for n := 2; n < 100; n++ {
				cand := fmt.Sprintf("%s-%d", base, n)
				if s.findByID(cand) < 0 {
					it.ID = cand
					break
				}
			}
		}

		if idx := s.findByID(it.ID); idx >= 0 {
			it.Order = s.data.Items[idx].Order // 保原位置，不因为导入打乱已有顺序
			s.data.Items[idx] = it
			updated++
			continue
		}
		it.Order = len(s.data.Items)
		s.data.Items = append(s.data.Items, it)
		if !contains(s.data.Groups, it.Group) {
			s.data.Groups = append(s.data.Groups, it.Group)
		}
		added++
	}
	s.mu.Unlock()
	return added, updated, s.Save()
}

// findByID 调用方必须已持有锁
func (s *Store) findByID(id string) int {
	for i := range s.data.Items {
		if s.data.Items[i].ID == id {
			return i
		}
	}
	return -1
}

// DefaultEngines 新账号默认两个搜索引擎（用户可增删改）
func DefaultEngines() []Engine {
	return []Engine{
		{ID: "baidu", Name: "百度", URL: "https://www.baidu.com/s?wd=%s"},
		{ID: "google", Name: "谷歌", URL: "https://www.google.com/search?q=%s"},
	}
}

func defaultData() Data {
	return Data{
		Version: schemaVersion,
		// 新账号不预置任何分组：以前这里写死了作者的四个分组（影音媒体/下载与存储/
		// 家庭与网络/开发工具），开源出去等于把作者的习惯当成所有人的默认值，
		// 新用户进来看到四个空分类更困惑。分组由用户添加服务时自己建。2026-11-20
		Groups: []string{},
		Items:  []Item{},
		Settings: Settings{
			NetMode:  "in",
			Theme:    "glass",
			Wall:     "none",
			WallTone: "crisp",
			EngineID: "baidu",
			Engines:  DefaultEngines(),
		},
	}
}

func atomicWrite(path string, payload []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".nav-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后这里必然失败，无害

	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// 0600：nav.json 的 Settings.Downloaders 里有下载器的明文密码，
	// 同机其他普通用户不该读到。
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func slugify(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		case r == ' ' || r == '_':
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		// ⚠️ 中文名会被上面全部过滤掉。这里【不能】用时间戳做兜底 ——
		// 那样同一个名字每次生成不同 ID，导入去重完全失效（重复导入就重复插入）。
		// 改成对名字取哈希：同名必同 ID，稳定可去重。
		sum := sha1.Sum([]byte(s))
		return "item-" + hex.EncodeToString(sum[:6])
	}
	return string(out)
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
