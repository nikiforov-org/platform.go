package utils

import (
	"testing"
	"time"
)

func TestGetEnv_String(t *testing.T) {
	t.Setenv("TEST_STR", "hello")
	if got := GetEnv(testLogger(), "TEST_STR", "fallback"); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestGetEnv_FallbackWhenUnset(t *testing.T) {
	if got := GetEnv(testLogger(), "TEST_UNSET", "fallback"); got != "fallback" {
		t.Errorf("got %q, want fallback", got)
	}
}

func TestGetEnv_FallbackWhenEmpty(t *testing.T) {
	t.Setenv("TEST_EMPTY", "")
	if got := GetEnv(testLogger(), "TEST_EMPTY", "fallback"); got != "fallback" {
		t.Errorf("empty env should return fallback, got %q", got)
	}
}

func TestGetEnv_Int(t *testing.T) {
	t.Setenv("TEST_INT", "42")
	if got := GetEnv(testLogger(), "TEST_INT", 0); got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestGetEnv_IntInvalidReturnsFallback(t *testing.T) {
	t.Setenv("TEST_INT_BAD", "not-a-number")
	if got := GetEnv(testLogger(), "TEST_INT_BAD", 99); got != 99 {
		t.Errorf("invalid int should return fallback, got %d", got)
	}
}

func TestGetEnv_Bool(t *testing.T) {
	t.Setenv("TEST_BOOL_T", "true")
	t.Setenv("TEST_BOOL_F", "false")
	if got := GetEnv(testLogger(), "TEST_BOOL_T", false); got != true {
		t.Errorf("got %v, want true", got)
	}
	if got := GetEnv(testLogger(), "TEST_BOOL_F", true); got != false {
		t.Errorf("got %v, want false", got)
	}
}

func TestGetEnv_Duration(t *testing.T) {
	tests := []struct {
		val  string
		want time.Duration
	}{
		{"15m", 15 * time.Minute},
		{"30s", 30 * time.Second},
		{"168h", 168 * time.Hour},
		{"100ms", 100 * time.Millisecond},
	}

	for _, tc := range tests {
		t.Run(tc.val, func(t *testing.T) {
			t.Setenv("TEST_DUR", tc.val)
			got := GetEnv(testLogger(), "TEST_DUR", time.Duration(0))
			if got != tc.want {
				t.Errorf("GetEnv(%q) = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

func TestGetEnv_DurationInvalidReturnsFallback(t *testing.T) {
	t.Setenv("TEST_DUR_BAD", "not-a-duration")
	fallback := 5 * time.Second
	if got := GetEnv(testLogger(), "TEST_DUR_BAD", fallback); got != fallback {
		t.Errorf("invalid duration should return fallback, got %v", got)
	}
}

func TestGetEnv_DurationWithoutUnit(t *testing.T) {
	// time.ParseDuration("15") возвращает ошибку — fmt.Sscan здесь не сработает,
	// потому что мы попадаем в специальную ветку для time.Duration.
	t.Setenv("TEST_DUR_NOUNIT", "15")
	fallback := 5 * time.Second
	if got := GetEnv(testLogger(), "TEST_DUR_NOUNIT", fallback); got != fallback {
		t.Errorf("duration without unit should fall back, got %v", got)
	}
}

func TestGetEnv_Float(t *testing.T) {
	t.Setenv("TEST_FLOAT", "3.14")
	if got := GetEnv(testLogger(), "TEST_FLOAT", 0.0); got != 3.14 {
		t.Errorf("got %v, want 3.14", got)
	}
}
