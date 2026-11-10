package application

import (
	"regexp"
	"testing"
	"time"
)

// TestGenerateOrderNoXMallPrefix 使用 t 验证新订单前缀和原有时间、随机数字格式，无返回值。
func TestGenerateOrderNoXMallPrefix(t *testing.T) {
	orderNo := generateOrderNo()
	if !regexp.MustCompile(`^XMALL[0-9]{20}$`).MatchString(orderNo) {
		t.Fatalf("unexpected order number format: %q", orderNo)
	}
	if _, err := time.Parse("20060102150405", orderNo[5:19]); err != nil {
		t.Fatalf("invalid order number timestamp: %v", err)
	}
}
