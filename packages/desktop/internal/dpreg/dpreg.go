// Package dpreg 是 1:N 多数据面的本地注册表。
//
// ## 设计要点
//
// 每个数据面是**独立 Cloudflare 账号**下的一个 Worker，字段互不混用：
// 凭据（Account ID / API Token / KV）仍由 store.Profile 保管（secrets.json，0600），
// 这里只用 ProfileID 引用，**不复制任何密钥**，也不会合并成一份全局 CONFIG。
//
// 注册表只描述「这个数据面是谁、域名/UUID/路径是什么、最近一次推送结果」。
// 其中渲染订阅需要的那部分（域名 / UUID / 路径）会同步到控制面，
// Account ID / Token / KV 永远不会离开本机。
package dpreg

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// Filename 是注册表文件名（与 scheduled-tasks.json 同目录）。
	Filename = "dataplanes.json"
	Schema   = 1
	// MaxPlanes 与内核的 DP_MAX_COUNT 保持一致。
	MaxPlanes = 16
	// MaxLog 是每个数据面保留的推送记录条数。
	MaxLog = 20
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// PushRecord 是一次推送的结果记录（每个数据面各自独立）。
type PushRecord struct {
	At       int64  `json:"at"`
	Reason   string `json:"reason"`   // scan / schedule / add / manual
	Mode     string `json:"mode"`     // append / replace / remove
	Status   string `json:"status"`   // ok / failed / skipped
	Message  string `json:"message"`  // 失败原因或说明
	Checked  int    `json:"checked"`  // 参与重新验证的 IP 数
	Valid    int    `json:"valid"`    // 通过验证的 IP 数
	Added    int    `json:"added"`    // 实际新增（去重后）
	Pool     int    `json:"pool"`     // 推送后该数据面池内条数
	Attempts int    `json:"attempts"` // 尝试次数（含重试）
}

// DataPlane 是一个数据面。
type DataPlane struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// ProfileID 引用该数据面所属账号（store.Profile）。每个数据面各用各的账号。
	ProfileID  string `json:"profileId"`
	WorkerName string `json:"workerName"`
	Domain     string `json:"domain"`

	UUID           string `json:"uuid"`
	WSPath         string `json:"wsPath"`
	TrojanPassword string `json:"trojanPassword"`
	TrojanPath     string `json:"trojanPath"`
	XHTTPPath      string `json:"xhttpPath"`

	Enabled bool `json:"enabled"`
	// AutoPush 为 false 时，扫描后的自动推送会跳过它（仍可手动推送）。
	AutoPush bool `json:"autoPush"`

	KernelVersion string `json:"kernelVersion"`
	LastDeployAt  string `json:"lastDeployAt"`

	CreatedAt int64        `json:"createdAt"`
	UpdatedAt int64        `json:"updatedAt"`
	Log       []PushRecord `json:"log"`
}

// Settings 是 1:N 全局推送设置。
type Settings struct {
	// ControlProfileID 指定控制面所在账号；空 = 当前活跃账号。
	ControlProfileID string `json:"controlProfileId"`
	// PushAfterAdd：新增数据面后立即推送当前优选结果（默认开）。
	PushAfterAdd bool `json:"pushAfterAdd"`
	// PushAfterScan：每次优选成功后自动推送到各数据面（默认开）。
	PushAfterScan bool `json:"pushAfterScan"`
	// Mode：自动推送的写入方式，append（默认）/ replace。
	Mode string `json:"mode"`
	// Top：每个数据面每次推送的 IP 数上限（默认 15）。
	Top int `json:"top"`
}

// FixedDomains 是固定默认优选域名。
//
// 必须与内核 packages/worker-core/src/data/pools.ts 的 FIXED_PREFERRED_DOMAINS 一致
// （dpreg_test.go 里有对照测试，改一边忘改另一边会直接失败）。
//
// 规则：它们**永远**在自定义订阅优选池里（由控制面内核保证）；这里只负责「测速并记录」，
// 测速结果不会让任何一个域名被剔除。
var FixedDomains = []string{
	"sb.mao99.eu.org",
	"sb6.mao99.eu.org",
	"sb4.mao99.eu.org",
	"vvv.me88.cloudns.org",
	"vvv4.me88.cloudns.org",
	"vvv6.me88.cloudns.org",
	"o0o.maoii.eu.org",
	"o0o4.maoii.eu.org",
	"o0o6.maoii.eu.org",
}

// FixedRecord 是某个固定域名最近一次测速记录（仅作记录）。
type FixedRecord struct {
	Domain    string   `json:"domain"`
	IPs       []string `json:"ips"`
	OK        bool     `json:"ok"`
	LatencyMs int64    `json:"latencyMs"`
	SpeedMBps float64  `json:"speedMBps"`
	Colo      string   `json:"colo"`
	Note      string   `json:"note"`
	At        int64    `json:"at"`
}

type fileData struct {
	Schema    int                    `json:"schema"`
	UpdatedAt int64                  `json:"updatedAt"`
	Settings  Settings               `json:"settings"`
	Planes    []DataPlane            `json:"planes"`
	Fixed     map[string]FixedRecord `json:"fixed,omitempty"`
}

// DefaultSettings 返回默认设置。
func DefaultSettings() Settings {
	return Settings{PushAfterAdd: true, PushAfterScan: true, Mode: "append", Top: 15}
}

func (s *Settings) normalize() {
	if s.Mode != "replace" {
		s.Mode = "append"
	}
	if s.Top <= 0 || s.Top > 200 {
		s.Top = 15
	}
}

// Store 是注册表存储（内存缓存 + 变更即原子落盘）。
type Store struct {
	mu   sync.Mutex
	dir  string
	data fileData
	ok   bool
}

