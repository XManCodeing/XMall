package domain

import (
	"errors"
	"math"
	"time"
)

const (
	// TermMonths 使用每月固定三十天，不按自然月计算。
	TermMonths = "months"
	// TermPermanent 是独立语义，禁止使用大月份数或日期哨兵。
	TermPermanent         = "permanent"
	secondsPerMonth int64 = 30 * 24 * 60 * 60
	// MaxMonths 保证转换为 Go time.Duration 时不会溢出。
	MaxMonths int64 = math.MaxInt64 / int64(time.Second) / secondsPerMonth
)

// ErrInvalidTerm 表示类型、月份或可表示时长无效。
var ErrInvalidTerm = errors.New("invalid membership term or duration overflow")

// DurationSeconds 校验 kind/months 并返回固定秒数；永久返回零，不表示到期。
func DurationSeconds(kind string, months int64) (int64, error) {
	if kind == TermPermanent && months == 0 {
		return 0, nil
	}
	if kind != TermMonths || months <= 0 || months > MaxMonths {
		return 0, ErrInvalidTerm
	}
	return months * secondsPerMonth, nil
}

// Expiry 从可信生效时间 start 计算期限；永久返回 nil，无效日期/溢出返回错误。
// 此函数不决定 B5 的起算政策，调用者必须传入固定业务时点。
func Expiry(start time.Time, kind string, months int64) (*time.Time, error) {
	seconds, err := DurationSeconds(kind, months)
	if err != nil {
		return nil, err
	}
	if kind == TermPermanent {
		return nil, nil
	}
	if start.IsZero() || start.Year() < 1 || start.Year() > 9999 {
		return nil, ErrInvalidTerm
	}
	end := start.UTC().Add(time.Duration(seconds) * time.Second)
	if !end.After(start) || end.Year() > 9999 {
		return nil, ErrInvalidTerm
	}
	return &end, nil
}
