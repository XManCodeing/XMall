package gormstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	c "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/contract"
	d "github.com/XManCodeing/XMall/internal/modules/telegram/recovery/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Store 实现统一任务持久化。时钟由构造器注入，不在事务中读取外部配置或调用网络。
type Store struct {
	db  *gorm.DB
	now func() time.Time
}

// New 接收数据库并返回使用系统时钟的仓储；迁移由宿主显式调用 Models。
func New(db *gorm.DB) *Store { return NewWithClock(db, time.Now) }

// NewWithClock 接收数据库与可信时钟，返回可测试仓储；nil 时钟使用系统时钟。
func NewWithClock(db *gorm.DB, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

// Models 无参数，返回迁移所需模型；本包不自动迁移宿主数据库。
func Models() []any { return []any{&d.Task{}, &d.ScopeSlot{}, &d.Step{}} }

// randomRef 无参数，返回 256 位随机引用或熵源错误，不包含业务身份。
func randomRef() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("recovery reference: %w", err)
	}
	return hex.EncodeToString(data[:]), nil
}

// mapped 将数据库未找到转换为端口错误，其他数据库错误保持可诊断信息。
func mapped(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return c.ErrNotFound
	}
	return err
}

// cas 返回写入错误；非单行写入返回版本冲突。
func cas(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return c.ErrStale
	}
	return nil
}

// lockTask 用 tx 锁定 id 对应父任务，返回行或错误；SQLite 单连接也不会触碰全局句柄。
func lockTask(tx *gorm.DB, id uint) (*d.Task, error) {
	var task d.Task
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, id).Error
	return &task, mapped(err)
}