// New 创建存储。
func New(dir string) *Store { return &Store{dir: dir} }

func (s *Store) path() string { return filepath.Join(s.dir, Filename) }

func (s *Store) loadLocked() {
	if s.ok {
		return
	}
	s.ok = true
	s.data = fileData{Schema: Schema, Settings: DefaultSettings()}
	raw, err := os.ReadFile(s.path())
	if err != nil {
		return
	}
	var f fileData
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	f.Settings.normalize()
	s.data = f
}

func (s *Store) saveLocked() error {
	s.data.Schema = Schema
	s.data.UpdatedAt = time.Now().UnixMilli()
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp := s.path() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

// NewID 生成数据面 id（`dp-` + 6 位十六进制，满足内核的 id 规则）。
func NewID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "dp-" + hex.EncodeToString(b)
}

// ValidID 判断 id 是否合法。
func ValidID(id string) bool { return idRe.MatchString(id) }

// List 返回全部数据面（副本）。
func (s *Store) List() []DataPlane {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]DataPlane, len(s.data.Planes))
	copy(out, s.data.Planes)
	return out
}

// Get 按 id 取一个数据面。
func (s *Store) Get(id string) (DataPlane, bool) {
	for _, dp := range s.List() {
		if dp.ID == id {
			return dp, true
		}
	}
	return DataPlane{}, false
}

// Settings 返回全局设置。
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	return s.data.Settings
}

// SetSettings 保存全局设置。
func (s *Store) SetSettings(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	v.normalize()
	s.data.Settings = v
	return s.saveLocked()
}

func cleanPath(p, def string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return def
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// NormalizeDomain 去掉协议 / 路径并转小写。
func NormalizeDomain(d string) string {
	d = strings.ToLower(strings.TrimSpace(d))
	d = strings.TrimPrefix(strings.TrimPrefix(d, "https://"), "http://")
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	return d
}

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// Validate 校验一个数据面记录。
func Validate(dp *DataPlane) error {
	dp.Domain = NormalizeDomain(dp.Domain)
	dp.Name = strings.TrimSpace(dp.Name)
	if dp.Name == "" {
		dp.Name = dp.ID
	}
	if r := []rune(dp.Name); len(r) > 24 {
		dp.Name = string(r[:24])
	}
	if !ValidID(dp.ID) {
		return fmt.Errorf("数据面 ID 不合法（只能小写字母/数字/连字符）")
	}
	if strings.TrimSpace(dp.ProfileID) == "" {
		return fmt.Errorf("请选择该数据面所属的账号")
	}
	if dp.Domain == "" || !domainRe.MatchString(dp.Domain) {
		return fmt.Errorf("数据面域名不合法")
	}
	if strings.TrimSpace(dp.UUID) == "" {
		return fmt.Errorf("缺少 UUID（可点「从部署读取」自动填入）")
	}
	dp.WSPath = cleanPath(dp.WSPath, "/")
	dp.TrojanPath = cleanPath(dp.TrojanPath, "/trojan")
	dp.XHTTPPath = cleanPath(dp.XHTTPPath, "/xhttp")
	return nil
}

// Upsert 新增或更新一个数据面。更新时保留推送记录与创建时间。
func (s *Store) Upsert(dp DataPlane) (DataPlane, error) {
	if err := Validate(&dp); err != nil {
		return dp, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	now := time.Now().UnixMilli()
	for i, cur := range s.data.Planes {
		if cur.ID == dp.ID {
			dp.CreatedAt = cur.CreatedAt
			dp.Log = cur.Log
			dp.UpdatedAt = now
			s.data.Planes[i] = dp
			return dp, s.saveLocked()
		}
		// 同一账号 + 同一域名只能登记一次，避免重复推送、订阅里出现重复节点
		if cur.ProfileID == dp.ProfileID && cur.Domain == dp.Domain {
			return dp, fmt.Errorf("该账号下已登记过域名 %s（%s）", dp.Domain, cur.Name)
		}
	}
	if len(s.data.Planes) >= MaxPlanes {
		return dp, fmt.Errorf("数据面数量已达上限 %d", MaxPlanes)
	}
	dp.CreatedAt, dp.UpdatedAt = now, now
	s.data.Planes = append(s.data.Planes, dp)
	return dp, s.saveLocked()
}

// Delete 删除一个数据面。
func (s *Store) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	for i, cur := range s.data.Planes {
		if cur.ID == id {
			s.data.Planes = append(s.data.Planes[:i], s.data.Planes[i+1:]...)
			return true, s.saveLocked()
		}
	}
	return false, nil
}

// AddRecord 追加一条推送记录（只动这一个数据面，互不影响）。
func (s *Store) AddRecord(id string, rec PushRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	for i := range s.data.Planes {
		if s.data.Planes[i].ID != id {
			continue
		}
		l := append(s.data.Planes[i].Log, rec)
		if len(l) > MaxLog {
			l = l[len(l)-MaxLog:]
		}
		s.data.Planes[i].Log = l
		return s.saveLocked()
	}
	return nil
}

// SetFixed 保存固定域名的最近测速记录（整体覆盖，只作记录）。
func (s *Store) SetFixed(recs map[string]FixedRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	s.data.Fixed = recs
	return s.saveLocked()
}

// FixedRecords 返回固定域名的最近测速记录（按 FixedDomains 顺序，没测过的也占位）。
func (s *Store) FixedRecords() []FixedRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]FixedRecord, 0, len(FixedDomains))
	for _, d := range FixedDomains {
		if r, ok := s.data.Fixed[d]; ok {
			out = append(out, r)
		} else {
			out = append(out, FixedRecord{Domain: d, Note: "尚未测速"})
		}
	}
	return out
}
