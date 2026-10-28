package gormstore

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/homles698698-lab/XMall/internal/constants"
	"github.com/homles698698-lab/XMall/internal/persistence/gormutil"
	"gorm.io/gorm"
)

// Only Name is used by SQL expression builders; no database is opened.
type expressionDialect struct {
	gorm.Dialector
	name string
}

func (d expressionDialect) Name() string { return d.name }

func expressionDB(name string) *gorm.DB {
	return &gorm.DB{Config: &gorm.Config{Dialector: expressionDialect{name: name}}}
}

func TestLocalizedJSONCoalesceExprDialects(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres", "mysql"} {
		t.Run(dialect, func(t *testing.T) {
			parts := make([]string, 0, len(constants.SupportedLocales)+1)
			for _, locale := range constants.SupportedLocales {
				parts = append(parts, gormutil.JSONTextExprByDialect(dialect, "title_json", locale))
			}
			parts = append(parts, "''")
			want := "COALESCE(" + strings.Join(parts, ", ") + ")"
			if got := localizedJSONCoalesceExpr(expressionDB(dialect), "title_json"); got != want {
				t.Fatalf("got %s, want %s", got, want)
			}
		})
	}
}

func TestDateGroupExprDialects(t *testing.T) {
	ref := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, dialect string
		loc           *time.Location
		want          string
	}{
		{"mysql UTC", "mysql", nil, "DATE_FORMAT(TIMESTAMPADD(SECOND, 0, created_at), '%Y-%m-%d')"},
		{"mysql east", "mysql", time.FixedZone("+08:00", 8*3600), "DATE_FORMAT(TIMESTAMPADD(SECOND, 28800, created_at), '%Y-%m-%d')"},
		{"mysql west half-hour", "mysql", time.FixedZone("-03:30", -3*3600-30*60), "DATE_FORMAT(TIMESTAMPADD(SECOND, -12600, created_at), '%Y-%m-%d')"},
		{"mysql quarter-hour", "mysql", time.FixedZone("+05:45", 5*3600+45*60), "DATE_FORMAT(TIMESTAMPADD(SECOND, 20700, created_at), '%Y-%m-%d')"},
		{"sqlite UTC", "sqlite", nil, "strftime('%Y-%m-%d', created_at, '+0 hours')"},
		{"sqlite west half-hour", "sqlite", time.FixedZone("-03:30", -3*3600-30*60), "strftime('%Y-%m-%d', created_at, '-3 hours', '-30 minutes')"},
		{"postgres UTC", "postgres", nil, "TO_CHAR(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD')"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dateGroupExpr(expressionDB(tc.dialect), "created_at", tc.loc, ref); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestTimeRangeQueryDialects(t *testing.T) {
	start := time.Date(2026, 1, 2, 0, 0, 0, 123456000, time.FixedZone("+08:00", 8*3600))
	end := start.Add(24 * time.Hour)
	cases := []struct {
		dialect  string
		wantSQL  string
		wantArgs []interface{}
	}{
		{"mysql", "created_at >= ? AND created_at < ?", []interface{}{start.UTC(), end.UTC()}},
		{"postgres", "created_at >= ? AND created_at < ?", []interface{}{start, end}},
		{"sqlite", "datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?)", []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}},
	}
	for _, tc := range cases {
		t.Run(tc.dialect, func(t *testing.T) {
			gotSQL, gotArgs := timeRangeQuery(expressionDB(tc.dialect), "created_at", start, end)
			if gotSQL != tc.wantSQL || !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Fatalf("got %s %#v, want %s %#v", gotSQL, gotArgs, tc.wantSQL, tc.wantArgs)
			}
		})
	}
}