// snapshotKeys 校验并规范化输入的资源集合，返回唯一锁键与错误。
// 输入切片复制后排序；禁止空 scope 和非规范 ID，避免相同资源绕过唯一占用。
func snapshotKeys(snapshot *d.Snapshot) ([]string, error) {
	groups := []struct {
		name   string
		values *[]string
	}{
		{"target", &snapshot.Scope.TargetIDs}, {"bot", &snapshot.Scope.BotIDs}, {"chat", &snapshot.Scope.ChatIDs},
		{"account", &snapshot.Scope.AccountIDs}, {"telegram", &snapshot.Scope.TelegramIDs},
	}
	keys := make([]string, 0)
	for _, group := range groups {
		values := append([]string(nil), (*group.values)...)
		sort.Strings(values)
		unique := make([]string, 0, len(values))
		for _, value := range values {
			if !validResourceID(group.name, value) {
				return nil, c.ErrInvalid
			}
			if len(unique) > 0 && unique[len(unique)-1] == value {
				continue
			}
			unique = append(unique, value)
			keys = append(keys, group.name+":"+value)
		}
		*group.values = unique
	}
	if len(keys) == 0 || len(keys) > 256 {
		return nil, c.ErrInvalid
	}
	for _, endpoint := range []d.Endpoint{snapshot.Old, snapshot.New} {
		if endpoint.TelegramID < 0 || (endpoint.ChatID != 0 && !contains(keys, "chat:"+strconv.FormatInt(endpoint.ChatID, 10))) {
			return nil, c.ErrInvalid
		}
		resources := map[string]uint64{"target": uint64(endpoint.TargetID), "bot": uint64(endpoint.BotID), "account": uint64(endpoint.AccountID), "telegram": uint64(endpoint.TelegramID)}
		for kind, id := range resources {
			if id != 0 && !contains(keys, kind+":"+strconv.FormatUint(id, 10)) {
				return nil, c.ErrInvalid
			}
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// validResourceID 校验 kind 对应的十进制 ID；chat 可为负，其余资源必须为正。
func validResourceID(kind, value string) bool {
	if kind == "chat" || kind == "telegram" {
		number, err := strconv.ParseInt(value, 10, 64)
		return err == nil && number != 0 && (kind == "chat" || number > 0) && strconv.FormatInt(number, 10) == value
	}
	number, err := strconv.ParseUint(value, 10, 64)
	return err == nil && number > 0 && strconv.FormatUint(number, 10) == value
}

// contains 检查 values 是否含 exact value，返回布尔结果。
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// validKind 检查 kind 是否是已支持父任务类型。
func validKind(kind d.Kind) bool {
	return kind == d.GroupReplace || kind == d.BotReplace || kind == d.IdentityTransfer
}

// validState 检查 state 是否属于持久状态白名单。
func validState(state d.State) bool {
	return contains([]string{string(d.AwaitingConfirmation), string(d.Fencing), string(d.Switching), string(d.Recovering), string(d.NeedsReview), string(d.PartialFailed), string(d.Completed), string(d.Cancelled)}, string(state))
}

// codeValid 限制内部分类/key 的字符与长度，返回是否可安全持久化，不接受 URL/原始响应。
func codeValid(value string, max int) bool {
	if value == "" || len(value) > max {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("_-.:", char)) {
			return false
		}
	}
	return true
}

// prepare 校验 input 并规范化白名单快照，返回 JSON 与幂等摘要，不保存任意扩展字段。
func prepare(input *c.PreflightInput) (string, string, error) {
	if !validKind(input.Kind) || !codeValid(input.RequestKey, 128) || input.ActorID == 0 ||
		strings.TrimSpace(input.Reason) == "" || len(input.Reason) > 512 || len(input.VerificationNote) > 1024 ||
		input.TTL < time.Second || input.TTL > time.Hour {
		return "", "", c.ErrInvalid
	}
	if _, err := snapshotKeys(&input.Snapshot); err != nil {
		return "", "", err
	}
	if input.Snapshot.Old == input.Snapshot.New {
		return "", "", c.ErrInvalid
	}
	switch input.Kind {
	case d.GroupReplace:
		if input.Snapshot.Old.TargetID == 0 || input.Snapshot.New.TargetID == 0 || input.Snapshot.Old.ChatID == 0 || input.Snapshot.New.ChatID == 0 {
			return "", "", c.ErrInvalid
		}
	case d.BotReplace:
		if input.Snapshot.Old.BotID == 0 || input.Snapshot.New.BotID == 0 || input.Snapshot.Old.BotID == input.Snapshot.New.BotID {
			return "", "", c.ErrInvalid
		}
	case d.IdentityTransfer:
		if input.Snapshot.Old.AccountID == 0 || input.Snapshot.New.AccountID == 0 || input.Snapshot.Old.TelegramID == 0 || input.Snapshot.New.TelegramID == 0 {
			return "", "", c.ErrInvalid
		}
	}
	encoded, err := json.Marshal(input.Snapshot)
	if err != nil {
		return "", "", err
	}
	request, err := json.Marshal(input)
	if err != nil {
		return "", "", err
	}
	digest := sha256.Sum256(request)
	return string(encoded), hex.EncodeToString(digest[:]), nil
}

// CreatePreflight 持久保存 input，返回唯一父任务；同 key 不同内容冲突，重放不刷新 TTL。
func (s *Store) CreatePreflight(ctx context.Context, input c.PreflightInput) (*d.Task, error) {
	snapshot, hash, err := prepare(&input)
	if err != nil {
		return nil, err
	}
	ref, err := randomRef()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	task := d.Task{Kind: input.Kind, State: d.AwaitingConfirmation, RequestKey: input.RequestKey, RequestHash: hash,
		PreflightRef: ref, SnapshotJSON: snapshot, ActorID: input.ActorID, Reason: input.Reason, VerificationNote: input.VerificationNote,
		Version: 1, ExpiresAt: now.Add(input.TTL), CreatedAt: now, UpdatedAt: now}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_key"}}, DoNothing: true}).Create(&task)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		if err := tx.Where("request_key = ?", input.RequestKey).First(&task).Error; err != nil {
			return err
		}
		if task.RequestHash != hash {
			return c.ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "recovery preflight persisted", "task_id", task.ID)
	return &task, nil
}

var _ c.Store = (*Store)(nil)
